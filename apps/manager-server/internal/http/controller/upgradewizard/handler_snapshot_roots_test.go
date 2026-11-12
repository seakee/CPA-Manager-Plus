package upgradewizard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

// materializingManagerTarget copies a real 1.x source with a live WAL into a
// fresh data root and marks it as an unfinished copy-in. The credential read
// behind every wizard request then has to authenticate it from a private copy:
// opening the store in place would rewrite its WAL index.
func materializingManagerTarget(t *testing.T, sourceRoot string) string {
	t.Helper()
	source := filepath.Join(sourceRoot, "usage.sqlite")
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatalf("open the copied source: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`create table snapshot_root_padding (payload blob)`,
		`insert into snapshot_root_padding values (zeroblob(4194304))`,
	} {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatalf("prepare the WAL source: %v", err)
		}
	}
	if info, err := os.Lstat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the copied source has no WAL: info=%v err=%v", info, err)
	}

	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "usage.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatalf("read the source member %q: %v", suffix, err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			t.Fatalf("write the target member %q: %v", suffix, err)
		}
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          target,
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
	}); err != nil {
		t.Fatalf("publish the materializing marker: %v", err)
	}
	return target
}

// digestManagerMembers records the three members a materializing target must
// keep byte for byte.
func digestManagerMembers(t *testing.T, database string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(database + suffix)
		if err != nil {
			t.Fatalf("read target member %q: %v", suffix, err)
		}
		digests[suffix] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return digests
}

