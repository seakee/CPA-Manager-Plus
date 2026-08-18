//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPrivateWindowsPathsProtectDACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := ensurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "status.json")
	if err := writePrivateAtomic(filePath, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{root, filePath} {
		assertPrivateWindowsACL(t, path)
	}
}

func TestEnsurePrivateDirectoryRejectsWindowsReparsePoint(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("creating a Windows directory symlink requires Developer Mode or elevation")
		}
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("ensurePrivateDirectory accepted a reparse point")
	}
}

func TestRollbackSnapshotUsesPrivateWindowsACLs(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "usage.sqlite"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction.DataPaths = []string{dataDir}
	backupPath := filepath.Join(root, "backups", "snapshot")
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		backupPath,
		filepath.Join(backupPath, "backup-manifest.json"),
		filepath.Join(backupPath, "data", "data", "usage.sqlite"),
	} {
		assertPrivateWindowsACL(t, path)
	}
}

func TestRestoreRollbackSnapshotDoesNotApplyPrivateBackupACLToLiveData(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(dataDir, "usage.sqlite")
	if err := os.WriteFile(dataPath, []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction.DataPaths = []string{dataDir}
	backupPath := filepath.Join(root, "backups", "snapshot")
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("new-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(dataPath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		t.Fatal("restored live data inherited the private backup DACL")
	}
}

func assertPrivateWindowsACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("read ACL for %s: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read security control for %s: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL is not protected for %s", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("read DACL for %s: err=%v", path, err)
	}
	unsafeSIDs := make([]*windows.SID, 0, 3)
	for _, sidString := range []string{"S-1-1-0", "S-1-5-11", "S-1-5-32-545"} {
		sid, err := windows.StringToSid(sidString)
		if err != nil {
			t.Fatal(err)
		}
		unsafeSIDs = append(unsafeSIDs, sid)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatalf("read ACE %d for %s: %v", index, path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, unsafeSID := range unsafeSIDs {
			if sid.Equals(unsafeSID) {
				t.Fatalf("unsafe write principal remains in private ACL for %s: %s", path, unsafeSID.String())
			}
		}
	}
}
