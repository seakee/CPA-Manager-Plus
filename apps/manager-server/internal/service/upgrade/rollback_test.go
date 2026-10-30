package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// rollbackFixture is a stopped 1.x volume plus the v2 data root a real copy-in
// produced from it. It is the state a rollback is decided against: the row
// names the source root, both fingerprints and the backup that made the copy
// eligible, and the backup is still on disk.
type rollbackFixture struct {
	volume     legacyVolume
	targetRoot string
	bundleDir  string
	recorded   model.UpgradeOperation
}

func newRollbackFixture(t *testing.T) rollbackFixture {
	t.Helper()
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("copy the 1.x fixture in: %v", err)
	}
	recorded, found, err := ReadRecord(context.Background(), filepath.Join(targetRoot, databaseFileName))
	if err != nil || !found {
		t.Fatalf("read the recorded copy-in: found=%t err=%v", found, err)
	}
	if recorded.SourceRoot == "" || recorded.BackupManifestSHA256 == "" {
		t.Fatalf("the copy-in recorded no source root or backup: %+v", recorded)
	}
	return rollbackFixture{volume: volume, targetRoot: targetRoot, bundleDir: result.BackupDirectory, recorded: recorded}
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

// writeRow rewrites the recorded row, which is how a test builds a row the
// production paths would refuse to create.
func (f rollbackFixture) writeRow(t *testing.T, mutate func(*model.UpgradeOperation)) {
	t.Helper()
	db, err := managersqlite.Open(filepath.Join(f.targetRoot, databaseFileName))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	row := f.recordedRow(t)
	mutate(&row)
	if err := upgradeoperation.New(db).Put(context.Background(), row); err != nil {
		_ = db.Close()
		t.Fatalf("rewrite the recorded operation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
}

func assertNoPlan(t *testing.T, targetRoot string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(targetRoot, rollbackWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("a refused rollback wrote a plan under the v2 data root (err=%v)", err)
	}
}

// rollbackProbe serves a 1.x health endpoint. The body is the documented
// contract, so a test can decide what identity answers.
func rollbackProbe(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LegacyHealthPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL + LegacyHealthPath
}

// healthyProbe answers exactly as the 1.x manager does on GET /health.
func healthyProbe(t *testing.T) string {
	t.Helper()
	return rollbackProbe(t, http.StatusOK, `{"ok":true,"service":"`+LegacyServiceID+`"}`)
}

func (f rollbackFixture) observation(t *testing.T) RollbackObservation {
	t.Helper()
	return RollbackObservation{
		LegacyRoot:   f.volume.root,
		TargetRoot:   f.targetRoot,
		BackupDir:    f.bundleDir,
		Image:        LegacyImageRepository + ":v" + strings.TrimPrefix(f.recorded.SourceVersion, "v"),
		StatusURL:    healthyProbe(t),
		ObservedAtMS: time.Now().UnixMilli(),
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
	if _, err := upgradeoperation.New(db).RecordRollback(context.Background(), model.RollbackEvidence{
		Image:             LegacyImageRepository + ":v1.14.7",
		StatusURL:         LegacyHealthURL(),
		SourceFingerprint: "observed",
		ObservedAtMS:      time.Now().UnixMilli(),
	}); err != nil {
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

// TestPrepareRollbackBindsTheVolumeToTheRecordedRoot covers the claim "the
// original volume": a byte-identical directory is still a different volume when
// the row recorded one.
func TestPrepareRollbackBindsTheVolumeToTheRecordedRoot(t *testing.T) {
	fixture := newRollbackFixture(t)
	clone := canonicalTempDir(t)
	copyTree(t, fixture.volume.root, clone)
	cloneBefore := fingerprintTree(t, clone)
	sourceBefore := fingerprintTree(t, fixture.volume.root)

	options := fixture.options()
	options.LegacyRoot = clone
	_, err := PrepareRollback(context.Background(), options)
	if !errors.Is(err, ErrRollbackVolumeUnbound) {
		t.Fatalf("a different directory = %v, want %v", err, ErrRollbackVolumeUnbound)
	}
	assertNoPlan(t, fixture.targetRoot)
	assertTreeUnchanged(t, "cloned 1.x source", cloneBefore, fingerprintTree(t, clone))
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
}

// TestPrepareRollbackBackfillsAMissingSourceRoot covers the row an earlier build
// wrote: the copy-in cannot record the directory again once it is recorded, so a
// directory that reproduces the recorded fingerprint is bound once instead of
// being answered with a command that cannot run.
func TestPrepareRollbackBackfillsAMissingSourceRoot(t *testing.T) {
	fixture := newRollbackFixture(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)
	fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = "" })

	plan, err := PrepareRollback(context.Background(), fixture.options())
	if err != nil {
		t.Fatalf("prepare a plan for a row without a source root: %v", err)
	}
	if plan.SourceRoot != fixture.volume.root {
		t.Fatalf("plan source root = %q, want the backfilled %q", plan.SourceRoot, fixture.volume.root)
	}
	backfilled := fixture.recordedRow(t)
	if backfilled.SourceRoot != fixture.volume.root {
		t.Fatalf("recorded source root = %q, want %q", backfilled.SourceRoot, fixture.volume.root)
	}
	// Only the missing directory was filled: the rest of the provenance survives.
	if backfilled.SourceFingerprintBefore != fixture.recorded.SourceFingerprintBefore ||
		backfilled.SourceFingerprintAfter != fixture.recorded.SourceFingerprintAfter ||
		backfilled.BackupManifestSHA256 != fixture.recorded.BackupManifestSHA256 ||
		backfilled.SourceVersion != fixture.recorded.SourceVersion {
		t.Fatalf("the backfill rewrote provenance: %+v", backfilled)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))

	// A directory whose contents do not reproduce the recorded fingerprint cannot
	// be bound, and the refusal says what does work instead of pointing at the
	// copy-in that refuses to run again.
	fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = "" })
	other := canonicalTempDir(t)
	copyTree(t, fixture.volume.root, other)
	if err := os.WriteFile(filepath.Join(other, dataKeyFileName), []byte("not the volume\n"), 0o600); err != nil {
		t.Fatalf("change the other directory: %v", err)
	}
	options := fixture.options()
	options.LegacyRoot = other
	_, err = PrepareRollback(context.Background(), options)
	if !errors.Is(err, ErrRollbackVolumeUnbound) {
		t.Fatalf("an unprovable directory = %v, want %v", err, ErrRollbackVolumeUnbound)
	}
	if message := err.Error(); strings.Contains(message, "record the copy-in again") || !strings.Contains(message, "docker volume inspect") {
		t.Fatalf("the refusal does not say what works: %s", message)
	}
	if again := fixture.recordedRow(t); again.SourceRoot != "" {
		t.Fatalf("a refused backfill wrote %q", again.SourceRoot)
	}
}

