package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// rollbackFixture is a stopped 1.x volume plus a v2 data root that already
// records the copy-in the operation performed. It is the state a rollback is
// decided against: the source still holds the bytes the row describes.
type rollbackFixture struct {
	volume     legacyVolume
	targetRoot string
	recorded   model.UpgradeOperation
}

func newRollbackFixture(t *testing.T) rollbackFixture {
	t.Helper()
	volume := newLegacyVolume(t)
	sourceFingerprint, _, err := fingerprintSource(volume.root)
	if err != nil {
		t.Fatalf("fingerprint the 1.x fixture: %v", err)
	}
	targetRoot := canonicalTempDir(t)
	databasePath := filepath.Join(targetRoot, databaseFileName)
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("create the v2 fixture store: %v", err)
	}
	recorded, err := upgradeoperation.New(db).Advance(context.Background(), model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		_ = db.Close()
		t.Fatalf("record the copy-in fixture: %v", err)
	}
	recorded.SourceKind = SourceKindUpgradedLegacy
	recorded.SourceVersion = "v1.14.7"
	recorded.SourceFingerprintBefore = sourceFingerprint
	recorded.SourceFingerprintAfter = sourceFingerprint
	recorded.BackupManifestSHA256 = "backup-manifest-sha256"
	if err := upgradeoperation.New(db).Put(context.Background(), recorded); err != nil {
		_ = db.Close()
		t.Fatalf("fill the copy-in fixture evidence: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 fixture store: %v", err)
	}
	return rollbackFixture{volume: volume, targetRoot: targetRoot, recorded: recorded}
}

func (f rollbackFixture) options() RollbackOptions {
	return RollbackOptions{LegacyRoot: f.volume.root, TargetRoot: f.targetRoot}
}

func (f rollbackFixture) recordedRow(t *testing.T) model.UpgradeOperation {
	t.Helper()
	row, found, err := ReadRecord(context.Background(), filepath.Join(f.targetRoot, databaseFileName))
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}
	return row
}

func assertNoPlan(t *testing.T, targetRoot string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(targetRoot, rollbackWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("a refused rollback wrote a plan under the v2 data root (err=%v)", err)
	}
}

func TestPrepareRollbackRefusesWithoutARecord(t *testing.T) {
	source := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	sourceBefore := fingerprintTree(t, source.root)
	_, err := PrepareRollback(context.Background(), RollbackOptions{LegacyRoot: source.root, TargetRoot: targetRoot})
	if !errors.Is(err, ErrRollbackNotApplicable) {
		t.Fatalf("no record = %v, want %v", err, ErrRollbackNotApplicable)
	}
	assertNoPlan(t, targetRoot)
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, source.root))
}