// watchSnapshotDirs reports every private copy directory that appears under root
// while it is watching. A snapshot is removed on the way out of a successful
// read, so a check after the request alone cannot prove nothing was staged: the
// watcher runs while the request is in flight.
func watchSnapshotDirs(t *testing.T, root string) func() []string {
	t.Helper()
	stop := make(chan struct{})
	seen := make(chan []string, 1)
	go func() {
		names := map[string]bool{}
		for {
			entries, _ := os.ReadDir(root)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "cpamp-") {
					names[entry.Name()] = true
				}
			}
			select {
			case <-stop:
				list := make([]string, 0, len(names))
				for name := range names {
					list = append(list, name)
				}
				seen <- list
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var once sync.Once
	return func() []string {
		var list []string
		once.Do(func() {
			close(stop)
			list = <-seen
		})
		return list
	}
}

func snapshotRouteStatus(t *testing.T, handler *Handler, path string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "manager.test"
	r.Header.Set("Authorization", bearer(testAdminKey))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// waitForMountFlight waits until the handler has no lookup in flight, so a test
// that releases a blocked resolver leaves no goroutine behind.
func waitForMountFlight(t *testing.T, handler *Handler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		handler.mountMu.Lock()
		flight := handler.mountFlight
		handler.mountMu.Unlock()
		if flight == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the mount flight did not finish after its resolver was released")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestMountTimeoutKeepsTheSnapshotOutsideTheConfiguredSourceRoot is the
// mount-timeout half of the two roots' contract. A lookup that outlives its
// bound has not assessed the mount, so the request falls back to the Manager
// store — but the configured source root it already resolved must never be
// resolved again to keep the private copy out of it. The route fails closed with
// the mount's own deadline before the snapshot's exclusion check runs, so even a
// TMPDIR inside the configured root stages nothing there. Before the fix the
// timeout answer dropped the root and a full database copy was staged inside the
// source tree.
func TestMountTimeoutKeepsTheSnapshotOutsideTheConfiguredSourceRoot(t *testing.T) {
	target := materializingManagerTarget(t, legacySourceDir(t))
	before := digestManagerMembers(t, target)
	configured := t.TempDir() // A configured root that holds no accepted database.
	handler := newHandler(configured, target)
	handler.MountTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	handler.lookupMount = func(root string) mountResolution {
		<-release
		return mountResolution{root: root, code: CodeSourceMissing}
	}

	// The flight publishes the root before it runs the lookup that hangs, so a
	// bound that expires still names the tree the copy must stay out of.
	expired := handler.resolveMount(context.Background())
	if expired.code != CodeMountTimeout || !expired.rootKnown || expired.root != configured {
		t.Fatalf("timed mount = %+v, want %q kept as a known root", expired, configured)
	}

	t.Setenv("TMPDIR", configured)
	watch := watchSnapshotDirs(t, configured)
	status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
	names := watch()
	t.Logf("timed mount status=%d body=%s snapshots inside configured source=%v", status, body, names)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeMountTimeout) {
		t.Fatalf("timed mount answered %d: %s", status, body)
	}
	if strings.Contains(body, CodeSourceUnreadable) {
		t.Fatalf("timed mount reported the deadline as an unreadable source: %s", body)
	}
	if len(names) != 0 {
		t.Fatalf("the timed-out route staged %v inside the configured source root", names)
	}
	after := digestManagerMembers(t, target)
	for suffix, digest := range before {
		if after[suffix] != digest {
			t.Fatalf("the refused read rewrote the target member %q", suffix)
		}
	}
}

// TestMountTimeoutFailsClosedBeforeTheMaterializingSnapshot pins the request
// chain behind the timeout guard. The preflight guard stops that route before the
// inspection, but authentication runs first, and a materializing Manager target
// is authenticated from a private copy whose exclusion check canonicalizes the
// configured source root — the same root whose lookup just timed out. That
// canonicalization must not run on the request: the route answers the mount's
// deadline before the credential read resolves the root, and a scratch directory
// outside both trees (where a read that proceeded would have succeeded) stays
// empty.
func TestMountTimeoutFailsClosedBeforeTheMaterializingSnapshot(t *testing.T) {
	target := materializingManagerTarget(t, legacySourceDir(t))
	before := digestManagerMembers(t, target)
	configured := t.TempDir()
	scratch := t.TempDir() // Outside both roots: a snapshot here would succeed.
	t.Setenv("TMPDIR", scratch)
	handler := newHandler(configured, target)
	handler.MountTimeout = 20 * time.Millisecond
	release := make(chan struct{})
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(stop)
	handler.lookupMount = func(string) mountResolution {
		<-release
		return mountResolution{code: CodeSourceMissing}
	}

	watch := watchSnapshotDirs(t, scratch)
	for _, resource := range []string{StatusSuffix, PreflightSuffix} {
		status, body := snapshotRouteStatus(t, handler, testPrefix+resource)
		t.Logf("timed materializing mount %s status=%d body=%s", resource, status, body)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeMountTimeout) {
			t.Fatalf("timed materializing mount %s answered %d: %s", resource, status, body)
		}
		if strings.Contains(body, CodeSourceUnreadable) {
			t.Fatalf("timed materializing mount %s reported the deadline as an unreadable source: %s", resource, body)
		}
	}
	names := watch()
	stop()
	waitForMountFlight(t, handler)
	if len(names) != 0 {
		t.Fatalf("the timed-out route staged %v outside both roots", names)
	}
	after := digestManagerMembers(t, target)
	for suffix, digest := range before {
		if after[suffix] != digest {
			t.Fatalf("the refused read rewrote the target member %q", suffix)
		}
	}
}

// TestUnresolvedRootRefusesTheMaterializingSnapshot is the fail-closed half: a
// bound that expires before the root itself was resolved cannot name the tree
// the copy must stay out of, so the materializing read is refused rather than
// staged in the operating system's temporary directory. A live Manager store is
// unaffected, because it needs no snapshot at all.
func TestUnresolvedRootRefusesTheMaterializingSnapshot(t *testing.T) {
	target := materializingManagerTarget(t, legacySourceDir(t))
	before := digestManagerMembers(t, target)
	release := make(chan struct{})
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	handler := &Handler{
		LegacyRoot:   func() string { <-release; return "" },
		Database:     func() string { return target },
		Timeout:      10 * time.Second,
		MountTimeout: 50 * time.Millisecond,
	}
	t.Cleanup(stop)

	unknown := handler.resolveMount(context.Background())
	if unknown.code != CodeMountTimeout || unknown.rootKnown {
		t.Fatalf("unresolved timed mount = %+v, want a timeout without a known root", unknown)
	}

	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	watch := watchSnapshotDirs(t, scratch)
	status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
	names := watch()
	stop()
	waitForMountFlight(t, handler)
	t.Logf("unresolved root status=%d body=%s snapshots staged=%v", status, body, names)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeSourceUnreadable) {
		t.Fatalf("unresolved root answered %d: %s", status, body)
	}
	if len(names) != 0 {
		t.Fatalf("the unresolved root staged %v instead of refusing", names)
	}
	after := digestManagerMembers(t, target)
	for suffix, digest := range before {
		if after[suffix] != digest {
			t.Fatalf("the refused read rewrote the target member %q", suffix)
		}
	}
}

