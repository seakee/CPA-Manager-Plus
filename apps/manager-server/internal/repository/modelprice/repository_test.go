package modelprice_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
)

func openTestDB(t *testing.T) (*sql.DB, modelprice.Repository) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "modelprice.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, modelprice.New(db)
}

func insertArchiveRef(t *testing.T, db *sql.DB, rawDeletedAt any) {
	t.Helper()
	if _, err := db.Exec(`pragma foreign_keys = off`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	hash := "hash-1"
	if rawDeletedAt != nil {
		hash = "hash-deleted"
	}
	if _, err := db.Exec(`insert or replace into usage_archive_event_refs(
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values(?, 'run-1', 1, 1, 1000, 1000, ?)`, hash, rawDeletedAt); err != nil {
		t.Fatalf("insert archive ref: %v", err)
	}
}

func markRawDeleted(t *testing.T, db *sql.DB) {
	t.Helper()
	insertArchiveRef(t, db, 2000)
}

func markRawDeletedWithRetainedProjection(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const eventHash = "hash-retained"
	if _, err := db.Exec(`insert into usage_events(
		id, event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values(1, ?, 1000, '1970-01-01T00:00:01Z', 'model-a', 1000)`, eventHash); err != nil {
		t.Fatalf("insert retained raw event: %v", err)
	}
	projection := usagemonitoring.New(db)
	result, err := projection.CatchUpProjection(ctx, 10, 1500)
	if err != nil {
		t.Fatalf("build retained projection: %v", err)
	}
	if result.CoverageEventID != 1 || result.Pending {
		t.Fatalf("unexpected retained projection result: %#v", result)
	}
	if _, err := db.Exec(`pragma foreign_keys = off`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := db.Exec(`insert into usage_archive_event_refs(
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values(?, 'run-retained', 1, 1, 1000, 1600, 1700)`, eventHash); err != nil {
		t.Fatalf("insert retained archive ref: %v", err)
	}
	if _, err := db.Exec(`delete from usage_events where id = 1`); err != nil {
		t.Fatalf("delete retained raw event: %v", err)
	}
}

// Test P1-1：没有 raw deletion，structure change 允许
func TestModelPriceStructureChangeAllowedWithoutRawDeletion(t *testing.T) {
	ctx := context.Background()
	_, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	updatedPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
		"model-b": {Prompt: 3.0, Completion: 4.0},
	}
	if err := repo.ReplaceAll(ctx, updatedPrices); err != nil {
		t.Fatalf("updated ReplaceAll: %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("expected 2 models persisted, got %d", len(persisted))
	}
	if _, ok := persisted["model-b"]; !ok {
		t.Fatalf("expected model-b to be persisted")
	}
}

// Test P1-2：archive metadata 存在但没有 raw deletion
func TestModelPriceStructureChangeAllowedWhenArchiveRefsExistWithoutDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	insertArchiveRef(t, db, nil)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	updatedPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ContextTiers: []model.ModelPriceContextTier{
				{ThresholdTokens: 200000, Prompt: 0.5, PromptConfigured: true},
			},
		},
	}
	if err := repo.ReplaceAll(ctx, updatedPrices); err != nil {
		t.Fatalf("updated ReplaceAll with context tier: %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted["model-a"].ContextTiers) != 1 {
		t.Fatalf("expected 1 context tier persisted, got %d", len(persisted["model-a"].ContextTiers))
	}
}

// Test P1-3：raw deletion 后 rate-only manual ReplaceAll
func TestModelPriceRateOnlyUpdateAllowedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0, Cache: 0.5},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	beforeRev := model.ModelPriceStructureRevision(initialPrices)
	updatedPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.5, Completion: 2.5, Cache: 0.8},
	}
	afterRev := model.ModelPriceStructureRevision(updatedPrices)
	if beforeRev != afterRev {
		t.Fatalf("revisions must match for rate-only update: %q vs %q", beforeRev, afterRev)
	}

	if err := repo.ReplaceAll(ctx, updatedPrices); err != nil {
		t.Fatalf("rate-only ReplaceAll failed: %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if persisted["model-a"].Prompt != 1.5 || persisted["model-a"].Completion != 2.5 || persisted["model-a"].Cache != 0.8 {
		t.Fatalf("unexpected persisted prices: %+v", persisted["model-a"])
	}
}

// Test P1-4：raw deletion 后 service-tier price update
func TestModelPriceServiceTierUpdateAllowedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ServiceTiers: []model.ModelPriceServiceTier{
				{Mode: "fast", ServiceTier: "priority", Prompt: 1.2, PromptConfigured: true},
			},
		},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	// Updating rates inside service tiers doesn't change structure revision
	updatedPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ServiceTiers: []model.ModelPriceServiceTier{
				{Mode: "fast", ServiceTier: "priority", Prompt: 1.8, PromptConfigured: true},
			},
		},
	}
	if model.ModelPriceStructureRevision(initialPrices) != model.ModelPriceStructureRevision(updatedPrices) {
		t.Fatal("expected service-tier rate change not to change structure revision")
	}

	if err := repo.ReplaceAll(ctx, updatedPrices); err != nil {
		t.Fatalf("service-tier rate ReplaceAll failed: %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted["model-a"].ServiceTiers) != 1 || persisted["model-a"].ServiceTiers[0].Prompt != 1.8 {
		t.Fatalf("unexpected service tier prices: %+v", persisted["model-a"].ServiceTiers)
	}
}