func TestPrepareRollbackRefusesAStageItCannotStartFrom(t *testing.T) {
	fixture := newRollbackFixture(t)
	db, err := managersqlite.Open(filepath.Join(fixture.targetRoot, databaseFileName))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if _, err := upgradeoperation.New(db).Advance(context.Background(), "", model.UpgradeStageRolledBack); err != nil {
		_ = db.Close()
		t.Fatalf("record a rolled-back operation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
	_, err = PrepareRollback(context.Background(), fixture.options())
	if !errors.Is(err, ErrRollbackNotApplicable) {
		t.Fatalf("rolled-back record = %v, want %v", err, ErrRollbackNotApplicable)
	}
	assertNoPlan(t, fixture.targetRoot)
}

func TestPrepareRollbackRefusesAChangedSource(t *testing.T) {
	fixture := newRollbackFixture(t)
	// A new byte on the 1.x volume means it is no longer the volume the
	// operation copied, so a v1 restart would not restore the recorded state.
	if err := os.WriteFile(filepath.Join(fixture.volume.root, dataKeyFileName), []byte("rotated\n"), 0o600); err != nil {
		t.Fatalf("change the 1.x data key: %v", err)
	}
	_, err := PrepareRollback(context.Background(), fixture.options())
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source = %v, want %v", err, ErrSourceChanged)
	}
	assertNoPlan(t, fixture.targetRoot)
}

func TestPrepareRollbackPinsTheExactLegacyImage(t *testing.T) {
	fixture := newRollbackFixture(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)
	plan, err := PrepareRollback(context.Background(), fixture.options())
	if err != nil {
		t.Fatalf("prepare the rollback plan: %v", err)
	}
	if plan.Image != LegacyImageRepository+":v1.14.7" {
		t.Fatalf("pinned image = %q, want %q", plan.Image, LegacyImageRepository+":v1.14.7")
	}
	if strings.Contains(plan.Image, ":latest") || strings.Contains(plan.ComposeFile, ":latest") {
		t.Fatalf("plan names a moving tag: image=%q compose=%q", plan.Image, plan.ComposeFile)
	}
	if !strings.Contains(plan.ComposeFile, plan.Image) {
		t.Fatalf("compose file %q does not name the pinned image %q", plan.ComposeFile, plan.Image)
	}
	if plan.SourceFingerprint != fixture.recorded.SourceFingerprintBefore {
		t.Fatalf("plan fingerprint = %q, want %q", plan.SourceFingerprint, fixture.recorded.SourceFingerprintBefore)
	}
	if plan.BackupManifestSHA256 != fixture.recorded.BackupManifestSHA256 {
		t.Fatalf("plan manifest = %q, want %q", plan.BackupManifestSHA256, fixture.recorded.BackupManifestSHA256)
	}
	if len(plan.HealthChecks) != 2 {
		t.Fatalf("health checks = %v, want a status probe and a read-only open", plan.HealthChecks)
	}
	if !strings.Contains(plan.ComposeInvocation, "docker compose -p cpamp -f - up -d") {
		t.Fatalf("compose invocation = %q", plan.ComposeInvocation)
	}
	if plan.Digest == "" {
		t.Fatal("plan carries no digest")
	}

	// The report is written under the v2 data root and nowhere else.
	reportPath := filepath.Join(fixture.targetRoot, rollbackWorkDirectoryName, rollbackPlanFileName)
	if plan.ReportPath != reportPath {
		t.Fatalf("report path = %q, want %q", plan.ReportPath, reportPath)
	}
	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read the written plan: %v", err)
	}
	var written Plan
	if err := json.Unmarshal(encoded, &written); err != nil {
		t.Fatalf("decode the written plan: %v", err)
	}
	if written.Digest != plan.Digest {
		t.Fatalf("written digest = %q, want %q", written.Digest, plan.Digest)
	}
	// The 1.x volume is not written, not even beside.
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
}

