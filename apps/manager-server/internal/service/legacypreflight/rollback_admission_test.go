package legacypreflight

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// TestRolledBackAdmissionStaysOnTheUpgradeSurface is the blocking half of a
// rollback: the v2 root was abandoned, so the process may start but must not
// serve the console, the key workspace or gateway traffic, and the console must
// learn why.
func TestRolledBackAdmissionStaysOnTheUpgradeSurface(t *testing.T) {
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	recordFixtureStage(t, paths.Database, string(model.UpgradeStageRolledBack))

	reader := DatabaseUpgradeAdmission{DatabasePath: paths.Database}
	stage, err := reader.Admitted(context.Background())
	if err != nil {
		t.Fatalf("admit a rolled-back store: %v", err)
	}
	if stage != UpgradeStageInProgress {
		t.Fatalf("stage = %q, want %q", stage, UpgradeStageInProgress)
	}
	admission := &Admission{UpgradeStage: stage, UpgradeReason: ReasonUpgradeRolledBack}
	if surface := admission.Surface(); surface != SurfaceUpgradeOnly {
		t.Fatalf("surface = %q, want %q", surface, SurfaceUpgradeOnly)
	}
	// A rolled-back record is not a finished install: it never relabels or
	// completes anything on its own.
	if !startupAllowed(Report{SourceKind: SourceKindNativeV2}, stage) {
		t.Fatal("the store itself stopped being startable")
	}

	reasoned, ok := UpgradeAdmission(reader).(UpgradeReasonAdmission)
	if !ok {
		t.Fatal("DatabaseUpgradeAdmission cannot explain its restriction")
	}
	reasonStage, reason, err := reasoned.AdmittedReason(context.Background())
	if err != nil {
		t.Fatalf("read the admission reason: %v", err)
	}
	if reasonStage != UpgradeStageInProgress || reason != ReasonUpgradeRolledBack {
		t.Fatalf("reasoned admission = %q/%q", reasonStage, reason)
	}
	resolvedStage, resolvedReason, err := upgradeAdmissionStageAndReason(context.Background(), reader)
	if err != nil {
		t.Fatalf("resolve stage and reason: %v", err)
	}
	if resolvedStage != UpgradeStageInProgress || resolvedReason != ReasonUpgradeRolledBack {
		t.Fatalf("resolved = %q/%q", resolvedStage, resolvedReason)
	}
}

func TestOtherStagesCarryNoRollbackReason(t *testing.T) {
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	reader := DatabaseUpgradeAdmission{DatabasePath: paths.Database}
	for _, stage := range []string{"copy_in", "reconcile", "complete"} {
		recordFixtureStage(t, paths.Database, stage)
		_, reason, err := reader.AdmittedReason(context.Background())
		if err != nil {
			t.Fatalf("%s reason: %v", stage, err)
		}
		if reason != "" {
			t.Fatalf("%s carried the rollback reason %q", stage, reason)
		}
	}
	// A reader that only implements the original interface keeps working and
	// simply produces no reason.
	stage, reason, err := upgradeAdmissionStageAndReason(context.Background(), staticAdmission(UpgradeStageInProgress))
	if err != nil || stage != UpgradeStageInProgress || reason != "" {
		t.Fatalf("plain reader = %q/%q/%v", stage, reason, err)
	}
}