// TestPreservedStatusFailsClosedOnLinkedDatabaseAndSidecarsWithoutWAL verifies
// that readRecordedStatusPreserved enforces InspectSourceFile on the database
// and all three journal sidecars (-wal, -shm, -journal) even when no WAL frames
// are pending, returning 503 rather than 200 recorded:false.
func TestPreservedStatusFailsClosedOnLinkedDatabaseAndSidecarsWithoutWAL(t *testing.T) {
	sourceRoot := legacySourceDir(t)
	sourceDB := filepath.Join(sourceRoot, "usage.sqlite")
	cleanBytes, err := os.ReadFile(sourceDB)
	if err != nil {
		t.Fatalf("read clean 1.x source: %v", err)
	}

	makePreservedHandler := func(t *testing.T) (string, *Handler) {
		t.Helper()
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(dbPath, cleanBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dbPath, &Handler{
			LegacyRoot:           func() string { return sourceRoot },
			Database:             func() string { return dbPath },
			DataKey:              func() string { return filepath.Join(dir, "data.key") },
			Timeout:              5 * time.Second,
			PreserveManagerStore: true,
		}
	}

	// Control: clean preserved store without upgrade_operations returns 200 recorded:false.
	_, controlHandler := makePreservedHandler(t)
	if status, body := snapshotRouteStatus(t, controlHandler, testPrefix+StatusSuffix); status != http.StatusOK || !strings.Contains(body, `"recorded":false`) {
		t.Fatalf("clean preserved status = %d: %s, want 200 recorded:false", status, body)
	}

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, dbPath string)
	}{
		{
			name: "symlinked-database",
			mutate: func(t *testing.T, dbPath string) {
				realDB := dbPath + ".real"
				if err := os.Rename(dbPath, realDB); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realDB, dbPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlinked-database",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.Link(dbPath, filepath.Join(t.TempDir(), "db-link")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked-shm",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.Symlink(dbPath, dbPath+"-shm"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlinked-shm",
			mutate: func(t *testing.T, dbPath string) {
				peer := filepath.Join(t.TempDir(), "shm-link")
				if err := os.WriteFile(peer, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(peer, dbPath+"-shm"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlinked-empty-wal",
			mutate: func(t *testing.T, dbPath string) {
				peer := filepath.Join(t.TempDir(), "wal-link")
				if err := os.WriteFile(peer, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(peer, dbPath+"-wal"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked-journal",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.Symlink(dbPath, dbPath+"-journal"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlinked-empty-journal",
			mutate: func(t *testing.T, dbPath string) {
				peer := filepath.Join(t.TempDir(), "journal-link")
				if err := os.WriteFile(peer, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(peer, dbPath+"-journal"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nonempty-rollback-journal",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-journal", []byte("pending-rollback-journal"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, handler := makePreservedHandler(t)
			tc.mutate(t, dbPath)
			status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
			if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeOperationUnread) {
				t.Fatalf("%s status = %d (%s), want 503 %s", tc.name, status, body, CodeOperationUnread)
			}
		})
	}
}

// TestPreservedStatusFailsClosedWhenTargetDatabaseMissingWithOrphanSidecars
// enforces that a missing target database never answers 200 recorded:false when
// orphan -wal, -shm, or -journal sidecars remain in the target directory.
func TestPreservedStatusFailsClosedWhenTargetDatabaseMissingWithOrphanSidecars(t *testing.T) {
	sourceRoot := legacySourceDir(t)
	liveTarget := materializingManagerTarget(t, sourceRoot)
	liveWALBytes, err := os.ReadFile(liveTarget + "-wal")
	if err != nil || len(liveWALBytes) == 0 {
		t.Fatalf("read valid WAL bytes (len=%d, err=%v)", len(liveWALBytes), err)
	}

	makeMissingTargetHandler := func(t *testing.T) (string, *Handler) {
		t.Helper()
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dbPath, &Handler{
			LegacyRoot:           func() string { return sourceRoot },
			Database:             func() string { return dbPath },
			DataKey:              func() string { return filepath.Join(dir, "data.key") },
			Timeout:              5 * time.Second,
			PreserveManagerStore: true,
		}
	}

	// Control: clean absent target with no sidecars reports recorded:false / preflight.
	_, cleanHandler := makeMissingTargetHandler(t)
	if status, body := snapshotRouteStatus(t, cleanHandler, testPrefix+StatusSuffix); status != http.StatusOK ||
		!strings.Contains(body, `"recorded":false`) ||
		!strings.Contains(body, `"nextAction":"preflight"`) {
		t.Fatalf("clean missing target status = %d: %s, want 200 recorded:false/preflight", status, body)
	}

	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dbPath string)
	}{
		{
			name: "orphan-valid-nonempty-wal",
			plant: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-wal", liveWALBytes, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan-corrupt-nonempty-wal",
			plant: func(t *testing.T, dbPath string) {
				corrupt := append([]byte(nil), liveWALBytes...)
				corrupt[0] ^= 0xff
				if err := os.WriteFile(dbPath+"-wal", corrupt, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan-empty-wal",
			plant: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-wal", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan-shm",
			plant: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-shm", []byte("stale-wal-index"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan-journal",
			plant: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-journal", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan-wal-and-shm",
			plant: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-wal", liveWALBytes, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dbPath+"-shm", []byte("stale-wal-index"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, handler := makeMissingTargetHandler(t)
			tc.plant(t, dbPath)
			status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
			if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeOperationUnread) {
				t.Fatalf("%s status = %d (%s), want 503 %s", tc.name, status, body, CodeOperationUnread)
			}
			if _, err := os.Lstat(dbPath); !os.IsNotExist(err) {
				t.Fatalf("%s status created missing target database (err=%v)", tc.name, err)
			}
		})
	}
}

func TestPreservedWizardWALOnlyOperationHelperProcess(t *testing.T) {
	dbPath := os.Getenv("CPAMP_WIZARD_WAL_ONLY_DB")
	if dbPath == "" {
		return
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA wal_autocheckpoint = 0`,
		`CREATE TABLE upgrade_operations (
			id INTEGER PRIMARY KEY,
			stage TEXT NOT NULL,
			domains_done TEXT NOT NULL DEFAULT '[]',
			source_kind TEXT NOT NULL DEFAULT 'legacy-manager',
			source_version TEXT NOT NULL DEFAULT 'v1.14.10',
			reconciliation TEXT NOT NULL DEFAULT '{}',
			manual_items TEXT NOT NULL DEFAULT '[]',
			error_code TEXT NOT NULL DEFAULT '',
			error_detail TEXT NOT NULL DEFAULT '',
			created_at_ms INTEGER NOT NULL DEFAULT 1000,
			updated_at_ms INTEGER NOT NULL DEFAULT 2000
		)`,
		`INSERT INTO upgrade_operations (id, stage, domains_done, source_kind, source_version, reconciliation, manual_items, created_at_ms, updated_at_ms)
		 VALUES (1, 'reconcile', '["source_copy"]', 'legacy-manager', 'v1.14.10', '{}', '[]', 1000, 2000)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	os.Exit(0)
}

// TestPreservedStatusDetectsConcurrentJournalAndRewritesOnFastPath enforces
// that readRecordedStatusPreserved re-verifies database and sidecar identity
// after RecordedOperationEvidence so a concurrent WAL creation or in-place
// rewrite routes to a private snapshot or fails closed instead of answering
// 200 recorded:false.
func TestPreservedStatusDetectsConcurrentJournalAndRewritesOnFastPath(t *testing.T) {
	sourceRoot := legacySourceDir(t)
	donorDir := t.TempDir()
	donorDB := filepath.Join(donorDir, "usage.sqlite")
	cleanBytes, err := os.ReadFile(filepath.Join(sourceRoot, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(donorDB, cleanBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPreservedWizardWALOnlyOperationHelperProcess$")
	cmd.Env = append(os.Environ(), "CPAMP_WIZARD_WAL_ONLY_DB="+donorDB)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wizard WAL-only helper: %v %s", err, out)
	}
	walBytes, err := os.ReadFile(donorDB + "-wal")
	if err != nil || len(walBytes) == 0 {
		t.Fatalf("read WAL-only bytes (len=%d, err=%v)", len(walBytes), err)
	}

	makeHandler := func(t *testing.T) (string, *Handler) {
		t.Helper()
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(dbPath, cleanBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dbPath, &Handler{
			LegacyRoot:           func() string { return sourceRoot },
			Database:             func() string { return dbPath },
			DataKey:              func() string { return filepath.Join(dir, "data.key") },
			Timeout:              5 * time.Second,
			PreserveManagerStore: true,
		}
	}

	t.Run("wal-only-recorded-operation-appears-routes-to-snapshot", func(t *testing.T) {
		_, handler := makeHandler(t)
		origHook := afterPreservedEvidenceProbe
		afterPreservedEvidenceProbe = func(target string) {
			_ = os.WriteFile(target+"-wal", walBytes, 0o600)
		}
		defer func() { afterPreservedEvidenceProbe = origHook }()

		status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
		if status != http.StatusOK || !strings.Contains(body, `"recorded":true`) || !strings.Contains(body, `"stage":"reconcile"`) {
			t.Fatalf("status = %d (%s), want 200 recorded:true stage:reconcile", status, body)
		}
	})

	t.Run("corrupt-wal-appears-fails-closed", func(t *testing.T) {
		_, handler := makeHandler(t)
		corruptWAL := append([]byte(nil), walBytes...)
		corruptWAL[0] ^= 0xff
		origHook := afterPreservedEvidenceProbe
		afterPreservedEvidenceProbe = func(target string) {
			_ = os.WriteFile(target+"-wal", corruptWAL, 0o600)
		}
		defer func() { afterPreservedEvidenceProbe = origHook }()

		status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeOperationUnread) {
			t.Fatalf("status = %d (%s), want 503 %s", status, body, CodeOperationUnread)
		}
	})

	t.Run("database-rewritten-with-restored-mtime-fails-closed", func(t *testing.T) {
		dbPath, handler := makeHandler(t)
		// The preserved-evidence guard compares identity, size, mtime and change
		// stamp, so a rewrite that restores its own bytes and mtime is only
		// detectable through the change stamp. Pin that stamp into the past
		// first: on Windows ChangeTime is file metadata, so every later write
		// reports a strictly later one instead of relying on two writes landing
		// in different clock ticks.
		testutil.AgeChangeStamp(t, dbPath, copyinmarker.ChangeStamp)
		origInfo, err := os.Lstat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		origID, err := copyinmarker.Identity(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		origStamp, err := copyinmarker.ChangeStamp(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		// The rewrite must change bytes, not merely re-write the ones already
		// there. Mutate the header's user-version field, which SQLite tolerates
		// on read, so the probe still answers normally and only the before/after
		// identity comparison can reject the change.
		const userVersionOffset = 60
		rewritten := append([]byte(nil), cleanBytes...)
		rewrittenVersion := binary.BigEndian.Uint32(cleanBytes[userVersionOffset:]) ^ 0xa5a5a5a5
		if rewrittenVersion == binary.BigEndian.Uint32(cleanBytes[userVersionOffset:]) {
			rewrittenVersion++
		}
		binary.BigEndian.PutUint32(rewritten[userVersionOffset:], rewrittenVersion)

		var hookErr error
		origHook := afterPreservedEvidenceProbe
		afterPreservedEvidenceProbe = func(target string) {
			hookErr = func() error {
				f, err := os.OpenFile(target, os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				if _, err := f.WriteAt(rewritten[userVersionOffset:userVersionOffset+4], userVersionOffset); err != nil {
					_ = f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				return os.Chtimes(target, origInfo.ModTime(), origInfo.ModTime())
			}()
			if hookErr != nil {
				return
			}
			// Name a broken precondition here rather than reporting it as a
			// guard failure below.
			testutil.RequireChangeStampAdvanced(t, target, origStamp, copyinmarker.ChangeStamp)
		}
		defer func() { afterPreservedEvidenceProbe = origHook }()

		status, body := snapshotRouteStatus(t, handler, testPrefix+StatusSuffix)
		if hookErr != nil {
			t.Fatalf("apply mid-probe rewrite: %v", hookErr)
		}
		if status != http.StatusServiceUnavailable || !strings.Contains(body, CodeOperationUnread) {
			t.Fatalf("status = %d (%s), want 503 %s", status, body, CodeOperationUnread)
		}

		// Prove the fixture exercised the invariant instead of assuming it: the
		// file keeps its identity, size and restored mtime, and only its content
		// moved.
		afterInfo, err := os.Lstat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		afterID, err := copyinmarker.Identity(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if afterID != origID || afterInfo.Size() != origInfo.Size() || afterInfo.ModTime().UnixNano() != origInfo.ModTime().UnixNano() {
			t.Fatalf("fixture did not preserve inode/size/mtime: id %+v->%+v size %d->%d mtime %d->%d",
				origID, afterID, origInfo.Size(), afterInfo.Size(), origInfo.ModTime().UnixNano(), afterInfo.ModTime().UnixNano())
		}
		current, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(current[userVersionOffset:userVersionOffset+4], cleanBytes[userVersionOffset:userVersionOffset+4]) {
			t.Fatal("fixture rewrite left the user-version field unchanged; the mutation never reached the file")
		}
	})
}
