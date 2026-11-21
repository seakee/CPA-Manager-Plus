package freshkeys_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func openRepoTestDB(t *testing.T) (*sql.DB, *freshkeys.Repository, *security.Protector) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "freshkeys-rotation.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	protector, err := security.NewProtector([]byte("freshkeys-rotation-test-data-key"))
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}
	return db, freshkeys.New(db), protector
}

func testDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func TestCreatePersistsNameAndAdoptedBackfillsAliases(t *testing.T) {
	ctx := t.Context()
	db, repo, protector := openRepoTestDB(t)

	keyID := identity.APIKeyID(strings.Repeat("a", 32))
	reqID := strings.Repeat("1", 32)
	secret := model.WorkspaceClientKeyPrefix + strings.Repeat("b", 64)
	enc, err := protector.ProtectString(secret)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.Create(ctx, freshkeys.Record{
		RequestID: reqID,
		Identity: identity.APIKeyIdentity{
			ID:          keyID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		},
		Ciphertext:   enc,
		Hash:         testDigest(secret),
		Preview:      model.MaskedClientKey(secret),
		Name:         "Production Gateway",
		NameRevision: 1,
		Origin:       freshkeys.OriginWorkspace,
	}, "managed-runtime-1")
	if err != nil || !created {
		t.Fatalf("create named record = %v, %v", created, err)
	}

	got, err := repo.ByID(ctx, keyID)
	if err != nil || got == nil || got.Name != "Production Gateway" || got.NameRevision != 1 {
		t.Fatalf("ByID after named create = %+v, err=%v", got, err)
	}

	// Seed api_key_aliases rows (uppercase digest, whitespace + control char + >120 runes, and pre-bound upgrade identity).
	adoptedSecret1 := "legacy-v1-secret-alpha"
	adoptedHash1 := testDigest(adoptedSecret1)
	adoptedEnc1, _ := protector.ProtectString(adoptedSecret1)

	adoptedSecret2 := "legacy-v1-secret-prebound"
	adoptedHash2 := testDigest(adoptedSecret2)
	adoptedEnc2, _ := protector.ProtectString(adoptedSecret2)
	preBoundID := identity.APIKeyID(strings.Repeat("c", 32))

	longAlias := "  Legacy\tKey " + strings.Repeat("界", 130) + "  "
	if _, err := db.ExecContext(ctx, `insert into api_key_aliases (api_key_hash, alias, updated_at_ms) values (?, ?, 1500), (?, ?, 1501)`,
		strings.ToUpper(adoptedHash1), longAlias,
		adoptedHash2, "  Upgraded Primary  "); err != nil {
		t.Fatalf("seed api_key_aliases: %v", err)
	}
	if _, err := db.ExecContext(ctx, `insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
		values (?, 1, 'active', 1200, 1200)`, string(preBoundID)); err != nil {
		t.Fatalf("seed prebound identity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) values (?, 'managed-runtime-1', ?, '1', 1200, 1250, null)`, string(preBoundID), adoptedHash2); err != nil {
		t.Fatalf("seed prebound binding: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repo.StageAdoptedTx(ctx, tx, []freshkeys.Record{
		{
			RequestID: strings.Repeat("2", 32),
			Identity: identity.APIKeyIdentity{
				ID:          identity.APIKeyID(strings.Repeat("d", 32)),
				Revision:    1,
				Lifecycle:   identity.LifecycleActive,
				CreatedAtMS: 2000,
				UpdatedAtMS: 2000,
			},
			Ciphertext: adoptedEnc1,
			Hash:       adoptedHash1,
			Preview:    model.MaskedImportedClientKey(adoptedSecret1),
			Origin:     freshkeys.OriginAdopted,
		},
		{
			RequestID: strings.Repeat("3", 32),
			Identity: identity.APIKeyIdentity{
				ID:          identity.APIKeyID(strings.Repeat("e", 32)),
				Revision:    1,
				Lifecycle:   identity.LifecycleActive,
				CreatedAtMS: 2001,
				UpdatedAtMS: 2001,
			},
			Ciphertext: adoptedEnc2,
			Hash:       adoptedHash2,
			Preview:    model.MaskedImportedClientKey(adoptedSecret2),
			Origin:     freshkeys.OriginAdopted,
		},
	}, "managed-runtime-1")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("StageAdoptedTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved len = %d, want 2", len(resolved))
	}
	if resolved[0].NameRevision != 2 || len([]rune(resolved[0].Name)) != 120 || strings.Contains(resolved[0].Name, "\t") || !strings.HasPrefix(resolved[0].Name, "LegacyKey ") {
		t.Fatalf("adopted[0] sanitized name = %q rev=%d", resolved[0].Name, resolved[0].NameRevision)
	}
	if resolved[1].Identity.ID != preBoundID || resolved[1].Name != "Upgraded Primary" || resolved[1].NameRevision != 2 {
		t.Fatalf("adopted[1] prebound = %+v", resolved[1])
	}
}

