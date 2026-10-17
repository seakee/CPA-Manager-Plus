package httpapi

import (
	"bytes"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
)

// FullWebHandler is the shared embed/static seam. Production has no verified
// effective snapshot yet and must not manufacture one to activate this mount.
func FullWebHandler(snapshot routing.RoutingSnapshot, api http.Handler) http.Handler {
	dist, _ := fs.Sub(embeddedPanel, "web/fullweb")
	return fullweb.Handler{Snapshot: snapshot, Dist: dist, API: api}
}

func BootstrapWebHandler() http.Handler {
	dist, _ := fs.Sub(embeddedPanel, "web/fullweb")
	return fullweb.BootstrapHandler{Dist: dist}
}

func InitialFullWebHandler(target string, api http.Handler) http.Handler {
	dist, _ := fs.Sub(embeddedPanel, "web/fullweb")
	return fullweb.InitialHandler{Target: target, Dist: dist, API: api}
}

// ProtectBrandResources also wraps the pre-ready listener: fixed immutable
// brand delivery needs no product/data initialization or authentication.
func ProtectBrandResources(next http.Handler) http.Handler {
	return routing.ProtectBrandResources(next, brandHandler(embeddedPanel))
}

func brandHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if !routing.ExactBrandResource(r) || assets == nil {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, "web"+r.URL.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType := "image/x-icon"
		if strings.HasSuffix(r.URL.Path, ".png") {
			contentType = "image/png"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, r.URL.Path, time.Time{}, bytes.NewReader(data))
	})
}
