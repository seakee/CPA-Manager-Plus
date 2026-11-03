package upgrade

// Review regressions for R4 (parity fail-closed), R5 (fence before reads),
// R6 (cancellation) and R8 (typed lock classification).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

// R4 ---------------------------------------------------------------------

// TestPreflightBlocksWhenParityInspectionFails keeps the parity check honest in
// the failing direction: every required table name is present and quick_check
// passes, so the structural gate accepts the source, but the retained refs
// table lost the column the inspection reads. An inspection that cannot run is
// not evidence that history is intact, so the report blocks with a fixed
// nonsensitive detail instead of the SQL error text.
func TestPreflightBlocksWhenParityInspectionFails(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `drop table usage_archive_event_refs`)
		execFixture(t, db, `create table usage_archive_event_refs (
			event_hash text primary key,
			run_id text not null,
			segment_sequence integer not null,
			raw_event_id integer not null,
			timestamp_ms integer not null,
			archived_at_ms integer not null
		)`)
	})
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceUnreadable)

	ownership, found := checkByName(report, CheckSourceOwnership)
	if !found || ownership.Status != StatusPass {
		t.Fatalf("ownership check = %+v, want pass: the database itself was readable", ownership)
	}
	check, found := checkByName(report, CheckParity)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceUnreadable {
		t.Fatalf("parity check = %+v, want blocked %s", check, CodeSourceUnreadable)
	}
	if check.Detail != parityInspectionFailedDetail {
		t.Fatalf("parity detail = %q, want the fixed %q", check.Detail, parityInspectionFailedDetail)
	}
	for _, leak := range []string{"raw_deleted_at_ms", "select", "sqlite"} {
		if strings.Contains(strings.ToLower(check.Detail), leak) {
			t.Fatalf("parity detail %q leaks inspection internals (%q)", check.Detail, leak)
		}
	}
	if report.ParityGaps.Applicable || report.ParityGaps.any() {
		t.Fatalf("a failed inspection reported measured parity counts: %+v", report.ParityGaps)
	}
	if report.Ready {
		t.Fatal("ready = true after the parity inspection failed")
	}
}

// R5 ---------------------------------------------------------------------

