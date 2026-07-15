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
	if items[0].Provider != "codex" {
		t.Fatalf("default provider = %q, want codex", items[0].Provider)
	}

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:     "auth-a.json",
		Provider:     "xai",
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
	if len(items) != 1 || items[0].Provider != "xai" || items[0].AuthIndex != "auth-2" || items[0].AccountID != "account-2" || items[0].DisabledAtMS != 456 {
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
	revokedAll, err := repository.RevokeDisableOwnership(ctx, nil, true)
	if err != nil {
		t.Fatalf("revoke all ownership: %v", err)
	}
	if len(revokedAll) != 1 || revokedAll[0].FileName != "auth-b.json" {
		t.Fatalf("revoked all ownership = %#v", revokedAll)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership after revoke all: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ownership after revoke all = %#v, want empty", items)
	}

	for _, item := range []model.CodexInspectionDisableOwnership{
		{FileName: "auth-c.json", AuthIndex: "auth-3", DisabledAtMS: 789},
		{FileName: "auth-d.json", AuthIndex: "auth-4", DisabledAtMS: 987},
	} {
		if err := repository.UpsertDisableOwnership(ctx, item); err != nil {
			t.Fatalf("seed ownership %s: %v", item.FileName, err)
		}
	}
	revoked, err := repository.RevokeDisableOwnership(ctx, []string{"auth-c.json"}, false)
	if err != nil {
		t.Fatalf("revoke ownership: %v", err)
	}
	if len(revoked) != 1 || revoked[0].FileName != "auth-c.json" || revoked[0].DisabledAtMS != 789 {
		t.Fatalf("revoked ownership = %#v", revoked)
	}
	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:     "auth-c.json",
		AuthIndex:    "new-auth",
		DisabledAtMS: 999,
	}); err != nil {
		t.Fatalf("insert concurrent ownership: %v", err)
	}
	if err := repository.RestoreDisableOwnership(ctx, revoked); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list restored ownership: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("restored ownership = %#v, want 2 items", items)
	}
	for _, item := range items {
		if item.FileName == "auth-c.json" && (item.AuthIndex != "new-auth" || item.DisabledAtMS != 999) {
			t.Fatalf("restore overwrote newer ownership: %#v", item)
		}
	}
}
