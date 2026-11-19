package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func newZeroByteWALLegacyVolume(t *testing.T, adminPlaintext string) legacyVolume {
	t.Helper()
	template := newLegacyVolume(t)
	if adminPlaintext != "" {
		db, err := sql.Open("sqlite", template.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(adminPlaintext, "test")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		credJSON, err := json.Marshal(cred)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at_ms) VALUES ('admin_credential_v1', ?, 1000)`, string(credJSON)); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		_ = db.Close()
	}

	// Clone schema + rows into a brand-new uncheckpointed WAL database so the
	// main SQLite file stays 0 bytes and all pages (including page 1) live in -wal.
	stageDir := canonicalTempDir(t)
	seedDBPath := filepath.Join(stageDir, "uncheckpointed.sqlite")
	seedDB, err := sql.Open("sqlite", seedDBPath)
	if err != nil {
		t.Fatal(err)
	}
	seedDB.SetMaxOpenConns(1)
	if _, err := seedDB.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;`); err != nil {
		_ = seedDB.Close()
		t.Fatal(err)
	}
	escapedTemplate := strings.ReplaceAll(template.databasePath(), "'", "''")
	if _, err := seedDB.Exec(fmt.Sprintf(`ATTACH DATABASE '%s' AS tpl;`, escapedTemplate)); err != nil {
		_ = seedDB.Close()
		t.Fatal(err)
	}
	rows, err := seedDB.Query(`SELECT sql, name FROM tpl.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'usage_monitoring_event_search_v1_%' ORDER BY rowid`)
	if err != nil {
		_ = seedDB.Close()
		t.Fatal(err)
	}
	type tableDef struct {
		ddl  string
		name string
	}
	var tables []tableDef
	for rows.Next() {
		var d tableDef
		if err := rows.Scan(&d.ddl, &d.name); err != nil {
			_ = rows.Close()
			_ = seedDB.Close()
			t.Fatal(err)
		}
		tables = append(tables, d)
	}
	_ = rows.Close()
	for _, tbl := range tables {
		if _, err := seedDB.Exec(tbl.ddl); err != nil {
			_ = seedDB.Close()
			t.Fatalf("create table %s in uncheckpointed WAL db: %v", tbl.name, err)
		}
		if _, err := seedDB.Exec(fmt.Sprintf(`INSERT INTO main."%s" SELECT * FROM tpl."%s"`, tbl.name, tbl.name)); err != nil {
			_ = seedDB.Close()
			t.Fatalf("copy rows for %s: %v", tbl.name, err)
		}
	}
	idxRows, err := seedDB.Query(`SELECT sql FROM tpl.sqlite_master WHERE type = 'index' AND sql IS NOT NULL`)
	if err != nil {
		_ = seedDB.Close()
		t.Fatal(err)
	}
	var idxDDLs []string
	for idxRows.Next() {
		var ddl string
		if err := idxRows.Scan(&ddl); err != nil {
			_ = idxRows.Close()
			_ = seedDB.Close()
			t.Fatal(err)
		}
		idxDDLs = append(idxDDLs, ddl)
	}
	_ = idxRows.Close()
	for _, ddl := range idxDDLs {
		if _, err := seedDB.Exec(ddl); err != nil {
			_ = seedDB.Close()
			t.Fatalf("create index: %v", err)
		}
	}
	if _, err := seedDB.Exec(`DETACH DATABASE tpl;`); err != nil {
		_ = seedDB.Close()
		t.Fatal(err)
	}

	walBytes, err := os.ReadFile(seedDBPath + "-wal")
	_ = seedDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(walBytes) <= 32 {
		t.Fatalf("expected populated WAL frames, got %d bytes", len(walBytes))
	}

	if err := os.WriteFile(template.databasePath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(template.databasePath()+"-wal", walBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return template
}

func TestCopyInRollsBackMaterializedTargetAndReturnsTypedSourceErrors(t *testing.T) {
	t.Run("non-symlink post-materialization fingerprint error rolls back target and marker and returns CodeSourceUnreadable", func(t *testing.T) {
		volume := newLegacyVolume(t)
		target := canonicalTempDir(t)
		bundle := filepath.Join(canonicalTempDir(t), "backup")
		lock, err := processlock.AcquireExistingReadOnly(volume.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Close() }()

		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("chmod 0000 permission test skipped on Windows or when running as root")
		}
		archiveSeg := volume.archives()[0]
		t.Cleanup(func() { _ = os.Chmod(archiveSeg, 0o600) })
		_, copyErr := CopyIn(context.Background(), Options{
			LegacyRoot:      volume.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetRoot:      target,
			BundleDir:       bundle,
			Lock:            lock,
			checkpoint: func(name string) error {
				if name == "target-restored" {
					return os.Chmod(archiveSeg, 0o000)
				}
				return nil
			},
		})
		if copyErr == nil {
			t.Fatalf("expected CopyIn to fail when post-materialization fingerprint fails")
		}
		code, ok := UpgradeErrorCodeOf(copyErr)
		if !ok || code != UpgradeErrorCode(CodeSourceUnreadable) {
			t.Fatalf("CopyIn error = (%v, %q), want CodeSourceUnreadable", copyErr, code)
		}
		if _, found, mErr := copyinmarker.Read(target); mErr != nil || found {
			t.Fatalf("marker remained after rollback: found=%v err=%v", found, mErr)
		}
		for _, member := range []string{dataKeyFileName, archivesDirName, databaseFileName, copyinmarker.WorkDirectoryName} {
			if _, statErr := os.Lstat(filepath.Join(target, member)); !os.IsNotExist(statErr) {
				t.Fatalf("stranded target member %s after post-materialization rollback (err=%v)", member, statErr)
			}
		}
	})

	t.Run("context cancellation during post-materialization fingerprint rolls back target and preserves context.Canceled", func(t *testing.T) {
		volume := newLegacyVolume(t)
		target := canonicalTempDir(t)
		bundle := filepath.Join(canonicalTempDir(t), "backup")
		lock, err := processlock.AcquireExistingReadOnly(volume.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Close() }()

		ctx, cancel := context.WithCancel(context.Background())
		_, copyErr := CopyIn(ctx, Options{
			LegacyRoot:      volume.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetRoot:      target,
			BundleDir:       bundle,
			Lock:            lock,
			checkpoint: func(name string) error {
				if name == "target-restored" {
					cancel()
				}
				return nil
			},
		})
		if !errors.Is(copyErr, context.Canceled) {
			t.Fatalf("CopyIn error = %v, want context.Canceled", copyErr)
		}
		if _, found, mErr := copyinmarker.Read(target); mErr != nil || found {
			t.Fatalf("marker remained after cancelled fingerprint rollback: found=%v err=%v", found, mErr)
		}
		if _, statErr := os.Lstat(filepath.Join(target, dataKeyFileName)); !os.IsNotExist(statErr) {
			t.Fatalf("stranded data.key after cancelled fingerprint rollback: %v", statErr)
		}
	})
}

func TestReconcileMapsSourceFingerprintFailuresToTypedErrors(t *testing.T) {
	volume := newLegacyVolume(t)
	target := canonicalTempDir(t)
	bundle := filepath.Join(canonicalTempDir(t), "backup")
	sourceLock, err := processlock.AcquireExistingReadOnly(volume.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CopyIn(context.Background(), Options{
		LegacyRoot:      volume.root,
		DeclaredVersion: bootstrap.PlannedDirectRelease,
		TargetRoot:      target,
		BundleDir:       bundle,
		Lock:            sourceLock,
	}); err != nil {
		_ = sourceLock.Close()
		t.Fatal(err)
	}
	_ = sourceLock.Close()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod 0000 permission test skipped on Windows or when running as root")
	}
	archiveSeg := volume.archives()[0]
	t.Cleanup(func() { _ = os.Chmod(archiveSeg, 0o600) })
	if err := os.Chmod(archiveSeg, 0o000); err != nil {
		t.Fatal(err)
	}
	sLock, err := processlock.AcquireExistingReadOnly(volume.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sLock.Close() }()
	tLock, err := processlock.Acquire(filepath.Join(target, databaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tLock.Close() }()

	_, recErr := Reconcile(context.Background(), Options{
		LegacyRoot:      volume.root,
		DeclaredVersion: bootstrap.PlannedDirectRelease,
		TargetRoot:      target,
		BundleDir:       bundle,
		Lock:            sLock,
		TargetLock:      tLock,
	})
	if code, ok := UpgradeErrorCodeOf(recErr); !ok || code != UpgradeErrorCode(CodeSourceUnreadable) {
		t.Fatalf("Reconcile error = (%v, %q), want CodeSourceUnreadable", recErr, code)
	}
}

func TestZeroByteDatabaseWithPopulatedWALAndSidecarPolicyConsistency(t *testing.T) {
	ctx := context.Background()

	t.Run("0-byte usage.sqlite with valid populated WAL agrees across MountDatabase, CredentialDatabase, Discovery, Preflight, CopyIn, and Reconcile", func(t *testing.T) {
		const adminKey = "legacy-zero-byte-wal-admin-key-123456"
		volume := newZeroByteWALLegacyVolume(t, adminKey)
		managerDB := filepath.Join(canonicalTempDir(t), databaseFileName)

		mountDB, refusal := MountDatabase(volume.root)
		if refusal != "" || mountDB != volume.databasePath() {
			t.Fatalf("MountDatabase = (%q, %q), want (%q, \"\")", mountDB, refusal, volume.databasePath())
		}
		if got := CredentialDatabase(volume.root, managerDB); got != volume.databasePath() {
			t.Fatalf("CredentialDatabase = %q, want %q", got, volume.databasePath())
		}
		if err := VerifyAdmin(ctx, volume.root, adminKey); err != nil {
			t.Fatalf("VerifyAdmin failed on 0-byte+WAL legacy database: %v", err)
		}

		source := PreservedSource{
			Database: volume.databasePath(),
			DataKey:  filepath.Join(volume.root, dataKeyFileName),
		}
		verdict, cacheable := InspectReadOnlyLegacySource(ctx, source, volume.root, nil)
		if verdict != LegacyDiscoveryUnsupportedUpgrade || !cacheable {
			t.Fatalf("InspectReadOnlyLegacySource = (%q, %v), want (%q, true)", verdict, cacheable, LegacyDiscoveryUnsupportedUpgrade)
		}

		target := canonicalTempDir(t)
		bundle := filepath.Join(canonicalTempDir(t), "backup")
		report, err := Preflight(ctx, Options{
			LegacyRoot:      volume.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetDir:       target,
			BundleDir:       bundle,
		})
		if err != nil || !report.Ready {
			t.Fatalf("Preflight = (ready=%v, blocked=%v, checks=%+v, err=%v), want Ready", report.Ready, report.Blocked, report.Checks, err)
		}

		sLock, err := processlock.AcquireExistingReadOnly(volume.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CopyIn(ctx, Options{
			LegacyRoot:      volume.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetRoot:      target,
			BundleDir:       bundle,
			Lock:            sLock,
		}); err != nil {
			_ = sLock.Close()
			t.Fatalf("CopyIn failed on 0-byte+WAL legacy source: %v", err)
		}
		tLock, err := processlock.Acquire(filepath.Join(target, databaseFileName))
		if err != nil {
			_ = sLock.Close()
			t.Fatal(err)
		}
		recReport, err := Reconcile(ctx, Options{
			LegacyRoot:      volume.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetRoot:      target,
			BundleDir:       bundle,
			Lock:            sLock,
			TargetLock:      tLock,
		})
		_ = tLock.Close()
		_ = sLock.Close()
		if err != nil || recReport.Stage != model.UpgradeStageReconcile {
			t.Fatalf("Reconcile failed on 0-byte+WAL legacy source: report=%+v err=%v", recReport, err)
		}
	})

	t.Run("invalid sidecars cause MountDatabase, CredentialDatabase, EntryCredentialDatabase, Discovery, and Preflight to agree", func(t *testing.T) {
		managerDB := filepath.Join(canonicalTempDir(t), databaseFileName)

		cases := []struct {
			name        string
			mutate      func(t *testing.T, dbPath string)
			wantRefusal string
		}{
			{
				name: "symlinked WAL",
				mutate: func(t *testing.T, dbPath string) {
					outside := filepath.Join(t.TempDir(), "outside.wal")
					if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, dbPath+"-wal"); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				},
				wantRefusal: CodeSourceSymlinked,
			},
			{
				name: "hard-linked WAL",
				mutate: func(t *testing.T, dbPath string) {
					walPath := dbPath + "-wal"
					if err := os.WriteFile(walPath, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Link(walPath, filepath.Join(t.TempDir(), "linked.wal")); err != nil {
						t.Skipf("hard link unavailable: %v", err)
					}
				},
				wantRefusal: CodeSourceLockUnavailable,
			},
			{
				name: "non-regular directory WAL",
				mutate: func(t *testing.T, dbPath string) {
					if err := os.Mkdir(dbPath+"-wal", 0o700); err != nil {
						t.Fatal(err)
					}
				},
				wantRefusal: CodeSourceUnreadable,
			},
			{
				name: "non-empty rollback journal",
				mutate: func(t *testing.T, dbPath string) {
					if err := os.WriteFile(dbPath+"-journal", []byte("pending-journal"), 0o600); err != nil {
						t.Fatal(err)
					}
				},
				wantRefusal: CodeSourceUnreadable,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				volume := newLegacyVolume(t)
				tc.mutate(t, volume.databasePath())

				gotDB, gotRefusal := MountDatabase(volume.root)
				if gotDB != "" || gotRefusal != tc.wantRefusal {
					t.Fatalf("MountDatabase = (%q, %q), want (\"\", %q)", gotDB, gotRefusal, tc.wantRefusal)
				}
				if cred := CredentialDatabase(volume.root, managerDB); cred != managerDB {
					t.Fatalf("CredentialDatabase = %q, want fallback %q", cred, managerDB)
				}
				if entryCred := EntryCredentialDatabase(volume.root, managerDB); entryCred != managerDB {
					t.Fatalf("EntryCredentialDatabase = %q, want fallback %q", entryCred, managerDB)
				}
				verdict, _ := InspectReadOnlyLegacySource(ctx, PreservedSource{
					Database: volume.databasePath(),
					DataKey:  filepath.Join(volume.root, dataKeyFileName),
				}, volume.root, nil)
				if verdict != LegacyDiscoveryNone {
					t.Fatalf("InspectReadOnlyLegacySource = %q, want %q", verdict, LegacyDiscoveryNone)
				}
				report, err := Preflight(ctx, Options{
					LegacyRoot:      volume.root,
					DeclaredVersion: bootstrap.PlannedDirectRelease,
					TargetDir:       canonicalTempDir(t),
				})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, b := range report.Blocked {
					if b == tc.wantRefusal {
						found = true
					}
				}
				if !found {
					t.Fatalf("Preflight.Blocked = %v, want %q included", report.Blocked, tc.wantRefusal)
				}
			})
		}
	})

	t.Run("ownershipFailureOf evaluates ErrLocked and ErrHardLinked before databaseMissing on 0-byte database", func(t *testing.T) {
		busyVol := newLegacyVolume(t)
		if err := os.Truncate(busyVol.databasePath(), 0); err != nil {
			t.Fatal(err)
		}
		held, err := processlock.AcquireExisting(busyVol.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Close() }()

		busyReport, err := Preflight(ctx, Options{
			LegacyRoot:      busyVol.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetDir:       canonicalTempDir(t),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(busyReport.Blocked) == 0 || busyReport.Blocked[0] != CodeSourceBusy {
			t.Fatalf("0-byte locked database Preflight.Blocked = %v, want %s", busyReport.Blocked, CodeSourceBusy)
		}

		linkedVol := newLegacyVolume(t)
		if err := os.Truncate(linkedVol.databasePath(), 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(linkedVol.databasePath(), filepath.Join(t.TempDir(), "second-link.sqlite")); err != nil {
			t.Skipf("hard link unavailable: %v", err)
		}
		_, mountRefusal := MountDatabase(linkedVol.root)
		linkedReport, err := Preflight(ctx, Options{
			LegacyRoot:      linkedVol.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			TargetDir:       canonicalTempDir(t),
		})
		if err != nil {
			t.Fatal(err)
		}
		if mountRefusal != CodeSourceLockUnavailable {
			t.Fatalf("MountDatabase refusal = %q, want %s", mountRefusal, CodeSourceLockUnavailable)
		}
		if len(linkedReport.Blocked) == 0 || linkedReport.Blocked[0] != CodeSourceLockUnavailable {
			t.Fatalf("0-byte hard-linked database Preflight.Blocked = %v, want %s", linkedReport.Blocked, CodeSourceLockUnavailable)
		}
	})
}

func TestIdentifySourceTreePass2ValidatesNonSymlinkAndNonFollowingIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("copyinmarker Identity and ChangeStamp reject symlinks without following targets", func(t *testing.T) {
		dir := canonicalTempDir(t)
		realFile := filepath.Join(dir, "real.txt")
		if err := os.WriteFile(realFile, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(dir, "link.txt")
		if err := os.Symlink(realFile, linkPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := copyinmarker.Identity(linkPath); !errors.Is(err, copyinmarker.ErrSymlink) {
			t.Fatalf("Identity(symlink) error = %v, want ErrSymlink", err)
		}
		if _, err := copyinmarker.ChangeStamp(linkPath); !errors.Is(err, copyinmarker.ErrSymlink) {
			t.Fatalf("ChangeStamp(symlink) error = %v, want ErrSymlink", err)
		}
	})

	t.Run("Pass 2 rejects file-to-symlink, file-to-directory, and directory-to-file swaps", func(t *testing.T) {
		t.Cleanup(func() { beforeIdentifySourceTreePass2 = nil })

		// File swapped to symlink before Pass 2.
		vol1 := newLegacyVolume(t)
		seg1 := vol1.archives()[0]
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeIdentifySourceTreePass2 = func() {
			_ = os.Remove(seg1)
			_ = os.Symlink(outside, seg1)
		}
		_, err := IdentifySourceTree(ctx, vol1.root)
		if !errors.Is(err, ErrSourceSymlinked) {
			t.Fatalf("IdentifySourceTree file->symlink error = %v, want ErrSourceSymlinked", err)
		}

		// File swapped to directory before Pass 2.
		vol2 := newLegacyVolume(t)
		seg2 := vol2.archives()[0]
		beforeIdentifySourceTreePass2 = func() {
			_ = os.Remove(seg2)
			_ = os.Mkdir(seg2, 0o700)
		}
		if _, err := IdentifySourceTree(ctx, vol2.root); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("IdentifySourceTree file->dir error = %v, want not a regular file", err)
		}

		// Root directory swapped to regular file before Pass 2.
		vol3 := newLegacyVolume(t)
		extraDir := filepath.Join(vol3.root, "extra-dir")
		if err := os.MkdirAll(extraDir, 0o700); err != nil {
			t.Fatal(err)
		}
		beforeIdentifySourceTreePass2 = func() {
			_ = os.Remove(extraDir)
			_ = os.WriteFile(extraDir, []byte("file"), 0o600)
		}
		if _, err := IdentifySourceTree(ctx, vol3.root); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("IdentifySourceTree dir->file error = %v, want not a directory", err)
		}
	})
}

func TestCompletedStoreEnforcesDurableReconciliationCoverageAndDomains(t *testing.T) {
	ctx := context.Background()

	buildCompletedExternalWithIncompleteDay := func(t *testing.T) *verifyFixture {
		t.Helper()
		f := newVerifyFixture(t)
		f.reconcile(t)
		// Add an acknowledged permanently incomplete day into both reconciliation_json
		// and usage_coverage_incomplete_days before external-connect and verify.
		db, err := managersqlite.Open(f.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		repo := upgradeoperation.New(db)
		op, found, err := repo.Get(ctx)
		if err != nil || !found {
			_ = db.Close()
			t.Fatalf("load op: found=%v err=%v", found, err)
		}
		var ev ReconcileEvidence
		if err := json.Unmarshal([]byte(op.Reconciliation), &ev); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		ev.ParityGapDays = []string{"2026-03-15"}
		ev.AcknowledgedDays = []string{"2026-03-15"}
		ev.IncompleteDays = []ReconcileMarkedDay{{Day: "2026-03-15", Reason: coverageincomplete.ReasonProjectionLostBeforeV1146}}
		rawEv, err := json.Marshal(ev)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE upgrade_operations SET reconciliation = ? WHERE id = 1`, string(rawEv)); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO usage_coverage_incomplete_days (day, reason, source, recorded_at_ms) VALUES ('2026-03-15', ?, 'reconcile', 1000)`,
			coverageincomplete.ReasonProjectionLostBeforeV1146,
		); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		_ = db.Close()

		f.connectExternal(t)
		f.handManagerConnection(t, verifyExternalAddress, externalTestKey)
		res, err := Verify(ctx, VerifyOptions{
			LegacyRoot:      f.volume.root,
			TargetRoot:      f.target,
			ControlBasePath: "/cpamp-admin",
			observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v8.0.55"), nil
			},
			readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
				return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
			},
			readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
				return cpa.V8ConfigObservation{BaseURL: verifyExternalAddress, ClientKeyCount: 1}, nil
			},
			proveModelSurface: func(context.Context, string, string) error { return nil },
		})
		if err != nil || !res.Complete || res.Stage != model.UpgradeStageComplete {
			t.Fatalf("initial Verify = (%+v, %v), want complete", res, err)
		}
		return f
	}

	t.Run("intact StageComplete store passes RequireActionStage and source-less Verify replay", func(t *testing.T) {
		f := buildCompletedExternalWithIncompleteDay(t)
		if err := RequireActionStage(ctx, f.databasePath(), "verify", nil); err != nil {
			t.Fatalf("RequireActionStage(verify) rejected intact StageComplete store: %v", err)
		}
		replay, err := Verify(ctx, VerifyOptions{
			TargetRoot:      f.target,
			ControlBasePath: "/cpamp-admin",
		})
		if err != nil || !replay.Complete || !replay.AlreadyVerified {
			t.Fatalf("Verify replay = (%+v, %v), want complete & alreadyVerified", replay, err)
		}
	})

	t.Run("missing usage_coverage_incomplete_days row on StageComplete fails RequireActionStage, Verify, and ConfigureAccessRouting", func(t *testing.T) {
		f := buildCompletedExternalWithIncompleteDay(t)
		execTarget(t, f.databasePath(), `DELETE FROM usage_coverage_incomplete_days WHERE day = '2026-03-15'`)

		gateErr := RequireActionStage(ctx, f.databasePath(), "verify", nil)
		if code, ok := UpgradeErrorCodeOf(gateErr); !ok || code != UpgradeErrorVerifyReconciliationUnacknowledged {
			t.Fatalf("RequireActionStage error = (%v, %q), want %s", gateErr, code, UpgradeErrorVerifyReconciliationUnacknowledged)
		}

		_, verifyErr := Verify(ctx, VerifyOptions{TargetRoot: f.target, ControlBasePath: "/cpamp-admin"})
		if code, ok := UpgradeErrorCodeOf(verifyErr); !ok || code != UpgradeErrorVerifyReconciliationUnacknowledged {
			t.Fatalf("Verify error = (%v, %q), want %s", verifyErr, code, UpgradeErrorVerifyReconciliationUnacknowledged)
		}

		_, routeErr := ConfigureAccessRouting(ctx, AccessRoutingOptions{
			TargetRoot:      f.target,
			ControlBasePath: "/cpamp-admin",
		})
		if code, ok := UpgradeErrorCodeOf(routeErr); !ok || code != UpgradeErrorVerifyReconciliationUnacknowledged {
			t.Fatalf("ConfigureAccessRouting error = (%v, %q), want %s", routeErr, code, UpgradeErrorVerifyReconciliationUnacknowledged)
		}
	})

	t.Run("corrupted reconciliation_json, fingerprint mismatch, pending days, and missing established domain fail on StageComplete", func(t *testing.T) {
		// Corrupted reconciliation JSON.
		f1 := buildCompletedExternalWithIncompleteDay(t)
		execTarget(t, f1.databasePath(), `UPDATE upgrade_operations SET reconciliation = '{"domains":[]}' WHERE id = 1`)
		_, err := Verify(ctx, VerifyOptions{TargetRoot: f1.target})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyReconciliationInvalid {
			t.Fatalf("corrupted reconciliation error = (%v, %q), want %s", err, code, UpgradeErrorVerifyReconciliationInvalid)
		}

		// Source fingerprint mismatch between reconciliation.sourceFingerprint and source_fingerprint_after.
		f2 := buildCompletedExternalWithIncompleteDay(t)
		db2, err := managersqlite.Open(f2.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		op2, _, err := upgradeoperation.New(db2).Get(ctx)
		if err != nil {
			_ = db2.Close()
			t.Fatal(err)
		}
		var ev2 ReconcileEvidence
		if err := json.Unmarshal([]byte(op2.Reconciliation), &ev2); err != nil {
			_ = db2.Close()
			t.Fatal(err)
		}
		ev2.SourceFingerprint = strings.Repeat("f", 64)
		rawEv2, _ := json.Marshal(ev2)
		if _, err := db2.Exec(`UPDATE upgrade_operations SET reconciliation = ? WHERE id = 1`, string(rawEv2)); err != nil {
			_ = db2.Close()
			t.Fatal(err)
		}
		_ = db2.Close()
		_, err = Verify(ctx, VerifyOptions{TargetRoot: f2.target})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifySourceMismatch {
			t.Fatalf("fingerprint mismatch error = (%v, %q), want %s", err, code, UpgradeErrorVerifySourceMismatch)
		}

		// Non-empty PendingReconcileDays on StageComplete fails RequireVerifyStage and completedVerifyResultForStore.
		f3 := buildCompletedExternalWithIncompleteDay(t)
		op3 := f3.operation(t)
		op3.PendingReconcileDays = []string{"2026-03-16"}
		err = RequireVerifyStage(ctx, nil, op3, true)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorReconcileMismatch {
			t.Fatalf("RequireVerifyStage pending reconcile error = (%v, %q), want %s", err, code, UpgradeErrorReconcileMismatch)
		}
		db3, err := managersqlite.Open(f3.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		_, err = completedVerifyResultForStore(ctx, db3, f3.paths, op3, upgradeoperation.ExternalBinding{}, true)
		_ = db3.Close()
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorReconcileMismatch {
			t.Fatalf("completedVerifyResultForStore pending reconcile error = (%v, %q), want %s", err, code, UpgradeErrorReconcileMismatch)
		}

		// Missing established domain in domains_done.
		f4 := buildCompletedExternalWithIncompleteDay(t)
		op4 := f4.operation(t)
		var trimmedDomains []string
		for _, d := range op4.DomainsDone {
			if d != model.UpgradeDomainCoverage {
				trimmedDomains = append(trimmedDomains, d)
			}
		}
		ve4, err := op4.VerificationEvidence()
		if err != nil {
			t.Fatal(err)
		}
		ve4.Domains = trimmedDomains
		rawDomains, _ := json.Marshal(trimmedDomains)
		rawVerif, _ := json.Marshal(ve4)
		execTarget(t, f4.databasePath(), fmt.Sprintf(`UPDATE upgrade_operations SET domains_done = '%s', verification = '%s' WHERE id = 1`, string(rawDomains), string(rawVerif)))
		_, err = Verify(ctx, VerifyOptions{TargetRoot: f4.target})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyDomainsIncomplete {
			t.Fatalf("missing domain error = (%v, %q), want %s", err, code, UpgradeErrorVerifyDomainsIncomplete)
		}
	})
}

func TestExternalConnectCanonicalizesLegacySettingsURLForms(t *testing.T) {
	ctx := context.Background()
	legacyForms := []string{
		"127.0.0.1:8317",
		"http://127.0.0.1:8317/v0",
		"http://127.0.0.1:8317/v0/management",
	}
	for _, rawForm := range legacyForms {
		t.Run(rawForm, func(t *testing.T) {
			f := newVerifyFixture(t)
			f.reconcile(t)
			// Seed raw 1.x URL spelling directly into both manager_config_v1 and setup.
			key, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
			if err != nil {
				t.Fatal(err)
			}
			protector, err := security.NewProtector(key)
			if err != nil {
				t.Fatal(err)
			}
			db, err := managersqlite.Open(f.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			repo := setting.New(db, protector)
			if err := repo.SaveManagerConfig(ctx, model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{
					CPABaseURL:    rawForm,
					ManagementKey: externalTestKey,
				},
			}); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := repo.SaveSetup(ctx, model.Setup{
				CPAUpstreamURL: rawForm,
				ManagementKey:  externalTestKey,
			}); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			_ = db.Close()

			// Pass canonical v2 BaseURL into ExternalConnect while legacy 1.x spelling lives in settings.
			connRes, err := ExternalConnect(ctx, ExternalConnectOptions{
				TargetRoot:    f.target,
				BaseURL:       "http://127.0.0.1:8317",
				ManagementKey: externalTestKey,
				observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
					return cpa.ClassifyExternalVersion("v8.0.55"), nil
				},
				readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
					return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
				},
				readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
					return cpa.V8ConfigObservation{BaseURL: "http://127.0.0.1:8317", ClientKeyCount: 1}, nil
				},
				proveModelSurface: func(context.Context, string, string) error { return nil },
			})
			if err != nil {
				t.Fatalf("ExternalConnect(%q) error = %v", rawForm, err)
			}
			if connRes.Stage != model.UpgradeStageExternalConnect {
				t.Fatalf("stage = %q, want external_connect", connRes.Stage)
			}

			// Verify settings were canonicalized and both routing + Verify succeed.
			db2, err := managersqlite.Open(f.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			st := store.New(db2, protector)
			cfg, ok, err := st.LoadManagerConfig(ctx)
			_ = db2.Close()
			if err != nil || !ok || cfg.CPAConnection.CPABaseURL != "http://127.0.0.1:8317" {
				t.Fatalf("stored CPABaseURL = %q (ok=%v err=%v), want http://127.0.0.1:8317", cfg.CPAConnection.CPABaseURL, ok, err)
			}

			if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
				TargetRoot:      f.target,
				ControlBasePath: "/cpamp-admin",
			}); err != nil {
				t.Fatalf("ConfigureAccessRouting failed after canonicalization: %v", err)
			}

			verRes, err := Verify(ctx, VerifyOptions{
				LegacyRoot: f.volume.root,
				TargetRoot: f.target,
				observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
					return cpa.ClassifyExternalVersion("v8.0.55"), nil
				},
				readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
					return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
				},
				readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
					return cpa.V8ConfigObservation{BaseURL: "http://127.0.0.1:8317", ClientKeyCount: 1}, nil
				},
				proveModelSurface: func(context.Context, string, string) error { return nil },
			})
			if err != nil || !verRes.Complete {
				t.Fatalf("Verify after canonicalization = (%+v, %v), want complete", verRes, err)
			}
		})
	}
}

func TestRootOverlapAndScratchExclusionGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("rejectOverlap and rollback refuse bundleDir containing sourceRoot while allowing default upgrade-backup", func(t *testing.T) {
		parent := canonicalTempDir(t)
		source := filepath.Join(parent, "legacy")
		target := canonicalTempDir(t)
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}

		// Default <target>/upgrade-backup remains valid.
		if err := RefuseOverlappingRoots(source, target, DefaultBundleDirectory(target)); err != nil {
			t.Fatalf("RefuseOverlappingRoots rejected default upgrade-backup: %v", err)
		}

		// bundleDir containing sourceRoot must fail.
		if err := RefuseOverlappingRoots(source, target, parent); err == nil || !strings.Contains(err.Error(), "1.x source root must not sit inside the backup destination") {
			t.Fatalf("RefuseOverlappingRoots(bundle contains source) = %v, want rejection", err)
		}

		// PrepareRollback and VerifyRollback refuse BackupDir containing sourceRoot or targetRoot.
		_, prepErr := PrepareRollback(ctx, RollbackOptions{
			LegacyRoot: source,
			TargetRoot: target,
			BackupDir:  parent,
		})
		if code, ok := UpgradeErrorCodeOf(prepErr); !ok || code != UpgradeErrorRollbackRootsOverlap {
			t.Fatalf("PrepareRollback(BackupDir contains source) = (%v, %q), want %s", prepErr, code, UpgradeErrorRollbackRootsOverlap)
		}

		_, verErr := RecordRollback(ctx, RollbackObservation{
			LegacyRoot: source,
			TargetRoot: target,
			BackupDir:  parent,
		})
		if code, ok := UpgradeErrorCodeOf(verErr); !ok || code != UpgradeErrorRollbackRootsOverlap {
			t.Fatalf("RecordRollback(BackupDir contains source) = (%v, %q), want %s", verErr, code, UpgradeErrorRollbackRootsOverlap)
		}
	})

	t.Run("Preflight enforces RefuseOverlappingRoots up front before reading source", func(t *testing.T) {
		vol := newLegacyVolume(t)
		nestedTarget := filepath.Join(vol.root, "nested-target")
		if _, err := Preflight(ctx, Options{
			LegacyRoot: vol.root,
			TargetDir:  nestedTarget,
		}); err == nil || !strings.Contains(err.Error(), "must not contain each other") {
			t.Fatalf("Preflight(target inside legacy) = %v, want overlap error", err)
		}
	})
}
