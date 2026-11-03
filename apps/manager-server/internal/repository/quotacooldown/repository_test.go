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