func TestStageCommitAndAbortOptionBRotationInvariants(t *testing.T) {
	ctx := t.Context()
	db, repo, protector := openRepoTestDB(t)

	keyID := identity.APIKeyID(strings.Repeat("a", 32))
	createReqID := strings.Repeat("1", 32)
	oldSecret := model.WorkspaceClientKeyPrefix + strings.Repeat("0", 64)
	oldHash := testDigest(oldSecret)
	oldEnc, _ := protector.ProtectString(oldSecret)

	created, err := repo.Create(ctx, freshkeys.Record{
		RequestID: createReqID,
		Identity: identity.APIKeyIdentity{
			ID:          keyID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		},
		Ciphertext:   oldEnc,
		Hash:         oldHash,
		Preview:      model.MaskedClientKey(oldSecret),
		Name:         "Primary Service Key",
		NameRevision: 3,
		Origin:       freshkeys.OriginWorkspace,
	}, "managed-runtime-1")
	if err != nil || !created {
		t.Fatalf("create key: %v %v", created, err)
	}
	// Advance last_seen_at_ms on active binding beyond planned completedAtMS to verify floor+1 monotonic handoff.
	if _, err := db.ExecContext(ctx, `update gateway_api_key_source_bindings set observed_runtime_generation = '4', last_seen_at_ms = 5000 where api_key_id = ?`, string(keyID)); err != nil {
		t.Fatal(err)
	}

	// Stage an unattempted rotation and abort it; candidate must be deleted atomically.
	abortOpID := strings.Repeat("2", 32)
	abortSecret := model.WorkspaceClientKeyPrefix + strings.Repeat("1", 64)
	abortEnc, _ := protector.ProtectString(abortSecret)
	_, _, stagedAbort, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     abortOpID,
		APIKeyID:      keyID,
		NewCiphertext: abortEnc,
		NewHash:       testDigest(abortSecret),
		NewPreview:    model.MaskedClientKey(abortSecret),
		CreatedAtMS:   2000,
	})
	if err != nil || !stagedAbort {
		t.Fatalf("stage abortable rotation: %v %v", stagedAbort, err)
	}
	abortOp := freshkeys.Operation{
		ID:          abortOpID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 2000,
	}
	if err := repo.BeginOperation(ctx, abortOp); err != nil {
		t.Fatalf("BeginOperation abortable: %v", err)
	}
	abortOp.Outcome = freshkeys.OutcomeAborted
	abortOp.CompletedAtMS = 2100
	if err := repo.SaveOperation(ctx, abortOp); err != nil {
		t.Fatalf("SaveOperation aborted: %v", err)
	}
	if rotAfterAbort, err := repo.RotationByRequestID(ctx, abortOpID); err != nil || rotAfterAbort != nil {
		t.Fatalf("aborted rotation candidate lingered: %+v err=%v", rotAfterAbort, err)
	}

	// Now stage and commit a real rotation.
	rotOpID := strings.Repeat("3", 32)
	newSecret := model.WorkspaceClientKeyPrefix + strings.Repeat("9", 64)
	newHash := testDigest(newSecret)
	newPreview := model.MaskedClientKey(newSecret)
	newEnc, _ := protector.ProtectString(newSecret)

	_, _, stagedCreated, committed, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     rotOpID,
		APIKeyID:      keyID,
		NewCiphertext: newEnc,
		NewHash:       newHash,
		NewPreview:    newPreview,
		CreatedAtMS:   3000,
	})
	if err != nil || !stagedCreated || committed {
		t.Fatalf("StageRotation = created:%v committed:%v err:%v", stagedCreated, committed, err)
	}

	// Competing rotation request on same key while unsettled must fail with ErrConflict.
	otherSecret := model.WorkspaceClientKeyPrefix + strings.Repeat("8", 64)
	otherEnc, _ := protector.ProtectString(otherSecret)
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     strings.Repeat("4", 32),
		APIKeyID:      keyID,
		NewCiphertext: otherEnc,
		NewHash:       testDigest(otherSecret),
		NewPreview:    model.MaskedClientKey(otherSecret),
		CreatedAtMS:   3001,
	}); !errors.Is(err, freshkeys.ErrConflict) {
		t.Fatalf("competing StageRotation err = %v, want ErrConflict", err)
	}

	op := freshkeys.Operation{
		ID:          rotOpID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 3000,
	}
	if err := repo.BeginOperation(ctx, op); err != nil {
		t.Fatalf("BeginOperation rotate: %v", err)
	}
	op.Steps[0].State = "observed"
	op.Steps[0].Returned = true
	op.Outcome = freshkeys.OutcomeCommitted
	op.CompletedAtMS = 3500
	if err := repo.SaveOperation(ctx, op); err != nil {
		t.Fatalf("SaveOperation rotate commit: %v", err)
	}

	// Candidate row must be consumed and deleted.
	if rotAfterCommit, err := repo.RotationByRequestID(ctx, rotOpID); err != nil || rotAfterCommit != nil {
		t.Fatalf("committed rotation candidate lingered: %+v err=%v", rotAfterCommit, err)
	}

	// Active record preserves ID, RequestID, Name, NameRevision, Origin, CreatedAtMS while updating secret & LastRotatedAtMS.
	updated, err := repo.ByID(ctx, keyID)
	if err != nil || updated == nil {
		t.Fatalf("ByID after rotate: %+v %v", updated, err)
	}
	if updated.Identity.ID != keyID ||
		updated.RequestID != createReqID ||
		updated.Name != "Primary Service Key" ||
		updated.NameRevision != 3 ||
		updated.Origin != freshkeys.OriginWorkspace ||
		updated.Identity.CreatedAtMS != 1000 ||
		updated.Identity.Lifecycle != identity.LifecycleActive ||
		updated.Identity.Revision != 2 ||
		updated.Hash != newHash ||
		updated.Preview != newPreview ||
		updated.LastRotatedAtMS != 3500 ||
		updated.RevokedAtMS != 0 {
		t.Fatalf("updated record mismatch: %+v", updated)
	}

	// Old secret hash is in fresh_client_api_key_secret_history and ByHash reports it retired; Create rejects collision with ErrRetired.
	retiredByHash, err := repo.ByHash(ctx, oldHash)
	if err != nil || retiredByHash == nil || retiredByHash.Identity.Lifecycle != identity.LifecycleSuperseded {
		t.Fatalf("ByHash(oldHash) = %+v, err=%v", retiredByHash, err)
	}
	if _, err := repo.Create(ctx, freshkeys.Record{
		RequestID: strings.Repeat("5", 32),
		Identity: identity.APIKeyIdentity{
			ID:          identity.APIKeyID(strings.Repeat("f", 32)),
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 4000,
			UpdatedAtMS: 4000,
		},
		Ciphertext:   oldEnc,
		Hash:         oldHash,
		Preview:      model.MaskedClientKey(oldSecret),
		NameRevision: 1,
		Origin:       freshkeys.OriginWorkspace,
	}, ""); !errors.Is(err, freshkeys.ErrRetired) {
		t.Fatalf("Create with retired rotated hash = %v, want ErrRetired", err)
	}

	// Source bindings handoff: old binding retired at 5001 (max(3500, 5000+1)), new active binding opened at 5001 with preserved observed_runtime_generation '4'.
	var (
		oldRetired sql.NullInt64
		newFirst   int64
		newGen     sql.NullString
		newRetired sql.NullInt64
	)
	if err := db.QueryRowContext(ctx, `select retired_at_ms from gateway_api_key_source_bindings where api_key_id = ? and api_key_hash = ?`, string(keyID), oldHash).Scan(&oldRetired); err != nil || !oldRetired.Valid || oldRetired.Int64 != 5001 {
		t.Fatalf("old binding retired_at_ms = %+v, err=%v, want 5001", oldRetired, err)
	}
	if err := db.QueryRowContext(ctx, `select first_seen_at_ms, observed_runtime_generation, retired_at_ms from gateway_api_key_source_bindings where api_key_id = ? and api_key_hash = ?`, string(keyID), newHash).Scan(&newFirst, &newGen, &newRetired); err != nil || newFirst != 3000 || !newGen.Valid || newGen.String != "4" || newRetired.Valid {
		t.Fatalf("new binding = first:%d gen:%+v retired:%+v err:%v, want first=3000", newFirst, newGen, newRetired, err)
	}

	// Replaying StageRotation with committed requestID reports committed=true and created=false.
	_, replayTarget, replayCreated, replayCommitted, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     rotOpID,
		APIKeyID:      keyID,
		NewCiphertext: otherEnc,
		NewHash:       testDigest(otherSecret),
		NewPreview:    model.MaskedClientKey(otherSecret),
		CreatedAtMS:   6000,
	})
	if err != nil || replayCreated || !replayCommitted || replayTarget.Hash != newHash {
		t.Fatalf("replay StageRotation = created:%v committed:%v hash:%s err:%v", replayCreated, replayCommitted, replayTarget.Hash, err)
	}
}

