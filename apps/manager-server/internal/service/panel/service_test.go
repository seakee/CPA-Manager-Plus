package panel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const embeddedPanelFile = "web/management.html"

const embeddedPanelBody = "<html><body>embedded panel</body></html>"

func newEmbeddedService(t *testing.T, body string) *Service {
	t.Helper()
	return New("", fstest.MapFS{embeddedPanelFile: &fstest.MapFile{Data: []byte(body)}})
}

func serve(t *testing.T, s *Service, method, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for key, values := range header {
		for _, value := range values {
			r.Header.Add(key, value)
		}
	}
	rr := httptest.NewRecorder()
	s.ServeManagementHTML(rr, r, func(w http.ResponseWriter, status int, err error) {
		http.Error(w, err.Error(), status)
	})
	return rr
}

func TestEmbeddedPanelServesValidators(t *testing.T) {
	s := newEmbeddedService(t, embeddedPanelBody)

	rr := serve(t, s, http.MethodGet, "/management.html", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Body.String() != embeddedPanelBody {
		t.Fatalf("body = %q, want %q", rr.Body.String(), embeddedPanelBody)
	}
	if got, want := rr.Header().Get("Content-Length"), strconv.Itoa(len(embeddedPanelBody)); got != want {
		t.Fatalf("content length = %q, want %q", got, want)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control = %q, want %q", got, "no-cache")
	}
	etag := rr.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 3 {
		t.Fatalf("etag = %q, want a quoted content hash", etag)
	}
	if got := rr.Header().Get("Last-Modified"); got != "" {
		t.Fatalf("last modified = %q, want empty for embedded content", got)
	}
}

func TestEmbeddedPanelETagIsContentAddressed(t *testing.T) {
	first := serve(t, newEmbeddedService(t, embeddedPanelBody), http.MethodGet, "/management.html", nil).Header().Get("ETag")
	restarted := serve(t, newEmbeddedService(t, embeddedPanelBody), http.MethodGet, "/management.html", nil).Header().Get("ETag")
	if first != restarted {
		t.Fatalf("etag changed across service instances: %q vs %q", first, restarted)
	}
	changed := serve(t, newEmbeddedService(t, embeddedPanelBody+"<!-- rebuilt -->"), http.MethodGet, "/management.html", nil).Header().Get("ETag")
	if changed == first {
		t.Fatalf("etag %q did not change after content changed", changed)
	}
}

func TestEmbeddedPanelReturnsNotModifiedForMatchingETag(t *testing.T) {
	s := newEmbeddedService(t, embeddedPanelBody)
	etag := serve(t, s, http.MethodGet, "/management.html", nil).Header().Get("ETag")

	rr := serve(t, s, http.MethodGet, "/management.html", http.Header{"If-None-Match": {etag}})
	if rr.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotModified)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0", rr.Body.Len())
	}
	if got := rr.Header().Get("ETag"); got != etag {
		t.Fatalf("etag = %q, want %q", got, etag)
	}

	stale := serve(t, s, http.MethodGet, "/management.html", http.Header{"If-None-Match": {`"stale"`}})
	if stale.Code != http.StatusOK {
		t.Fatalf("stale status = %d, want %d", stale.Code, http.StatusOK)
	}
	if stale.Body.String() != embeddedPanelBody {
		t.Fatalf("stale body = %q", stale.Body.String())
	}
}

func TestEmbeddedPanelSupportsHeadAndRange(t *testing.T) {
	s := newEmbeddedService(t, embeddedPanelBody)

	head := serve(t, s, http.MethodHead, "/management.html", nil)
	if head.Code != http.StatusOK {
		t.Fatalf("head status = %d, want %d", head.Code, http.StatusOK)
	}
	if got, want := head.Header().Get("Content-Length"), strconv.Itoa(len(embeddedPanelBody)); got != want {
		t.Fatalf("head content length = %q, want %q", got, want)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("head body length = %d, want 0", head.Body.Len())
	}

	ranged := serve(t, s, http.MethodGet, "/management.html", http.Header{"Range": {"bytes=0-5"}})
	if ranged.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want %d", ranged.Code, http.StatusPartialContent)
	}
	if got, want := ranged.Body.String(), embeddedPanelBody[:6]; got != want {
		t.Fatalf("range body = %q, want %q", got, want)
	}
	if got, want := ranged.Header().Get("Content-Range"), "bytes 0-5/"+strconv.Itoa(len(embeddedPanelBody)); got != want {
		t.Fatalf("content range = %q, want %q", got, want)
	}
	if got := ranged.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("accept ranges = %q, want %q", got, "bytes")
	}
}

func TestEmbeddedPanelReportsMissingContent(t *testing.T) {
	s := New("", fstest.MapFS{})
	rr := serve(t, s, http.MethodGet, "/management.html", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestPanelPathStillServesExternalFile(t *testing.T) {
	panelPath := filepath.Join(t.TempDir(), "management.html")
	external := "<html><body>external panel</body></html>"
	if err := os.WriteFile(panelPath, []byte(external), 0o600); err != nil {
		t.Fatalf("write panel: %v", err)
	}
	s := New(panelPath, fstest.MapFS{embeddedPanelFile: &fstest.MapFile{Data: []byte(embeddedPanelBody)}})

	rr := serve(t, s, http.MethodGet, "/management.html", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Body.String() != external {
		t.Fatalf("body = %q, want %q", rr.Body.String(), external)
	}
	if got, want := rr.Header().Get("Content-Length"), strconv.Itoa(len(external)); got != want {
		t.Fatalf("content length = %q, want %q", got, want)
	}
	if rr.Header().Get("Last-Modified") == "" {
		t.Fatalf("external panel lost its Last-Modified validator")
	}
}

func TestPanelPathFallsBackToEmbeddedWhenMissing(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "absent.html"), fstest.MapFS{embeddedPanelFile: &fstest.MapFile{Data: []byte(embeddedPanelBody)}})

	rr := serve(t, s, http.MethodGet, "/management.html", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Body.String() != embeddedPanelBody {
		t.Fatalf("body = %q, want %q", rr.Body.String(), embeddedPanelBody)
	}
	if rr.Header().Get("ETag") == "" {
		t.Fatalf("fallback response is missing its ETag validator")
	}
}
