package fullweb

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

func TestBootstrapSameBuildBoundedStaticGraph(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":                      {Data: []byte(`<div id="root"></div><script src="./assets/index-AbCd1234.js"></script><link href="./assets/index-AbCd1234.css"><script>__CPAMP_ROUTING_CONTEXT__</script>`)},
		"assets/index-AbCd1234.js":        {Data: []byte(`import{a}from"./shared-AbCd1234.js";import("./console-AbCd1234.js")`)},
		"assets/shared-AbCd1234.js":       {Data: []byte(`export const a=1;`)},
		"assets/console-AbCd1234.js":      {Data: []byte(`console.log('must not load through fixed carrier')`)},
		"assets/index-AbCd1234.css":       {Data: []byte(`body{background:url(image-AbCd1234.png)}`)},
		"assets/image-AbCd1234.png":       {Data: []byte("image")},
		"assets/unreferenced-AbCd1234.js": {Data: []byte("unknown")},
	}
	h := BootstrapHandler{Dist: dist}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/ui", 200}, {"HEAD", "/ui", 200}, {"POST", "/ui", 404},
		{"GET", "/assets/index-AbCd1234.js", 200}, {"GET", "/assets/shared-AbCd1234.js", 200}, {"GET", "/assets/index-AbCd1234.css", 200}, {"GET", "/assets/image-AbCd1234.png", 200},
		{"GET", "/assets/console-AbCd1234.js", 404}, {"GET", "/assets/unreferenced-AbCd1234.js", 404}, {"GET", "/assets/missing-AbCd1234.js", 404}, {"GET", "/ui/anything", 404}, {"GET", "/assets/index-AbCd1234.js/", 404},
		{"GET", "/assets%2findex-AbCd1234.js", 404}, {"GET", "/assets/../index.html", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, bootstrap.FixedBootstrapDiscoveryPath+tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Location") != "" {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.path == "/ui" && tc.status == 200 && tc.method == "GET" {
				body := w.Body.String()
				if !strings.Contains(body, bootstrap.FixedBootstrapAssetsPath+"index-AbCd1234.js") || !strings.Contains(body, `"bootstrap":true`) || strings.Contains(body, "verifiedEffectiveControlBasePath") {
					t.Fatal(body)
				}
			}
		})
	}
}