// TestPreflightDoesNotInspectASourceWhoseLockIsHeld proves the fence ordering
// with a source that would fail loudly if it were read: the archive path is a
// plain file, so any unfenced tree walk errors. A busy source must stop at the
// lock attempt instead, report no digest, and mark every source-observing check
// skipped.
func TestPreflightDoesNotInspectASourceWhoseLockIsHeld(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	archivePath := filepath.Join(fixture.root, archivesDirName)
	if err := os.RemoveAll(archivePath); err != nil {
		t.Fatalf("remove archive tree: %v", err)
	}
	writeFixtureFile(t, archivePath, "not an archive directory\n")

	held, err := processlock.AcquireExisting(fixture.dbPath)
	if err != nil {
		t.Fatalf("hold source lock: %v", err)
	}
	defer func() { _ = held.Close() }()

	before := snapshotTree(t, fixture.root)
	var events []string
	report, err := Preflight(context.Background(), Options{
		LegacyRoot:      fixture.root,
		DeclaredVersion: pinnedSourceVersion(),
		observe:         func(event string) { events = append(events, event) },
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	requireBlocked(t, report, CodeSourceBusy)
	if !slices.Equal(events, []string{observationLock}) {
		t.Fatalf("observed source operations = %v, want only the lock attempt", events)
	}
	if report.Fingerprint != "" {
		t.Fatalf("a busy source reported a digest %q", report.Fingerprint)
	}
	for _, name := range []string{
		CheckSourceVersion, CheckFingerprint, CheckDisk, CheckCPAClassification, CheckParity,
	} {
		check, found := checkByName(report, name)
		if !found || check.Status != StatusSkipped {
			t.Fatalf("%s = %+v, want skipped for a busy source", name, check)
		}
	}
	after := snapshotTree(t, fixture.root)
	if !maps.Equal(before, after) {
		t.Fatalf("a busy preflight changed the source tree")
	}
}

// TestPreflightObservationSeamReportsEachSourceRead is the non-vacuous control
// for the test seam above: an ordinary fenced run reaches every observation in
// order, so the empty busy sequence means the reads really were skipped.
func TestPreflightObservationSeamReportsEachSourceRead(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	var events []string
	report, err := Preflight(context.Background(), Options{
		LegacyRoot:      fixture.root,
		DeclaredVersion: pinnedSourceVersion(),
		observe:         func(event string) { events = append(events, event) },
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !report.Ready {
		t.Fatalf("ready = false, blocked = %v", report.Blocked)
	}
	want := []string{
		observationLock, observationFingerprint, observationSnapshot, observationSnapshotOpen,
		observationFingerprintAfter, observationSchema, observationDisk, observationCPA, observationParity,
	}
	if !slices.Equal(events, want) {
		t.Fatalf("observed source operations = %v, want %v", events, want)
	}
}

// R6 ---------------------------------------------------------------------

// cancelAfterFirstBatch cancels the context after returning one batch and then
// refuses a second read, so a regression fails deterministically instead of
// hanging.
type cancelAfterFirstBatch struct {
	cancel context.CancelFunc
	batch  int
	reads  int
}

func (r *cancelAfterFirstBatch) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 1 {
		return 0, errors.New("a second batch was read after cancellation")
	}
	n := min(r.batch, len(p))
	for index := range n {
		p[index] = 'x'
	}
	r.cancel()
	return n, nil
}

func TestCopyChunksStopsBetweenBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelAfterFirstBatch{cancel: cancel, batch: digestChunkSize}
	destination := &bytes.Buffer{}
	written, err := copyChunks(ctx, destination, source)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if source.reads != 1 {
		t.Fatalf("reads = %d, want 1: the cancelled batch loop kept reading", source.reads)
	}
	if written != int64(digestChunkSize) || destination.Len() != digestChunkSize {
		t.Fatalf("written = %d destination = %d, want the first batch only", written, destination.Len())
	}
}

func TestFileContentDigestIsUnchangedByChunking(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 3*digestChunkSize/16+1)
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	digest, err := fileContentDigest(context.Background(), path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); digest != want {
		t.Fatalf("digest = %q, want %q: chunking changed the hash", digest, want)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fileContentDigest(cancelled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled digest = %v, want context.Canceled", err)
	}
}

// TestPreflightCancellationAfterSnapshotCleansUp cancels between operations
// once the private copy exists. The call must return the cancellation itself,
// remove the scratch copy, leave the source untouched and release the fence the
// deferred cleanup owns.
func TestPreflightCancellationAfterSnapshotCleansUp(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	scratch := t.TempDir()
	before := snapshotTree(t, fixture.root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	report, err := Preflight(ctx, Options{
		LegacyRoot:      fixture.root,
		DeclaredVersion: pinnedSourceVersion(),
		ScratchDir:      scratch,
		observe: func(event string) {
			events = append(events, event)
			if event == observationSnapshotOpen {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled (events %v)", err, events)
	}
	if report.Ready || len(report.Checks) != 0 {
		t.Fatalf("a cancelled preflight returned a report: %+v", report)
	}
	if !slices.Contains(events, observationSnapshotOpen) {
		t.Fatalf("events = %v, want the snapshot to have been opened", events)
	}
	entries, readErr := os.ReadDir(scratch)
	if readErr != nil {
		t.Fatalf("read scratch: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch was not cleaned up after cancellation: %v", entries)
	}
	if after := snapshotTree(t, fixture.root); !maps.Equal(before, after) {
		t.Fatalf("the source changed during a cancelled preflight")
	}
	assertFenceReleased(t, fixture.dbPath)
}

func TestPreflightCancellationBeforeSnapshotReleasesFence(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	scratch := t.TempDir()
	before := snapshotTree(t, fixture.root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report, err := Preflight(ctx, Options{
		LegacyRoot:      fixture.root,
		DeclaredVersion: pinnedSourceVersion(),
		ScratchDir:      scratch,
		observe: func(event string) {
			if event == observationFingerprint {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if report.Ready || len(report.Checks) != 0 {
		t.Fatalf("a cancelled preflight returned a report: %+v", report)
	}
	entries, readErr := os.ReadDir(scratch)
	if readErr != nil {
		t.Fatalf("read scratch: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch was created before the fence after cancellation: %v", entries)
	}
	if after := snapshotTree(t, fixture.root); !maps.Equal(before, after) {
		t.Fatalf("the source changed during a cancelled preflight")
	}
	assertFenceReleased(t, fixture.dbPath)
}

func TestSnapshotCopyFailureKeepsCancellationIdentity(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		err := snapshotCopyFailure(want)
		if !errors.Is(err, want) || !errors.Is(err, ErrSnapshotFailed) {
			t.Fatalf("snapshotCopyFailure(%v) = %v, want both sentinels", want, err)
		}
	}
	plain := snapshotCopyFailure(errors.New("disk full"))
	if !errors.Is(plain, ErrSnapshotFailed) || errors.Is(plain, context.Canceled) {
		t.Fatalf("snapshotCopyFailure(plain) = %v, want only the snapshot sentinel", plain)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenSnapshot(ctx, filepath.Join(t.TempDir(), databaseFileName), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenSnapshot with a cancelled context = %v, want context.Canceled", err)
	}
}

func assertFenceReleased(t *testing.T, databasePath string) {
	t.Helper()
	lock, err := processlock.AcquireExistingReadOnly(databasePath)
	if err != nil {
		t.Fatalf("the cancelled preflight left the ownership fence held: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release ownership fence: %v", err)
	}
}

// R8 ---------------------------------------------------------------------

// TestPreflightReportsUnreadableDatabaseAsUnreadable keeps the unreadable
// source distinct from a lock conflict: the ownership marker exists, so the
// fence is taken, but the database itself cannot be read.
func TestPreflightReportsUnreadableDatabaseAsUnreadable(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	before := snapshotTree(t, fixture.root)
	if err := os.Chmod(fixture.dbPath, 0o000); err != nil {
		t.Fatalf("make database unreadable: %v", err)
	}
	restore := func() { _ = os.Chmod(fixture.dbPath, 0o600) }
	defer restore()
	if file, err := os.Open(fixture.dbPath); err == nil {
		_ = file.Close()
		restore()
		t.Skip("this account can read a mode-000 database")
	}
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceUnreadable)
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceUnreadable {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceUnreadable)
	}
	for _, other := range []string{"source database is missing", "lock file is missing", "held by another process"} {
		if strings.Contains(check.Detail, other) {
			t.Fatalf("unreadable database detail %q reuses %q", check.Detail, other)
		}
	}
	if report.Fingerprint != "" {
		t.Fatal("an unreadable source reported a digest")
	}
	restore()
	after := snapshotTree(t, fixture.root)
	if !maps.Equal(before, after) {
		t.Fatalf("an unreadable preflight changed the source tree")
	}
}
