package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// watchSnapshots reports every cpamp-* entry that appears under root while it is
// watching. A private copy is removed on the way out of a successful read, so a
// listing taken after the request cannot prove nothing was staged: the watcher
// runs while the request is in flight.
func watchSnapshots(t *testing.T, root string) func() []string {
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

// materializingManagerStore copies a real 1.x source with a live WAL into a
// fresh data root and marks it as an unfinished copy-in, so the entry's
// Manager-store fallback authenticates it from a private copy. It returns the
// target database path and the three members' digests as the read found them.
func materializingManagerStore(t *testing.T, source string) (string, map[string]string) {
	t.Helper()
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatalf("open the copied source: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`create table entry_root_padding (payload blob)`,
		`insert into entry_root_padding values (zeroblob(4194304))`,
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
	digests := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatalf("read the source member %q: %v", suffix, err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			t.Fatalf("write the target member %q: %v", suffix, err)
		}
		digests[suffix] = fmt.Sprintf("%x", sha256.Sum256(data))
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
	return target, digests
}

// TestStartupEntrySnapshotStaysOutsideConfiguredSourceRoot is the entry half of
// the two roots' contract. The configured 1.x root can exist and still hold no
// database the mount policy accepts; the credential then falls back to the
// Manager store, which is right — but the root is still a tree the upgrade
// preserves, so the private copy that authenticates a materializing target must
// be refused there. Before the fix the entry passed an empty source root and a
// full database copy was staged inside the configured source tree.
func TestStartupEntrySnapshotStaysOutsideConfiguredSourceRoot(t *testing.T) {
	const key = "entry-snapshot-root-credential"
	_, paths := upgradeAuthSource(t, key)
	target, before := materializingManagerStore(t, paths.Database)

	// A configured root that exists yet holds no usable database: the mount
	// policy counts it as no mount, which must not forget that it is a preserved
	// tree.
	legacyRoot := t.TempDir()
	t.Setenv(legacyRootEnvironment, legacyRoot)
	t.Setenv("TMPDIR", legacyRoot)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(target), legacypreflight.Options{}, 5*time.Second)
	if h.upgradeAuth == nil {
		t.Fatal("the Upgrade entry was not mounted")
	}

	watch := watchSnapshots(t, legacyRoot)
	r := httptest.NewRequest(http.MethodPost, upgradeauth.Path, nil)
	r.Host = "manager.test"
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	names := watch()
	t.Logf("entry status=%d body=%s snapshots inside configured source=%v", w.Code, w.Body.String(), names)

	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"source_unavailable"`) {
		t.Fatalf("entry answered %d: %s", w.Code, w.Body.String())
	}
	if len(names) != 0 {
		t.Fatalf("the entry staged %v inside the configured source root", names)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(after)); got != before[""] {
		t.Fatal("the refused entry read rewrote the target database")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		data, err := os.ReadFile(target + suffix)
		if err != nil {
			t.Fatalf("read target member %q: %v", suffix, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != before[suffix] {
			t.Fatalf("the refused entry read rewrote the target member %q", suffix)
		}
	}
}