func TestSequentialMultiRotationPreservesPerOperationHistoryAndBlocksRetiredReuse(t *testing.T) {
	ctx := t.Context()
	_, repo, p := openRepoTestDB(t)

	keyID, _ := identity.NewAPIKeyID()
	secA := model.WorkspaceClientKeyPrefix + strings.Repeat("1", 64)
	secB := model.WorkspaceClientKeyPrefix + strings.Repeat("2", 64)
	secC := model.WorkspaceClientKeyPrefix + strings.Repeat("3", 64)
	hashA, hashB, hashC := testDigest(secA), testDigest(secB), testDigest(secC)
	encA, _ := p.ProtectString(secA)
	encB, _ := p.ProtectString(secB)
	encC, _ := p.ProtectString(secC)

	if _, err := repo.Create(ctx, freshkeys.Record{
		RequestID: strings.Repeat("a", 32),
		Identity: identity.APIKeyIdentity{
			ID:          keyID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		},
		Ciphertext:   encA,
		Hash:         hashA,
		Preview:      model.MaskedClientKey(secA),
		Name:         "Multi Rotate Key",
		NameRevision: 1,
		Origin:       freshkeys.OriginWorkspace,
	}, ""); err != nil {
		t.Fatalf("Create A: %v", err)
	}

	// Rotate 1: A -> B
	op1ID := strings.Repeat("b", 32)
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     op1ID,
		APIKeyID:      keyID,
		NewCiphertext: encB,
		NewHash:       hashB,
		NewPreview:    model.MaskedClientKey(secB),
		CreatedAtMS:   2000,
	}); err != nil {
		t.Fatalf("StageRotation 1: %v", err)
	}
	op1 := freshkeys.Operation{
		ID:          op1ID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 2000,
	}
	if err := repo.BeginOperation(ctx, op1); err != nil {
		t.Fatalf("BeginOperation 1: %v", err)
	}
	op1.Outcome = freshkeys.OutcomeCommitted
	op1.CompletedAtMS = 2500
	op1.Steps[0].State = "observed"
	op1.Steps[0].Returned = true
	if err := repo.SaveOperation(ctx, op1); err != nil {
		t.Fatalf("SaveOperation 1: %v", err)
	}

	// Rotate 2: B -> C
	op2ID := strings.Repeat("c", 32)
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     op2ID,
		APIKeyID:      keyID,
		NewCiphertext: encC,
		NewHash:       hashC,
		NewPreview:    model.MaskedClientKey(secC),
		CreatedAtMS:   3000,
	}); err != nil {
		t.Fatalf("StageRotation 2: %v", err)
	}
	op2 := freshkeys.Operation{
		ID:          op2ID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 3000,
	}
	if err := repo.BeginOperation(ctx, op2); err != nil {
		t.Fatalf("BeginOperation 2: %v", err)
	}
	op2.Outcome = freshkeys.OutcomeCommitted
	op2.CompletedAtMS = 3500
	op2.Steps[0].State = "observed"
	op2.Steps[0].Returned = true
	if err := repo.SaveOperation(ctx, op2); err != nil {
		t.Fatalf("SaveOperation 2: %v", err)
	}

	// Per-operation history lookup preserves exact A->B for op1 even after op2 committed B->C.
	hist1, err := repo.SecretHistoryByRequestID(ctx, op1ID)
	if err != nil || hist1 == nil || hist1.Hash != hashA || hist1.NextHash != hashB {
		t.Fatalf("SecretHistoryByRequestID(op1) = %+v, err=%v, want A->B", hist1, err)
	}
	hist2, err := repo.SecretHistoryByRequestID(ctx, op2ID)
	if err != nil || hist2 == nil || hist2.Hash != hashB || hist2.NextHash != hashC {
		t.Fatalf("SecretHistoryByRequestID(op2) = %+v, err=%v, want B->C", hist2, err)
	}

	// Neither retired hash A nor retired hash B can be reused in StageRotation.
	for _, retired := range []struct {
		hash string
		enc  string
	}{{hashA, encA}, {hashB, encB}} {
		if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
			RequestID:     strings.Repeat("d", 32),
			APIKeyID:      keyID,
			NewCiphertext: retired.enc,
			NewHash:       retired.hash,
			NewPreview:    "cpamp_sk_…0000",
			CreatedAtMS:   4000,
		}); !errors.Is(err, freshkeys.ErrRetired) {
			t.Fatalf("StageRotation with retired hash %s = %v, want ErrRetired", retired.hash, err)
		}
	}
}