// TestRollbackRefusesNestedRoots covers the rule copy-in already applies: the
// 1.x volume is a source, so neither root may contain the other.
func TestRollbackRefusesNestedRoots(t *testing.T) {
	fixture := newRollbackFixture(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)

	nested := filepath.Join(fixture.volume.root, "v2-data")
	options := fixture.options()
	options.TargetRoot = nested
	if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackRootsOverlap) {
		t.Fatalf("nested target = %v, want %v", err, ErrRollbackRootsOverlap)
	}
	observation := fixture.observation(t)
	observation.TargetRoot = nested
	if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackRootsOverlap) {
		t.Fatalf("nested target verdict = %v, want %v", err, ErrRollbackRootsOverlap)
	}

	// The other direction: a 1.x root inside the v2 data root.
	options = fixture.options()
	options.LegacyRoot = fixture.bundleDir
	if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackRootsOverlap) {
		t.Fatalf("source root inside the v2 data root = %v, want %v", err, ErrRollbackRootsOverlap)
	}
	// Nothing was created inside the 1.x volume, and its bytes are unchanged.
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
	if _, err := os.Lstat(nested); !os.IsNotExist(err) {
		t.Fatalf("a refused rollback created %s (err=%v)", nested, err)
	}
}

func TestPrepareRollbackPinsTheExactLegacyImage(t *testing.T) {
	fixture := newRollbackFixture(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)
	plan, err := PrepareRollback(context.Background(), fixture.options())
	if err != nil {
		t.Fatalf("prepare the rollback plan: %v", err)
	}
	wantImage := LegacyImageRepository + ":v" + strings.TrimPrefix(fixture.recorded.SourceVersion, "v")
	if plan.Image != wantImage {
		t.Fatalf("pinned image = %q, want %q", plan.Image, wantImage)
	}
	if strings.Contains(plan.Image, ":latest") || strings.Contains(plan.ComposeFile, ":latest") {
		t.Fatalf("plan names a moving tag: image=%q compose=%q", plan.Image, plan.ComposeFile)
	}
	if !strings.Contains(plan.ComposeFile, plan.Image) {
		t.Fatalf("compose file %q does not name the pinned image %q", plan.ComposeFile, plan.Image)
	}
	if plan.SourceRoot != fixture.recorded.SourceRoot || plan.SourceRoot != fixture.volume.root {
		t.Fatalf("plan source root = %q, want the recorded %q", plan.SourceRoot, fixture.recorded.SourceRoot)
	}
	if plan.Volume != LegacyVolumeName || plan.VolumeMountPoint != LegacyVolumeMountPoint {
		t.Fatalf("plan volume = %q at %q, want %q at %q", plan.Volume, plan.VolumeMountPoint, LegacyVolumeName, LegacyVolumeMountPoint)
	}
	if !strings.Contains(plan.ComposeFile, plan.Volume+":"+plan.VolumeMountPoint) {
		t.Fatalf("compose file does not mount %q at %q: %s", plan.Volume, plan.VolumeMountPoint, plan.ComposeFile)
	}
	if plan.SourceFingerprint != fixture.recorded.SourceFingerprintBefore {
		t.Fatalf("plan fingerprint = %q, want %q", plan.SourceFingerprint, fixture.recorded.SourceFingerprintBefore)
	}
	if plan.BackupManifestSHA256 != fixture.recorded.BackupManifestSHA256 {
		t.Fatalf("plan manifest = %q, want %q", plan.BackupManifestSHA256, fixture.recorded.BackupManifestSHA256)
	}
	if !strings.Contains(plan.ComposeInvocation, "docker compose -p cpamp -f - up -d") {
		t.Fatalf("compose invocation = %q", plan.ComposeInvocation)
	}
	if plan.Digest == "" {
		t.Fatal("plan carries no digest")
	}

	// The plan proves health with the unauthenticated endpoint and its identity,
	// never with the credentialed /status.
	checks := strings.Join(plan.HealthChecks, "\n")
	if !strings.Contains(checks, LegacyHealthURL()) || !strings.Contains(checks, LegacyServiceID) {
		t.Fatalf("health checks = %v, want %s and the %s identity", plan.HealthChecks, LegacyHealthURL(), LegacyServiceID)
	}
	if strings.Contains(checks, "/status") || strings.Contains(plan.ComposeFile, "/status") {
		t.Fatalf("plan still proves health with /status: %v", plan.HealthChecks)
	}
	if !strings.Contains(plan.ComposeFile, LegacyHealthPath) {
		t.Fatalf("compose healthcheck does not use %s: %s", LegacyHealthPath, plan.ComposeFile)
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
	fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceVersion = VersionUnknown })
	_, err := PrepareRollback(context.Background(), fixture.options())
	if !errors.Is(err, ErrRollbackImageUnpinned) {
		t.Fatalf("unknown version = %v, want %v", err, ErrRollbackImageUnpinned)
	}
	assertNoPlan(t, fixture.targetRoot)

	// A repository without a version, an empty tag, a digest reference and a
	// partial version are all refused instead of being turned into a moving or
	// inexact reference.
	for _, override := range []string{
		LegacyImageRepository,
		LegacyImageRepository + ":",
		LegacyImageRepository + "@sha256:" + strings.Repeat("a", 64),
		LegacyImageRepository + ":latest",
		"latest",
		LegacyImageRepository + ":v1.14",
	} {
		options := fixture.options()
		options.TagOverride = override
		if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackImageUnpinned) {
			t.Fatalf("override %q = %v, want %v", override, err, ErrRollbackImageUnpinned)
		}
		assertNoPlan(t, fixture.targetRoot)
	}
}

