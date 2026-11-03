package cpa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExternalModelSurface proves the model-list probe reads exactly that list on
// the External transport: it answers only for a non-empty model set, refuses an
// empty one, and never moves a non-200 answer into a pass.
func TestExternalModelSurface(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		wantOK bool
	}{
		"a model list":       {status: http.StatusOK, body: `{"data":[{"id":"gpt-test"}]}`, wantOK: true},
		"an empty list":      {status: http.StatusOK, body: `{"data":[]}`},
		"an id-less entry":   {status: http.StatusOK, body: `{"data":[{"id":"   "}]}`},
		"an unparsable body": {status: http.StatusOK, body: `{"data":[`},
		"an error status":    {status: http.StatusBadGateway, body: `{"data":[{"id":"gpt-test"}]}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/models" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				authorization = request.Header.Get("Authorization")
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			err := NewTransportForEndpoint(server.URL).ExternalModelSurface(context.Background(), "client-key-1")
			if testCase.wantOK && err != nil {
				t.Fatalf("a served model list was refused: %v", err)
			}
			if !testCase.wantOK && err == nil {
				t.Fatal("an unserved model list was accepted")
			}
			if authorization != "Bearer client-key-1" {
				t.Fatalf("authorization = %q", authorization)
			}
		})
	}
	// A nil transport has no direct, proxy-free client to speak on, so it never
	// reports the surface as served.
	var absent *Transport
	if err := absent.ExternalModelSurface(context.Background(), ""); err == nil {
		t.Fatal("a nil transport reported a served model surface")
	}
}
