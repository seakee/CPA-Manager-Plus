package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// observeOnce runs one public observation of a source and reports the verdict.
func observeOnce(t *testing.T, source PreservedSource) (LegacyDiscoveryObservation, bool) {
	t.Helper()
	return InspectReadOnlyLegacySource(context.Background(), source, "", func(LegacyDiscoveryObservation) {})
}

// TestReadOnlyLegacyObservationDoesNotBlameTheMarkerForAnUnreadableSource pins the
// attribution of a permission failure during the private copy. The ownership hold
// is already held at that point, so a permission error belongs to the database or
// one of its sidecars — the very files the verdict is about. Projecting it as
// "ownership unavailable" would send the operator to a chown the source does not
// need, and would remember that answer for an unchanged source.
func TestReadOnlyLegacyObservationDoesNotBlameTheMarkerForAnUnreadableSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}
	t.Run("unreadable-wal-sidecar", func(t *testing.T) {
		fixture := newSourceFixture(t, nil)
		wal := fixture.dbPath + "-wal"
		writeFixtureFile(t, wal, "sidecar the process cannot read\n")
		if err := os.Chmod(wal, 0o000); err != nil {
			t.Fatalf("make the journal unreadable: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(wal, 0o600) })

		verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
		if verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("verdict = (%q, reusable=%v), want (%q, false)", verdict, reusable, LegacyDiscoveryNone)
		}
	})
	t.Run("unreadable-database-with-readable-marker", func(t *testing.T) {
		fixture := newSourceFixture(t, nil)
		if err := os.Chmod(fixture.dbPath, 0o000); err != nil {
			t.Fatalf("make the database unreadable: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(fixture.dbPath, 0o600) })

		verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
		if verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("verdict = (%q, reusable=%v), want (%q, false)", verdict, reusable, LegacyDiscoveryNone)
		}
	})
}

// TestReadOnlyLegacyObservationAuditsSidecarsBeforeTheOwnershipVerdict pins the one
// source-file policy across the members this read does not copy. A WAL index that
// is a link describes a store no read of the database file alone can account for,
// so it is refused instead of being projected as the ownership condition of a 1.x
// volume.
func TestReadOnlyLegacyObservationAuditsSidecarsBeforeTheOwnershipVerdict(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	marker := fixture.dbPath + ".manager.lock"
	// The marker itself is out of reach, which is the condition that asks for the
	// schema-only classification.
	if err := os.Chmod(marker, 0o000); err != nil {
		t.Fatalf("make the ownership marker unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

	verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
	if verdict != LegacyDiscoveryOwnershipUnavailable {
		t.Fatalf("verdict without a sidecar = %q, want the ownership condition", verdict)
	}
	if reusable {
		t.Fatal("unfenced ownership verdict must not be marked reusable")
	}

	baseIdentity, ok := LegacySourceIdentity(fixture.dbPath)
	if !ok {
		t.Fatal("identify clean source")
	}

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := fixture.dbPath + suffix
		if err := os.Symlink(fixture.dbPath, sidecar); err != nil {
			t.Fatalf("symlink %s: %v", suffix, err)
		}
		if got, _ := observeOnce(t, PreservedSource{Database: fixture.dbPath}); got != LegacyDiscoveryNone {
			_ = os.Remove(sidecar)
			t.Fatalf("verdict with symlinked %s = %q, want fail-closed observation", suffix, got)
		}
		_ = os.Remove(sidecar)

		peer := filepath.Join(t.TempDir(), "hardlink-target"+suffix)
		writeFixtureFile(t, peer, "")
		if err := os.Link(peer, sidecar); err != nil {
			t.Fatalf("hardlink %s: %v", suffix, err)
		}
		if suffix == "-journal" {
			withJournalIdentity, ok := LegacySourceIdentity(fixture.dbPath)
			if !ok || withJournalIdentity == baseIdentity {
				_ = os.Remove(sidecar)
				t.Fatalf("LegacySourceIdentity did not track -journal (ok=%v)", ok)
			}
		}
		if got, _ := observeOnce(t, PreservedSource{Database: fixture.dbPath}); got != LegacyDiscoveryNone {
			_ = os.Remove(sidecar)
			t.Fatalf("verdict with hard-linked %s = %q, want fail-closed observation", suffix, got)
		}
		_ = os.Remove(sidecar)
	}
}

func TestReadOnlySnapshotsAndCredentialChecksFailClosedOnCorruptWALAndPendingRollbackJournal(t *testing.T) {
	const key = "snapshot-wal-journal-guard-key"
	fixture := newCredentialFixture(t, key)
	// Warm credential cache on clean state first so subsequent sidecar mutations
	// prove cache identity invalidation and sidecar policy enforcement.
	if err := VerifyAdminSource(context.Background(), PreservedSource{Database: fixture.dbPath}, key); err != nil {
		t.Fatalf("warm credential cache: %v", err)
	}

	// Produce valid WAL bytes from a real WAL write on a sibling copy.
	donorDir := t.TempDir()
	donorDB := filepath.Join(donorDir, databaseFileName)
	rawDB, err := os.ReadFile(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(donorDB, rawDB, 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", donorDB)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_checkpoint(truncate)`,
		`pragma wal_autocheckpoint = 0`,
		`update settings set updated_at_ms = updated_at_ms + 1`,
	} {
		if _, err := writer.Exec(stmt); err != nil {
			_ = writer.Close()
			t.Fatalf("exec donor WAL stmt: %v", err)
		}
	}
	walBytes, err := os.ReadFile(donorDB + "-wal")
	_ = writer.Close()
	if err != nil || len(walBytes) < 32 {
		t.Fatalf("read donor WAL: %v (len=%d)", err, len(walBytes))
	}

	checkAllClosed := func(t *testing.T, label string) {
		t.Helper()
		if snap, err := OpenSnapshot(context.Background(), fixture.dbPath, ""); err == nil {
			_ = snap.Close()
			t.Fatalf("%s: OpenSnapshot succeeded, want error", label)
		}
		if verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath}); verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("%s: InspectReadOnlyLegacySource = (%q, %v), want (none, false)", label, verdict, reusable)
		}
		if err := VerifyAdminSource(context.Background(), PreservedSource{Database: fixture.dbPath}, key); err == nil || errors.Is(err, ErrAdminUnauthorized) {
			t.Fatalf("%s: VerifyAdminSource = %v, want non-unauthorized source error", label, err)
		}
	}

	// 1. Truncated WAL header.
	writeFixtureFile(t, fixture.dbPath+"-wal", "short-wal")
	checkAllClosed(t, "truncated-wal")

	// 2. Corrupt WAL header magic.
	corruptMagic := append([]byte(nil), walBytes...)
	corruptMagic[0] ^= 0xff
	if err := os.WriteFile(fixture.dbPath+"-wal", corruptMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	checkAllClosed(t, "corrupt-wal-magic")

	// 3. Corrupt WAL header checksum.
	corruptChecksum := append([]byte(nil), walBytes...)
	corruptChecksum[24] ^= 0xff
	if err := os.WriteFile(fixture.dbPath+"-wal", corruptChecksum, 0o600); err != nil {
		t.Fatal(err)
	}
	checkAllClosed(t, "corrupt-wal-checksum")
	_ = os.Remove(fixture.dbPath + "-wal")

	// 4. Non-empty rollback journal.
	writeFixtureFile(t, fixture.dbPath+"-journal", "pending-rollback-journal-frames")
	checkAllClosed(t, "nonempty-rollback-journal")
	_ = os.Remove(fixture.dbPath + "-journal")

	// 5. Symlinked and hard-linked -journal / -shm after warm cache.
	for _, suffix := range []string{"-shm", "-journal"} {
		sidecar := fixture.dbPath + suffix
		if err := os.Symlink(fixture.dbPath, sidecar); err != nil {
			t.Fatal(err)
		}
		checkAllClosed(t, "symlink"+suffix)
		_ = os.Remove(sidecar)

		peer := filepath.Join(t.TempDir(), "peer"+suffix)
		writeFixtureFile(t, peer, "")
		if err := os.Link(peer, sidecar); err != nil {
			t.Fatal(err)
		}
		checkAllClosed(t, "hardlink"+suffix)
		_ = os.Remove(sidecar)
	}
}

// TestPreservedScratchDirectoryKeepsOutOfTheDataKeyDirectory pins the exclusion a
// configuration can create: the Manager data key may live outside the source
// database's directory, and a private copy that landed in the key's directory would
// write a whole database into a tree the read promises to preserve.
func TestPreservedScratchDirectoryKeepsOutOfTheDataKeyDirectory(t *testing.T) {
	root := t.TempDir()
	databaseDir := filepath.Join(root, "data")
	keyDir := filepath.Join(root, "keys")
	scratchDir := filepath.Join(root, "scratch")
	for _, dir := range []string{databaseDir, keyDir, scratchDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	source := PreservedSource{
		Database: filepath.Join(databaseDir, databaseFileName),
		DataKey:  filepath.Join(keyDir, dataKeyFileName),
	}

	// The source database's own directory stays excluded.
	if err := ValidatePreservedScratchDir(databaseDir, source); !errors.Is(err, ErrSnapshotFailed) {
		t.Fatalf("scratch inside the source directory = %v, want the snapshot refusal", err)
	}
	// So does the data key's, even though the database is not there.
	err := ValidatePreservedScratchDir(keyDir, source)
	if !errors.Is(err, ErrSnapshotFailed) || !strings.Contains(err.Error(), "data key") {
		t.Fatalf("scratch inside the data key directory = %v, want the data key refusal", err)
	}
	// A host whose temporary directory points into the key's directory is refused
	// by the default-scratch branch, which is the case an operator never spelled out.
	t.Setenv("TMPDIR", keyDir)
	if err := ValidatePreservedScratchDir("", source); !errors.Is(err, ErrSnapshotFailed) {
		t.Fatalf("default scratch inside the data key directory = %v, want the snapshot refusal", err)
	}
	// Anywhere else stays usable.
	t.Setenv("TMPDIR", scratchDir)
	if err := ValidatePreservedScratchDir("", source); err != nil {
		t.Fatalf("default scratch outside the preserved trees = %v", err)
	}
}

// TestReadOnlyLegacyObservationUnreadableMarkerFailsClosedOnCorruptWAL enforces
// that an unreadable ownership marker never skips WAL header validation or
// private WAL replay to project a corrupt WAL as a valid 1.x ownership problem.
func TestReadOnlyLegacyObservationUnreadableMarkerFailsClosedOnCorruptWAL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}
	fixture := newSourceFixture(t, nil)
	marker := fixture.dbPath + ".manager.lock"
	if err := os.Chmod(marker, 0o000); err != nil {
		t.Fatalf("make ownership marker unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

	wal := fixture.dbPath + "-wal"
	if err := os.WriteFile(wal, []byte(strings.Repeat("x", 64)), 0o644); err != nil {
		t.Fatalf("write corrupt WAL header: %v", err)
	}

	verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
	if verdict != LegacyDiscoveryNone {
		t.Fatalf("verdict with unreadable marker and corrupt WAL = %q, want %q", verdict, LegacyDiscoveryNone)
	}
	if reusable {
		t.Fatal("failed observation was marked reusable")
	}
}

// TestReadRecordFromSnapshotFailsClosedOnZeroLengthDatabaseWithOrphanSidecars
// enforces that a zero-length target database rejects any orphan -wal, -shm,
// or -journal sidecar rather than reporting found=false, err=nil.
func TestReadRecordFromSnapshotFailsClosedOnZeroLengthDatabaseWithOrphanSidecars(t *testing.T) {
	// Positive control: a clean 0-byte single-link database with no sidecars reports found=false, err=nil.
	cleanDB := filepath.Join(t.TempDir(), databaseFileName)
	if err := os.WriteFile(cleanDB, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadRecordFromSnapshot(context.Background(), cleanDB, ""); err != nil || found {
		t.Fatalf("clean zero-length database = (found=%v, err=%v), want (false, nil)", found, err)
	}

	for _, tc := range []struct {
		name   string
		suffix string
		data   []byte
	}{
		{name: "orphan-32kib-shm", suffix: "-shm", data: make([]byte, 32*1024)},
		{name: "orphan-empty-shm", suffix: "-shm", data: nil},
		{name: "orphan-empty-wal", suffix: "-wal", data: nil},
		{name: "orphan-empty-journal", suffix: "-journal", data: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), databaseFileName)
			if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dbPath+tc.suffix, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, found, err := ReadRecordFromSnapshot(context.Background(), dbPath, "")
			if err == nil || found || !strings.Contains(err.Error(), tc.suffix) {
				t.Fatalf("%s ReadRecordFromSnapshot = (found=%v, err=%v), want sidecar error naming %s", tc.name, found, err, tc.suffix)
			}
		})
	}
}