// TestPrepareRollbackKeepsTheImageInTheSourceFamily covers M6: a rollback
// restarts the version the operation copied, so another family needs the
// explicit override.
func TestPrepareRollbackKeepsTheImageInTheSourceFamily(t *testing.T) {
	fixture := newRollbackFixture(t)
	family := releaseFamilyText(releaseOf(t, fixture.recorded.SourceVersion))
	other := "v1.13.0"
	if family == "1.13" {
		other = "v1.14.0"
	}
	options := fixture.options()
	options.TagOverride = other
	if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackImageFamily) {
		t.Fatalf("foreign family %q = %v, want %v", other, err, ErrRollbackImageFamily)
	}
	assertNoPlan(t, fixture.targetRoot)

	// The explicit override, and a same-family patch release, are accepted.
	options.AllowImageFamilyMismatch = true
	plan, err := PrepareRollback(context.Background(), options)
	if err != nil {
		t.Fatalf("override family %q: %v", other, err)
	}
	if !strings.HasSuffix(plan.Image, ":"+other) {
		t.Fatalf("override image = %q, want the requested %q", plan.Image, other)
	}

	options = fixture.options()
	options.TagOverride = LegacyImageRepository + ":v" + family + ".9"
	plan, err = PrepareRollback(context.Background(), options)
	if err != nil {
		t.Fatalf("same-family override: %v", err)
	}
	if !strings.HasSuffix(plan.Image, ":v"+family+".9") {
		t.Fatalf("same-family image = %q", plan.Image)
	}

	// A custom registry that names the same exact release is kept as given.
	options = fixture.options()
	options.TagOverride = "registry.example.com/cpamp/cpamp:v" + family + ".9"
	if plan, err = PrepareRollback(context.Background(), options); err != nil {
		t.Fatalf("custom registry override: %v", err)
	}
	if plan.Image != options.TagOverride {
		t.Fatalf("custom registry image = %q, want %q", plan.Image, options.TagOverride)
	}
}

func releaseOf(t *testing.T, version string) int {
	t.Helper()
	release, ok := parseRelease(version)
	if !ok {
		t.Fatalf("recorded source version %q is not an exact release", version)
	}
	return release
}

