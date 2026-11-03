package quotacooldown_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotacooldown "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotacooldown"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestUpsertActiveGenerationStrictlyAdvancesWithinClockResolution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	input := model.QuotaCooldownUpsert{
		AuthFileName: "codex.json", AuthIndex: "auth-1", Provider: "codex",
		Owner: model.QuotaCooldownOwnerUsage429, RecoverAtMS: time.Now().Add(time.Hour).UnixMilli(),
		EventHash: "old-429",
	}
	old, err := st.QuotaCooldowns.UpsertActive(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	// Force the previous generation ahead of the wall clock, exercising the
	// same-millisecond/clock-rollback collision without sleeps or timing races.
	generation := time.Now().Add(time.Hour).UnixMilli()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "update quota_cooldowns set updated_at_ms = ? where id = ?", generation, old.ID); err != nil {
		t.Fatal(err)
	}
	input.EventHash = "new-429"
	current, err := st.QuotaCooldowns.UpsertActive(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != old.ID || current.UpdatedAtMS != generation+1 || current.EventHash != "new-429" {
		t.Fatalf("generation did not advance: %+v", current)
	}
}

func TestUpsertActiveUsesCredentialIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	first, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName:    "shared.json",
		AuthIndex:       "auth-1",
		AccountSnapshot: "old@example.com",
		Provider:        "x_ai",
		RecoverAtMS:     1_000,
		Owner:           model.QuotaCooldownOwnerXAIFreeUsage,
	})
	if err != nil {
		t.Fatalf("insert indexed cooldown: %v", err)
	}
	updated, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName:    "shared.json",
		AuthIndex:       "auth-1",
		AccountSnapshot: "renamed@example.com",
		Provider:        "xai",
		RecoverAtMS:     2_000,
		Owner:           model.QuotaCooldownOwnerXAIFreeUsage,
	})
	if err != nil {
		t.Fatalf("update indexed cooldown: %v", err)
	}
	if updated.ID != first.ID || updated.AccountSnapshot != "renamed@example.com" || updated.Provider != "xai" || updated.RecoverAtMS != 2_000 {
		t.Fatalf("updated indexed cooldown = %#v, first = %#v", updated, first)
	}

	for _, account := range []string{"alice@example.com", "bob@example.com"} {
		if _, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
			AuthFileName:    "shared.json",
			AccountSnapshot: account,
			Provider:        "codex",
			RecoverAtMS:     3_000,
			Owner:           model.QuotaCooldownOwnerUsage429,
		}); err != nil {
			t.Fatalf("insert fallback cooldown %q: %v", account, err)
		}
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("active cooldowns = %#v, want three credential identities", active)
	}
}

func TestUpsertActiveUpgradesFallbackIdentityAndPreservesOwnershipOrigin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	first, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName:     "shared.json",
		AccountSnapshot:  "user@example.com",
		Provider:         "x_ai",
		RecoverAtMS:      1_000,
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: false,
		DisabledAtMS:     100,
	})
	if err != nil {
		t.Fatalf("insert fallback cooldown: %v", err)
	}
	upgraded, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName:     "shared.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "xai",
		RecoverAtMS:      2_000,
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: true,
		DisabledAtMS:     200,
	})
	if err != nil {
		t.Fatalf("upgrade fallback cooldown: %v", err)
	}
	if upgraded.ID != first.ID || upgraded.AuthIndex != "auth-1" || upgraded.Provider != "xai" || upgraded.RecoverAtMS != 2_000 {
		t.Fatalf("upgraded cooldown = %#v, first = %#v", upgraded, first)
	}
	if upgraded.PreDisabledState || upgraded.DisabledAtMS != 100 {
		t.Fatalf("ownership origin changed during extension: %#v", upgraded)
	}
}

