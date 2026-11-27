package freshadopt

import (
	"context"
	"errors"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

type countFakeSource struct {
	files []CredentialFile
	doc   map[string]any
	err   error
}

func (f countFakeSource) ListCredentialFiles(context.Context) ([]CredentialFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.files, nil
}

func (f countFakeSource) DownloadCredential(context.Context, string) ([]byte, error) {
	return nil, errors.New("unused in CountSourceInventory")
}

func (f countFakeSource) ReadConfigDocument(context.Context) (map[string]any, error) {
	if f.err != nil && f.doc == nil {
		return nil, f.err
	}
	return f.doc, nil
}

func TestCountSourceInventoryReportsNonSecretCounts(t *testing.T) {
	src := countFakeSource{
		files: []CredentialFile{{Name: "a.json"}, {Name: "b.json"}},
		doc: map[string]any{
			"access": map[string]any{
				"api-keys": []any{"sk-1", "sk-2", "sk-3"},
			},
			"routing": map[string]any{
				"strategy": "round-robin",
				"retry": map[string]any{
					"request-retry": float64(3),
				},
			},
			"api-keys": map[string]any{
				"claude": []any{map[string]any{"name": "g1"}},
			},
			"server": map[string]any{
				"host": "0.0.0.0",
				"port": float64(8317),
			},
		},
	}

	counts := CountSourceInventory(context.Background(), src)
	if counts[string(bootstrap.AdoptionRefCPAAuthFiles)] != 2 {
		t.Fatalf("auth files count = %d, want 2", counts[string(bootstrap.AdoptionRefCPAAuthFiles)])
	}
	if counts[string(bootstrap.AdoptionRefClientAPIKeys)] != 3 {
		t.Fatalf("client keys count = %d, want 3", counts[string(bootstrap.AdoptionRefClientAPIKeys)])
	}
	if counts[string(bootstrap.AdoptionRefConfigRouting)] != 2 {
		t.Fatalf("routing count = %d, want 2", counts[string(bootstrap.AdoptionRefConfigRouting)])
	}
	if counts[string(bootstrap.AdoptionRefProviderStateAPIKeys)] != 1 {
		t.Fatalf("provider keys count = %d, want 1", counts[string(bootstrap.AdoptionRefProviderStateAPIKeys)])
	}
	if counts[string(bootstrap.AdoptionRefConfigServer)] != 2 {
		t.Fatalf("server manual count = %d, want 2", counts[string(bootstrap.AdoptionRefConfigServer)])
	}
}

func TestCountSourceInventorySeparatesSharedRootsAndCountsUnknown(t *testing.T) {
	src := countFakeSource{
		files: []CredentialFile{{Name: "a.json"}},
		doc: map[string]any{
			"config-version": float64(8),
			"access":         map[string]any{"api-keys": []any{"sk-1"}},
			"server": map[string]any{
				"commercial-mode": true,
				"kv-store":        map[string]any{"disabled": true, "path": "/src/kv.db"},
				"host":            "0.0.0.0",
				"unknown-srv-opt": "x",
			},
			"plugins": map[string]any{
				"enabled": true,
				"configs": map[string]any{"p1": map[string]any{"k": "v"}},
			},
			"observability": map[string]any{
				"logs": map[string]any{"debug": true, "request-log": true},
			},
			"credentials": map[string]any{
				"concurrency": map[string]any{"lifecycle-config-revision": "r1"},
			},
			"oauth": map[string]any{
				"providers": map[string]any{
					"codex": map[string]any{
						"live-media-relay": map[string]any{
							"ice-servers": turnCredentialsRedactedSentinel{},
						},
					},
				},
			},
			"custom-unrecognized-root": map[string]any{"foo": "bar", "baz": 1},
		},
	}

	counts := CountSourceInventory(context.Background(), src)
	// routing: server.commercial-mode(1) + server.kv-store.disabled(1) + debug(1) + request-log(1) + plugins.configs(1) + ice-servers(1) = 6
	// (revision-only credentials.concurrency must count as 0).
	if got := counts[string(bootstrap.AdoptionRefConfigRouting)]; got != 6 {
		t.Fatalf("routing count = %d, want 6", got)
	}
	// server host-managed: server.host(1) + server.kv-store.path(1) = 2
	if got := counts[string(bootstrap.AdoptionRefConfigServer)]; got != 2 {
		t.Fatalf("server count = %d, want 2", got)
	}
	// plugins host-managed: plugins.enabled(1)
	if got := counts[string(bootstrap.AdoptionRefConfigPlugins)]; got != 1 {
		t.Fatalf("plugins count = %d, want 1", got)
	}
	// observability has only debug/request-log (both migrated), so host observability/logging must be 0.
	if got := counts[string(bootstrap.AdoptionRefConfigObservability)]; got != 0 {
		t.Fatalf("observability count = %d, want 0", got)
	}
	if got := counts[string(bootstrap.AdoptionRefConfigLogging)]; got != 0 {
		t.Fatalf("logging count = %d, want 0", got)
	}
	// unknown: server.unknown-srv-opt(1) + custom-unrecognized-root(1) = 2
	if got := counts[string(bootstrap.AdoptionRefConfigUnknown)]; got != 2 {
		t.Fatalf("unknown count = %d, want 2", got)
	}
}