// TestRecordRollbackAcceptsAVolumeThatKeptWriting covers the shape the review
// found impossible before: after the copy-in the 1.x manager was restarted and
// wrote new rows, so the volume can never match the recorded fingerprint again.
// The rollback must still be provable, and the observed fingerprint is recorded
// as information rather than demanded as equality.
func TestRecordRollbackAcceptsAVolumeThatKeptWriting(t *testing.T) {
	fixture := newRollbackFixture(t)
	before := fixture.recordedRow(t)
	emptyV1AndWrite(t, fixture.volume)

	observation := fixture.observation(t)
	recorded, err := RecordRollback(context.Background(), observation)
	if err != nil {
		t.Fatalf("record the rollback of a volume that kept writing: %v", err)
	}
	if recorded.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("stage = %q, want %q", recorded.Stage, model.UpgradeStageRolledBack)
	}
	if recorded.SourceFingerprintBefore != before.SourceFingerprintBefore {
		t.Fatalf("rollback rewrote the recorded fingerprint: %q", recorded.SourceFingerprintBefore)
	}
	if recorded.Rollback == nil {
		t.Fatal("the verdict carries no evidence")
	}
	observedFingerprint, _, err := fingerprintSource(fixture.volume.root)
	if err != nil {
		t.Fatalf("fingerprint the written 1.x volume: %v", err)
	}
	if recorded.Rollback.SourceFingerprint != observedFingerprint {
		t.Fatalf("recorded observation = %q, want the observed %q", recorded.Rollback.SourceFingerprint, observedFingerprint)
	}
	if recorded.Rollback.SourceFingerprint == before.SourceFingerprintBefore {
		t.Fatal("the test did not write to the volume, so it proves nothing about new writes")
	}

	// The evidence is readable from the row, not only from the return value.
	persisted := fixture.recordedRow(t)
	if persisted.Rollback == nil {
		t.Fatalf("the recorded row lost the rollback evidence: %+v", persisted)
	}
	if persisted.Rollback.Image != observation.Image ||
		persisted.Rollback.StatusURL != observation.StatusURL ||
		persisted.Rollback.SourceFingerprint != observedFingerprint {
		t.Fatalf("recorded evidence = %+v", persisted.Rollback)
	}
	// The observation time is stamped by the service after its own proofs, not
	// taken from the caller's label: it is no older than the moment the caller
	// asked and no newer than now.
	if persisted.Rollback.ObservedAtMS < observation.ObservedAtMS ||
		persisted.Rollback.ObservedAtMS > time.Now().UnixMilli() {
		t.Fatalf("recorded observation time = %d, want a service stamp in [%d, %d]",
			persisted.Rollback.ObservedAtMS, observation.ObservedAtMS, time.Now().UnixMilli())
	}
	if persisted.SourceRoot != before.SourceRoot || persisted.SourceKind != before.SourceKind ||
		persisted.BackupManifestSHA256 != before.BackupManifestSHA256 ||
		persisted.SourceFingerprintAfter != before.SourceFingerprintAfter {
		t.Fatalf("rollback rewrote provenance: before=%+v after=%+v", before, persisted)
	}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("encode the recorded row: %v", err)
	}
	for _, field := range []string{`"rollback":{"image":`, `"statusUrl":`, `"sourceFingerprint":`, `"observedAtMs":`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("recorded row does not expose %s: %s", field, encoded)
		}
	}

	// A second verdict is refused and changes nothing.
	if _, err := RecordRollback(context.Background(), fixture.observation(t)); !errors.Is(err, ErrRollbackNotApplicable) {
		t.Fatalf("second verdict = %v, want %v", err, ErrRollbackNotApplicable)
	}
	if again := fixture.recordedRow(t); again.UpdatedAtMS != persisted.UpdatedAtMS {
		t.Fatalf("a refused second verdict touched the row: %+v", again)
	}
}

// TestRecordRollbackRefusesAForeignInstall covers M4 and the row proof: the
// volume must still retain the rows and archives the recorded backup describes,
// and the health probe must answer with this install's identity.
func TestRecordRollbackRefusesAForeignInstall(t *testing.T) {
	t.Run("rows the backup holds are gone", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		foreign := canonicalTempDir(t)
		copyTree(t, fixture.volume.root, foreign)
		dropOneUsageEvent(t, foreign)
		// The row under decision names the foreign directory, so the volume
		// binding passes and only the install proof can refuse.
		fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = foreign })
		observation := fixture.observation(t)
		observation.LegacyRoot = foreign
		before := fixture.recordedRow(t)
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackIdentity) {
			t.Fatalf("foreign rows = %v, want %v", err, ErrRollbackIdentity)
		}
		if after := fixture.recordedRow(t); after.Stage != before.Stage {
			t.Fatalf("a refused verdict moved the stage to %q", after.Stage)
		}
	})

	t.Run("another data key", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		foreign := canonicalTempDir(t)
		copyTree(t, fixture.volume.root, foreign)
		if err := os.WriteFile(filepath.Join(foreign, dataKeyFileName), []byte("another key\n"), 0o600); err != nil {
			t.Fatalf("replace the foreign data key: %v", err)
		}
		fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = foreign })
		observation := fixture.observation(t)
		observation.LegacyRoot = foreign
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackIdentity) {
			t.Fatalf("foreign data key = %v, want %v", err, ErrRollbackIdentity)
		}
	})

	t.Run("an archived segment is gone", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		foreign := canonicalTempDir(t)
		copyTree(t, fixture.volume.root, foreign)
		for _, archived := range fixture.volume.archives() {
			relative, err := filepath.Rel(fixture.volume.root, archived)
			if err != nil {
				t.Fatalf("resolve archive path: %v", err)
			}
			if err := os.Remove(filepath.Join(foreign, relative)); err != nil {
				t.Fatalf("remove the foreign archive segment: %v", err)
			}
		}
		fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = foreign })
		observation := fixture.observation(t)
		observation.LegacyRoot = foreign
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackIdentity) {
			t.Fatalf("missing archive segment = %v, want %v", err, ErrRollbackIdentity)
		}
	})

	t.Run("another service answers", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		observation := fixture.observation(t)
		observation.StatusURL = rollbackProbe(t, http.StatusOK, `{"ok":true,"service":"existing-manager"}`)
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackIdentity) {
			t.Fatalf("foreign service = %v, want %v", err, ErrRollbackIdentity)
		}
		if after := fixture.recordedRow(t); after.Stage == model.UpgradeStageRolledBack {
			t.Fatal("a foreign service identity was recorded")
		}
	})
}