func TestPrepareRollbackRefusesAnUnpinnableImage(t *testing.T) {
	fixture := newRollbackFixture(t)
	db, err := managersqlite.Open(filepath.Join(fixture.targetRoot, databaseFileName))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	row := fixture.recorded
	row.SourceVersion = VersionUnknown
	if err := upgradeoperation.New(db).Put(context.Background(), row); err != nil {
		_ = db.Close()
		t.Fatalf("record an unknown source version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
	_, err = PrepareRollback(context.Background(), fixture.options())
	if !errors.Is(err, ErrRollbackImageUnpinned) {
		t.Fatalf("unknown version = %v, want %v", err, ErrRollbackImageUnpinned)
	}
	assertNoPlan(t, fixture.targetRoot)

	options := fixture.options()
	options.TagOverride = LegacyImageRepository + ":latest"
	if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackImageUnpinned) {
		t.Fatalf("latest override = %v, want %v", err, ErrRollbackImageUnpinned)
	}
	assertNoPlan(t, fixture.targetRoot)
}

func (f rollbackFixture) provenEvidence() RollbackEvidence {
	return RollbackEvidence{
		LegacyRoot:        f.volume.root,
		TargetRoot:        f.targetRoot,
		Image:             LegacyImageRepository + ":v1.14.7",
		ReadOnlyOpen:      true,
		StatusURL:         "http://127.0.0.1:18317/status",
		StatusOK:          true,
		SourceFingerprint: f.recorded.SourceFingerprintBefore,
		ObservedAtMS:      1,
	}
}

func TestRecordRollbackRefusesUnprovenEvidence(t *testing.T) {
	fixture := newRollbackFixture(t)
	before := fixture.recordedRow(t)
	for _, test := range []struct {
		name   string
		mutate func(*RollbackEvidence)
	}{
		{name: "no read-only open", mutate: func(e *RollbackEvidence) { e.ReadOnlyOpen = false }},
		{name: "no status probe", mutate: func(e *RollbackEvidence) { e.StatusURL = "" }},
		{name: "unhealthy status", mutate: func(e *RollbackEvidence) { e.StatusOK = false }},
		{name: "no observation time", mutate: func(e *RollbackEvidence) { e.ObservedAtMS = 0 }},
		{name: "unpinned image", mutate: func(e *RollbackEvidence) { e.Image = LegacyImageRepository + ":latest" }},
		{name: "another fingerprint", mutate: func(e *RollbackEvidence) { e.SourceFingerprint = "not-the-recorded-volume" }},
	} {
		evidence := fixture.provenEvidence()
		test.mutate(&evidence)
		if _, err := RecordRollback(context.Background(), evidence); err == nil {
			t.Fatalf("%s was recorded", test.name)
		}
		after := fixture.recordedRow(t)
		if after.Stage != before.Stage {
			t.Fatalf("%s moved the stage from %q to %q", test.name, before.Stage, after.Stage)
		}
		if after.SourceKind != before.SourceKind || after.SourceFingerprintBefore != before.SourceFingerprintBefore ||
			after.SourceFingerprintAfter != before.SourceFingerprintAfter ||
			after.BackupManifestSHA256 != before.BackupManifestSHA256 {
			t.Fatalf("%s rewrote provenance: %+v", test.name, after)
		}
	}
}

func TestRecordRollbackKeepsProvenanceAndRefusesASecondVerdict(t *testing.T) {
	fixture := newRollbackFixture(t)
	before := fixture.recordedRow(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)
	recorded, err := RecordRollback(context.Background(), fixture.provenEvidence())
	if err != nil {
		t.Fatalf("record the rollback: %v", err)
	}
	if recorded.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("stage = %q, want %q", recorded.Stage, model.UpgradeStageRolledBack)
	}
	after := fixture.recordedRow(t)
	if after.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("persisted stage = %q, want %q", after.Stage, model.UpgradeStageRolledBack)
	}
	// Advancing never rewrites the observations the rollback depends on.
	if after.SourceKind != before.SourceKind ||
		after.SourceFingerprintBefore != before.SourceFingerprintBefore ||
		after.SourceFingerprintAfter != before.SourceFingerprintAfter ||
		after.BackupManifestSHA256 != before.BackupManifestSHA256 {
		t.Fatalf("rollback rewrote provenance: before=%+v after=%+v", before, after)
	}
	if len(after.DomainsDone) != len(before.DomainsDone) {
		t.Fatalf("rollback changed domains: before=%v after=%v", before.DomainsDone, after.DomainsDone)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))

	// The verdict is recorded once; a second attempt is refused and changes
	// nothing.
	if _, err := RecordRollback(context.Background(), fixture.provenEvidence()); !errors.Is(err, ErrRollbackNotApplicable) {
		t.Fatalf("second verdict = %v, want %v", err, ErrRollbackNotApplicable)
	}
	if again := fixture.recordedRow(t); again.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("second verdict moved the stage to %q", again.Stage)
	}
}

func TestRolledBackIsTerminal(t *testing.T) {
	if !model.UpgradeStageRolledBack.Valid() {
		t.Fatal("rolled_back is not a supported stage")
	}
	if !model.UpgradeStageRolledBack.CanAdvanceTo(model.UpgradeStageRolledBack) {
		t.Fatal("re-entering rolled_back is refused")
	}
	for _, next := range []model.UpgradeStage{
		model.UpgradeStagePreflight, model.UpgradeStageCopyIn, model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport, model.UpgradeStageExternalConnect, model.UpgradeStageVerify,
		model.UpgradeStageComplete, model.UpgradeStageFailed,
	} {
		if model.UpgradeStageRolledBack.CanAdvanceTo(next) {
			t.Fatalf("rolled_back advanced to %q", next)
		}
	}
	for _, previous := range []model.UpgradeStage{
		model.UpgradeStageCopyIn, model.UpgradeStageComplete, model.UpgradeStageFailed,
	} {
		if !previous.CanAdvanceTo(model.UpgradeStageRolledBack) {
			t.Fatalf("%q cannot advance to rolled_back", previous)
		}
	}
}

func TestRollbackProbesReadOnlyWithoutWritingTheSource(t *testing.T) {
	volume := newLegacyVolume(t)
	sourceBefore := fingerprintTree(t, volume.root)
	readOnly, err := ProbeLegacyReadOnly(context.Background(), volume.root)
	if err != nil {
		t.Fatalf("probe the 1.x volume: %v", err)
	}
	if !readOnly {
		t.Fatal("a readable 1.x volume did not open read-only")
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, volume.root))
}