func TestDiscardOrphanedRotationSweepsUnjournaledRowWhilePreservingJournaledCandidate(t *testing.T) {
	ctx := t.Context()
	_, repo, p := openRepoTestDB(t)

	keyID, _ := identity.NewAPIKeyID()
	secA := model.WorkspaceClientKeyPrefix + strings.Repeat("4", 64)
	secB := model.WorkspaceClientKeyPrefix + strings.Repeat("5", 64)
	secC := model.WorkspaceClientKeyPrefix + strings.Repeat("6", 64)
	encA, _ := p.ProtectString(secA)
	encB, _ := p.ProtectString(secB)
	encC, _ := p.ProtectString(secC)

	if _, err := repo.Create(ctx, freshkeys.Record{
		RequestID: strings.Repeat("1", 32),
		Identity: identity.APIKeyIdentity{
			ID:          keyID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		},
		Ciphertext:   encA,
		Hash:         testDigest(secA),
		Preview:      model.MaskedClientKey(secA),
		Name:         "Orphan Sweep Key",
		NameRevision: 1,
		Origin:       freshkeys.OriginWorkspace,
	}, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 1. Stage R1 without BeginOperation (simulating crash before journal write).
	r1 := strings.Repeat("2", 32)
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     r1,
		APIKeyID:      keyID,
		NewCiphertext: encB,
		NewHash:       testDigest(secB),
		NewPreview:    model.MaskedClientKey(secB),
		CreatedAtMS:   2000,
	}); err != nil {
		t.Fatalf("StageRotation R1: %v", err)
	}
	if has, err := repo.HasStagedRotation(ctx, keyID); err != nil || !has {
		t.Fatalf("HasStagedRotation after R1 = %v, %v, want true", has, err)
	}

	// DiscardOrphanedRotation sweeps unjournaled R1 cleanly.
	r2 := strings.Repeat("3", 32)
	if err := repo.DiscardOrphanedRotation(ctx, r2, keyID); err != nil {
		t.Fatalf("DiscardOrphanedRotation(R2, keyID): %v", err)
	}
	if has, err := repo.HasStagedRotation(ctx, keyID); err != nil || has {
		t.Fatalf("HasStagedRotation after sweep = %v, %v, want false", has, err)
	}

	// 2. Stage R2 WITH BeginOperation: DiscardOrphanedRotation must preserve journaled R2.
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     r2,
		APIKeyID:      keyID,
		NewCiphertext: encC,
		NewHash:       testDigest(secC),
		NewPreview:    model.MaskedClientKey(secC),
		CreatedAtMS:   3000,
	}); err != nil {
		t.Fatalf("StageRotation R2: %v", err)
	}
	if err := repo.BeginOperation(ctx, freshkeys.Operation{
		ID:          r2,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 3000,
	}); err != nil {
		t.Fatalf("BeginOperation R2: %v", err)
	}
	if err := repo.DiscardOrphanedRotation(ctx, strings.Repeat("4", 32), ""); err != nil {
		t.Fatalf("DiscardOrphanedRotation global sweep: %v", err)
	}
	if has, err := repo.HasStagedRotation(ctx, keyID); err != nil || !has {
		t.Fatalf("HasStagedRotation for journaled R2 = %v, %v, want true", has, err)
	}
}