// TestRecordRollbackRefusesAnUnprovenHealthAnswer is the H1 regression: the
// probe rejects an unauthenticated 2xx that does not state the documented 1.x
// identity, so a stray 200 cannot pass as a health proof.
func TestRecordRollbackRefusesAnUnprovenHealthAnswer(t *testing.T) {
	fixture := newRollbackFixture(t)
	for _, test := range []struct {
		name   string
		probe  func(t *testing.T) string
		reason error
	}{
		{
			name: "bare 200 without the documented body",
			probe: func(t *testing.T) string {
				return rollbackProbe(t, http.StatusOK, "OK")
			},
			reason: ErrRollbackIdentity,
		},
		{
			name: "200 that reports another service",
			probe: func(t *testing.T) string {
				return rollbackProbe(t, http.StatusOK, `{"ok":true,"service":"cpamp-ingress"}`)
			},
			reason: ErrRollbackIdentity,
		},
		{
			name: "200 without a service",
			probe: func(t *testing.T) string {
				return rollbackProbe(t, http.StatusOK, `{"ok":true}`)
			},
			reason: ErrRollbackIdentity,
		},
		{
			name: "200 that does not report healthy",
			probe: func(t *testing.T) string {
				return rollbackProbe(t, http.StatusOK, `{"ok":false,"service":"`+LegacyServiceID+`"}`)
			},
			reason: ErrRollbackIdentity,
		},
		{
			name: "the credentialed endpoint",
			probe: func(t *testing.T) string {
				return strings.TrimSuffix(healthyProbe(t), LegacyHealthPath) + "/status"
			},
			reason: ErrRollbackProbeEndpoint,
		},
		{
			name: "the credentialed endpoint answering 401",
			probe: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				t.Cleanup(server.Close)
				return server.URL + "/status"
			},
			reason: ErrRollbackProbeEndpoint,
		},
		{
			name: "unhealthy",
			probe: func(t *testing.T) string {
				return rollbackProbe(t, http.StatusServiceUnavailable, "")
			},
			reason: ErrRollbackUnproven,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceBefore := fingerprintTree(t, fixture.volume.root)
			before := fixture.recordedRow(t)
			observation := fixture.observation(t)
			observation.StatusURL = test.probe(t)
			_, err := RecordRollback(context.Background(), observation)
			if !errors.Is(err, test.reason) {
				t.Fatalf("%s = %v, want %v", test.name, err, test.reason)
			}
			if after := fixture.recordedRow(t); after.Stage != before.Stage || after.Rollback != nil {
				t.Fatalf("%s was recorded: %+v", test.name, after)
			}
			assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
		})
	}
}

// TestRecordRollbackRefusesStaleEvidence covers M3's freshness window: an
// observation taken before the install being recorded is stale, not evidence.
// A supplied time keeps its window; a zero is the caller's "now", covered below.
func TestRecordRollbackRefusesStaleEvidence(t *testing.T) {
	fixture := newRollbackFixture(t)
	for _, observedAt := range []int64{
		-1,
		time.Now().Add(-rollbackEvidenceFreshness - time.Minute).UnixMilli(),
		time.Now().Add(rollbackEvidenceFutureSkew + time.Minute).UnixMilli(),
	} {
		observation := fixture.observation(t)
		observation.ObservedAtMS = observedAt
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackEvidenceStale) {
			t.Fatalf("observation at %d = %v, want %v", observedAt, err, ErrRollbackEvidenceStale)
		}
		if after := fixture.recordedRow(t); after.Rollback != nil {
			t.Fatalf("stale evidence was recorded: %+v", after.Rollback)
		}
	}
}

// TestRecordRollbackLeavesTheMomentToTheService covers the programmatic caller
// that proves the install and names no observation time: the verdict is stamped
// from this service's own clock after every proof, so a zero is not a stale
// claim and the caller is not refused for omitting a label it cannot need.
func TestRecordRollbackLeavesTheMomentToTheService(t *testing.T) {
	fixture := newRollbackFixture(t)
	observation := fixture.observation(t)
	observation.ObservedAtMS = 0
	recorded, err := RecordRollback(context.Background(), observation)
	if err != nil {
		t.Fatalf("record with no observation time: %v", err)
	}
	if recorded.Stage != model.UpgradeStageRolledBack || recorded.Rollback == nil {
		t.Fatalf("recorded = %+v, want a rolled_back verdict with its evidence", recorded)
	}
	if recorded.Rollback.ObservedAtMS <= 0 {
		t.Fatalf("recorded observation time = %d, want this service's clock", recorded.Rollback.ObservedAtMS)
	}
	persisted := fixture.recordedRow(t)
	if persisted.Rollback == nil || persisted.Rollback.ObservedAtMS != recorded.Rollback.ObservedAtMS {
		t.Fatalf("persisted evidence = %+v, want the stamped moment", persisted.Rollback)
	}
	if persisted.Rollback.Image != observation.Image {
		t.Fatalf("recorded image = %q, want %q", persisted.Rollback.Image, observation.Image)
	}
}

