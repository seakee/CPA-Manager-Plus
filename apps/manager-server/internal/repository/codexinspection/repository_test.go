package codexinspection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestResultRoundTripPreservesAccountSnapshot(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()
	run, err := repository.CreateRun(ctx, model.CodexInspectionRun{
		TriggerType: "manual",
		Status:      model.CodexInspectionStatusCompleted,
		StartedAtMS: 1,
		Settings:    model.DefaultCodexInspectionConfig(),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	result, err := repository.InsertResult(ctx, model.CodexInspectionResult{
		RunID:           run.ID,
		AccountKey:      "shared.json::-::alice",
		FileName:        "shared.json",
		DisplayAccount:  "Friendly Alice",
		AccountSnapshot: "alice@example.com",
		Provider:        "codex",
		Action:          "disable",
	})
	if err != nil {
		t.Fatalf("insert result: %v", err)
	}
	if result.ID <= 0 {
		t.Fatalf("inserted result ID = %d", result.ID)
	}

	items, err := repository.ListResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(items) != 1 || items[0].DisplayAccount != "Friendly Alice" || items[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("stored results = %#v", items)
	}

	if _, err := repository.InsertResult(ctx, model.CodexInspectionResult{
		RunID:           run.ID,
		AccountKey:      "shared.json::-::alice",
		FileName:        "shared.json",
		DisplayAccount:  "Renamed Alice",
		AccountSnapshot: "alice+updated@example.com",
		Provider:        "codex",
		Action:          "keep",
	}); err != nil {
		t.Fatalf("upsert result: %v", err)
	}
	items, err = repository.ListResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("list updated results: %v", err)
	}
	if len(items) != 1 || items[0].DisplayAccount != "Renamed Alice" || items[0].AccountSnapshot != "alice+updated@example.com" {
		t.Fatalf("updated results = %#v", items)
	}
}

func TestResultRoundTripPreservesExplicitEmptyQuotaInventory(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()
	run, err := repository.CreateRun(ctx, model.CodexInspectionRun{
		TriggerType: "manual",
		Status:      model.CodexInspectionStatusCompleted,
		StartedAtMS: 1,
		Settings:    model.DefaultCodexInspectionConfig(),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	inserted, err := repository.InsertResult(ctx, model.CodexInspectionResult{
		RunID:                  run.ID,
		AccountKey:             "codex.json::-::alice",
		FileName:               "codex.json",
		DisplayAccount:         "alice@example.com",
		Provider:               "codex",
		Action:                 "keep",
		QuotaInventoryObserved: true,
	})
	if err != nil {
		t.Fatalf("insert result: %v", err)
	}
	if inserted.QuotaWindowsJSON != "[]" || !inserted.QuotaInventoryObserved {
		t.Fatalf("inserted quota inventory = %#v", inserted)
	}

	items, err := repository.ListResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(items) != 1 || len(items[0].QuotaWindows) != 0 || items[0].QuotaWindowsJSON != "[]" || !items[0].QuotaInventoryObserved {
		t.Fatalf("stored quota inventory = %#v", items)
	}
	payload, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var contract map[string]any
	if err := json.Unmarshal(payload, &contract); err != nil {
		t.Fatalf("unmarshal result contract: %v", err)
	}
	if observed, ok := contract["quotaInventoryObserved"].(bool); !ok || !observed {
		t.Fatalf("quotaInventoryObserved contract = %#v", contract["quotaInventoryObserved"])
	}
}

func TestResultRoundTripRejectsMalformedQuotaInventoryAndPreservesValidUpsertEvidence(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()
	run, err := repository.CreateRun(ctx, model.CodexInspectionRun{
		TriggerType: "manual",
		Status:      model.CodexInspectionStatusCompleted,
		StartedAtMS: 1,
		Settings:    model.DefaultCodexInspectionConfig(),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	valid := model.CodexInspectionResult{
		RunID:          run.ID,
		AccountKey:     "codex.json::-::alice",
		FileName:       "codex.json",
		DisplayAccount: "alice@example.com",
		Provider:       "codex",
		Action:         "keep",
		QuotaWindows: []model.CodexInspectionQuotaWindow{{
			ID: "five-hour",
		}},
	}
	inserted, err := repository.InsertResult(ctx, valid)
	if err != nil {
		t.Fatalf("insert valid result: %v", err)
	}
	if !inserted.QuotaInventoryObserved || len(inserted.QuotaWindows) != 1 {
		t.Fatalf("inserted valid quota inventory = %#v", inserted)
	}

	for _, raw := range []string{"{", "null", `{"five-hour":{}}`} {
		updated := valid
		updated.Action = "disable"
		updated.QuotaWindows = nil
		updated.QuotaWindowsJSON = raw
		stored, insertErr := repository.InsertResult(ctx, updated)
		if insertErr != nil {
			t.Fatalf("upsert malformed quota inventory %q: %v", raw, insertErr)
		}
		if stored.QuotaInventoryObserved {
			t.Fatalf("malformed quota inventory %q was accepted: %#v", raw, stored)
		}
	}

	items, err := repository.ListResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(items) != 1 || !items[0].QuotaInventoryObserved || len(items[0].QuotaWindows) != 1 || items[0].QuotaWindows[0].ID != "five-hour" {
		t.Fatalf("valid quota inventory was not preserved: %#v", items)
	}
}

func TestDisableOwnershipCRUD(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:        "auth-a.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "account-1",
		AccountSnapshot: "alice@example.com",
		DisabledAtMS:    123,
	}); err != nil {
		t.Fatalf("insert ownership: %v", err)
	}
	items, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].FileName != "auth-a.json" || items[0].AuthIndex != "auth-1" || items[0].AccountID != "account-1" || items[0].AccountSnapshot != "alice@example.com" || items[0].DisabledAtMS != 123 {
		t.Fatalf("inserted ownership = %#v", items)
	}
	if items[0].Provider != "codex" {
		t.Fatalf("provider = %q, want codex", items[0].Provider)
	}

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:        "auth-a.json",
		Provider:        "xai",
		AuthIndex:       "auth-2",
		AccountID:       "account-2",
		AccountSnapshot: "bob@example.com",
		DisabledAtMS:    456,
	}); err != nil {
		t.Fatalf("update ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list updated ownership: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("same-file ownership = %#v, want 2 items", items)
	}

	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	accountSnapshot := "alice@example.com"
	if err := repository.DeleteDisableOwnership(ctx, model.CodexInspectionDisableOwnershipTarget{
		FileName:        "auth-a.json",
		Provider:        &provider,
		AuthIndex:       &authIndex,
		AccountID:       &accountID,
		AccountSnapshot: &accountSnapshot,
	}); err != nil {
		t.Fatalf("delete ownership: %v", err)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list deleted ownership: %v", err)
	}
	if len(items) != 1 || items[0].Provider != "xai" || items[0].AuthIndex != "auth-2" {
		t.Fatalf("ownership after exact delete = %#v", items)
	}
	if err := repository.DeleteDisableOwnership(ctx, model.CodexInspectionDisableOwnershipTarget{FileName: "auth-a.json"}); err != nil {
		t.Fatalf("delete whole-file ownership: %v", err)
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
	revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{FileName: "auth-c.json"}}, false)
	if err != nil {
		t.Fatalf("revoke ownership: %v", err)
	}
	if len(revoked) != 1 || revoked[0].FileName != "auth-c.json" || revoked[0].DisabledAtMS != 789 {
		t.Fatalf("revoked ownership = %#v", revoked)
	}
	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:     "auth-c.json",
		AuthIndex:    "auth-3",
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
		if item.FileName == "auth-c.json" && item.AuthIndex == "auth-3" && item.DisabledAtMS != 999 {
			t.Fatalf("restore overwrote newer ownership: %#v", item)
		}
	}
}