func TestUpsertActiveKeepsMetadataForWinningRecovery(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	first, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		ReasonCode:   "weekly_limit",
		WindowKind:   "weekly",
		EvidenceJSON: `{"recover_at_ms":2000,"source":"weekly"}`,
		RecoverAtMS:  2_000,
		Owner:        model.QuotaCooldownOwnerUsage429,
		EventHash:    "evt-weekly",
	})
	if err != nil {
		t.Fatalf("insert winning cooldown: %v", err)
	}

	shorter, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		ReasonCode:   "five_hour_limit",
		WindowKind:   "five_hour",
		EvidenceJSON: `{"recover_at_ms":1000,"source":"five-hour"}`,
		RecoverAtMS:  1_000,
		Owner:        model.QuotaCooldownOwnerUsage429,
		EventHash:    "evt-five-hour",
	})
	if err != nil {
		t.Fatalf("upsert shorter cooldown: %v", err)
	}
	if shorter.ID != first.ID || shorter.RecoverAtMS != 2_000 || shorter.ReasonCode != "weekly_limit" || shorter.WindowKind != "weekly" || shorter.EvidenceJSON != `{"recover_at_ms":2000,"source":"weekly"}` || shorter.EventHash != "evt-weekly" {
		t.Fatalf("shorter cooldown replaced winning metadata: %#v", shorter)
	}

	longer, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		ReasonCode:   "monthly_limit",
		WindowKind:   "monthly",
		EvidenceJSON: `{"recover_at_ms":3000,"source":"monthly"}`,
		RecoverAtMS:  3_000,
		Owner:        model.QuotaCooldownOwnerUsage429,
		EventHash:    "evt-monthly",
	})
	if err != nil {
		t.Fatalf("upsert longer cooldown: %v", err)
	}
	if longer.RecoverAtMS != 3_000 || longer.ReasonCode != "monthly_limit" || longer.WindowKind != "monthly" || longer.EvidenceJSON != `{"recover_at_ms":3000,"source":"monthly"}` || longer.EventHash != "evt-monthly" {
		t.Fatalf("longer cooldown did not replace winning metadata: %#v", longer)
	}
}

func TestUpsertActiveBeginsNewCycleAfterObservedEnable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	ctx := context.Background()
	first, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		ReasonCode:   "weekly_limit",
		WindowKind:   "weekly",
		RecoverAtMS:  7_000,
		Owner:        model.QuotaCooldownOwnerUsage429,
		EventHash:    "evt-weekly",
		DisabledAtMS: 100,
	})
	if err != nil {
		t.Fatalf("insert old cooldown cycle: %v", err)
	}

	second, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName:        "codex.json",
		AuthIndex:           "auth-1",
		Provider:            "codex",
		ReasonCode:          "five_hour_limit",
		WindowKind:          "five_hour",
		RecoverAtMS:         5_000,
		Owner:               model.QuotaCooldownOwnerUsage429,
		EventHash:           "evt-five-hour",
		ObservedEnabledAtMS: 200,
		DisabledAtMS:        300,
	})
	if err != nil {
		t.Fatalf("begin new cooldown cycle: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("new cooldown reused stale active record id=%d", second.ID)
	}
	if second.RecoverAtMS != 5_000 || second.ReasonCode != "five_hour_limit" || second.WindowKind != "five_hour" || second.EventHash != "evt-five-hour" || second.DisabledAtMS != 300 {
		t.Fatalf("new cooldown = %#v", second)
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active cooldowns = %#v, want only new cycle", active)
	}

	var oldStatus string
	var oldRecoveredAtMS sql.NullInt64
	if err := raw.QueryRowContext(ctx, `select status, recovered_at_ms from quota_cooldowns where id = ?`, first.ID).Scan(&oldStatus, &oldRecoveredAtMS); err != nil {
		t.Fatalf("read archived cooldown: %v", err)
	}
	if oldStatus != model.QuotaCooldownStatusRecovered || !oldRecoveredAtMS.Valid {
		t.Fatalf("archived cooldown status=%q recovered_at_ms=%#v", oldStatus, oldRecoveredAtMS)
	}
	if oldRecoveredAtMS.Int64 != 200 {
		t.Fatalf("archived cooldown recovered_at_ms=%d, want 200", oldRecoveredAtMS.Int64)
	}
	if oldRecoveredAtMS.Int64 > second.DisabledAtMS {
		t.Fatalf("archived cooldown recovered_at_ms=%d exceeds new disabled_at_ms=%d", oldRecoveredAtMS.Int64, second.DisabledAtMS)
	}
}

func TestMarkRecoveredRequiresMatchingActiveGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	active, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		Owner:        model.QuotaCooldownOwnerUsage429,
		RecoverAtMS:  time.Now().Add(time.Hour).UnixMilli(),
		EventHash:    "evt-cas",
	})
	if err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	recoveredAtMS := time.Now().UnixMilli()
	if err := st.QuotaCooldowns.MarkRecovered(ctx, active.ID, active.UpdatedAtMS+1, recoveredAtMS); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("stale generation mark recovered error = %v, want ErrConflict", err)
	}
	untouched, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(untouched) != 1 || untouched[0] != active {
		t.Fatalf("active cooldowns = %#v, want untouched generation %#v", untouched, active)
	}

	if err := st.QuotaCooldowns.MarkRecovered(ctx, active.ID, active.UpdatedAtMS, recoveredAtMS); err != nil {
		t.Fatalf("matching generation mark recovered: %v", err)
	}
	if err := st.QuotaCooldowns.MarkRecovered(ctx, active.ID, active.UpdatedAtMS, recoveredAtMS); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("repeat mark recovered error = %v, want ErrConflict for a non-active row", err)
	}
	left, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("active cooldowns = %#v, want recovered", left)
	}
}

func TestClaimRecoveryFencesObservedGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	active, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		Owner:        model.QuotaCooldownOwnerUsage429,
		RecoverAtMS:  time.Now().Add(-time.Minute).UnixMilli(),
		EventHash:    "evt-claim",
	})
	if err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	if _, err := st.QuotaCooldowns.ClaimRecovery(ctx, active.ID, active.UpdatedAtMS+1, "enable in progress"); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("stale generation claim error = %v, want ErrConflict", err)
	}
	residue, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(residue) != 1 || residue[0] != active {
		t.Fatalf("active cooldowns = %#v, want untouched generation %#v", residue, active)
	}

	claimedUpdatedAtMS, err := st.QuotaCooldowns.ClaimRecovery(ctx, active.ID, active.UpdatedAtMS, "enable in progress")
	if err != nil {
		t.Fatalf("claim observed generation: %v", err)
	}
	if claimedUpdatedAtMS <= active.UpdatedAtMS {
		t.Fatalf("claimed generation = %d, want strictly after %d", claimedUpdatedAtMS, active.UpdatedAtMS)
	}
	status, lastError, updatedAtMS := readCooldownRow(t, dbPath, active.ID)
	if status != model.QuotaCooldownStatusSkipped {
		t.Fatalf("claimed status = %q, want %q", status, model.QuotaCooldownStatusSkipped)
	}
	if lastError != "enable in progress" {
		t.Fatalf("claimed last_error = %q, want the claim reason", lastError)
	}
	if updatedAtMS != claimedUpdatedAtMS {
		t.Fatalf("claimed generation = %d, want %d", updatedAtMS, claimedUpdatedAtMS)
	}
	due, err := st.QuotaCooldowns.ListDue(ctx, time.Now().Add(24*time.Hour).UnixMilli(), 10)
	if err != nil {
		t.Fatalf("list due cooldowns: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due cooldowns = %#v, want a claimed generation to be ineligible", due)
	}

	// A claimed generation can only be finalized through its own generation.
	if err := st.QuotaCooldowns.MarkClaimedRecovered(ctx, active.ID, active.UpdatedAtMS, time.Now().UnixMilli()); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("stale claimed marker error = %v, want ErrConflict", err)
	}
	if _, unchangedError, unchangedUpdatedAt := readCooldownRow(t, dbPath, active.ID); unchangedUpdatedAt != claimedUpdatedAtMS || unchangedError != "enable in progress" {
		t.Fatalf("claimed row changed by a stale marker: updated_at=%d last_error=%q", unchangedUpdatedAt, unchangedError)
	}

	if err := st.QuotaCooldowns.MarkClaimedRecovered(ctx, active.ID, claimedUpdatedAtMS, time.Now().UnixMilli()); err != nil {
		t.Fatalf("claimed generation mark recovered: %v", err)
	}
	if status, _, _ := readCooldownRow(t, dbPath, active.ID); status != model.QuotaCooldownStatusRecovered {
		t.Fatalf("final status = %q, want %q", status, model.QuotaCooldownStatusRecovered)
	}
	if err := st.QuotaCooldowns.ReopenClaim(ctx, active.ID, claimedUpdatedAtMS, "retry"); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("reopen of a finalized claim error = %v, want ErrConflict", err)
	}
}

func TestReopenClaimRestoresRetryableGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	active, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "xai.json",
		AuthIndex:    "auth-xai",
		Provider:     "xai",
		Owner:        model.QuotaCooldownOwnerXAIFreeUsage,
		RecoverAtMS:  time.Now().Add(-time.Minute).UnixMilli(),
		EventHash:    "evt-reopen",
	})
	if err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}
	claimedUpdatedAtMS, err := st.QuotaCooldowns.ClaimRecovery(ctx, active.ID, active.UpdatedAtMS, "enable in progress")
	if err != nil {
		t.Fatalf("claim observed generation: %v", err)
	}
	if err := st.QuotaCooldowns.MarkClaimedSkipped(ctx, active.ID, active.UpdatedAtMS, "unconfirmed"); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("stale claimed skip error = %v, want ErrConflict", err)
	}
	if status, lastError, updatedAtMS := readCooldownRow(t, dbPath, active.ID); status != model.QuotaCooldownStatusSkipped ||
		lastError != "enable in progress" || updatedAtMS != claimedUpdatedAtMS {
		t.Fatalf("claimed row changed by a stale skip: status=%q last_error=%q updated_at=%d", status, lastError, updatedAtMS)
	}
	if err := st.QuotaCooldowns.ReopenClaim(ctx, active.ID, claimedUpdatedAtMS, "rejected before apply"); err != nil {
		t.Fatalf("reopen claimed generation: %v", err)
	}
	reopened, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(reopened) != 1 || reopened[0].ID != active.ID || reopened[0].UpdatedAtMS <= claimedUpdatedAtMS {
		t.Fatalf("active cooldowns = %#v, want the reopened generation", reopened)
	}
	if reopened[0].LastError != "rejected before apply" {
		t.Fatalf("reopened last_error = %q, want the rejection reason", reopened[0].LastError)
	}
}

func TestMarkSkippedRequiresObservedActiveGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	active, err := st.QuotaCooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		Owner:        model.QuotaCooldownOwnerUsage429,
		RecoverAtMS:  time.Now().Add(time.Hour).UnixMilli(),
		EventHash:    "evt-skip",
	})
	if err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}
	if err := st.QuotaCooldowns.MarkSkipped(ctx, active.ID, active.UpdatedAtMS+1, "identity changed"); !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("stale generation skip error = %v, want ErrConflict", err)
	}
	residue, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(residue) != 1 || residue[0] != active {
		t.Fatalf("active cooldowns = %#v, want untouched generation %#v", residue, active)
	}
	if err := st.QuotaCooldowns.MarkSkipped(ctx, active.ID, active.UpdatedAtMS, "identity changed"); err != nil {
		t.Fatalf("matching generation skip: %v", err)
	}
	status, lastError, _ := readCooldownRow(t, dbPath, active.ID)
	if status != model.QuotaCooldownStatusSkipped || lastError != "identity changed" {
		t.Fatalf("skipped row status=%q last_error=%q", status, lastError)
	}
}