// TestRecordRollbackRefusesAProbeOfThisManager is the M4 regression: the 1.x and
// the v2 Manager answer /health with the same identity, so an answer from this
// Manager's own listener proves the v2 stack is up, never that a 1.x volume was
// restored. No stage is written.
func TestRecordRollbackRefusesAProbeOfThisManager(t *testing.T) {
	fixture := newRollbackFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LegacyHealthPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"service":"`+LegacyServiceID+`"}`)
	}))
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	sourceBefore := fingerprintTree(t, fixture.volume.root)
	before := fixture.recordedRow(t)
	observation := fixture.observation(t)
	observation.StatusURL = server.URL + LegacyHealthPath
	observation.ManagerAddress = address
	if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackProbeSelf) {
		t.Fatalf("a probe of this Manager = %v, want %v", err, ErrRollbackProbeSelf)
	}
	after := fixture.recordedRow(t)
	if after.Stage != before.Stage || after.Rollback != nil {
		t.Fatalf("a self probe was recorded: %+v", after)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))

	// The same answer on the plan's own port is a health proof: the refusal is
	// about the listener, not about the identity.
	observation.ManagerAddress = ""
	if _, err := RecordRollback(context.Background(), observation); err != nil {
		t.Fatalf("a probe of another port was refused: %v", err)
	}
}

// TestPrepareRollbackRefusesAPortItMayNotPublish covers the two ways a plan can
// be rollback-ready while the v2 stack still holds the port: the plan's port is
// the port this Manager listens on, or something already answers there.
func TestPrepareRollbackRefusesAPortItMayNotPublish(t *testing.T) {
	t.Run("the port this Manager holds", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		port := freePort(t)
		options := fixture.options()
		options.ManagerAddress = "127.0.0.1:" + strconv.Itoa(port)
		options.Port = port
		if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackPortCollision) {
			t.Fatalf("the Manager's own port = %v, want %v", err, ErrRollbackPortCollision)
		}
		assertNoPlan(t, fixture.targetRoot)
	})

	t.Run("a port something else holds", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("hold a port: %v", err)
		}
		defer func() { _ = listener.Close() }()
		options := fixture.options()
		options.Port = listener.Addr().(*net.TCPAddr).Port
		if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackPortBusy) {
			t.Fatalf("an occupied port = %v, want %v", err, ErrRollbackPortBusy)
		}
		assertNoPlan(t, fixture.targetRoot)
	})

	t.Run("the v2 stack still answers", func(t *testing.T) {
		fixture := newRollbackFixture(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ok":true,"service":"`+LegacyServiceID+`"}`)
		}))
		t.Cleanup(server.Close)
		options := fixture.options()
		options.Port = freePort(t)
		options.ManagerAddress = strings.TrimPrefix(server.URL, "http://")
		if _, err := PrepareRollback(context.Background(), options); !errors.Is(err, ErrRollbackV2Running) {
			t.Fatalf("a running v2 stack = %v, want %v", err, ErrRollbackV2Running)
		}
		assertNoPlan(t, fixture.targetRoot)

		// The verdict is refused for the same reason: it would say a v2 root was
		// abandoned while that stack still answers.
		observation := fixture.observation(t)
		observation.ManagerAddress = strings.TrimPrefix(server.URL, "http://")
		before := fixture.recordedRow(t)
		if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackV2Running) {
			t.Fatalf("a verdict beside a running v2 stack = %v, want %v", err, ErrRollbackV2Running)
		}
		if after := fixture.recordedRow(t); after.Stage != before.Stage || after.Rollback != nil {
			t.Fatalf("a refused verdict wrote the row: %+v", after)
		}
	})
}

// TestPrepareRollbackPublishesItsOwnPort keeps the plan's port off the v2
// Manager's port and prints the check the operator reproduces on the host.
func TestPrepareRollbackPublishesItsOwnPort(t *testing.T) {
	fixture := newRollbackFixture(t)
	plan, err := PrepareRollback(context.Background(), fixture.options())
	if err != nil {
		t.Fatalf("prepare the rollback plan: %v", err)
	}
	if plan.Port != LegacyRollbackPort || plan.Port == ManagerDefaultPort {
		t.Fatalf("plan port = %d, want %d beside the Manager's %d", plan.Port, LegacyRollbackPort, ManagerDefaultPort)
	}
	if plan.ManagerPort != ManagerDefaultPort {
		t.Fatalf("plan manager port = %d, want the Manager default %d", plan.ManagerPort, ManagerDefaultPort)
	}
	published := strconv.Itoa(plan.Port)
	if !strings.Contains(plan.ComposeFile, published+":"+published) {
		t.Fatalf("compose file does not publish %s: %s", published, plan.ComposeFile)
	}
	if !strings.Contains(plan.ComposeFile, "HTTP_ADDR: \"0.0.0.0:"+published+"\"") {
		t.Fatalf("compose file does not bind %s: %s", published, plan.ComposeFile)
	}
	joined := strings.Join(plan.Preconditions, "\n")
	for _, want := range []string{
		"--filter publish=" + published,
		"-iTCP:" + published + " -sTCP:LISTEN",
		"stop the v2 stack",
		plan.BackupDirectory,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("preconditions do not name %q: %v", want, plan.Preconditions)
		}
	}
	health := strings.Join(plan.HealthChecks, "\n")
	if !strings.Contains(health, RollbackHealthURL(plan.Port)) {
		t.Fatalf("health checks do not probe the plan's port: %v", plan.HealthChecks)
	}
	// A port the operator chose is published too, and one the Manager holds is
	// never accepted silently.
	options := fixture.options()
	options.Port = freePort(t)
	plan, err = PrepareRollback(context.Background(), options)
	if err != nil {
		t.Fatalf("prepare on an explicit port: %v", err)
	}
	if !strings.Contains(plan.ComposeFile, strconv.Itoa(options.Port)+":") {
		t.Fatalf("compose file ignores the explicit port %d: %s", options.Port, plan.ComposeFile)
	}
}

