package cpa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReadV8ConfigDocumentClassifiesTheDocument pins the two verdicts the
// External connect step acts on, because the operator is sent after a different
// cause for each.
//
// A document that declares another generation is the CPA saying it is not v8,
// and only that answer may tell the operator to upgrade the CPA. A body that
// cannot be a v8 document at all — a proxy's page, a truncated body, an
// impossible port, a field of the wrong shape — says nothing about the
// generation, so it must not borrow the version verdict.
//
// The accepted rows are the shapes a real v8 CPA legitimately serves: a default
// auth directory, an empty one, no client Key list at all and a null one (Go
// serialises a nil slice as null).
func TestReadV8ConfigDocumentClassifiesTheDocument(t *testing.T) {
	const (
		authDir = `"oauth":{"auth-dir":"/auth"}`
		keys    = `"access":{"api-keys":["client-key-1"]}`
	)
	cases := []struct {
		name        string
		contentType string
		body        string
		want        V8ReadError // "" means the document is accepted
		wantKeys    int
	}{
		{name: "a v8 document", body: `{"config-version":8,"server":{"port":8317},` + authDir + `,` + keys + `}`, wantKeys: 1},
		{name: "no auth directory", body: `{"config-version":8,"server":{"port":8317},` + keys + `}`, wantKeys: 1},
		{name: "an empty auth directory", body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":""},` + keys + `}`, wantKeys: 1},
		{name: "no client keys", body: `{"config-version":8,"server":{"port":8317},` + authDir + `}`},
		{name: "a null client-key list", body: `{"config-version":8,"server":{"port":8317},` + authDir + `,"access":{"api-keys":null}}`},
		{name: "an empty client-key list", body: `{"config-version":8,"server":{"port":8317},` + authDir + `,"access":{"api-keys":[]}}`},
		{
			name: "the previous generation",
			body: `{"config-version":7,"server":{"port":8317},` + authDir + `,` + keys + `}`,
			want: V8InvalidConfig,
		},
		{
			name: "a later generation",
			body: `{"config-version":9,"server":{"port":8317},` + authDir + `,` + keys + `}`,
			want: V8InvalidConfig,
		},
		{
			name: "no generation",
			body: `{"server":{"port":8317},` + authDir + `,` + keys + `}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "an unbounded port",
			body: `{"config-version":8,"server":{"port":0},` + authDir + `,` + keys + `}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "a port above the range",
			body: `{"config-version":8,"server":{"port":65536},` + authDir + `,` + keys + `}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "no port",
			body: `{"config-version":8,"server":{},` + authDir + `,` + keys + `}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "a truncated document",
			body: `{"config-version":`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name:        "a proxy page",
			contentType: "text/html",
			body:        `<html><body>gateway</body></html>`,
			want:        V8ConfigDocumentInvalid,
		},
		{
			name: "an auth directory of the wrong shape",
			body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":5},` + keys + `}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "client keys that are not strings",
			body: `{"config-version":8,"server":{"port":8317},` + authDir + `,"access":{"api-keys":[7]}}`,
			want: V8ConfigDocumentInvalid,
		},
		{
			name: "a client-key value that is not a list",
			body: `{"config-version":8,"server":{"port":8317},` + authDir + `,"access":{"api-keys":"client-key-1"}}`,
			want: V8ConfigDocumentInvalid,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			contentType := testCase.contentType
			if contentType == "" {
				contentType = "application/json; charset=utf-8"
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v8/management/config" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				if request.Header.Get("Authorization") != "Bearer management-key-1" {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				writer.Header().Set("Content-Type", contentType)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			observation, err := ReadV8ConfigDocument(context.Background(), server.URL, "management-key-1")
			var readErr V8ReadError
			errors.As(err, &readErr)
			if err == nil && testCase.want != "" {
				t.Fatalf("accepted a document that is not v8: %+v", observation)
			}
			if err != nil && testCase.want == "" {
				t.Fatalf("refused a v8 document: %v", err)
			}
			if err != nil && readErr != testCase.want {
				t.Fatalf("error = %v, want %v", readErr, testCase.want)
			}
			// The two verdicts answer different questions, so a refusal may
			// never be both and a document error may never arrive as a version
			// verdict or the other way round.
			if testCase.want != "" && err != nil && testCase.want != V8InvalidConfig && readErr == V8InvalidConfig {
				t.Fatalf("a document error arrived as a version verdict: %v", err)
			}
			if testCase.want == "" && observation.ClientKeyCount != testCase.wantKeys {
				t.Fatalf("client key count = %d, want %d", observation.ClientKeyCount, testCase.wantKeys)
			}
			if testCase.want == "" && observation.BaseURL != server.URL {
				t.Fatalf("observation names %q, want %q", observation.BaseURL, server.URL)
			}
		})
	}
}

// TestReadV8ConfigDocumentKeepsTheSurfaceVerdictsDistinct proves the reason the
// generation question is asked after the HTTP status: an absent surface, a
// rejected credential and a server fault all mean something other than "not a
// document", and none of them may be reported as the document stop.
func TestReadV8ConfigDocumentKeepsTheSurfaceVerdictsDistinct(t *testing.T) {
	cases := map[string]struct {
		status int
		want   V8ReadError
	}{
		"the surface is absent":       {status: http.StatusNotFound, want: V8OwnerUpgradeRequired},
		"the method is not served":    {status: http.StatusMethodNotAllowed, want: V8OwnerUpgradeRequired},
		"the endpoint has moved":      {status: http.StatusGone, want: V8OwnerUpgradeRequired},
		"the credential is rejected":  {status: http.StatusUnauthorized, want: V8AuthenticationRejected},
		"the credential is forbidden": {status: http.StatusForbidden, want: V8AuthenticationRejected},
		"the server faulted":          {status: http.StatusInternalServerError, want: V8ReadUnavailable},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
			}))
			defer server.Close()
			_, err := ReadV8ConfigDocument(context.Background(), server.URL, "management-key-1")
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestReadV8ConfigDocumentRefusesAnUnusableEndpoint keeps the connect step's
// contract: the reader never sends a request to an address or credential the
// transport would not use, so an unusable input is a connection verdict.
func TestReadV8ConfigDocumentRefusesAnUnusableEndpoint(t *testing.T) {
	for name, testCase := range map[string]struct{ baseURL, key string }{
		"an empty address":   {baseURL: "", key: "management-key-1"},
		"a non-http address": {baseURL: "ftp://127.0.0.1:8317", key: "management-key-1"},
		"an empty key":       {baseURL: "http://127.0.0.1:1", key: ""},
		"a padded key":       {baseURL: "http://127.0.0.1:1", key: " management-key-1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadV8ConfigDocument(context.Background(), testCase.baseURL, testCase.key)
			if !errors.Is(err, V8InvalidConnection) {
				t.Fatalf("err = %v, want %v", err, V8InvalidConnection)
			}
		})
	}
}
