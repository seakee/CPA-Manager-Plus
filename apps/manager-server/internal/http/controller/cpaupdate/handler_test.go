package cpaupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHasValidUploadContentType(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "missing header", values: nil, want: false},
		{name: "empty header", values: []string{"   "}, want: false},
		{name: "application/gzip", values: []string{"application/gzip"}, want: true},
		{name: "application/x-gzip with params", values: []string{"application/x-gzip; charset=binary"}, want: true},
		{name: "application/octet-stream", values: []string{"APPLICATION/OCTET-STREAM"}, want: true},
		{name: "multi-value header rejected", values: []string{"application/gzip", "text/plain"}, want: false},
		{name: "duplicate valid headers rejected", values: []string{"application/gzip", "application/octet-stream"}, want: false},
		{name: "malformed media params rejected", values: []string{"application/gzip; malformed"}, want: false},
		{name: "comma-joined media types rejected", values: []string{"application/gzip, text/plain"}, want: false},
		{name: "unsupported media type rejected", values: []string{"text/plain"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/usage-service/runtime/updates/prepare-upload", nil)
			if tc.values != nil {
				req.Header["Content-Type"] = append([]string(nil), tc.values...)
			}
			if got := hasValidUploadContentType(req); got != tc.want {
				t.Fatalf("hasValidUploadContentType(%v) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
}
