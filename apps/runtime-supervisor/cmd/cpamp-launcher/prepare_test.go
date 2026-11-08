//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testSeedYAML    = "port: 8317\nhost: 127.0.0.1\n"
	testPasswdEntry = "root:x:0:0:root:/root:/bin/sh\ncpamp-cpa:x:10001:10001:CPA:/nonexistent:/sbin/nologin\ncpamp-manager:x:10002:10002:Manager:/nonexistent:/sbin/nologin\ncpamp-ingress:x:10003:10003:Ingress:/nonexistent:/sbin/nologin\n"
	testGroupEntry  = "root:x:0:\ncpamp-cpa:x:10001:\ncpamp-manager:x:10002:\ncpamp-ingress:x:10003:\n"
)

type nodeMeta struct {
	uid  int
	gid  int
	perm fs.FileMode
}

type ownershipRecord struct {
	Path string
	UID  int
	GID  int
}

type modeRecord struct {
	Path string
	Perm fs.FileMode
}

type fakeSystem struct {
	root        string
	meta        map[string]nodeMeta
	randomBytes [32]byte
	chownCalls  []ownershipRecord
	lchownCalls []ownershipRecord
	chmodCalls  []modeRecord
	mkdirCalls  []modeRecord
	linkCalls   [][2]string
	mutations   int
	readDirHook func(string)
	opHook      func(op, path string)
}

func (f *fakeSystem) runOpHook(op, cleaned string) {
	if f.opHook != nil {
		f.opHook(op, cleaned)
	}
}

// noteMutation records a volume-mutating operation so tests can assert that a
// refusal happened before any write, chown, chmod, link, or mkdir.
func (f *fakeSystem) noteMutation() {
	f.mutations++
}

func newFakeSystem(t *testing.T) *fakeSystem {
	t.Helper()
	root := t.TempDir()
	var rnd [32]byte
	for i := range len(rnd) {
		rnd[i] = byte(i + 1)
	}
	sys := &fakeSystem{
		root:        root,
		meta:        make(map[string]nodeMeta),
		randomBytes: rnd,
	}
	sys.mustSeedFile(t, DefaultSeedConfigPath, []byte(testSeedYAML), 0, 0, 0o0644)
	sys.mustSeedFile(t, DefaultCPAArtifactManifestPath, []byte(`{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"}`), 0, 0, 0o0644)
	sys.mustSeedFile(t, DefaultPasswdPath, []byte(testPasswdEntry), 0, 0, 0o0644)
	sys.mustSeedFile(t, DefaultGroupPath, []byte(testGroupEntry), 0, 0, 0o0644)
	return sys
}

func (f *fakeSystem) cleanPath(virtualPath string) string {
	cleaned := filepath.Clean(virtualPath)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

func (f *fakeSystem) hostPath(virtualPath string) string {
	cleaned := f.cleanPath(virtualPath)
	return filepath.Join(f.root, strings.TrimPrefix(cleaned, "/"))
}

func (f *fakeSystem) mustSeedDir(t *testing.T, virtualPath string, uid, gid int, perm fs.FileMode) {
	t.Helper()
	cleaned := f.cleanPath(virtualPath)
	if err := os.MkdirAll(f.hostPath(cleaned), 0o700); err != nil {
		t.Fatalf("seed mkdir %s: %v", cleaned, err)
	}
	f.meta[cleaned] = nodeMeta{
		uid:  uid,
		gid:  gid,
		perm: NormalizeUnixPerm(perm),
	}
}

func (f *fakeSystem) mustSeedFile(t *testing.T, virtualPath string, data []byte, uid, gid int, perm fs.FileMode) {
	t.Helper()
	cleaned := f.cleanPath(virtualPath)
	host := f.hostPath(cleaned)
	if err := os.MkdirAll(filepath.Dir(host), 0o700); err != nil {
		t.Fatalf("seed parent dir for %s: %v", cleaned, err)
	}
	if err := os.WriteFile(host, data, 0o600); err != nil {
		t.Fatalf("seed file %s: %v", cleaned, err)
	}
	f.meta[cleaned] = nodeMeta{
		uid:  uid,
		gid:  gid,
		perm: NormalizeUnixPerm(perm),
	}
}

func (f *fakeSystem) mustSeedSymlink(t *testing.T, target, linkPath string, uid, gid int) {
	t.Helper()
	cleaned := f.cleanPath(linkPath)
	host := f.hostPath(cleaned)
	if err := os.MkdirAll(filepath.Dir(host), 0o700); err != nil {
		t.Fatalf("seed symlink parent for %s: %v", cleaned, err)
	}
	_ = os.RemoveAll(host)
	if err := os.Symlink(target, host); err != nil {
		t.Fatalf("seed symlink %s -> %s: %v", cleaned, target, err)
	}
	f.meta[cleaned] = nodeMeta{
		uid:  uid,
		gid:  gid,
		perm: 0o0777,
	}
}

func (f *fakeSystem) Lstat(virtualPath string) (FileStat, error) {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("lstat", cleaned)
	info, err := os.Lstat(f.hostPath(cleaned))
	if err != nil {
		return FileStat{}, err
	}
	m, ok := f.meta[cleaned]
	if !ok {
		m = nodeMeta{uid: 0, gid: 0, perm: NormalizeUnixPerm(info.Mode())}
	}
	mode := (info.Mode() & fs.ModeType) | (m.perm & 0o7777)
	if m.perm&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	if m.perm&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if m.perm&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	return FileStat{
		Mode: mode,
		UID:  m.uid,
		GID:  m.gid,
		Size: info.Size(),
	}, nil
}

func (f *fakeSystem) ReadDir(virtualPath string) ([]fs.DirEntry, error) {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("readdir", cleaned)
	if f.readDirHook != nil {
		f.readDirHook(cleaned)
	}
	return os.ReadDir(f.hostPath(cleaned))
}

func (f *fakeSystem) ReadFile(virtualPath string) ([]byte, error) {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("readfile", cleaned)
	info, err := os.Lstat(f.hostPath(cleaned))
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fs.ErrInvalid
	}
	return os.ReadFile(f.hostPath(cleaned))
}