// TestRecordRollbackRefusesAnotherRoot covers the binding half of the verdict: a
// verdict may only be recorded for the directory the operation recorded.
func TestRecordRollbackRefusesAnotherRoot(t *testing.T) {
	fixture := newRollbackFixture(t)
	clone := canonicalTempDir(t)
	copyTree(t, fixture.volume.root, clone)
	observation := fixture.observation(t)
	observation.LegacyRoot = clone
	if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackVolumeUnbound) {
		t.Fatalf("another directory = %v, want %v", err, ErrRollbackVolumeUnbound)
	}
	if after := fixture.recordedRow(t); after.Rollback != nil {
		t.Fatalf("a foreign root was recorded: %+v", after.Rollback)
	}
}

// TestRecordRollbackRefusesAnUnprovenVolume keeps the read-only open a required
// half of the proof: a volume without a database cannot be verified.
func TestRecordRollbackRefusesAnUnprovenVolume(t *testing.T) {
	fixture := newRollbackFixture(t)
	empty := canonicalTempDir(t)
	fixture.writeRow(t, func(row *model.UpgradeOperation) { row.SourceRoot = empty })
	observation := fixture.observation(t)
	observation.LegacyRoot = empty
	if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackUnproven) {
		t.Fatalf("empty volume = %v, want %v", err, ErrRollbackUnproven)
	}
	if after := fixture.recordedRow(t); after.Rollback != nil {
		t.Fatalf("an unproven volume was recorded: %+v", after.Rollback)
	}
}

// TestRecordRollbackRefusesAForeignBackup keeps the backup binding: the snapshot
// read here must be the one the operation recorded.
func TestRecordRollbackRefusesAForeignBackup(t *testing.T) {
	fixture := newRollbackFixture(t)
	fixture.writeRow(t, func(row *model.UpgradeOperation) { row.BackupManifestSHA256 = strings.Repeat("b", 64) })
	observation := fixture.observation(t)
	if _, err := RecordRollback(context.Background(), observation); !errors.Is(err, ErrRollbackIdentity) {
		t.Fatalf("another backup = %v, want %v", err, ErrRollbackIdentity)
	}
	if after := fixture.recordedRow(t); after.Rollback != nil {
		t.Fatalf("a foreign backup was recorded: %+v", after.Rollback)
	}
}

// TestCheckLegacyHealthRequiresTheDocumentedIdentity is the health regression on
// its own: an unauthenticated 2xx that does not state the documented 1.x
// identity is not a health proof, however healthy it looks.
func TestCheckLegacyHealthRequiresTheDocumentedIdentity(t *testing.T) {
	observation, err := CheckLegacyHealth(context.Background(),
		rollbackProbe(t, http.StatusOK, `{"ok":true,"service":"`+LegacyServiceID+`"}`), "")
	if err != nil {
		t.Fatalf("the documented identity was refused: %v", err)
	}
	if observation.StatusCode != http.StatusOK || observation.Service != LegacyServiceID {
		t.Fatalf("observation = %+v", observation)
	}
	for _, body := range []string{
		"",
		"OK",
		`{"ok":true}`,
		`{"ok":true,"service":"existing-manager"}`,
		`{"ok":false,"service":"` + LegacyServiceID + `"}`,
		`[1,2,3]`,
	} {
		if _, err := CheckLegacyHealth(context.Background(), rollbackProbe(t, http.StatusOK, body), ""); !errors.Is(err, ErrRollbackIdentity) {
			t.Fatalf("body %q = %v, want %v", body, err, ErrRollbackIdentity)
		}
	}
	if _, err := CheckLegacyHealth(context.Background(), "http://127.0.0.1:18317/status", ""); !errors.Is(err, ErrRollbackProbeEndpoint) {
		t.Fatalf("the credentialed endpoint = %v, want %v", err, ErrRollbackProbeEndpoint)
	}
}