// TestUnfenceableDiscoveryEnforcesBaselineIdentityAndChangeStamp enforces that
// classifyUnfenceableSource captures file + sidecar identity first, requires a
// valid ChangeStamp, branches to snapshot validation when baseline has a WAL,
// and fails closed if a WAL appears during the immutable read.
func TestUnfenceableDiscoveryEnforcesBaselineIdentityAndChangeStamp(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}

	t.Run("wal-created-before-baseline-capture", func(t *testing.T) {
		fixture := newSourceFixture(t, nil)
		marker := fixture.dbPath + ".manager.lock"
		if err := os.Chmod(marker, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

		origHook := afterUnfenceableSidecarAudit
		afterUnfenceableSidecarAudit = func(dbPath string) {
			_ = os.WriteFile(dbPath+"-wal", []byte(strings.Repeat("z", 64)), 0o644)
		}
		defer func() { afterUnfenceableSidecarAudit = origHook }()

		verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
		if verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("verdict = (%q, reusable=%v), want (%q, false)", verdict, reusable, LegacyDiscoveryNone)
		}
	})

	t.Run("wal-created-during-immutable-read", func(t *testing.T) {
		fixture := newSourceFixture(t, nil)
		marker := fixture.dbPath + ".manager.lock"
		if err := os.Chmod(marker, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

		origHook := beforeUnfenceableImmutableRead
		beforeUnfenceableImmutableRead = func(dbPath string) {
			_ = os.WriteFile(dbPath+"-wal", []byte(strings.Repeat("w", 64)), 0o644)
		}
		defer func() { beforeUnfenceableImmutableRead = origHook }()

		verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
		if verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("verdict = (%q, reusable=%v), want (%q, false)", verdict, reusable, LegacyDiscoveryNone)
		}
	})

	t.Run("change-stamp-unavailable-fails-closed", func(t *testing.T) {
		fixture := newSourceFixture(t, nil)
		marker := fixture.dbPath + ".manager.lock"
		if err := os.Chmod(marker, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

		origStamp := credentialChangeStamp
		credentialChangeStamp = func(string) (string, error) {
			return "", copyinmarker.ErrChangeStampUnavailable
		}
		defer func() { credentialChangeStamp = origStamp }()

		verdict, reusable := observeOnce(t, PreservedSource{Database: fixture.dbPath})
		if verdict != LegacyDiscoveryNone || reusable {
			t.Fatalf("verdict with unavailable ChangeStamp = (%q, reusable=%v), want (%q, false)", verdict, reusable, LegacyDiscoveryNone)
		}
	})
}