func TestUpsertDisableOwnershipsIsAtomic(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create trigger fail_second_ownership before insert on codex_inspection_disable_ownership
		when new.auth_index = 'auth-2'
		begin
			select raise(abort, 'forced batch ownership failure');
		end`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	repository := New(db)
	err = repository.UpsertDisableOwnerships(context.Background(), []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2"},
	})
	if err == nil {
		t.Fatal("batch ownership upsert succeeded, want forced failure")
	}
	items, listErr := repository.ListDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	if len(items) != 0 {
		t.Fatalf("ownership after failed batch = %#v, want empty", items)
	}
}

func TestDisableOwnershipTargetRevokesCompatibleLegacyWildcard(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	for _, item := range []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2"},
	} {
		if err := repository.UpsertDisableOwnership(ctx, item); err != nil {
			t.Fatalf("seed ownership %#v: %v", item, err)
		}
	}

	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{
		FileName:  "shared.json",
		Provider:  &provider,
		AuthIndex: &authIndex,
		AccountID: &accountID,
	}}, false)
	if err != nil {
		t.Fatalf("revoke ownership: %v", err)
	}
	if len(revoked) != 2 {
		t.Fatalf("revoked ownership = %#v, want exact and legacy wildcard", revoked)
	}
	items, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].AuthIndex != "auth-2" {
		t.Fatalf("remaining ownership = %#v, want auth-2 only", items)
	}
}

func TestDisableOwnershipSeparatesAccountSnapshotFallbackIdentities(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	for _, snapshot := range []string{"alice@example.com", "bob@example.com"} {
		if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
			FileName:        "shared.json",
			Provider:        "codex",
			AccountSnapshot: snapshot,
		}); err != nil {
			t.Fatalf("seed ownership %q: %v", snapshot, err)
		}
	}
	items, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("snapshot fallback ownership = %#v, want 2 items", items)
	}

	provider := "codex"
	authIndex := ""
	accountID := ""
	accountSnapshot := "alice@example.com"
	revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{
		FileName:        "shared.json",
		Provider:        &provider,
		AuthIndex:       &authIndex,
		AccountID:       &accountID,
		AccountSnapshot: &accountSnapshot,
	}}, false)
	if err != nil {
		t.Fatalf("revoke snapshot ownership: %v", err)
	}
	if len(revoked) != 1 || revoked[0].AccountSnapshot != accountSnapshot {
		t.Fatalf("revoked ownership = %#v, want alice only", revoked)
	}
	items, err = repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list remaining ownership: %v", err)
	}
	if len(items) != 1 || items[0].AccountSnapshot != "bob@example.com" {
		t.Fatalf("remaining ownership = %#v, want bob only", items)
	}
}

func TestDisableOwnershipStableAccountIDDoesNotRevokeDifferentSnapshotIdentity(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	if err := repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
		FileName:        "shared.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountSnapshot: "bob@example.com",
	}); err != nil {
		t.Fatalf("seed snapshot ownership: %v", err)
	}
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-alice"
	accountSnapshot := "alice@example.com"
	revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{
		FileName:        "shared.json",
		Provider:        &provider,
		AuthIndex:       &authIndex,
		AccountID:       &accountID,
		AccountSnapshot: &accountSnapshot,
	}}, false)
	if err != nil {
		t.Fatalf("revoke stable account identity: %v", err)
	}
	if len(revoked) != 0 {
		t.Fatalf("revoked ownership = %#v, want snapshot identity preserved", revoked)
	}
	items, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].AccountSnapshot != "bob@example.com" {
		t.Fatalf("remaining ownership = %#v, want bob snapshot", items)
	}
}

func TestDisableOwnershipEmptyAccountIDWithoutSnapshotDoesNotRevokeSnapshotIdentity(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	ctx := context.Background()

	for _, item := range []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex"},
		{FileName: "shared.json", Provider: "codex", AccountSnapshot: "alice@example.com"},
	} {
		if err := repository.UpsertDisableOwnership(ctx, item); err != nil {
			t.Fatalf("seed ownership %#v: %v", item, err)
		}
	}
	provider := "codex"
	authIndex := ""
	accountID := ""
	revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{
		FileName:  "shared.json",
		Provider:  &provider,
		AuthIndex: &authIndex,
		AccountID: &accountID,
	}}, false)
	if err != nil {
		t.Fatalf("revoke empty account identity: %v", err)
	}
	if len(revoked) != 1 || revoked[0].AccountSnapshot != "" {
		t.Fatalf("revoked ownership = %#v, want only legacy wildcard", revoked)
	}
	remaining, err := repository.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(remaining) != 1 || remaining[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("remaining ownership = %#v, want snapshot identity preserved", remaining)
	}
}

func TestDisableOwnershipWritesRetrySQLiteBusy(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, repository Repository)
		operation func(ctx context.Context, repository Repository) error
		verify    func(t *testing.T, repository Repository)
	}{
		{
			name: "upsert",
			operation: func(ctx context.Context, repository Repository) error {
				return repository.UpsertDisableOwnership(ctx, model.CodexInspectionDisableOwnership{FileName: "auth-a.json"})
			},
			verify: func(t *testing.T, repository Repository) {
				items, err := repository.ListDisableOwnership(context.Background())
				if err != nil || len(items) != 1 || items[0].FileName != "auth-a.json" {
					t.Fatalf("ownership after retried upsert = %#v err=%v", items, err)
				}
			},
		},
		{
			name: "batch upsert",
			operation: func(ctx context.Context, repository Repository) error {
				return repository.UpsertDisableOwnerships(ctx, []model.CodexInspectionDisableOwnership{
					{FileName: "auth-a.json"},
					{FileName: "auth-b.json"},
				})
			},
			verify: func(t *testing.T, repository Repository) {
				items, err := repository.ListDisableOwnership(context.Background())
				if err != nil || len(items) != 2 {
					t.Fatalf("ownership after retried batch upsert = %#v err=%v", items, err)
				}
			},
		},
		{
			name: "delete",
			setup: func(t *testing.T, repository Repository) {
				if err := repository.UpsertDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{FileName: "auth-a.json"}); err != nil {
					t.Fatalf("seed ownership: %v", err)
				}
			},
			operation: func(ctx context.Context, repository Repository) error {
				return repository.DeleteDisableOwnership(ctx, model.CodexInspectionDisableOwnershipTarget{FileName: "auth-a.json"})
			},
			verify: func(t *testing.T, repository Repository) {
				items, err := repository.ListDisableOwnership(context.Background())
				if err != nil || len(items) != 0 {
					t.Fatalf("ownership after retried delete = %#v err=%v", items, err)
				}
			},
		},
		{
			name: "revoke",
			setup: func(t *testing.T, repository Repository) {
				if err := repository.UpsertDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{FileName: "auth-a.json"}); err != nil {
					t.Fatalf("seed ownership: %v", err)
				}
			},
			operation: func(ctx context.Context, repository Repository) error {
				revoked, err := repository.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{FileName: "auth-a.json"}}, false)
				if err == nil && (len(revoked) != 1 || revoked[0].FileName != "auth-a.json") {
					t.Fatalf("revoked ownership = %#v", revoked)
				}
				return err
			},
			verify: func(t *testing.T, repository Repository) {
				items, err := repository.ListDisableOwnership(context.Background())
				if err != nil || len(items) != 0 {
					t.Fatalf("ownership after retried revoke = %#v err=%v", items, err)
				}
			},
		},
		{
			name: "restore",
			operation: func(ctx context.Context, repository Repository) error {
				return repository.RestoreDisableOwnership(ctx, []model.CodexInspectionDisableOwnership{{FileName: "auth-a.json"}})
			},
			verify: func(t *testing.T, repository Repository) {
				items, err := repository.ListDisableOwnership(context.Background())
				if err != nil || len(items) != 1 || items[0].FileName != "auth-a.json" {
					t.Fatalf("ownership after retried restore = %#v err=%v", items, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := sqlite.Open(dbPath)
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			locker, err := sqlite.Open(dbPath)
			if err != nil {
				t.Fatalf("open lock sqlite: %v", err)
			}
			t.Cleanup(func() { _ = locker.Close() })
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			if _, err := db.ExecContext(context.Background(), `pragma busy_timeout = 1`); err != nil {
				t.Fatalf("set short busy timeout: %v", err)
			}
			repository := New(db)
			if test.setup != nil {
				test.setup(t, repository)
			}

			lockConn, err := locker.Conn(context.Background())
			if err != nil {
				t.Fatalf("open lock connection: %v", err)
			}
			defer lockConn.Close()
			if _, err := lockConn.ExecContext(context.Background(), `begin immediate`); err != nil {
				t.Fatalf("begin write lock: %v", err)
			}
			releaseErr := make(chan error, 1)
			go func() {
				time.Sleep(40 * time.Millisecond)
				_, err := lockConn.ExecContext(context.Background(), `commit`)
				releaseErr <- err
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := test.operation(ctx, repository); err != nil {
				t.Fatalf("operation after transient write lock: %v", err)
			}
			if err := <-releaseErr; err != nil {
				t.Fatalf("release write lock: %v", err)
			}
			test.verify(t, repository)
		})
	}
}