func TestBindFreshKeySourcesBackfillsRetiredHistorySegmentsWhenRotatedBeforeFirstBind(t *testing.T) {
	ctx := t.Context()
	db, repo, p := openRepoTestDB(t)

	keyID, _ := identity.NewAPIKeyID()
	sec1 := model.WorkspaceClientKeyPrefix + strings.Repeat("7", 64)
	sec2 := model.WorkspaceClientKeyPrefix + strings.Repeat("8", 64)
	enc1, _ := p.ProtectString(sec1)
	enc2, _ := p.ProtectString(sec2)
	hash1 := testDigest(sec1)
	hash2 := testDigest(sec2)

	// Create key WITHOUT sourceRuntimeIdentity (simulating pre-ConnectExternal / monitoring-off staging).
	if _, err := repo.Create(ctx, freshkeys.Record{
		RequestID: strings.Repeat("5", 32),
		Identity: identity.APIKeyIdentity{
			ID:          keyID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		},
		Ciphertext:   enc1,
		Hash:         hash1,
		Preview:      model.MaskedClientKey(sec1),
		Name:         "Deferred Bind Rotated Key",
		NameRevision: 1,
		Origin:       freshkeys.OriginWorkspace,
	}, ""); err != nil {
		t.Fatalf("Create unbound: %v", err)
	}

	// Commit an in-place rotation at t=3000 BEFORE BindFreshKeySources ever runs.
	rotID := strings.Repeat("6", 32)
	if _, _, _, _, err := repo.StageRotation(ctx, freshkeys.RotationRecord{
		RequestID:     rotID,
		APIKeyID:      keyID,
		NewCiphertext: enc2,
		NewHash:       hash2,
		NewPreview:    model.MaskedClientKey(sec2),
		CreatedAtMS:   2500,
	}); err != nil {
		t.Fatalf("StageRotation: %v", err)
	}
	if err := repo.BeginOperation(ctx, freshkeys.Operation{
		ID:          rotID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomePending,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "unattempted"}},
		CreatedAtMS: 2500,
	}); err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	if err := repo.SaveOperation(ctx, freshkeys.Operation{
		ID:          rotID,
		TargetID:    string(keyID),
		Mode:        "rotate",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     freshkeys.OutcomeCommitted,
		Steps:       []freshkeys.KeyStep{{Name: "rotate", State: "observed", Returned: true}},
		CreatedAtMS: 2500,
		CompletedAtMS: 3000,
	}); err != nil {
		t.Fatalf("SaveOperation(Committed): %v", err)
	}

	// Now BindFreshKeySources runs at t=4000: must backfill retired [1000, 3000) for hash1 AND active [3000, 4000] for hash2!
	bound, err := repo.BindFreshKeySources(ctx, "external:https://cpa.example", 4000)
	if err != nil || bound != 1 {
		t.Fatalf("BindFreshKeySources = %d, %v, want 1, nil", bound, err)
	}

	var first1, last1 int64
	var retired1 *int64
	if err := db.QueryRowContext(ctx, `select first_seen_at_ms, last_seen_at_ms, retired_at_ms from gateway_api_key_source_bindings where api_key_hash = ?`, hash1).Scan(&first1, &last1, &retired1); err != nil {
		t.Fatalf("query backfilled hash1 binding: %v", err)
	}
	if first1 != 1000 || last1 != 3000 || retired1 == nil || *retired1 != 3000 {
		t.Fatalf("hash1 binding = [%d, %d, retired=%v], want [1000, 3000, retired=3000]", first1, last1, retired1)
	}

	var first2, last2 int64
	var retired2 *int64
	if err := db.QueryRowContext(ctx, `select first_seen_at_ms, last_seen_at_ms, retired_at_ms from gateway_api_key_source_bindings where api_key_hash = ?`, hash2).Scan(&first2, &last2, &retired2); err != nil {
		t.Fatalf("query active hash2 binding: %v", err)
	}
	if first2 != 3000 || last2 != 4000 || retired2 != nil {
		t.Fatalf("hash2 binding = [%d, %d, retired=%v], want [3000, 4000, retired=nil]", first2, last2, retired2)
	}
}
