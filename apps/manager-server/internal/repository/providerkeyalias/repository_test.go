package providerkeyalias

import (
	"context"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestRepositoryUpsertLoadAndDelete(t *testing.T) {
	db, err := sqliterepo.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	repo := New(db)
	ctx := context.Background()
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const secret = "sk-never-stored-in-provider-alias-table"

	if err := repo.Upsert(ctx, model.ProviderKeyAlias{
		Provider:   " Codex ",
		APIKeyHash: strings.ToUpper(hash),
		Alias:      " WWP1 ",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	items, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(items) != 1 || items[0].Provider != "codex" || items[0].APIKeyHash != hash || items[0].Alias != "WWP1" {
		t.Fatalf("items = %#v", items)
	}
	var raw string
	if err := db.QueryRow(`select provider || ':' || api_key_hash || ':' || alias from provider_key_aliases`).Scan(&raw); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("raw provider alias row contains the API key")
	}

	if err := repo.Upsert(ctx, model.ProviderKeyAlias{
		Provider:   "codex",
		APIKeyHash: hash,
		Alias:      "Wwp1 Updated",
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	items, err = repo.LoadAll(ctx)
	if err != nil || len(items) != 1 || items[0].Alias != "Wwp1 Updated" {
		t.Fatalf("replaced items = %#v, err = %v", items, err)
	}

	if err := repo.Delete(ctx, "CODEX", hash); err != nil {
		t.Fatalf("delete: %v", err)
	}
	items, err = repo.LoadAll(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("after delete items = %#v, err = %v", items, err)
	}
}

func TestRepositoryRejectsDuplicateAliasWithinProvider(t *testing.T) {
	db, err := sqliterepo.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	repo := New(db)
	ctx := context.Background()
	first := model.ProviderKeyAlias{
		Provider:   "codex",
		APIKeyHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Alias:      "WWP1",
	}
	second := first
	second.APIKeyHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	second.Alias = "wwp1"
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repo.Upsert(ctx, second); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate error = %v", err)
	}
}