func (f *fakeSystem) WriteFile(virtualPath string, data []byte, perm fs.FileMode) error {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("writefile", cleaned)
	if err := os.WriteFile(f.hostPath(cleaned), data, 0o600); err != nil {
		return err
	}
	existing, ok := f.meta[cleaned]
	if !ok {
		existing = nodeMeta{uid: 0, gid: 0}
	}
	existing.perm = NormalizeUnixPerm(perm)
	f.meta[cleaned] = existing
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Mkdir(virtualPath string, perm fs.FileMode) error {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("mkdir", cleaned)
	if err := os.Mkdir(f.hostPath(cleaned), 0o700); err != nil {
		return err
	}
	f.noteMutation()
	normPerm := NormalizeUnixPerm(perm)
	f.meta[cleaned] = nodeMeta{
		uid:  0,
		gid:  0,
		perm: normPerm,
	}
	f.mkdirCalls = append(f.mkdirCalls, modeRecord{Path: cleaned, Perm: normPerm})
	return nil
}

func (f *fakeSystem) Chmod(virtualPath string, perm fs.FileMode) error {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("chmod", cleaned)
	if _, err := os.Lstat(f.hostPath(cleaned)); err != nil {
		return err
	}
	normPerm := NormalizeUnixPerm(perm)
	m := f.meta[cleaned]
	m.perm = normPerm
	f.meta[cleaned] = m
	f.chmodCalls = append(f.chmodCalls, modeRecord{Path: cleaned, Perm: normPerm})
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Chown(virtualPath string, uid, gid int) error {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("chown", cleaned)
	if _, err := os.Lstat(f.hostPath(cleaned)); err != nil {
		return err
	}
	m := f.meta[cleaned]
	m.uid = uid
	m.gid = gid
	f.meta[cleaned] = m
	f.chownCalls = append(f.chownCalls, ownershipRecord{Path: cleaned, UID: uid, GID: gid})
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Lchown(virtualPath string, uid, gid int) error {
	cleaned := f.cleanPath(virtualPath)
	f.runOpHook("lchown", cleaned)
	if _, err := os.Lstat(f.hostPath(cleaned)); err != nil {
		return err
	}
	m := f.meta[cleaned]
	m.uid = uid
	m.gid = gid
	f.meta[cleaned] = m
	f.lchownCalls = append(f.lchownCalls, ownershipRecord{Path: cleaned, UID: uid, GID: gid})
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Link(oldname, newname string) error {
	cleanOld := f.cleanPath(oldname)
	cleanNew := f.cleanPath(newname)
	f.runOpHook("link", cleanNew)
	if err := os.Link(f.hostPath(cleanOld), f.hostPath(cleanNew)); err != nil {
		return err
	}
	f.meta[cleanNew] = f.meta[cleanOld]
	f.linkCalls = append(f.linkCalls, [2]string{cleanOld, cleanNew})
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Symlink(oldname, newname string) error {
	cleanNew := f.cleanPath(newname)
	hostNew := f.hostPath(cleanNew)
	if err := os.MkdirAll(filepath.Dir(hostNew), 0o700); err != nil {
		return err
	}
	if err := os.Symlink(oldname, hostNew); err != nil {
		return err
	}
	f.meta[cleanNew] = nodeMeta{uid: 0, gid: 0, perm: 0o0777}
	f.noteMutation()
	return nil
}

func (f *fakeSystem) Readlink(virtualPath string) (string, error) {
	return os.Readlink(f.hostPath(f.cleanPath(virtualPath)))
}
func (f *fakeSystem) Remove(virtualPath string) error {
	cleaned := f.cleanPath(virtualPath)
	err := os.Remove(f.hostPath(cleaned))
	delete(f.meta, cleaned)
	f.noteMutation()
	return err
}

func (f *fakeSystem) ReadRandom(dst []byte) error {
	copy(dst, f.randomBytes[:])
	return nil
}

func (f *fakeSystem) assertNode(t *testing.T, virtualPath string, wantUID, wantGID int, wantPerm fs.FileMode, wantDir bool) {
	t.Helper()
	stat, err := f.Lstat(virtualPath)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", virtualPath, err)
	}
	if stat.Mode.IsDir() != wantDir {
		t.Fatalf("%s IsDir=%v, want %v", virtualPath, stat.Mode.IsDir(), wantDir)
	}
	if stat.UID != wantUID || stat.GID != wantGID {
		t.Fatalf("%s owner=%d:%d, want %d:%d", virtualPath, stat.UID, stat.GID, wantUID, wantGID)
	}
	if stat.UnixPerm() != wantPerm {
		t.Fatalf("%s perm=%#04o, want %#04o", virtualPath, stat.UnixPerm(), wantPerm)
	}
}

func TestPrepareFreshVolumeCreatesExpectedLayout(t *testing.T) {
	sys := newFakeSystem(t)

	got, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatalf("prepare on fresh volume failed: %v", err)
	}

	wantToken := hex.EncodeToString(sys.randomBytes[:])
	if got.RuntimeToken != wantToken {
		t.Fatalf("RuntimeToken=%q, want %q", got.RuntimeToken, wantToken)
	}
	if got.RuntimeRoot != "/data/runtime" {
		t.Fatalf("RuntimeRoot=%q, want /data/runtime", got.RuntimeRoot)
	}
	if got.JournalPath != "/data/runtime/supervisor/operations.sqlite" {
		t.Fatalf("JournalPath=%q, want /data/runtime/supervisor/operations.sqlite", got.JournalPath)
	}

	// /data mode 01771 root:10002
	sys.assertNode(t, "/data", 0, 10002, 0o1771, true)
	// Runtime root 0:0 0711
	sys.assertNode(t, "/data/runtime", 0, 0, 0o0711, true)
	// Bootstrap marker 0:0 0444
	// /runtime symlink -> /data/runtime when /runtime is not a dedicated mount
	if linkTarget, linkErr := sys.Readlink("/runtime"); linkErr != nil || linkTarget != "/data/runtime" {
		t.Fatalf("/runtime symlink target=%q, err=%v, want /data/runtime", linkTarget, linkErr)
	}
	sys.assertNode(t, "/data/runtime/.cpamp-v8-bootstrap", 0, 0, 0o0444, false)
	// Supervisor dirs 0:10001 0710
	sys.assertNode(t, "/data/runtime/supervisor", 0, 10001, 0o0710, true)
	sys.assertNode(t, "/data/runtime/supervisor/artifacts", 0, 10001, 0o0710, true)
	sys.assertNode(t, "/data/runtime/supervisor/artifacts/cpa", 0, 10001, 0o0710, true)
	// Gateway dirs 10001:10001 0700
	sys.assertNode(t, "/data/runtime/gateway", 10001, 10001, 0o0700, true)
	sys.assertNode(t, "/data/runtime/gateway/auth", 10001, 10001, 0o0700, true)
	sys.assertNode(t, "/data/runtime/gateway/logs", 10001, 10001, 0o0700, true)
	sys.assertNode(t, "/data/runtime/gateway/plugins", 10001, 10001, 0o0700, true)
	// Gateway config 10001:10001 0600 matching seed
	sys.assertNode(t, "/data/runtime/gateway/config.yaml", 10001, 10001, 0o0600, false)
	cfgData, err := sys.ReadFile("/data/runtime/gateway/config.yaml")
	if err != nil || !bytes.Equal(cfgData, []byte(testSeedYAML)) {
		t.Fatalf("gateway/config.yaml content=%q, err=%v", string(cfgData), err)
	}
	// Token dir root:10002 0750, token file root:10002 0640
	sys.assertNode(t, "/data/runtime-secret", 0, 10002, 0o0750, true)
	sys.assertNode(t, "/data/runtime-secret/token", 0, 10002, 0o0640, false)
	tokenData, err := sys.ReadFile("/data/runtime-secret/token")
	if err != nil || string(tokenData) != wantToken {
		t.Fatalf("token file content=%q, want %q (err=%v)", string(tokenData), wantToken, err)
	}

	markerData, err := sys.ReadFile("/data/runtime/.cpamp-v8-bootstrap")
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimRight(string(markerData), "\n") != got.BootstrapIdentity {
		t.Fatalf("marker=%q, want %q", string(markerData), got.BootstrapIdentity)
	}
}

func TestPrepareRestartIsIdempotentAndPreservesTokenAndManagerFiles(t *testing.T) {
	sys := newFakeSystem(t)

	// Existing Manager-owned files under /data must not be recursed into or re-chowned.
	sys.mustSeedDir(t, "/data", 0, 0, 0o0755)
	sys.mustSeedFile(t, "/data/manager-state.db", []byte("sqlite"), 10002, 10002, 0o0600)
	sys.mustSeedFile(t, "/data/manager-secret", []byte("key"), 10002, 10002, 0o0600)
	first, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatalf("first prepare failed: %v", err)
	}

	// Change random bytes before second run to prove existing token file is preserved.
	for i := range len(sys.randomBytes) {
		sys.randomBytes[i] = 0xff
	}

	second, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatalf("second prepare on existing layout failed: %v", err)
	}
	if second.RuntimeToken != first.RuntimeToken {
		t.Fatalf("token changed across restart: got %q, want %q", second.RuntimeToken, first.RuntimeToken)
	}

	// Verify /data was fixed to 0:10002 01771 while Manager files kept 10002:10002 0600.
	sys.assertNode(t, "/data", 0, 10002, 0o1771, true)
	sys.assertNode(t, "/data/manager-state.db", 10002, 10002, 0o0600, false)
	sys.assertNode(t, "/data/manager-secret", 10002, 10002, 0o0600, false)
	sys.assertNode(t, "/data/runtime-secret/token", 0, 10002, 0o0640, false)
}

func TestPrepareExplicitTokenMismatchRefused(t *testing.T) {
	sys := newFakeSystem(t)

	firstToken := strings.Repeat("a", 64)
	if _, err := prepare(PrepareConfig{ExplicitRuntimeToken: firstToken}, sys); err != nil {
		t.Fatalf("initial prepare with explicit token failed: %v", err)
	}

	differentToken := strings.Repeat("b", 64)
	_, err := prepare(PrepareConfig{ExplicitRuntimeToken: differentToken}, sys)
	if err == nil || !strings.Contains(err.Error(), "explicit Runtime transport secret does not match existing secret source") {
		t.Fatalf("expected token mismatch refusal, got: %v", err)
	}
}

func TestPrepareShortOrWhitespaceTokenRefused(t *testing.T) {
	t.Run("short explicit token", func(t *testing.T) {
		sys := newFakeSystem(t)
		_, err := prepare(PrepareConfig{ExplicitRuntimeToken: "short-token-under-32-bytes"}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime transport secret must contain at least 32 bytes") {
			t.Fatalf("expected short token error, got: %v", err)
		}
	})

	t.Run("whitespace in explicit token", func(t *testing.T) {
		sys := newFakeSystem(t)
		_, err := prepare(PrepareConfig{ExplicitRuntimeToken: "0123456789abcdef0123456789abcdef with space"}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime transport secret must not contain whitespace") {
			t.Fatalf("expected whitespace token error, got: %v", err)
		}
	})

	t.Run("newline in persisted token file", func(t *testing.T) {
		sys := newFakeSystem(t)
		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("initial prepare failed: %v", err)
		}
		sys.mustSeedFile(t, "/data/runtime-secret/token", []byte(strings.Repeat("c", 32)+"\n"), 0, 10002, 0o0640)
		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime transport secret must not contain whitespace") {
			t.Fatalf("expected persisted whitespace token error, got: %v", err)
		}
	})
}

