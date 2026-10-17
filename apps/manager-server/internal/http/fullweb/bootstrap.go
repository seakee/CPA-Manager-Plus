package fullweb

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
)

// BootstrapHandler serves the same Full Web artifact with a bounded loading
// graph. It never invents an effective routing snapshot or serves SPA fallback.
type BootstrapHandler struct{ Dist fs.FS }

var htmlAssetReference = regexp.MustCompile(`(?:src|href)=["']\./(assets/[^"']+)["']`)
var moduleReference = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*)["']([^"']+)["']`)
var cssReference = regexp.MustCompile(`url\(["']?([^"')]+)["']?\)`)

func (h BootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !routing.CanonicalBootstrapRequest(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) || h.Dist == nil {
		http.NotFound(w, r)
		return
	}
	index, err := fs.ReadFile(h.Dist, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == bootstrap.FixedBootstrapUIPath {
		index = bytes.ReplaceAll(index, []byte("__CPAMP_ROUTING_CONTEXT__"), []byte(`{"state":"bootstrap","bootstrap":true}`))
		index = bytes.ReplaceAll(index, []byte(`"./assets/`), []byte(`"`+bootstrap.FixedBootstrapAssetsPath))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
		return
	}
	name := "assets/" + strings.TrimPrefix(r.URL.Path, bootstrap.FixedBootstrapAssetsPath)
	if !strings.HasPrefix(r.URL.Path, bootstrap.FixedBootstrapAssetsPath) || !h.loadingGraph(index)[name] {
		http.NotFound(w, r)
		return
	}
	Handler{Dist: h.Dist}.serveAsset(w, r, name)
}

// Dynamic Console imports are intentionally excluded. The Bootstrap entry is
// an eager part of Full Web; only static imports, entry CSS and its resources
// can be requested through the fixed carrier.
func (h BootstrapHandler) loadingGraph(index []byte) map[string]bool {
	allowed := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if allowed[name] || !hashedAsset.MatchString(name) {
			return
		}
		data, err := fs.ReadFile(h.Dist, name)
		if err != nil {
			return
		}
		allowed[name] = true
		references := moduleReference.FindAllSubmatch(data, -1)
		if path.Ext(name) == ".css" {
			references = cssReference.FindAllSubmatch(data, -1)
		}
		for _, reference := range references {
			value := string(reference[1])
			if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || (path.Ext(name) == ".css" && !strings.HasPrefix(value, "/") && !strings.Contains(value, ":") && !strings.HasPrefix(value, "#")) {
				visit(path.Clean(path.Join(path.Dir(name), value)))
			}
		}
	}
	for _, reference := range htmlAssetReference.FindAllSubmatch(index, -1) {
		visit(string(reference[1]))
	}
	return allowed
}

// InitialHandler serves a provisional route owned by the current Fresh
// checkpoint during apply/verify. It is not a verified-effective snapshot and
// must be mounted only after the durable initial routing intent is fenced.
type InitialHandler struct {
	Target string
	Dist   fs.FS
	API    http.Handler
}

func (h InitialHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !routing.ValidControlBasePath(h.Target) || (routing.RoutingSnapshot{State: routing.SnapshotDesiredOnly, DesiredControlBasePath: h.Target}).Validate() != nil {
		http.NotFound(w, r)
		return
	}
	serveFullWeb(w, r, h.Target, h.Dist, h.API, []byte(`{"state":"initial_routing"}`))
}
