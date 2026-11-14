package freshkeys

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	projectionadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	projectionports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestAddOperationCommitKeepsAddedIdentityActiveAndKeeperlessRevokeSupersedesTarget(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	ctx := t.Context()

	recA := stagedRecord(t, strings.Repeat("a", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("1", 64))
	recA.Hash = hashHex("cpamp_sk_" + strings.Repeat("1", 64))
	recA.Preview = "cpamp_sk_…1111"
	recA.Origin = "workspace"
	if _, err := repo.Create(ctx, recA, usageRuntime); err != nil {
		t.Fatalf("create key A: %v", err)
	}

	recB := stagedRecord(t, strings.Repeat("b", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("2", 64))
	recB.Hash = hashHex("cpamp_sk_" + strings.Repeat("2", 64))
	recB.Preview = "cpamp_sk_…2222"
	recB.Origin = "workspace"
	if _, err := repo.Create(ctx, recB, usageRuntime); err != nil {
		t.Fatalf("create key B: %v", err)
	}

	addID := strings.Repeat("c", 32)
	addOp := Operation{
		ID:          addID,
		TargetID:    usageKeyID,
		KeeperID:    "",
		Mode:        "add",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     "pending",
		Steps:       []KeyStep{{Name: "add", State: "unattempted"}},
		CreatedAtMS: time.Now().UnixMilli(),
	}
	if err := repo.BeginOperation(ctx, addOp); err != nil {
		t.Fatalf("begin add operation: %v", err)
	}
	addOp.Steps[0].State = "observed"
	addOp.Steps[0].Returned = true
	addOp.Outcome = "committed"
	addOp.CompletedAtMS = time.Now().UnixMilli()
	if err := repo.SaveOperation(ctx, addOp); err != nil {
		t.Fatalf("commit add operation: %v", err)
	}

	storedA, err := repo.ByID(ctx, identity.APIKeyID(usageKeyID))
	if err != nil || storedA == nil {
		t.Fatalf("read key A after add commit: %v (%v)", storedA, err)
	}
	if storedA.Identity.Lifecycle != identity.LifecycleActive {
		t.Fatalf("added key lifecycle = %q, want active", storedA.Identity.Lifecycle)
	}
	var activeBindings int
	if err := db.QueryRowContext(ctx, `select count(1) from gateway_api_key_source_bindings where api_key_id = ? and retired_at_ms is null`, usageKeyID).Scan(&activeBindings); err != nil || activeBindings != 1 {
		t.Fatalf("active source bindings after add commit = %d (%v), want 1", activeBindings, err)
	}

	revokeID := strings.Repeat("d", 32)
	revokeOp := Operation{
		ID:          revokeID,
		TargetID:    usageKeyID,
		KeeperID:    "",
		Mode:        "revoke",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     "pending",
		Steps:       []KeyStep{{Name: "revoke", State: "unattempted"}},
		CreatedAtMS: time.Now().UnixMilli(),
	}
	if err := repo.BeginOperation(ctx, revokeOp); err != nil {
		t.Fatalf("begin keeperless revoke operation: %v", err)
	}
	revokeOp.Steps[0].State = "observed"
	revokeOp.Steps[0].Returned = true
	revokeOp.Outcome = "committed"
	revokeOp.CompletedAtMS = time.Now().UnixMilli()
	if err := repo.SaveOperation(ctx, revokeOp); err != nil {
		t.Fatalf("commit keeperless revoke operation: %v", err)
	}

	storedA, err = repo.ByID(ctx, identity.APIKeyID(usageKeyID))
	if err != nil || storedA == nil || storedA.Identity.Lifecycle != identity.LifecycleSuperseded {
		t.Fatalf("revoked key lifecycle = %+v (%v), want superseded", storedA, err)
	}
	storedB, err := repo.ByID(ctx, identity.APIKeyID(usageKeeper))
	if err != nil || storedB == nil || storedB.Identity.Lifecycle != identity.LifecycleActive {
		t.Fatalf("surviving key lifecycle = %+v (%v), want active", storedB, err)
	}
}

func insertUsageEventForKeyTest(
	t *testing.T,
	db *sql.DB,
	eventHash string,
	timestampMS int64,
	apiKeyHash string,
	inputTokens, outputTokens, totalTokens int64,
	failed int,
) {
	t.Helper()
	if _, err := db.ExecContext(
		t.Context(),
		`insert into usage_events (
			request_id, event_hash, timestamp_ms, timestamp, model, api_key_hash,
			input_tokens, output_tokens, total_tokens, failed, raw_json, created_at_ms
		) values (?, ?, ?, '2026-01-01T00:00:00Z', 'gpt-4.1', ?, ?, ?, ?, ?, '{}', ?)`,
		"req-"+eventHash,
		eventHash,
		timestampMS,
		apiKeyHash,
		inputTokens,
		outputTokens,
		totalTokens,
		failed,
		timestampMS,
	); err != nil {
		t.Fatalf("insert usage_event %s: %v", eventHash, err)
	}
}

func TestStageAdoptedReusesPreExistingCanonicalIdentityAndScalesAbove128Keys(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	ctx := t.Context()
	protector, err := security.NewProtector([]byte("fresh key usage binding test data key"))
	if err != nil {
		t.Fatal(err)
	}

	const totalKeys = 210
	clockMS := int64(1_760_100_000_000)
	summaryFromMS := clockMS - usageapp.DefaultWindowMS
	baseMS := summaryFromMS - 60_000
	preHandoffEvent1MS := summaryFromMS + 1_000
	preHandoffEvent2MS := summaryFromMS + 2_000
	handoffMS := summaryFromMS + 10_000
	postHandoffEvent1MS := handoffMS
	postHandoffEvent2MS := handoffMS + 5_000
	highIndexEventMS := handoffMS + 6_000

	legacySecret := "legacy-v1-migrated-key-0"
	legacyHash := hashHex(legacySecret)
	preExistingID := usageOtherID
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
		preExistingID, baseMS, baseMS); err != nil {
		t.Fatalf("seed pre-existing identity: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, '1', ?, ?, null)`,
		preExistingID, "legacy-runtime", legacyHash, baseMS, preHandoffEvent2MS); err != nil {
		t.Fatalf("seed pre-existing source binding: %v", err)
	}

	insertUsageEventForKeyTest(t, db, "ev-pre-handoff-1", preHandoffEvent1MS, legacyHash, 10, 5, 15, 0)
	insertUsageEventForKeyTest(t, db, "ev-pre-handoff-2", preHandoffEvent2MS, legacyHash, 20, 10, 30, 1)

	projRepo := projectionadapter.New(db)
	if preCatchUp, err := projRepo.CatchUp(ctx, 500, preHandoffEvent2MS+500); err != nil || preCatchUp.Pending || preCatchUp.Processed != 2 {
		t.Fatalf("pre-handoff CatchUp = %+v (%v)", preCatchUp, err)
	}

	candidates := make([]Record, 0, totalKeys)
	for i := range totalKeys {
		secret := "adopted-client-secret-" + strconv.Itoa(i)
		if i == 0 {
			secret = legacySecret
		}
		ciphertext, err := protector.ProtectString(secret)
		if err != nil {
			t.Fatal(err)
		}
		idHex := strconv.FormatInt(int64(i+1000), 16)
		id := strings.Repeat("0", 32-len(idHex)) + idHex
		reqHex := strconv.FormatInt(int64(i+5000), 16)
		reqID := strings.Repeat("f", 32-len(reqHex)) + reqHex
		ts := handoffMS + int64(i)
		candidates = append(candidates, Record{
			RequestID:    reqID,
			Identity:     identity.APIKeyIdentity{ID: identity.APIKeyID(id), Revision: 1, Lifecycle: identity.LifecycleActive, CreatedAtMS: ts, UpdatedAtMS: ts},
			Ciphertext:   ciphertext,
			Hash:         hashHex(secret),
			Preview:      "adopted…",
			NameRevision: 1,
			Origin:       "adopted",
		})
	}

	staged, err := repo.StageAdopted(ctx, candidates, usageRuntime)
	if err != nil {
		t.Fatalf("stage %d adopted keys: %v", totalKeys, err)
	}
	if len(staged) != totalKeys {
		t.Fatalf("staged count = %d, want %d", len(staged), totalKeys)
	}
	if string(staged[0].Identity.ID) != preExistingID || staged[0].Identity.CreatedAtMS != baseMS {
		t.Fatalf("migrated key identity = %+v, want ID=%q CreatedAtMS=%d", staged[0].Identity, preExistingID, baseMS)
	}

	rows := bindingRows(t, db, preExistingID)
	if len(rows) != 2 {
		t.Fatalf("pre-existing identity bindings = %+v, want 2 non-overlapping windows", rows)
	}
	if rows[0].runtimeIdentity != "legacy-runtime" || rows[0].firstSeenAtMS != baseMS ||
		!rows[0].retiredAtMS.Valid || rows[0].retiredAtMS.Int64 != handoffMS {
		t.Fatalf("retired legacy binding = %+v, want [%d, %d)", rows[0], baseMS, handoffMS)
	}
	if rows[1].runtimeIdentity != usageRuntime || rows[1].firstSeenAtMS != handoffMS || rows[1].retiredAtMS.Valid {
		t.Fatalf("opened runtime binding = %+v, want [%d, null)", rows[1], handoffMS)
	}

	// Binding handoff must invalidate the previously ready projection checkpoint.
	if projState, err := projRepo.GetState(ctx); err != nil || projState.Status != "pending" {
		t.Fatalf("projection state after handoff = %+v (%v), want pending", projState, err)
	}

	insertUsageEventForKeyTest(t, db, "ev-post-handoff-1", postHandoffEvent1MS, legacyHash, 30, 15, 45, 0)
	insertUsageEventForKeyTest(t, db, "ev-post-handoff-2", postHandoffEvent2MS, legacyHash, 40, 20, 60, 0)
	insertUsageEventForKeyTest(t, db, "ev-high-index-key", highIndexEventMS, staged[205].Hash, 12, 8, 20, 0)

	postCatchUp, err := projRepo.CatchUp(ctx, 500, highIndexEventMS+1_000)
	if err != nil || !postCatchUp.Rebuilt || postCatchUp.Pending || postCatchUp.Processed != 5 {
		t.Fatalf("post-handoff CatchUp = %+v (%v), want rebuilt=true processed=5", postCatchUp, err)
	}
	for _, evHash := range []string{"ev-pre-handoff-1", "ev-pre-handoff-2", "ev-post-handoff-1", "ev-post-handoff-2"} {
		proj, err := projRepo.GetProjectionByEventHash(ctx, evHash)
		if err != nil || proj == nil || proj.APIKeyState != projectionports.StateMapped || proj.APIKeyID == nil || *proj.APIKeyID != preExistingID {
			t.Fatalf("projection(%s) = %+v (%v), want mapped to %s", evHash, proj, err, preExistingID)
		}
	}

	currentClock := baseMS
	usageSvc := usageapp.New(usageadapter.New(db), func() usageapp.CollectionPhase { return usageapp.PhaseRunning })
	usageSvc.SetClock(func() int64 { return currentClock })
	if err := usageSvc.MarkCollectionStart(ctx); err != nil {
		t.Fatalf("MarkCollectionStart: %v", err)
	}
	currentClock = clockMS

	detail, err := usageSvc.Detail(ctx, identity.APIKeyID(preExistingID), summaryFromMS, clockMS)
	if err != nil {
		t.Fatalf("usage Detail: %v", err)
	}
	if detail.Coverage.State != "complete" || detail.Coverage.Reason != "observed" {
		t.Fatalf("migrated key detail coverage = %+v, want complete/observed", detail.Coverage)
	}
	if detail.Totals == nil || detail.Totals.Calls != 4 || detail.Totals.Success != 3 || detail.Totals.Failure != 1 ||
		detail.Totals.InputTokens != 100 || detail.Totals.OutputTokens != 50 || detail.Totals.TotalTokens != 150 ||
		detail.Totals.LastUsedAtMS == nil || *detail.Totals.LastUsedAtMS != postHandoffEvent2MS {
		t.Fatalf("migrated key detail totals = %+v", detail.Totals)
	}

	allIDs := make([]identity.APIKeyID, 0, len(staged))
	for _, rec := range staged {
		allIDs = append(allIDs, rec.Identity.ID)
	}
	summary, err := usageSvc.Summary(ctx, allIDs)
	if err != nil || len(summary.Items) != totalKeys {
		t.Fatalf("usage Summary(210 keys) = len %d err %v", len(summary.Items), err)
	}
	if summary.Items[0].KeyID != preExistingID || summary.Items[0].Calls != 4 ||
		summary.Items[0].Coverage.State != "complete" || summary.Items[0].Coverage.Reason != "observed" {
		t.Fatalf("summary item[0] = %+v", summary.Items[0])
	}
	if summary.Items[205].KeyID != string(staged[205].Identity.ID) || summary.Items[205].Calls != 1 ||
		summary.Items[205].Coverage.State == "unavailable" || summary.Items[205].Coverage.Reason == "identity_unmapped" {
		t.Fatalf("summary item[205] = %+v", summary.Items[205])
	}

	// Second cross-runtime handoff via StageAdoptedTx (existing-row path) preserves all prior windows.
	handoff2MS := handoffMS + 20_000
	secondCandidates := make([]Record, len(candidates))
	copy(secondCandidates, candidates)
	for i := range secondCandidates {
		secondCandidates[i].Identity.CreatedAtMS = handoff2MS + int64(i)
		secondCandidates[i].Identity.UpdatedAtMS = handoff2MS + int64(i)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rehanded, err := repo.StageAdoptedTx(ctx, tx, secondCandidates, "runtime-managed-2")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("StageAdoptedTx second runtime handoff: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(rehanded) != totalKeys || string(rehanded[0].Identity.ID) != preExistingID {
		t.Fatalf("second handoff key[0] = %+v", rehanded[0].Identity)
	}
	rows = bindingRows(t, db, preExistingID)
	if len(rows) != 3 ||
		!rows[0].retiredAtMS.Valid || rows[0].retiredAtMS.Int64 != handoffMS ||
		!rows[1].retiredAtMS.Valid || rows[1].retiredAtMS.Int64 != handoff2MS ||
		rows[2].runtimeIdentity != "runtime-managed-2" || rows[2].firstSeenAtMS != handoff2MS || rows[2].retiredAtMS.Valid {
		t.Fatalf("three-runtime binding chain = %+v", rows)
	}
	insertUsageEventForKeyTest(t, db, "ev-post-handoff2-1", handoff2MS+1_000, legacyHash, 5, 5, 10, 0)
	if _, err := projRepo.CatchUp(ctx, 500, handoff2MS+2_000); err != nil {
		t.Fatalf("CatchUp after second handoff: %v", err)
	}
	detailAfterSecond, err := usageSvc.Detail(ctx, identity.APIKeyID(preExistingID), summaryFromMS, clockMS)
	if err != nil || detailAfterSecond.Coverage.State != "complete" || detailAfterSecond.Totals == nil || detailAfterSecond.Totals.Calls != 5 {
		t.Fatalf("detail after second handoff = %+v (%v)", detailAfterSecond, err)
	}
}

func TestStageAdoptedEnforcesLifecycleMissingRecoveryRetiredRejectionAndConflictAtomicity(t *testing.T) {
	t.Run("monotonic_handoff_clamps_to_prior_last_seen_under_clock_skew", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		secret := "skewed-legacy-secret"
		hash := hashHex(secret)
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 10000, 10000)`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, '1', 10000, 20000, null)`,
			usageOtherID, hash); err != nil {
			t.Fatal(err)
		}
		cand := stagedRecord(t, strings.Repeat("1", 32), usageKeyID, secret)
		cand.Identity.CreatedAtMS = 15000
		cand.Identity.UpdatedAtMS = 15000

		staged, err := repo.StageAdopted(ctx, []Record{cand}, usageRuntime)
		if err != nil || len(staged) != 1 || string(staged[0].Identity.ID) != usageOtherID {
			t.Fatalf("StageAdopted skewed = %+v (%v)", staged, err)
		}
		rows := bindingRows(t, db, usageOtherID)
		if len(rows) != 2 || !rows[0].retiredAtMS.Valid || rows[0].retiredAtMS.Int64 != 20000 ||
			rows[1].firstSeenAtMS != 20000 || rows[1].retiredAtMS.Valid {
			t.Fatalf("skewed handoff bindings = %+v, want [10000, 20000) and [20000, null)", rows)
		}
		insertUsageEventForKeyTest(t, db, "ev-skew-before", 18000, hash, 1, 1, 2, 0)
		insertUsageEventForKeyTest(t, db, "ev-skew-at-clamp", 20000, hash, 1, 1, 2, 0)
		projRepo := projectionadapter.New(db)
		if _, err := projRepo.CatchUp(ctx, 50, 25000); err != nil {
			t.Fatal(err)
		}
		for _, ev := range []string{"ev-skew-before", "ev-skew-at-clamp"} {
			p, err := projRepo.GetProjectionByEventHash(ctx, ev)
			if err != nil || p == nil || p.APIKeyState != projectionports.StateMapped || p.APIKeyID == nil || *p.APIKeyID != usageOtherID {
				t.Fatalf("projection(%s) = %+v (%v)", ev, p, err)
			}
		}
	})

	t.Run("passive_missing_identity_recovers_while_retired_only_binding_refuses", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		// 1. Pre-existing passive missing identity (retired_at_ms IS NULL) recovers to active.
		missingSecret := "passive-missing-preexisting-secret"
		missingHash := hashHex(missingSecret)
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'missing', 1000, 2000)`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, '2', 1000, 2000, null)`,
			usageOtherID, missingHash); err != nil {
			t.Fatal(err)
		}
		candMissing := stagedRecord(t, strings.Repeat("2", 32), usageKeyID, missingSecret)
		candMissing.Identity.CreatedAtMS = 3000
		candMissing.Identity.UpdatedAtMS = 3000
		recovered, err := repo.StageAdopted(ctx, []Record{candMissing}, usageRuntime)
		if err != nil || len(recovered) != 1 {
			t.Fatalf("StageAdopted(missing preexisting) = %+v (%v)", recovered, err)
		}
		if string(recovered[0].Identity.ID) != usageOtherID || recovered[0].Identity.Lifecycle != identity.LifecycleActive || recovered[0].Identity.Revision != 2 {
			t.Fatalf("recovered preexisting identity = %+v, want active rev=2", recovered[0].Identity)
		}

		// 2. Existing staged key marked missing by passive snapshot also recovers to active on cross-runtime handoff.
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_identities set lifecycle = 'missing', revision = revision + 1, updated_at_ms = 3500 where id = ?`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}
		candExistingMissing := stagedRecord(t, strings.Repeat("3", 32), usageKeeper, missingSecret)
		candExistingMissing.Identity.CreatedAtMS = 4000
		candExistingMissing.Identity.UpdatedAtMS = 4000
		recoveredExisting, err := repo.StageAdopted(ctx, []Record{candExistingMissing}, "runtime-managed-2")
		if err != nil || len(recoveredExisting) != 1 || recoveredExisting[0].Identity.Lifecycle != identity.LifecycleActive || recoveredExisting[0].Identity.Revision != 4 {
			t.Fatalf("recovered existing missing identity = %+v (%v), want active rev=4", recoveredExisting, err)
		}

		// 3. Explicit retired binding (e.g. rotated-out old secret hash where identity remains active) must refuse as ErrRetired.
		rotatedIdentity := "33334444555566667777888899990000"
		retiredOldSecret := "rotated-out-retired-secret"
		activeNewSecret := "rotated-in-active-secret"
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 2, 'active', 1000, 2000)`,
			rotatedIdentity); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, '1', 1000, 1500, 2000)`,
			rotatedIdentity, hashHex(retiredOldSecret)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, '2', 2000, 2000, null)`,
			rotatedIdentity, hashHex(activeNewSecret)); err != nil {
			t.Fatal(err)
		}
		candRetiredHash := stagedRecord(t, strings.Repeat("4", 32), "44445555666677778888999900001111", retiredOldSecret)
		if _, err := repo.StageAdopted(ctx, []Record{candRetiredHash}, usageRuntime); !errors.Is(err, ErrRetired) {
			t.Fatalf("StageAdopted(retired binding with active identity) = %v, want ErrRetired", err)
		}
	})

	t.Run("empty_runtime_and_same_identity_different_hash_conflicts_return_typed_err_and_rollback", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		// Seed key #0 for cross-runtime handoff so we prove batch rollback restores its unretired legacy window.
		handoffSecret := "batch-rollback-legacy-key"
		handoffHash := hashHex(handoffSecret)
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 1000, 1000)`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, '1', 1000, 1000, null)`,
			usageOtherID, handoffHash); err != nil {
			t.Fatal(err)
		}

		// Ambiguous active bindings (two different canonical IDs owning the same hash) must fail even with runtimeIdentity == "".
		ambiguousSecret := "ambiguous-two-owners-secret"
		ambiguousHash := hashHex(ambiguousSecret)
		idA := "aaaa1111222233334444555566667777"
		idB := "bbbb1111222233334444555566667777"
		for idx, id := range []string{idA, idB} {
			if _, err := db.ExecContext(ctx,
				`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 1000, 1000)`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx,
				`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 1000, 1000, null)`,
				id, "rt-"+strconv.Itoa(idx), ambiguousHash); err != nil {
				t.Fatal(err)
			}
		}

		cand0 := stagedRecord(t, strings.Repeat("5", 32), usageKeyID, handoffSecret)
		cand1 := stagedRecord(t, strings.Repeat("6", 32), usageKeeper, "clean-sibling-secret")
		candAmbig := stagedRecord(t, strings.Repeat("7", 32), "cccc1111222233334444555566667777", ambiguousSecret)
		if _, err := repo.StageAdopted(ctx, []Record{cand0, cand1, candAmbig}, ""); !errors.Is(err, ErrSourceOwned) {
			t.Fatalf("StageAdopted(ambiguous hash, empty runtimeIdentity) = %v, want ErrSourceOwned", err)
		}

		// Same APIKeyID + target runtimeIdentity holding a DIFFERENT active hash must return typed ErrSourceOwned, not SQLite constraint error.
		sameIDDiffHashID := "dddd1111222233334444555566667777"
		candidateSecret := "same-id-candidate-secret"
		conflictingActiveSecret := "same-id-other-active-secret"
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 1000, 1000)`,
			sameIDDiffHashID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, 'legacy-rt', ?, 1000, 1000, null)`,
			sameIDDiffHashID, hashHex(candidateSecret)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 1000, 1000, null)`,
			sameIDDiffHashID, usageRuntime, hashHex(conflictingActiveSecret)); err != nil {
			t.Fatal(err)
		}

		candDiffHash := stagedRecord(t, strings.Repeat("8", 32), "eeee1111222233334444555566667777", candidateSecret)
		if _, err := repo.StageAdopted(ctx, []Record{cand0, cand1, candDiffHash}, usageRuntime); !errors.Is(err, ErrSourceOwned) {
			t.Fatalf("StageAdopted(same ID + runtime different active hash) = %v, want ErrSourceOwned", err)
		}

		// Verify full atomic rollback: key #0's legacy-rt binding is still active, and zero keys were staged.
		legacyRows := bindingRows(t, db, usageOtherID)
		if len(legacyRows) != 1 || legacyRows[0].runtimeIdentity != "legacy-rt" || legacyRows[0].retiredAtMS.Valid {
			t.Fatalf("failed batch mutated key #0 legacy binding: %+v", legacyRows)
		}
		if listed, err := repo.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("failed batch left staged keys: %+v (%v)", listed, err)
		}
		var siblingCount int
		if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_identities where id = ?`, usageKeeper).Scan(&siblingCount); err != nil || siblingCount != 0 {
			t.Fatalf("failed batch left sibling identity: count=%d err=%v", siblingCount, err)
		}
	})
}