// TestRollbackReadsTheVolumeWithoutWritingIt is the read-only half on its own:
// the install proof opens both stores and changes neither.
func TestRollbackReadsTheVolumeWithoutWritingIt(t *testing.T) {
	fixture := newRollbackFixture(t)
	sourceBefore := fingerprintTree(t, fixture.volume.root)
	if err := verifyRollbackInstall(context.Background(), fixture.volume.root, fixture.bundleDir, fixture.recorded); err != nil {
		t.Fatalf("verify the install: %v", err)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
	if _, err := CheckLegacyHealth(context.Background(), healthyProbe(t), ""); err != nil {
		t.Fatalf("probe the health endpoint: %v", err)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, fixture.volume.root))
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

// TestReadRecordReportsAnOutdatedStore covers the read-only reader against a
// store written by an earlier build: the table exists, the columns a rollback
// records do not, and a read-only open cannot migrate. The operator gets a typed
// code instead of raw SQL, and the code says the store must be opened once by
// this build.
func TestReadRecordReportsAnOutdatedStore(t *testing.T) {
	root := canonicalTempDir(t)
	databasePath := filepath.Join(root, databaseFileName)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("create the earlier store: %v", err)
	}
	for _, statement := range []string{
		`create table upgrade_operations (
			id integer primary key check(id = 1),
			stage text not null default '',
			domains_done text not null default '[]',
			source_kind text,
			source_version text,
			source_fingerprint_before text,
			source_fingerprint_after text,
			backup_manifest_sha256 text,
			reconciliation text not null default '{}',
			manual_items text not null default '[]',
			error_code text,
			error_detail text,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`insert into upgrade_operations (id, stage, domains_done, source_kind, created_at_ms, updated_at_ms)
			values (1, 'copy_in', '[]', 'upgraded-legacy', 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("write the earlier store: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the earlier store: %v", err)
	}

	_, found, err := ReadRecord(context.Background(), databasePath)
	if !errors.Is(err, ErrStoreSchemaOutdated) {
		t.Fatalf("an outdated store = %v (found=%t), want %v", err, found, ErrStoreSchemaOutdated)
	}
	for _, want := range []string{"source_root", "rollback_image", "open it once with this build"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %s", want, err)
		}
	}
	if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "no such column") {
		t.Fatalf("the refusal surfaced raw SQL: %s", err)
	}
}

// TestReadRecordReportsATerminalRowWithoutEvidence covers the second shape of a
// store this build cannot describe: the columns exist, the columns are empty,
// and the row claims a terminal verdict. No write path of this build records the
// stage without its observation, and a writable open only adds columns, so the
// refusal must say the operation cannot be read as recorded and name the record
// the operator can decide against instead.
func TestReadRecordReportsATerminalRowWithoutEvidence(t *testing.T) {
	fixture := newRollbackFixture(t)
	databasePath := filepath.Join(fixture.targetRoot, databaseFileName)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	// Write behind the repository: this is the row the production paths refuse,
	// so only a build from before the evidence invariant could leave it.
	if _, err := db.Exec(`update upgrade_operations
		set stage = 'rolled_back',
			rollback_image = null, rollback_status_url = null,
			rollback_source_fingerprint = null, rollback_observed_at_ms = null
		where id = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("write the evidence-less verdict: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}

	_, found, err := ReadRecord(context.Background(), databasePath)
	if !errors.Is(err, ErrStoreSchemaOutdated) {
		t.Fatalf("an evidence-less verdict = %v (found=%t), want %v", err, found, ErrStoreSchemaOutdated)
	}
	for _, want := range []string{"cannot be read as recorded", "new v2 data root"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %s", want, err)
		}
	}
	if strings.Contains(err.Error(), "open it once with this build") {
		t.Fatalf("the refusal promises a repair a writable open cannot make: %s", err)
	}

	// A writable open migrates the table and repairs nothing else, so the row is
	// still unreadable afterwards. Keeping that true is what keeps the copy
	// honest.
	reopened, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open the store writable: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close the writable store: %v", err)
	}
	if _, _, err := ReadRecord(context.Background(), databasePath); !errors.Is(err, ErrStoreSchemaOutdated) {
		t.Fatalf("a writable open repaired the row: %v", err)
	}
}

// freePort reserves and releases a TCP port, so a test can name a port that is
// free at the moment it is asked.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return port
}

// emptyV1AndWrite puts the fixture volume in the state a restarted 1.x manager
// leaves behind: a new usage event, a new archived segment and a new live
// fingerprint.
func emptyV1AndWrite(t *testing.T, volume legacyVolume) {
	t.Helper()
	db, err := managersqlite.Open(volume.databasePath())
	if err != nil {
		t.Fatalf("open the 1.x fixture database: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
		values ('v1-wrote-after-copy-in', 3000, '3000', 'gpt-test', 'gpt-test', 3000)`); err != nil {
		_ = db.Close()
		t.Fatalf("write a new 1.x usage event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the 1.x fixture database: %v", err)
	}
	writeFixtureTree(t, filepath.Join(volume.root, archivesDirName, "2026-04", "segment-000001.jsonl"), "{\"segment\":3}\n")
}

func dropOneUsageEvent(t *testing.T, root string) {
	t.Helper()
	db, err := managersqlite.Open(filepath.Join(root, databaseFileName))
	if err != nil {
		t.Fatalf("open the foreign database: %v", err)
	}
	if _, err := db.Exec(`delete from usage_events where event_hash = 'legacy-event-2'`); err != nil {
		_ = db.Close()
		t.Fatalf("delete a foreign usage event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the foreign database: %v", err)
	}
}

// copyTree duplicates a fixture directory so a test can offer a directory that
// is not the recorded volume.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy %s: %v", from, err)
	}
}
