package codexinspection

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestDisableOwnershipCRUD(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:     "auth-a.json",
		AuthIndex:    "auth-1",
		AccountID:    "account-1",
		DisabledAtMS: 123,
	}); err != nil {
		t.Fatalf("insert ownership: %v", err)
	}
	items, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].FileName != "auth-a.json" || items[0].AuthIndex != "auth-1" || items[0].AccountID != "account-1" || items[0].DisabledAtMS != 123 {
		t.Fatalf("inserted ownership = %#v", items)
	}

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:     "auth-a.json",
		AuthIndex:    "auth-2",
		AccountID:    "account-2",
		DisabledAtMS: 456,
	}); err != nil {
		t.Fatalf("update ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list updated ownership: %v", err)
	}
	if len(items) != 1 || items[0].AuthIndex != "auth-2" || items[0].AccountID != "account-2" || items[0].DisabledAtMS != 456 {
		t.Fatalf("updated ownership = %#v", items)
	}

	if err := repository.DeleteDisableOwnership(ctx, "auth-a.json"); err != nil {
		t.Fatalf("delete ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list deleted ownership: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ownership after delete = %#v, want empty", items)
	}

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{FileName: "auth-b.json"}); err != nil {
		t.Fatalf("upsert ownership for clear all: %v", err)
	}
	if err := repository.DeleteAllDisableOwnership(ctx); err != nil {
		t.Fatalf("delete all ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership after clear all: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ownership after clear all = %#v, want empty", items)
	}
}