// Test P1-5：raw deletion 后新增 model
func TestModelPriceAddModelRejectedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	candidatePrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
		"model-b": {Prompt: 3.0, Completion: 4.0},
	}
	err := repo.ReplaceAll(ctx, candidatePrices)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected only 1 model persisted, got %d", len(persisted))
	}
	if _, ok := persisted["model-a"]; !ok {
		t.Fatalf("expected model-a to remain persisted")
	}
}

// Test P1-6：raw deletion 后删除 model
func TestModelPriceDeleteModelRejectedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
		"model-b": {Prompt: 3.0, Completion: 4.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	candidatePrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	err := repo.ReplaceAll(ctx, candidatePrices)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("expected 2 models persisted, got %d", len(persisted))
	}
}

// Test P1-7：raw deletion 后修改 context threshold
func TestModelPriceContextTierChangeRejectedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ContextTiers: []model.ModelPriceContextTier{
				{ThresholdTokens: 200000, Prompt: 0.8, PromptConfigured: true},
			},
		},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	candidatePrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ContextTiers: []model.ModelPriceContextTier{
				{ThresholdTokens: 272000, Prompt: 0.8, PromptConfigured: true},
			},
		},
	}
	err := repo.ReplaceAll(ctx, candidatePrices)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted["model-a"].ContextTiers) != 1 || persisted["model-a"].ContextTiers[0].ThresholdTokens != 200000 {
		t.Fatalf("expected context tier threshold 200000 unchanged, got %+v", persisted["model-a"].ContextTiers)
	}
}

// Test P1-8：sync rate-only update
func TestModelPriceUpsertSyncedRateOnlyAllowedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	syncCandidate := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.6, Completion: 2.6},
	}
	result, err := repo.UpsertSynced(ctx, syncCandidate)
	if err != nil {
		t.Fatalf("UpsertSynced rate-only: %v", err)
	}
	if result.Imported != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if persisted["model-a"].Prompt != 1.6 || persisted["model-a"].Completion != 2.6 {
		t.Fatalf("unexpected synced prices: %+v", persisted["model-a"])
	}
}

// Test P1-9：sync 新 model
func TestModelPriceUpsertSyncedNewModelRejectedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	syncCandidate := map[string]model.ModelPrice{
		"model-b": {Prompt: 3.0, Completion: 4.0},
	}
	result, err := repo.UpsertSynced(ctx, syncCandidate)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got result=%+v err=%v", result, err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected only 1 model persisted after rollback, got %d", len(persisted))
	}
	if _, ok := persisted["model-a"]; !ok {
		t.Fatalf("expected model-a to remain persisted")
	}
	if _, ok := persisted["model-b"]; ok {
		t.Fatalf("model-b should not exist after rollback")
	}
}

// Test P1-10：sync context-tier structure change
func TestModelPriceUpsertSyncedContextTierStructureChangeRejectedAfterRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ContextTiers: []model.ModelPriceContextTier{
				{ThresholdTokens: 200000, Prompt: 0.8, PromptConfigured: true},
			},
			ServiceTiers: []model.ModelPriceServiceTier{
				{Mode: "fast", ServiceTier: "priority", Prompt: 1.5, PromptConfigured: true},
			},
		},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	syncCandidate := map[string]model.ModelPrice{
		"model-a": {
			Prompt:     1.0,
			Completion: 2.0,
			ContextTiers: []model.ModelPriceContextTier{
				{ThresholdTokens: 300000, Prompt: 0.9, PromptConfigured: true},
			},
			ServiceTiers: []model.ModelPriceServiceTier{
				{Mode: "fast", ServiceTier: "priority", Prompt: 2.0, PromptConfigured: true},
			},
		},
	}
	result, err := repo.UpsertSynced(ctx, syncCandidate)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got result=%+v err=%v", result, err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	priceA := persisted["model-a"]
	if len(priceA.ContextTiers) != 1 || priceA.ContextTiers[0].ThresholdTokens != 200000 {
		t.Fatalf("context tiers modified despite rollback: %+v", priceA.ContextTiers)
	}
	if len(priceA.ServiceTiers) != 1 || priceA.ServiceTiers[0].Prompt != 1.5 {
		t.Fatalf("service tiers modified despite rollback: %+v", priceA.ServiceTiers)
	}
}

