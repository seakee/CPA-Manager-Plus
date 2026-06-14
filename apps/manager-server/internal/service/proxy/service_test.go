package proxy

import "testing"

func TestIsCPAProxyPathIncludesPluginRoutes(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/plugins", want: true},
		{path: "/plugins/", want: true},
		{path: "/plugins/example/config", want: true},
		{path: "/plugin-store", want: true},
		{path: "/plugin-store/", want: true},
		{path: "/plugin-store/example/install", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugins/codex-invite/assets/app.js", want: true},
		{path: "/plugin-pages/example/0", want: false},
		{path: "/plugin", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAProxyPath(tt.path); got != tt.want {
				t.Fatalf("IsCPAProxyPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCPAPluginResourcePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/resource/plugins", want: true},
		{path: "/v0/resource/plugins/", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugins/codex-invite/assets/app.js", want: true},
		{path: "/v0/resource/plugin", want: false},
		{path: "/v0/resource/plugin-store", want: false},
		{path: "/plugins/codex-invite/invite", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAPluginResourcePath(tt.path); got != tt.want {
				t.Fatalf("IsCPAPluginResourcePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