func TestSaveOperationEnforcesMonotonicClockSkewOverflowAndHalfOpenHistoricalMapping(t *testing.T) {
	t.Run("backward_clock_skew_keeps_workspace_list_valid_and_preserves_first_seen_and_last_seen_events", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		secret := "cpamp_sk_" + strings.Repeat("1", 64)
		rec := stagedRecord(t, strings.Repeat("a", 32), usageKeyID, secret)
		rec.Identity.CreatedAtMS = 10_000
		rec.Identity.UpdatedAtMS = 10_000
		if _, err := repo.Create(ctx, rec, usageRuntime); err != nil {
			t.Fatalf("create target key: %v", err)
		}
		keeper := stagedRecord(t, strings.Repeat("b", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("2", 64))
		keeper.Identity.CreatedAtMS = 10_000
		keeper.Identity.UpdatedAtMS = 10_000
		if _, err := repo.Create(ctx, keeper, usageRuntime); err != nil {
			t.Fatalf("create keeper key: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_source_bindings set last_seen_at_ms = 20000 where api_key_id = ? and retired_at_ms is null`,
			usageKeyID); err != nil {
			t.Fatalf("advance last_seen_at_ms: %v", err)
		}

		insertUsageEventForKeyTest(t, db, "ev-at-first-seen", 10_000, rec.Hash, 10, 5, 15, 0)
		insertUsageEventForKeyTest(t, db, "ev-at-last-seen", 20_000, rec.Hash, 20, 10, 30, 0)
		projRepo := projectionadapter.New(db)
		if res, err := projRepo.CatchUp(ctx, 50, 20_500); err != nil || res.Pending || res.Processed != 2 {
			t.Fatalf("initial CatchUp = %+v (%v)", res, err)
		}

		op := Operation{
			ID:          strings.Repeat("c", 32),
			TargetID:    usageKeyID,
			KeeperID:    usageKeeper,
			Mode:        "revoke",
			RunID:       usageRuntime,
			Generation:  "1",
			Revision:    1,
			Outcome:     "pending",
			Steps:       []KeyStep{{Name: "revoke", State: "unattempted"}},
			CreatedAtMS: 10_000,
		}
		if err := repo.BeginOperation(ctx, op); err != nil {
			t.Fatalf("begin operation: %v", err)
		}
		op.Steps[0].State = "observed"
		op.Steps[0].Returned = true
		op.Outcome = "committed"
		// Simulate backward wall-clock step (< CreatedAtMS = 10000 and < LastSeenAtMS = 20000).
		op.CompletedAtMS = 9_000
		if err := repo.SaveOperation(ctx, op); err != nil {
			t.Fatalf("commit skewed operation: %v", err)
		}

		listed, err := repo.List(ctx)
		if err != nil || len(listed) != 2 {
			t.Fatalf("List after backward clock step = %+v (%v), want 2 valid rows", listed, err)
		}
		revoked, err := repo.ByID(ctx, identity.APIKeyID(usageKeyID))
		if err != nil || revoked == nil {
			t.Fatalf("ByID(revoked) = %+v (%v)", revoked, err)
		}
		if revoked.Identity.Lifecycle != identity.LifecycleSuperseded || revoked.Identity.Revision != 2 || revoked.Identity.UpdatedAtMS != 10_001 {
			t.Fatalf("revoked identity = %+v, want superseded rev=2 updatedAtMs=10001", revoked.Identity)
		}

		rows := bindingRows(t, db, usageKeyID)
		if len(rows) != 1 || !rows[0].retiredAtMS.Valid || rows[0].retiredAtMS.Int64 != 20_001 {
			t.Fatalf("revoked binding = %+v, want half-open [10000, 20001)", rows)
		}

		insertUsageEventForKeyTest(t, db, "ev-after-retire", 20_001, rec.Hash, 5, 5, 10, 0)
		if res, err := projRepo.CatchUp(ctx, 50, 21_000); err != nil || !res.Rebuilt || res.Pending {
			t.Fatalf("post-retire CatchUp = %+v (%v)", res, err)
		}
		for _, ev := range []string{"ev-at-first-seen", "ev-at-last-seen"} {
			p, err := projRepo.GetProjectionByEventHash(ctx, ev)
			if err != nil || p == nil || p.APIKeyState != projectionports.StateMapped || p.APIKeyID == nil || *p.APIKeyID != usageKeyID {
				t.Fatalf("projection(%s) = %+v (%v), want mapped to %s", ev, p, err, usageKeyID)
			}
		}
		afterProj, err := projRepo.GetProjectionByEventHash(ctx, "ev-after-retire")
		if err != nil || afterProj == nil || afterProj.APIKeyState != projectionports.StateStale || afterProj.APIKeyID != nil {
			t.Fatalf("projection(ev-after-retire) = %+v (%v), want stale/nil", afterProj, err)
		}
	})

	t.Run("same_millisecond_retirement_keeps_boundary_event_mapped", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		secret := "cpamp_sk_" + strings.Repeat("3", 64)
		rec := stagedRecord(t, strings.Repeat("d", 32), usageKeyID, secret)
		rec.Identity.CreatedAtMS = 5_000
		rec.Identity.UpdatedAtMS = 5_000
		if _, err := repo.Create(ctx, rec, usageRuntime); err != nil {
			t.Fatal(err)
		}
		insertUsageEventForKeyTest(t, db, "ev-same-ms", 5_000, rec.Hash, 4, 4, 8, 0)

		op := Operation{
			ID:          strings.Repeat("e", 32),
			TargetID:    usageKeyID,
			Mode:        "revoke",
			RunID:       usageRuntime,
			Generation:  "1",
			Revision:    1,
			Outcome:     "pending",
			Steps:       []KeyStep{{Name: "revoke", State: "unattempted"}},
			CreatedAtMS: 5_000,
		}
		if err := repo.BeginOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		op.Steps[0].State = "observed"
		op.Outcome = "committed"
		op.CompletedAtMS = 5_000
		if err := repo.SaveOperation(ctx, op); err != nil {
			t.Fatalf("same-ms SaveOperation: %v", err)
		}
		rows := bindingRows(t, db, usageKeyID)
		if len(rows) != 1 || !rows[0].retiredAtMS.Valid || rows[0].retiredAtMS.Int64 != 5_001 {
			t.Fatalf("same-ms retired binding = %+v, want [5000, 5001)", rows)
		}
		projRepo := projectionadapter.New(db)
		if _, err := projRepo.CatchUp(ctx, 50, 6_000); err != nil {
			t.Fatal(err)
		}
		p, err := projRepo.GetProjectionByEventHash(ctx, "ev-same-ms")
		if err != nil || p == nil || p.APIKeyState != projectionports.StateMapped || p.APIKeyID == nil || *p.APIKeyID != usageKeyID {
			t.Fatalf("same-ms projection = %+v (%v), want mapped", p, err)
		}
	})

	t.Run("revision_and_timestamp_overflow_fail_closed_and_superseded_never_resurrects", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		rec := stagedRecord(t, strings.Repeat("1", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("4", 64))
		rec.Identity.CreatedAtMS = 1_000
		rec.Identity.UpdatedAtMS = 1_000
		if _, err := repo.Create(ctx, rec, usageRuntime); err != nil {
			t.Fatal(err)
		}
		op := Operation{
			ID:          strings.Repeat("2", 32),
			TargetID:    usageKeyID,
			Mode:        "revoke",
			RunID:       usageRuntime,
			Generation:  "1",
			Revision:    1,
			Outcome:     "pending",
			Steps:       []KeyStep{{Name: "revoke", State: "unattempted"}},
			CreatedAtMS: 1_000,
		}
		if err := repo.BeginOperation(ctx, op); err != nil {
			t.Fatal(err)
		}

		// 1. Revision overflow fails closed and rolls back journal update.
		if _, err := db.ExecContext(ctx, `update gateway_api_key_identities set revision = ? where id = ?`, int64(math.MaxInt64), usageKeyID); err != nil {
			t.Fatal(err)
		}
		commitAttempt := op
		commitAttempt.Steps = []KeyStep{{Name: "revoke", State: "observed"}}
		commitAttempt.Outcome = "committed"
		commitAttempt.CompletedAtMS = 2_000
		if err := repo.SaveOperation(ctx, commitAttempt); err == nil {
			t.Fatal("SaveOperation succeeded at MaxInt64 revision")
		}
		storedOp, err := repo.Operation(ctx, op.ID)
		if err != nil || storedOp == nil || storedOp.Outcome != "pending" || storedOp.Revision != 1 {
			t.Fatalf("failed revision-overflow SaveOperation mutated journal: %+v (%v)", storedOp, err)
		}

		// 2. Timestamp overflow fails closed and rolls back journal update.
		if _, err := db.ExecContext(ctx, `update gateway_api_key_identities set revision = 1, updated_at_ms = ? where id = ?`, int64(math.MaxInt64), usageKeyID); err != nil {
			t.Fatal(err)
		}
		commitAttempt.CompletedAtMS = math.MaxInt64
		if err := repo.SaveOperation(ctx, commitAttempt); err == nil {
			t.Fatal("SaveOperation succeeded at MaxInt64 updated_at_ms")
		}

		// 3. Normal commit succeeds once valid, and subsequent add/revoke against superseded fails closed.
		if _, err := db.ExecContext(ctx, `update gateway_api_key_identities set updated_at_ms = 1500 where id = ?`, usageKeyID); err != nil {
			t.Fatal(err)
		}
		commitAttempt.CompletedAtMS = 2_000
		if err := repo.SaveOperation(ctx, commitAttempt); err != nil {
			t.Fatalf("valid commit failed: %v", err)
		}
		resurrectAdd := Operation{
			ID:            strings.Repeat("3", 32),
			TargetID:      usageKeyID,
			Mode:          "add",
			RunID:         usageRuntime,
			Generation:    "1",
			Revision:      1,
			Outcome:       "committed",
			Steps:         []KeyStep{{Name: "add", State: "observed"}},
			CreatedAtMS:   3_000,
			CompletedAtMS: 3_000,
		}
		if err := repo.BeginOperation(ctx, resurrectAdd); !errors.Is(err, ErrConflict) {
			t.Fatalf("BeginOperation(add on superseded) = %v, want ErrConflict", err)
		}
	})
}