// Case A: manual price + raw deletion + sync
func TestModelPriceUpsertSyncedManualPricePreservedWithRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:           10.0,
			Completion:       20.0,
			PromptConfigured: true,
			Source:           "manual",
		},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	// Sync contains model-a with different candidate prices
	syncCandidate := map[string]model.ModelPrice{
		"model-a": {Prompt: 5.0, Completion: 10.0, Source: "sync"},
	}
	result, err := repo.UpsertSynced(ctx, syncCandidate)
	if err != nil {
		t.Fatalf("UpsertSynced with manual price and raw deletion: %v", err)
	}
	if result.Imported != 0 {
		t.Fatalf("expected Imported == 0, got %d", result.Imported)
	}
	if len(result.Preserved) != 1 || result.Preserved[0] != "model-a" {
		t.Fatalf("expected Preserved == ['model-a'], got %+v", result.Preserved)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if persisted["model-a"].Prompt != 10.0 || persisted["model-a"].Completion != 20.0 || persisted["model-a"].Source != "manual" {
		t.Fatalf("manual price overwritten: %+v", persisted["model-a"])
	}
}

// Case B: manual price + raw deletion + new model (rejected and transaction rolled back)
func TestModelPriceUpsertSyncedManualPriceAndNewModelRejectedWithRawDeletion(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	initialPrices := map[string]model.ModelPrice{
		"model-a": {
			Prompt:           10.0,
			Completion:       20.0,
			PromptConfigured: true,
			Source:           "manual",
		},
	}
	if err := repo.ReplaceAll(ctx, initialPrices); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}

	markRawDeleted(t, db)

	// Sync contains model-a (manual) and model-b (new)
	syncCandidate := map[string]model.ModelPrice{
		"model-a": {Prompt: 5.0, Completion: 10.0, Source: "sync"},
		"model-b": {Prompt: 3.0, Completion: 4.0, Source: "sync"},
	}
	result, err := repo.UpsertSynced(ctx, syncCandidate)
	if !errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("expected ErrStructureChangeAfterRawDeletion, got result=%+v err=%v", result, err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 model persisted after rollback, got %d", len(persisted))
	}
	if persisted["model-a"].Prompt != 10.0 || persisted["model-a"].Completion != 20.0 || persisted["model-a"].Source != "manual" {
		t.Fatalf("manual price altered: %+v", persisted["model-a"])
	}
	if _, ok := persisted["model-b"]; ok {
		t.Fatalf("model-b should not exist after rollback")
	}
}


func TestModelPriceStructureChangeAllowedAfterRawDeletionWithRetainedProjection(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	if err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}
	markRawDeletedWithRetainedProjection(t, db)

	if err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
		"model-b": {Prompt: 3.0, Completion: 4.0},
	}); err != nil {
		t.Fatalf("structure change with retained projection: %v", err)
	}

	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := persisted["model-b"]; !ok {
		t.Fatalf("expected model-b to be persisted")
	}
}

func TestModelPriceUpsertSyncedStructureChangeAllowedAfterRawDeletionWithRetainedProjection(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	if err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}
	markRawDeletedWithRetainedProjection(t, db)

	result, err := repo.UpsertSynced(ctx, map[string]model.ModelPrice{
		"model-b": {Prompt: 3.0, Completion: 4.0, Source: "sync"},
	})
	if err != nil {
		t.Fatalf("UpsertSynced with retained projection: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}
	persisted, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := persisted["model-b"]; !ok {
		t.Fatalf("expected model-b to be persisted")
	}
}


func TestModelPriceStructureChangePreservesRetainedVerifierSystemError(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	if err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}
	markRawDeleted(t, db)
	if _, err := db.Exec(`drop table usage_monitoring_rollup_state`); err != nil {
		t.Fatalf("drop monitoring rollup state: %v", err)
	}

	err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
		"model-b": {Prompt: 3.0, Completion: 4.0},
	})
	if err == nil {
		t.Fatal("expected retained verifier system error")
	}
	if errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("system error was misclassified as structure conflict: %v", err)
	}

	persisted, loadErr := repo.LoadAll(ctx)
	if loadErr != nil {
		t.Fatalf("LoadAll: %v", loadErr)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected mutation rollback, got %d models", len(persisted))
	}
	if _, ok := persisted["model-b"]; ok {
		t.Fatal("model-b should not exist after verifier system error")
	}
}

func TestModelPriceUpsertSyncedPreservesRetainedVerifierSystemError(t *testing.T) {
	ctx := context.Background()
	db, repo := openTestDB(t)

	if err := repo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, Completion: 2.0},
	}); err != nil {
		t.Fatalf("initial ReplaceAll: %v", err)
	}
	markRawDeleted(t, db)
	if _, err := db.Exec(`drop table usage_monitoring_rollup_state`); err != nil {
		t.Fatalf("drop monitoring rollup state: %v", err)
	}

	result, err := repo.UpsertSynced(ctx, map[string]model.ModelPrice{
		"model-b": {Prompt: 3.0, Completion: 4.0, Source: "sync"},
	})
	if err == nil {
		t.Fatalf("expected retained verifier system error, got result=%+v", result)
	}
	if errors.Is(err, modelprice.ErrStructureChangeAfterRawDeletion) {
		t.Fatalf("system error was misclassified as structure conflict: %v", err)
	}

	persisted, loadErr := repo.LoadAll(ctx)
	if loadErr != nil {
		t.Fatalf("LoadAll: %v", loadErr)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected sync rollback, got %d models", len(persisted))
	}
	if _, ok := persisted["model-b"]; ok {
		t.Fatal("model-b should not exist after verifier system error")
	}
}
