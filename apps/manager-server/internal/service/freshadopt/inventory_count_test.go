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