func TestAbortedOperationPreservesCanonicalIdentitiesAndCommitRecoversMissingKeeper(t *testing.T) {
	t.Run("aborted_unattempted_operation_is_terminal_without_superseding_target_and_attempted_cannot_abort", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		recTarget := stagedRecord(t, strings.Repeat("a", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("5", 64))
		recTarget.Hash = hashHex("cpamp_sk_" + strings.Repeat("5", 64))
		recTarget.Preview = "cpamp_sk_…5555"
		recTarget.Origin = "workspace"
		if _, err := repo.Create(ctx, recTarget, usageRuntime); err != nil {
			t.Fatalf("create target: %v", err)
		}
		recKeeper := stagedRecord(t, strings.Repeat("b", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("6", 64))
		recKeeper.Hash = hashHex("cpamp_sk_" + strings.Repeat("6", 64))
		recKeeper.Preview = "cpamp_sk_…6666"
		recKeeper.Origin = "workspace"
		if _, err := repo.Create(ctx, recKeeper, usageRuntime); err != nil {
			t.Fatalf("create keeper: %v", err)
		}

		directAbort := Operation{
			ID: strings.Repeat("1", 32), TargetID: usageKeyID, KeeperID: usageKeeper, Mode: "revoke",
			RunID: usageRuntime, Generation: "1", Revision: 1, Outcome: OutcomeAborted,
			Steps: []KeyStep{{Name: "revoke", State: "unattempted"}}, CreatedAtMS: 1_000, CompletedAtMS: 1_000,
		}
		if err := repo.BeginOperation(ctx, directAbort); !errors.Is(err, ErrConflict) {
			t.Fatalf("BeginOperation(OutcomeAborted) = %v, want ErrConflict", err)
		}

		unattemptedOp := Operation{
			ID: strings.Repeat("2", 32), TargetID: usageKeyID, KeeperID: usageKeeper, Mode: "revoke",
			RunID: usageRuntime, Generation: "1", Revision: 1, Outcome: OutcomePending,
			Steps: []KeyStep{{Name: "revoke", State: "unattempted"}}, CreatedAtMS: 1_000,
		}
		if err := repo.BeginOperation(ctx, unattemptedOp); err != nil {
			t.Fatalf("BeginOperation(pending): %v", err)
		}

		abortedOp := unattemptedOp
		abortedOp.Outcome = OutcomeAborted
		abortedOp.CompletedAtMS = 1_500
		if err := repo.SaveOperation(ctx, abortedOp); err != nil {
			t.Fatalf("SaveOperation(aborted): %v", err)
		}

		saved, err := repo.LatestOperation(ctx)
		if err != nil || saved == nil {
			t.Fatalf("LatestOperation = %+v (%v)", saved, err)
		}
		if !saved.IsTerminal() || saved.Outcome != OutcomeAborted || !saved.AllStepsUnattempted() {
			t.Fatalf("saved aborted op = %+v, want terminal OutcomeAborted with all steps unattempted", saved)
		}

		// Once terminal, further mutations fail closed.
		mutateAfterAbort := *saved
		mutateAfterAbort.Outcome = OutcomeCommitted
		mutateAfterAbort.Steps = []KeyStep{{Name: "revoke", State: "observed"}}
		if err := repo.SaveOperation(ctx, mutateAfterAbort); !errors.Is(err, ErrConflict) {
			t.Fatalf("SaveOperation after abort = %v, want ErrConflict", err)
		}

		// Target canonical identity and source binding remain active — never falsely superseded.
		detail, err := repo.ByID(ctx, identity.APIKeyID(usageKeyID))
		if err != nil || detail == nil {
			t.Fatalf("ByID(target) = %+v (%v)", detail, err)
		}
		if detail.Identity.Lifecycle != identity.LifecycleActive {
			t.Fatalf("target lifecycle after abort = %q, want active", detail.Identity.Lifecycle)
		}
		targetBindings := bindingRows(t, db, usageKeyID)
		if len(targetBindings) != 1 || targetBindings[0].retiredAtMS.Valid {
			t.Fatalf("target bindings after abort = %+v, want unretired active window", targetBindings)
		}

		// Attempted/unknown operations cannot transition to OutcomeAborted.
		attemptedOp := Operation{
			ID: strings.Repeat("3", 32), TargetID: usageKeyID, KeeperID: usageKeeper, Mode: "revoke",
			RunID: usageRuntime, Generation: "1", Revision: 1, Outcome: OutcomeUnknown,
			Steps: []KeyStep{{Name: "revoke", State: "attempted"}}, CreatedAtMS: 2_000,
		}
		if err := repo.BeginOperation(ctx, attemptedOp); err != nil {
			t.Fatalf("BeginOperation(attempted after aborted): %v", err)
		}
		forgedAbort := attemptedOp
		forgedAbort.Outcome = OutcomeAborted
		forgedAbort.Steps = []KeyStep{{Name: "revoke", State: "unattempted"}}
		forgedAbort.CompletedAtMS = 2_500
		if err := repo.SaveOperation(ctx, forgedAbort); !errors.Is(err, ErrConflict) {
			t.Fatalf("SaveOperation(abort attempted op) = %v, want ErrConflict", err)
		}
	})

	t.Run("replace_commit_recovers_missing_keeper_and_refuses_superseded_keeper", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		recTarget := stagedRecord(t, strings.Repeat("a", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("7", 64))
		recTarget.Hash = hashHex("cpamp_sk_" + strings.Repeat("7", 64))
		recTarget.Preview = "cpamp_sk_…7777"
		recTarget.Origin = "workspace"
		if _, err := repo.Create(ctx, recTarget, usageRuntime); err != nil {
			t.Fatalf("create target: %v", err)
		}
		recKeeper := stagedRecord(t, strings.Repeat("b", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("8", 64))
		recKeeper.Hash = hashHex("cpamp_sk_" + strings.Repeat("8", 64))
		recKeeper.Preview = "cpamp_sk_…8888"
		recKeeper.Origin = "workspace"
		if _, err := repo.Create(ctx, recKeeper, usageRuntime); err != nil {
			t.Fatalf("create keeper: %v", err)
		}
		if _, err := db.ExecContext(ctx, `update gateway_api_key_identities set lifecycle = 'missing' where id = ?`, usageKeeper); err != nil {
			t.Fatalf("seed missing keeper: %v", err)
		}

		replaceOp := Operation{
			ID: strings.Repeat("4", 32), TargetID: usageKeyID, KeeperID: usageKeeper, Mode: "replace",
			RunID: usageRuntime, Generation: "1", Revision: 1, Outcome: OutcomeCommitted,
			Steps:       []KeyStep{{Name: "add", State: "observed"}, {Name: "revoke", State: "observed"}},
			CreatedAtMS: 2_000, CompletedAtMS: 2_500,
		}
		if err := repo.BeginOperation(ctx, replaceOp); err != nil {
			t.Fatalf("BeginOperation(replace commit): %v", err)
		}
		keeperDetail, err := repo.ByID(ctx, identity.APIKeyID(usageKeeper))
		if err != nil || keeperDetail == nil {
			t.Fatalf("ByID(keeper) = %+v (%v)", keeperDetail, err)
		}
		if keeperDetail.Identity.Lifecycle != identity.LifecycleActive {
			t.Fatalf("keeper lifecycle after replace commit = %q, want active", keeperDetail.Identity.Lifecycle)
		}
		targetDetail, err := repo.ByID(ctx, identity.APIKeyID(usageKeyID))
		if err != nil || targetDetail == nil {
			t.Fatalf("ByID(target) = %+v (%v)", targetDetail, err)
		}
		if targetDetail.Identity.Lifecycle != identity.LifecycleSuperseded {
			t.Fatalf("target lifecycle after replace commit = %q, want superseded", targetDetail.Identity.Lifecycle)
		}

		// Attempting to use the now-superseded target as KeeperID must fail closed.
		badKeeperOp := Operation{
			ID: strings.Repeat("5", 32), TargetID: usageKeeper, KeeperID: usageKeyID, Mode: "revoke",
			RunID: usageRuntime, Generation: "1", Revision: 1, Outcome: OutcomeCommitted,
			Steps: []KeyStep{{Name: "revoke", State: "observed"}}, CreatedAtMS: 3_000, CompletedAtMS: 3_500,
		}
		if err := repo.BeginOperation(ctx, badKeeperOp); !errors.Is(err, ErrConflict) {
			t.Fatalf("BeginOperation(superseded keeper) = %v, want ErrConflict", err)
		}
	})
}

func hashHex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