func TestPrepareMarkerMismatchRefused(t *testing.T) {
	sys := newFakeSystem(t)
	if _, err := prepare(PrepareConfig{}, sys); err != nil {
		t.Fatalf("initial prepare failed: %v", err)
	}

	sys.mustSeedFile(t, "/data/runtime/.cpamp-v8-bootstrap", []byte("official-cpa-v8-bootstrap:deadbeef\n"), 0, 0, 0o0444)
	_, err := prepare(PrepareConfig{}, sys)
	if err == nil || !strings.Contains(err.Error(), "Default CPA v8 refuses existing or unknown Runtime data: different bootstrap identity") {
		t.Fatalf("expected different bootstrap identity refusal, got: %v", err)
	}
}

func TestPrepareNonemptyRuntimeRootWithoutMarkerRefused(t *testing.T) {
	sys := newFakeSystem(t)
	sys.mustSeedDir(t, "/data/runtime", 0, 0, 0o0711)
	sys.mustSeedDir(t, "/data/runtime/gateway", 10001, 10001, 0o0700)

	_, err := prepare(PrepareConfig{}, sys)
	if err == nil || !strings.Contains(err.Error(), "Default CPA v8 refuses existing or unknown Runtime data: nonempty volume without v8 bootstrap identity") {
		t.Fatalf("expected nonempty volume refusal, got: %v", err)
	}
}