func TestUpsertActiveDoesNotReviveClaimedGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	upsert := model.QuotaCooldownUpsert{
		AuthFileName: "codex.json",
		AuthIndex:    "auth-1",
		Provider:     "codex",
		Owner:        model.QuotaCooldownOwnerUsage429,
		RecoverAtMS:  time.Now().Add(time.Hour).UnixMilli(),
		EventHash:    "evt-claim-old",
	}
	active, err := st.QuotaCooldowns.UpsertActive(ctx, upsert)
	if err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}
	claimedUpdatedAtMS, err := st.QuotaCooldowns.ClaimRecovery(ctx, active.ID, active.UpdatedAtMS, "enable in progress")
	if err != nil {
		t.Fatalf("claim observed generation: %v", err)
	}
	next := upsert
	next.EventHash = "evt-claim-new"
	next.RecoverAtMS = time.Now().Add(2 * time.Hour).UnixMilli()
	newer, err := st.QuotaCooldowns.UpsertActive(ctx, next)
	if err != nil {
		t.Fatalf("upsert newer evidence: %v", err)
	}
	// Newer quota evidence owns the credential in its own fresh generation; the
	// claimed generation keeps no automatic enable authority.
	if newer.ID == active.ID {
		t.Fatalf("newer evidence reused the claimed row %d", active.ID)
	}
	if newer.Status != model.QuotaCooldownStatusActive || newer.EventHash != "evt-claim-new" {
		t.Fatalf("newer generation = %#v, want a fresh active generation", newer)
	}
	status, lastError, updatedAtMS := readCooldownRow(t, dbPath, active.ID)
	if status != model.QuotaCooldownStatusSkipped || lastError != "enable in progress" || updatedAtMS != claimedUpdatedAtMS {
		t.Fatalf("claimed row changed by newer evidence: status=%q last_error=%q updated_at=%d", status, lastError, updatedAtMS)
	}
	// A reopen must not join the newer active generation as a second active
	// owner for the same credential.
	if err := st.QuotaCooldowns.ReopenClaim(ctx, active.ID, claimedUpdatedAtMS, "retry"); !errors.Is(err, quotacooldown.ErrSuperseded) {
		t.Fatalf("reopen with a newer active cooldown error = %v, want ErrSuperseded", err)
	} else if !errors.Is(err, quotacooldown.ErrConflict) {
		t.Fatalf("superseded reopen error = %v, want it to keep the generation conflict semantics", err)
	}
	status, lastError, updatedAtMS = readCooldownRow(t, dbPath, active.ID)
	if status != model.QuotaCooldownStatusSkipped || lastError != "enable in progress" || updatedAtMS != claimedUpdatedAtMS {
		t.Fatalf("superseded reopen changed the claimed row: status=%q last_error=%q updated_at=%d", status, lastError, updatedAtMS)
	}
	owners, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(owners) != 1 || owners[0].ID != newer.ID {
		t.Fatalf("active cooldowns = %#v, want only the newer generation %d", owners, newer.ID)
	}
	// The newer generation keeps merging quota evidence for its identity.
	later := next
	later.EventHash = "evt-claim-later"
	later.RecoverAtMS = time.Now().Add(3 * time.Hour).UnixMilli()
	latest, err := st.QuotaCooldowns.UpsertActive(ctx, later)
	if err != nil {
		t.Fatalf("upsert after a superseded reopen: %v", err)
	}
	if latest.ID != newer.ID || latest.EventHash != "evt-claim-later" {
		t.Fatalf("upsert after a superseded reopen = %#v, want an update of the active generation %d", latest, newer.ID)
	}
}

