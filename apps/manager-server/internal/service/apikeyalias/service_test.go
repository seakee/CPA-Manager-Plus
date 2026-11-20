package apikeyalias

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func serviceTestHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func TestServiceListIncludesManagedAndLegacyAliases(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "service.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	svc := New(st)

	empty, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, want empty non-nil slice", empty)
	}

	managedHash := serviceTestHash("managed-1")
	legacyHash := serviceTestHash("legacy-1")
	if _, err := st.SQLDB().ExecContext(ctx, `
		insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
		values ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 1, 'active', 100, 200);
		insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
		values ('11111111111111111111111111111111', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'cipher', '`+managedHash+`', 'cpamp_sk_…abcd', 'Frontend App', 2, 'workspace');
	`); err != nil {
		t.Fatalf("seed managed key: %v", err)
	}

	saved, err := svc.Save(ctx, []store.APIKeyAlias{
		{APIKeyHash: legacyHash, Alias: "Legacy App"},
	}, nil, false)
	if err != nil {
		t.Fatalf("save legacy alias: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved len = %d, want 2: %#v", len(saved), saved)
	}
	if saved[0].APIKeyHash != managedHash || saved[0].Alias != "Frontend App" || saved[0].MaskedPreview != "cpamp_sk_…abcd" {
		t.Fatalf("saved[0] = %#v", saved[0])
	}
	if saved[1].APIKeyHash != legacyHash || saved[1].Alias != "Legacy App" || saved[1].APIKeyID != "" {
		t.Fatalf("saved[1] = %#v", saved[1])
	}
}