func TestPrepareConfigModifiedVsSeedRefused(t *testing.T) {
	sys := newFakeSystem(t)
	if _, err := prepare(PrepareConfig{}, sys); err != nil {
		t.Fatalf("initial prepare failed: %v", err)
	}

	sys.mustSeedFile(t, "/data/runtime/gateway/config.yaml", []byte("port: 9999\n"), 10001, 10001, 0o0600)
	_, err := prepare(PrepareConfig{}, sys)
	if err == nil || !strings.Contains(err.Error(), "Default CPA v8 refuses existing or unknown Runtime data: modified or incompatible bootstrap config") {
		t.Fatalf("expected modified bootstrap config refusal, got: %v", err)
	}
}

func TestPrepareSupervisorDirNotRootOwnedRefused(t *testing.T) {
	sys := newFakeSystem(t)
	if _, err := prepare(PrepareConfig{}, sys); err != nil {
		t.Fatalf("initial prepare failed: %v", err)
	}

	if err := sys.Chown("/data/runtime/supervisor", 10001, 10001); err != nil {
		t.Fatalf("chown supervisor dir: %v", err)
	}

	_, err := prepare(PrepareConfig{}, sys)
	if err == nil || !strings.Contains(err.Error(), "Supervisor persisted directory owner is not root: /data/runtime/supervisor (uid 10001)") {
		t.Fatalf("expected non-root supervisor dir refusal, got: %v", err)
	}
}