// TestReopenClaimTreatsLegacyAndIndexedCooldownsAsOneOwner covers the identity
// fallback UpsertActive applies when a cooldown arrives with an auth_index but
// the active row only carries the legacy provider/account_snapshot identity.
// The reopen guard must refuse exactly the pairs UpsertActive would have
// merged; otherwise one physical credential ends up with two active owners.
func TestReopenClaimTreatsLegacyAndIndexedCooldownsAsOneOwner(t *testing.T) {
	const claimReason = "enable in progress"
	legacy := func(account string) model.QuotaCooldownUpsert {
		return model.QuotaCooldownUpsert{
			AuthFileName:    "xai.json",
			AccountSnapshot: account,
			Provider:        "xai",
			Owner:           model.QuotaCooldownOwnerXAIFreeUsage,
			RecoverAtMS:     time.Now().Add(-time.Minute).UnixMilli(),
			EventHash:       "evt-legacy",
		}
	}
	indexed := func(authIndex string, account string) model.QuotaCooldownUpsert {
		return model.QuotaCooldownUpsert{
			AuthFileName:    "xai.json",
			AuthIndex:       authIndex,
			AccountSnapshot: account,
			Provider:        "x_ai",
			Owner:           model.QuotaCooldownOwnerXAIFreeUsage,
			RecoverAtMS:     time.Now().Add(-time.Minute).UnixMilli(),
			EventHash:       "evt-indexed",
		}
	}
	withoutProvider := legacy("alice@example.com")
	withoutProvider.Provider = ""

	tests := []struct {
		name           string
		claimed        model.QuotaCooldownUpsert
		sibling        model.QuotaCooldownUpsert
		wantSuperseded bool
	}{
		{
			name:           "indexed sibling owns a claimed legacy identity",
			claimed:        legacy("alice@example.com"),
			sibling:        indexed("auth-1", "alice@example.com"),
			wantSuperseded: true,
		},
		{
			name:           "legacy sibling owns a claimed indexed identity",
			claimed:        indexed("auth-1", "alice@example.com"),
			sibling:        legacy("alice@example.com"),
			wantSuperseded: true,
		},
		{
			name:    "another auth index is a different credential",
			claimed: indexed("auth-1", "alice@example.com"),
			sibling: indexed("auth-2", "alice@example.com"),
		},
		{
			name:    "another account snapshot is a different credential",
			claimed: indexed("auth-1", "alice@example.com"),
			sibling: legacy("bob@example.com"),
		},
		{
			name:    "a legacy row without a provider identity does not own the claimed credential",
			claimed: indexed("auth-1", "alice@example.com"),
			sibling: withoutProvider,
		},
		{
			name:    "a legacy row without an account snapshot identity does not own the claimed credential",
			claimed: indexed("auth-1", "alice@example.com"),
			sibling: legacy(""),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := context.Background()

			claimed, err := st.QuotaCooldowns.UpsertActive(ctx, tc.claimed)
			if err != nil {
				t.Fatalf("upsert claimed cooldown: %v", err)
			}
			claimedUpdatedAtMS, err := st.QuotaCooldowns.ClaimRecovery(ctx, claimed.ID, claimed.UpdatedAtMS, claimReason)
			if err != nil {
				t.Fatalf("claim observed generation: %v", err)
			}
			// The second writer records its own evidence while the claim is in
			// flight, so it cannot join the claimed row.
			sibling, err := st.QuotaCooldowns.UpsertActive(ctx, tc.sibling)
			if err != nil {
				t.Fatalf("upsert sibling cooldown: %v", err)
			}
			if sibling.ID == claimed.ID {
				t.Fatalf("sibling evidence reused the claimed row %d instead of recording a new generation", claimed.ID)
			}

			reopenErr := st.QuotaCooldowns.ReopenClaim(ctx, claimed.ID, claimedUpdatedAtMS, "retry")
			active, err := st.QuotaCooldowns.ListActive(ctx)
			if err != nil {
				t.Fatalf("list active cooldowns: %v", err)
			}
			status, lastError, updatedAtMS := readCooldownRow(t, dbPath, claimed.ID)

			if tc.wantSuperseded {
				if !errors.Is(reopenErr, quotacooldown.ErrSuperseded) {
					t.Fatalf("reopen with an owning sibling error = %v, want ErrSuperseded", reopenErr)
				}
				if !errors.Is(reopenErr, quotacooldown.ErrConflict) {
					t.Fatalf("superseded reopen error = %v, want it to keep the generation conflict semantics", reopenErr)
				}
				if status != model.QuotaCooldownStatusSkipped || lastError != claimReason || updatedAtMS != claimedUpdatedAtMS {
					t.Fatalf("superseded reopen changed the claimed row: status=%q last_error=%q updated_at=%d", status, lastError, updatedAtMS)
				}
				if len(active) != 1 || active[0].ID != sibling.ID {
					t.Fatalf("active cooldowns = %#v, want only the sibling generation %d", active, sibling.ID)
				}
				return
			}

			if reopenErr != nil {
				t.Fatalf("reopen a distinct credential error = %v, want nil", reopenErr)
			}
			if status != model.QuotaCooldownStatusActive || lastError != "retry" {
				t.Fatalf("reopened row status=%q last_error=%q, want an active retryable generation", status, lastError)
			}
			owners := make(map[int64]bool, len(active))
			for _, item := range active {
				owners[item.ID] = true
			}
			if len(owners) != 2 || !owners[claimed.ID] || !owners[sibling.ID] {
				t.Fatalf("active cooldowns = %#v, want the reopened claim %d and the sibling %d", active, claimed.ID, sibling.ID)
			}
		})
	}
}

func readCooldownRow(t *testing.T, dbPath string, id int64) (string, string, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open cooldown database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var status string
	var lastError sql.NullString
	var updatedAtMS int64
	if err := db.QueryRow(`select status, last_error, updated_at_ms from quota_cooldowns where id = ?`, id).Scan(&status, &lastError, &updatedAtMS); err != nil {
		t.Fatalf("read cooldown %d: %v", id, err)
	}
	return status, lastError.String, updatedAtMS
}
