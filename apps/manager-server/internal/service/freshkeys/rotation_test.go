package freshkeys

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func openServiceRotationTest(t *testing.T) (*Service, *repo.Repository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "freshkeys-service-rotation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("service-rotation-test-data-key!!"))
	if err != nil {
		t.Fatal(err)
	}
	r := repo.New(db)
	return New(r, protector), r
}

func TestServiceNamedCreateSettledRevokedAndRotatedOrigins(t *testing.T) {
	ctx := t.Context()
	svc, r := openServiceRotationTest(t)

	// Invalid names must fail with ErrRequest.
	for _, bad := range []string{"bad\nname", "null\x00char", strings.Repeat("a", 121), string([]byte{0xff})} {
		if _, _, err := svc.CreateNamed(ctx, strings.Repeat("1", 32), bad); !errors.Is(err, ErrRequest) {
			t.Fatalf("CreateNamed(%q) err = %v, want ErrRequest", bad, err)
		}
	}

	named, created, err := svc.CreateNamed(ctx, strings.Repeat("1", 32), "  Gateway Client A  ")
	if err != nil || !created || named.Key.Name != "Gateway Client A" || named.Key.NameRevision != 1 {
		t.Fatalf("CreateNamed = %+v, created=%v, err=%v", named, created, err)
	}

	// EnsureInitialKey + adopted key, then rotate BOTH to verify stagedSecretMatches accepts canonical cpamp_sk_ secrets on installer and adopted origins.
	initial, err := svc.EnsureInitialKey(ctx)
	if err != nil {
		t.Fatalf("EnsureInitialKey: %v", err)
	}
	adoptedKeys, err := svc.RegisterAdopted(ctx, []string{"legacy-imported-token-xyz"})
	if err != nil || len(adoptedKeys) != 1 {
		t.Fatalf("RegisterAdopted = %+v, err=%v", adoptedKeys, err)
	}

	for idx, targetID := range []string{string(initial.ID), string(adoptedKeys[0].ID), string(named.Key.ID)} {
		reqID := strings.Repeat(string(rune('a'+idx)), 32)
		staged, rotCreated, err := svc.StageRotate(ctx, reqID, targetID)
		if err != nil || !rotCreated || !strings.HasPrefix(staged.NewSecret, model.WorkspaceClientKeyPrefix) {
			t.Fatalf("StageRotate(%s) = %+v, created=%v, err=%v", targetID, staged, rotCreated, err)
		}
		// Replay with same requestID returns identical candidate with created=false.
		replay, replayCreated, err := svc.StageRotate(ctx, reqID, targetID)
		if err != nil || replayCreated || replay.NewSecret != staged.NewSecret || replay.OldSecret != staged.OldSecret {
			t.Fatalf("StageRotate replay = %+v, created=%v, err=%v", replay, replayCreated, err)
		}

		op := repo.Operation{
			ID:          reqID,
			TargetID:    targetID,
			Mode:        "rotate",
			RunID:       "run-1",
			Generation:  "1",
			Revision:    1,
			Outcome:     repo.OutcomePending,
			Steps:       []repo.KeyStep{{Name: "rotate", State: "unattempted"}},
			CreatedAtMS: 2000 + int64(idx*10),
		}
		if err := r.BeginOperation(ctx, op); err != nil {
			t.Fatalf("BeginOperation(%s): %v", targetID, err)
		}

		// Mid-rotation AllStaged must accept BOTH oldSecret and newSecret!
		okBoth, err := svc.AllStaged(ctx, []string{staged.OldSecret, staged.NewSecret})
		if err != nil || !okBoth {
			t.Fatalf("AllStaged mid-rotation = %v, err=%v", okBoth, err)
		}

		var gotOld, gotNext string
		if err := svc.WithRotationSecrets(ctx, reqID, targetID, false, func(oldSecret, nextSecret string) error {
			gotOld, gotNext = oldSecret, nextSecret
			return nil
		}); err != nil || gotOld != staged.OldSecret || gotNext != staged.NewSecret {
			t.Fatalf("WithRotationSecrets pre-commit = (%q, %q) err=%v", gotOld, gotNext, err)
		}

		op.Steps[0].State = "observed"
		op.Steps[0].Returned = true
		op.Outcome = repo.OutcomeCommitted
		op.CompletedAtMS = 3000 + int64(idx*10)
		if err := r.SaveOperation(ctx, op); err != nil {
			t.Fatalf("SaveOperation(%s): %v", targetID, err)
		}

		// Post-commit AllStaged accepts newSecret and rejects retired oldSecret.
		if okNew, err := svc.AllStaged(ctx, []string{staged.NewSecret}); err != nil || !okNew {
			t.Fatalf("AllStaged post-commit newSecret = %v, err=%v", okNew, err)
		}
		if okOld, err := svc.AllStaged(ctx, []string{staged.OldSecret}); err != nil || okOld {
			t.Fatalf("AllStaged post-commit oldSecret = %v, err=%v, want false", okOld, err)
		}

		// Post-commit WithRotationSecrets still resolves (oldSecret, nextSecret) from history + active row.
		if err := svc.WithRotationSecrets(ctx, reqID, targetID, true, func(oldSecret, nextSecret string) error {
			gotOld, gotNext = oldSecret, nextSecret
			return nil
		}); err != nil || gotOld != staged.OldSecret || gotNext != staged.NewSecret {
			t.Fatalf("WithRotationSecrets post-commit = (%q, %q) err=%v", gotOld, gotNext, err)
		}

		detail, err := svc.Detail(ctx, targetID)
		if err != nil {
			t.Fatalf("Detail(%s) after rotate: %v", targetID, err)
		}
		if detail.LastRotatedAtMS <= 0 || detail.MaskedPreview != model.MaskedClientKey(staged.NewSecret) {
			t.Fatalf("Detail(%s) missing rotation metadata: %+v", targetID, detail)
		}
	}

	// Revoke named.Key.ID and verify settled superseded key reports RuntimeState: "revoked_observed" and RevokedAtMS > 0, and StageRotate returns ErrRetired.
	revokeOpID := strings.Repeat("9", 32)
	revokeOp := repo.Operation{
		ID:          revokeOpID,
		TargetID:    string(named.Key.ID),
		KeeperID:    string(initial.ID),
		Mode:        "revoke",
		RunID:       "run-1",
		Generation:  "1",
		Revision:    1,
		Outcome:     repo.OutcomePending,
		Steps:       []repo.KeyStep{{Name: "revoke", State: "unattempted"}},
		CreatedAtMS: 4000,
	}
	if err := r.BeginOperation(ctx, revokeOp); err != nil {
		t.Fatal(err)
	}
	revokeOp.Steps[0].State = "observed"
	revokeOp.Steps[0].Returned = true
	revokeOp.Outcome = repo.OutcomeCommitted
	revokeOp.CompletedAtMS = 4500
	if err := r.SaveOperation(ctx, revokeOp); err != nil {
		t.Fatal(err)
	}

	revokedDetail, err := svc.Detail(ctx, string(named.Key.ID))
	if err != nil {
		t.Fatal(err)
	}
	if revokedDetail.Lifecycle != identity.LifecycleSuperseded || revokedDetail.RuntimeState != "revoked_observed" || revokedDetail.RevokedAtMS < 4500 {
		t.Fatalf("settled superseded key = %+v, want revoked_observed with RevokedAtMS >= 4500", revokedDetail)
	}
	if _, _, err := svc.StageRotate(ctx, strings.Repeat("8", 32), string(named.Key.ID)); !errors.Is(err, ErrRetired) {
		t.Fatalf("StageRotate on superseded key = %v, want ErrRetired", err)
	}
}