func TestPrepareSymlinkedGatewayDirRefused(t *testing.T) {
	t.Run("gateway root is symlink when marker present", func(t *testing.T) {
		sys := newFakeSystem(t)
		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("initial prepare failed: %v", err)
		}
		sys.mustSeedDir(t, "/data/elsewhere", 10001, 10001, 0o0700)
		sys.mustSeedSymlink(t, "/data/elsewhere", "/data/runtime/gateway", 10001, 10001)

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "Default CPA v8 refuses existing or unknown Runtime data: invalid Gateway directory") {
			t.Fatalf("expected invalid Gateway directory refusal, got: %v", err)
		}
	})

	t.Run("gateway subdir is symlink", func(t *testing.T) {
		sys := newFakeSystem(t)
		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("initial prepare failed: %v", err)
		}
		sys.mustSeedDir(t, "/data/elsewhere", 10001, 10001, 0o0700)
		sys.mustSeedSymlink(t, "/data/elsewhere", "/data/runtime/gateway/auth", 10001, 10001)

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "Gateway state path is not a real directory: /data/runtime/gateway/auth") {
			t.Fatalf("expected symlinked gateway subdir refusal, got: %v", err)
		}
	})
}

func TestSelectRuntimeRootAndMountPrecedence(t *testing.T) {
	mountinfoWithRuntime := []byte(strings.Join([]string{
		"36 35 0:32 / / rw,relatime - overlay overlay rw",
		"37 36 259:1 /volumes/data /data rw,relatime - ext4 /dev/nvme0n1p1 rw",
		"38 36 259:2 /volumes/runtime /runtime rw,relatime - ext4 /dev/nvme0n1p2 rw",
	}, "\n") + "\n")

	mountinfoWithoutRuntime := []byte(strings.Join([]string{
		"36 35 0:32 / / rw,relatime - overlay overlay rw",
		"37 36 259:1 /volumes/data /data rw,relatime - ext4 /dev/nvme0n1p1 rw",
	}, "\n") + "\n")

	if got := SelectRuntimeRoot(mountinfoWithRuntime); got != "/runtime" {
		t.Fatalf("SelectRuntimeRoot(with /runtime) = %q, want /runtime", got)
	}
	if got := SelectRuntimeRoot(mountinfoWithoutRuntime); got != "/data/runtime" {
		t.Fatalf("SelectRuntimeRoot(without /runtime) = %q, want /data/runtime", got)
	}

	t.Run("prepare uses /runtime when mounted", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mustSeedFile(t, DefaultMountInfoPath, mountinfoWithRuntime, 0, 0, 0o0444)
		sys.mustSeedDir(t, "/runtime", 0, 0, 0o0755)

		prepared, err := prepare(PrepareConfig{}, sys)
		if err != nil {
			t.Fatalf("prepare with /runtime mount failed: %v", err)
		}
		if prepared.RuntimeRoot != "/runtime" {
			t.Fatalf("RuntimeRoot=%q, want /runtime", prepared.RuntimeRoot)
		}
		sys.assertNode(t, "/runtime", 0, 0, 0o0711, true)
		sys.assertNode(t, "/runtime/.cpamp-v8-bootstrap", 0, 0, 0o0444, false)
		sys.assertNode(t, "/data", 0, 10002, 0o1771, true)
		sys.assertNode(t, "/data/runtime-secret", 0, 10002, 0o0750, true)
		sys.assertNode(t, "/data/runtime-secret/token", 0, 10002, 0o0640, false)
	})

	t.Run("prepare uses /data/runtime when /runtime is not mounted", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mustSeedFile(t, DefaultMountInfoPath, mountinfoWithoutRuntime, 0, 0, 0o0444)

		prepared, err := prepare(PrepareConfig{}, sys)
		if err != nil {
			t.Fatalf("prepare without /runtime mount failed: %v", err)
		}
		if prepared.RuntimeRoot != "/data/runtime" {
			t.Fatalf("RuntimeRoot=%q, want /data/runtime", prepared.RuntimeRoot)
		}
		sys.assertNode(t, "/data/runtime", 0, 0, 0o0711, true)
	})
}

