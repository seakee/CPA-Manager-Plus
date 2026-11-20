package upgradeworker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestWalkTargetOwnershipRepairsAllowlistedMembersAndExcludesRuntime(t *testing.T) {
	targetRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	for _, rel := range []string{
		"usage.sqlite",
		"usage.sqlite-wal",
		"usage.sqlite-shm",
		"usage.sqlite-journal",
		"usage.sqlite.manager.lock",
		"data.key",
		"runtime-secret",
		"usage.sqlite.lock",
	} {
		if err := os.WriteFile(filepath.Join(targetRoot, rel), []byte("data"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(targetRoot, "usage-archives", "2026-03"), 0o750); err != nil {
		t.Fatalf("mkdir usage-archives: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "usage-archives", "2026-03", "segment-000001.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetRoot, ".upgrade-copy-in"), 0o750); err != nil {
		t.Fatalf("mkdir .upgrade-copy-in: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, ".upgrade-copy-in", "copy-in.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetRoot, "runtime", "cpa"), 0o750); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "runtime", "cpa", "bin"), []byte("bin"), 0o600); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}

	var visited []string
	if err := walkTargetOwnership(targetRoot, func(relPath, _ string, _ os.FileInfo) error {
		visited = append(visited, filepath.ToSlash(relPath))
		return nil
	}); err != nil {
		t.Fatalf("walkTargetOwnership: %v", err)
	}

	want := []string{
		".upgrade-copy-in",
		".upgrade-copy-in/copy-in.json",
		"data.key",
		"usage-archives",
		"usage-archives/2026-03",
		"usage-archives/2026-03/segment-000001.jsonl",
		"usage.sqlite",
		"usage.sqlite-journal",
		"usage.sqlite-shm",
		"usage.sqlite-wal",
		"usage.sqlite.manager.lock",
	}
	slices.Sort(visited)
	if !slices.Equal(visited, want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
}

func TestWalkTargetOwnershipFailsClosedOnSymlinksAndHardlinks(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	t.Run("symlinked_wal", func(t *testing.T) {
		targetRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve target root: %v", err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(targetRoot, "usage.sqlite-wal")); err != nil {
			t.Fatalf("symlink wal: %v", err)
		}
		if err := walkTargetOwnership(targetRoot, nil); err == nil {
			t.Fatal("expected symlinked usage.sqlite-wal to fail closed")
		}
	})

	t.Run("symlinked_nested_archive", func(t *testing.T) {
		targetRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve target root: %v", err)
		}
		archiveDir := filepath.Join(targetRoot, "usage-archives")
		if err := os.MkdirAll(archiveDir, 0o750); err != nil {
			t.Fatalf("mkdir usage-archives: %v", err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(archiveDir, "linked.jsonl")); err != nil {
			t.Fatalf("symlink archive: %v", err)
		}
		if err := walkTargetOwnership(targetRoot, nil); err == nil {
			t.Fatal("expected symlink inside usage-archives to fail closed")
		}
	})

	t.Run("hardlinked_lock_file", func(t *testing.T) {
		targetRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve target root: %v", err)
		}
		sibling := filepath.Join(targetRoot, "other.txt")
		if err := os.WriteFile(sibling, []byte("shared"), 0o600); err != nil {
			t.Fatalf("write sibling: %v", err)
		}
		if err := os.Link(sibling, filepath.Join(targetRoot, "usage.sqlite.manager.lock")); err != nil {
			t.Skipf("hardlinks unsupported in temp filesystem: %v", err)
		}
		if err := walkTargetOwnership(targetRoot, nil); err == nil {
			t.Fatal("expected multi-link regular file to fail closed")
		}
	})
}

func TestSocketPeerEnforcementRefusesUntrustedPeer(t *testing.T) {
	svc := &Service{}
	req := httptest.NewRequest(http.MethodGet, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
	req = req.WithContext(withSocketPeerEnforcement(req.Context(), false))
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestWalkTargetOwnershipEnforcesDepthBound(t *testing.T) {
	targetRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	deep := filepath.Join(targetRoot, "usage-archives")
	for i := range maxTargetOwnershipDepth {
		deep = filepath.Join(deep, "d"+strconv.Itoa(i))
	}
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatalf("mkdir deep archive tree: %v", err)
	}
	if err := walkTargetOwnership(targetRoot, nil); !errors.Is(err, errTargetOwnershipUnsupported) {
		t.Fatalf("walkTargetOwnership deep error = %v, want %v", err, errTargetOwnershipUnsupported)
	}
}

func TestIsMutatingWizardRouteIncludesVerifyRoutingAndArtifactMutations(t *testing.T) {
	for _, tc := range []struct {
		method       string
		target       string
		wantMutating bool
		wantRepair   bool
	}{
		{http.MethodPost, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/verify", true, true},
		{http.MethodPost, "http://upgrade-worker/installation/upgrade/verify", false, false},
		{http.MethodGet, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/verify", false, false},
		{http.MethodPost, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/verify?x=1", false, false},
		{http.MethodPost, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/routing", true, true},
		{http.MethodPost, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/runtime/artifact/probe", true, true},
		{http.MethodPost, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/runtime/artifact/acquire", true, true},
		{http.MethodPut, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/runtime/artifact/upload", true, true},
		{http.MethodGet, "http://upgrade-worker/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", false, false},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		gotMut, gotRep := isMutatingWizardRoute(req)
		if gotMut != tc.wantMutating || gotRep != tc.wantRepair {
			t.Fatalf("%s %s = (%v, %v), want (%v, %v)", tc.method, tc.target, gotMut, gotRep, tc.wantMutating, tc.wantRepair)
		}
	}
}