func TestPrepareCPAIdentityAndSupervisorEnv(t *testing.T) {
	t.Run("non-decimal CPA UID refused", func(t *testing.T) {
		sys := newFakeSystem(t)
		_, err := prepare(PrepareConfig{CPAUIDEnv: "abc"}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime CPA UID/GID must be positive decimal integers") {
			t.Fatalf("expected positive decimal integer error, got: %v", err)
		}
	})

	t.Run("zero CPA UID refused", func(t *testing.T) {
		sys := newFakeSystem(t)
		_, err := prepare(PrepareConfig{CPAUIDEnv: "0"}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime CPA UID/GID must be positive decimal integers") {
			t.Fatalf("expected positive decimal integer error, got: %v", err)
		}
	})

	t.Run("passwd mismatch refused", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mustSeedFile(t, DefaultPasswdPath, []byte("cpamp-cpa:x:20001:10001:CPA:/nonexistent:/sbin/nologin\n"), 0, 0, 0o0644)
		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity") {
			t.Fatalf("expected passwd identity mismatch error, got: %v", err)
		}
	})

	t.Run("SupervisorEnv contains required keys", func(t *testing.T) {
		sys := newFakeSystem(t)
		prepared, err := prepare(PrepareConfig{}, sys)
		if err != nil {
			t.Fatalf("prepare failed: %v", err)
		}
		env := strings.Join(prepared.SupervisorEnv(), "\n")
		for _, want := range []string{
			"CPAMP_RUNTIME_ADDR=127.0.0.1:9081",
			"CPAMP_RUNTIME_IDENTITY=embedded-default",
			"CPAMP_RUNTIME_TOKEN=" + prepared.RuntimeToken,
			"CPAMP_RUNTIME_JOURNAL_PATH=/data/runtime/supervisor/operations.sqlite",
			"CPAMP_CPA_EXECUTABLE=/usr/local/bin/cli-proxy-api",
			"CPAMP_CPA_ARTIFACT_MANIFEST=/usr/local/share/cpamp/runtime/cpa-artifact.json",
			"CPAMP_RUNTIME_CPA_ADDR=127.0.0.1:8317",
			"CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1",
			"CPAMP_RUNTIME_CPA_UID=10001",
			"CPAMP_RUNTIME_CPA_GID=10001",
		} {
			if !strings.Contains(env, want) {
				t.Fatalf("SupervisorEnv missing %q in:\n%s", want, env)
			}
		}
	})
}

func TestPrepareArtifactManifestV8BootstrapSelection(t *testing.T) {
	t.Run("v8 manifest enforces bootstrap guard", func(t *testing.T) {
		sys := newFakeSystem(t)
		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("initial v8 prepare failed: %v", err)
		}
		sys.assertNode(t, "/data/runtime/.cpamp-v8-bootstrap", 0, 0, 0o0444, false)
		sys.mustSeedFile(t, "/data/runtime/gateway/config.yaml", []byte(testSeedYAML+"# modified\n"), 10001, 10001, 0o0600)
		if _, err := prepare(PrepareConfig{}, sys); err == nil || !strings.Contains(err.Error(), "modified or incompatible bootstrap config") {
			t.Fatalf("expected v8 manifest to refuse modified config, got: %v", err)
		}
	})
	t.Run("v7 manifest (6.x) skips bootstrap guard and survives modified config across re-prepare", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mustSeedFile(t, DefaultCPAArtifactManifestPath, []byte(`{"schemaVersion":1,"engine":"cpa","version":"6.9.15","artifactId":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`), 0, 0, 0o0644)

		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("initial v7 manifest prepare failed: %v", err)
		}
		if _, err := sys.Lstat("/data/runtime/.cpamp-v8-bootstrap"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("expected no .cpamp-v8-bootstrap marker in v7 mode, got err=%v", err)
		}

		modifiedConfig := []byte(testSeedYAML + "# operator comment\n")
		sys.mustSeedFile(t, "/data/runtime/gateway/config.yaml", modifiedConfig, 10001, 10001, 0o0600)

		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("v7 manifest restart after config edit should succeed, got: %v", err)
		}
		gotCfg, err := sys.ReadFile("/data/runtime/gateway/config.yaml")
		if err != nil || !bytes.Equal(gotCfg, modifiedConfig) {
			t.Fatalf("v7 config was overwritten or unreadable: %q (err=%v)", string(gotCfg), err)
		}
	})

	t.Run("malformed manifest refuses fail-closed", func(t *testing.T) {
		validID := "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		for _, tc := range []struct {
			name    string
			payload string
		}{
			{
				name:    "invalid JSON syntax",
				payload: `{not-valid-json`,
			},
			{
				name:    "empty version",
				payload: `{"schemaVersion":1,"engine":"cpa","version":"","artifactId":"` + validID + `"}`,
			},
			{
				name:    "invalid semver 7.invalid",
				payload: `{"schemaVersion":1,"engine":"cpa","version":"7.invalid","artifactId":"` + validID + `"}`,
			},
			{
				name:    "trailing garbage after valid object",
				payload: `{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"` + validID + `"} trailing`,
			},
			{
				name:    "invalid artifactId",
				payload: `{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"sha256:short"}`,
			},
		} {
			sys := newFakeSystem(t)
			sys.mustSeedFile(t, DefaultCPAArtifactManifestPath, []byte(tc.payload), 0, 0, 0o0644)
			if _, err := prepare(PrepareConfig{}, sys); err == nil {
				t.Fatalf("expected malformed manifest (%s) to fail closed, but prepare succeeded", tc.name)
			}
		}
	})
	t.Run("missing manifest refuses fail-closed", func(t *testing.T) {
		sys := newFakeSystem(t)
		if err := sys.Remove(DefaultCPAArtifactManifestPath); err != nil {
			t.Fatalf("remove manifest: %v", err)
		}
		if _, err := prepare(PrepareConfig{}, sys); err == nil || !strings.Contains(err.Error(), "failed to read CPA artifact manifest") {
			t.Fatalf("expected missing manifest error, got: %v", err)
		}
	})
}

func TestPrepareIgnoresManifestEnvOverrideForBootstrapDecision(t *testing.T) {
	sys := newFakeSystem(t)
	// The env override points at a valid 7.x manifest that would otherwise skip
	// the v8 guard.
	sys.mustSeedFile(t, "/data/forged/cpa-artifact.json", []byte(`{"schemaVersion":1,"engine":"cpa","version":"6.9.15","artifactId":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`), 0, 0, 0o0644)
	// Unknown non-empty runtime root without a bootstrap marker.
	sys.mustSeedDir(t, "/data/runtime", 0, 0, 0o0711)
	sys.mustSeedDir(t, "/data/runtime/unknown", 0, 0, 0o0700)

	before := sys.mutations
	_, err := prepare(PrepareConfig{CPAArtifactManifestPath: "/data/forged/cpa-artifact.json"}, sys)
	if err == nil || !strings.Contains(err.Error(), "nonempty volume without v8 bootstrap identity") {
		t.Fatalf("expected v8 refusal despite forged 7.x manifest env, got: %v", err)
	}
	if sys.mutations != before {
		t.Fatalf("refusal mutated the volume %d times; want 0", sys.mutations-before)
	}
}

func TestPrepareRefusesNonLoopbackServiceAddresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  PrepareConfig
		want string
	}{
		{name: "supervisor wildcard addr", cfg: PrepareConfig{RuntimeAddr: "0.0.0.0:9081"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "supervisor hostname addr", cfg: PrepareConfig{RuntimeAddr: "example.com:9081"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "supervisor non-canonical port", cfg: PrepareConfig{RuntimeAddr: "127.0.0.1:9000"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "supervisor ipv6 loopback", cfg: PrepareConfig{RuntimeAddr: "[::1]:9081"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "supervisor alternate ipv4 loopback", cfg: PrepareConfig{RuntimeAddr: "127.0.0.2:9081"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "supervisor localhost name", cfg: PrepareConfig{RuntimeAddr: "localhost:9081"}, want: "CPAMP_RUNTIME_ADDR must be the canonical loopback address 127.0.0.1:9081"},
		{name: "cpa wildcard addr", cfg: PrepareConfig{RuntimeCPAAddr: "0.0.0.0:8317"}, want: "CPAMP_RUNTIME_CPA_ADDR must be the canonical loopback address 127.0.0.1:8317"},
		{name: "cpa non-canonical port", cfg: PrepareConfig{RuntimeCPAAddr: "127.0.0.1:9000"}, want: "CPAMP_RUNTIME_CPA_ADDR must be the canonical loopback address 127.0.0.1:8317"},
		{name: "cpa ipv6 loopback", cfg: PrepareConfig{RuntimeCPAAddr: "[::1]:8317"}, want: "CPAMP_RUNTIME_CPA_ADDR must be the canonical loopback address 127.0.0.1:8317"},
		{name: "cpa alternate ipv4 loopback", cfg: PrepareConfig{RuntimeCPAAddr: "127.0.0.2:8317"}, want: "CPAMP_RUNTIME_CPA_ADDR must be the canonical loopback address 127.0.0.1:8317"},
		{name: "cpa localhost name", cfg: PrepareConfig{RuntimeCPAAddr: "localhost:8317"}, want: "CPAMP_RUNTIME_CPA_ADDR must be the canonical loopback address 127.0.0.1:8317"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := newFakeSystem(t)
			before := sys.mutations
			_, err := prepare(tc.cfg, sys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got: %v", tc.want, err)
			}
			if sys.mutations != before {
				t.Fatalf("refusal mutated the volume %d times; want 0", sys.mutations-before)
			}
		})
	}

	t.Run("exact canonical values accepted", func(t *testing.T) {
		sys := newFakeSystem(t)
		got, err := prepare(PrepareConfig{
			RuntimeAddr:    DefaultRuntimeAddr,
			RuntimeCPAAddr: DefaultRuntimeCPAAddr,
		}, sys)
		if err != nil {
			t.Fatalf("expected canonical addresses to succeed, got: %v", err)
		}
		if got.RuntimeAddr != DefaultRuntimeAddr || got.RuntimeCPAAddr != DefaultRuntimeCPAAddr {
			t.Fatalf("got RuntimeAddr=%q RuntimeCPAAddr=%q", got.RuntimeAddr, got.RuntimeCPAAddr)
		}
	})
}

func TestPrepareStopsWhenCanceledDuringGatewayWalk(t *testing.T) {
	cases := []struct {
		name          string
		preCancel     bool
		triggerOp     string
		triggerPath   string
		wantZeroDirty bool
	}{
		{
			name:          "pre-canceled before prepare starts",
			preCancel:     true,
			wantZeroDirty: true,
		},
		{
			name:          "canceled after mountinfo read before seed/manifest check",
			triggerOp:     "readfile",
			triggerPath:   DefaultMountInfoPath,
			wantZeroDirty: true,
		},
		{
			name:          "canceled after checkV8Bootstrap before first volume mutation",
			triggerOp:     "lstat",
			triggerPath:   "/data/runtime/.cpamp-v8-bootstrap",
			wantZeroDirty: true,
		},
		{
			name:        "canceled mid-critical-section finishes marker+config before stopping at gateway walk",
			triggerOp:   "mkdir",
			triggerPath: "/data/runtime",
		},
		{
			name:        "canceled after gateway config link finishes marker before stopping at gateway walk",
			triggerOp:   "link",
			triggerPath: "/data/runtime/gateway/config.yaml",
		},
		{
			name:        "canceled at gateway root lchown before auth entry",
			triggerOp:   "lchown",
			triggerPath: "/data/runtime/gateway",
		},
		{
			name:        "canceled at gateway/auth lchown before config.yaml entry",
			triggerOp:   "lchown",
			triggerPath: "/data/runtime/gateway/auth",
		},
		{
			name:        "canceled at gateway/config.yaml lchown before logs entry",
			triggerOp:   "lchown",
			triggerPath: "/data/runtime/gateway/config.yaml",
		},
		{
			name:        "canceled at gateway/logs lchown before plugins entry",
			triggerOp:   "lchown",
			triggerPath: "/data/runtime/gateway/logs",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys := newFakeSystem(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.preCancel {
				cancel()
			} else {
				sys.opHook = func(op, path string) {
					if op == tc.triggerOp && path == tc.triggerPath {
						cancel()
					}
				}
			}

			before := sys.mutations
			if _, err := prepareContext(ctx, PrepareConfig{}, sys); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got: %v", err)
			}
			if tc.wantZeroDirty && sys.mutations != before {
				t.Fatalf("pre-mutation cancellation mutated volume %d times; want 0", sys.mutations-before)
			}

			sys.opHook = nil
			got, err := prepare(PrepareConfig{}, sys)
			if err != nil {
				t.Fatalf("second prepare after cancellation failed: %v", err)
			}
			sys.assertNode(t, "/data", 0, 10002, 0o1771, true)
			sys.assertNode(t, "/data/runtime", 0, 0, 0o0711, true)
			sys.assertNode(t, "/data/runtime/.cpamp-v8-bootstrap", 0, 0, 0o0444, false)
			sys.assertNode(t, "/data/runtime/supervisor", 0, 10001, 0o0710, true)
			sys.assertNode(t, "/data/runtime/gateway", 10001, 10001, 0o0700, true)
			sys.assertNode(t, "/data/runtime/gateway/auth", 10001, 10001, 0o0700, true)
			sys.assertNode(t, "/data/runtime/gateway/logs", 10001, 10001, 0o0700, true)
			sys.assertNode(t, "/data/runtime/gateway/plugins", 10001, 10001, 0o0700, true)
			sys.assertNode(t, "/data/runtime/gateway/config.yaml", 10001, 10001, 0o0600, false)
			sys.assertNode(t, "/data/runtime-secret/token", 0, 10002, 0o0640, false)
			if got.RuntimeToken == "" {
				t.Fatalf("expected non-empty RuntimeToken after second prepare")
			}
		})
	}
}

func TestShutdownBudgetFitsContainerGrace(t *testing.T) {
	fixed := ShutdownFixedBudget()
	wantFixed := IngressDrainGraceExtra +
		ManagerShutdownTimeout +
		SupervisorShutdownTimeout +
		ProcessGroupKillGrace +
		4*SIGKILLReapWait +
		3*ChildIODrainTimeout
	if fixed != wantFixed {
		t.Fatalf("fixed shutdown budget=%s, want %s", fixed, wantFixed)
	}
	if fixed != 45*time.Second {
		t.Fatalf("fixed shutdown budget=%s, want 45s", fixed)
	}
	if fixed > ShutdownFixedBudgetLimit {
		t.Fatalf("fixed shutdown budget %s exceeds limit %s", fixed, ShutdownFixedBudgetLimit)
	}
	drain := DefaultDrainTimeout
	if drain+fixed > drain+ShutdownFixedBudgetLimit {
		t.Fatalf("drain+fixed=%s exceeds drain+limit=%s", drain+fixed, drain+ShutdownFixedBudgetLimit)
	}
	if drain+ShutdownFixedBudgetLimit > 150*time.Second {
		t.Fatalf("default container grace must cover drain+limit (%s)", drain+ShutdownFixedBudgetLimit)
	}
}
