package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnableManagedUpdatesWritesLoadableManifest(t *testing.T) {
	root := t.TempDir()
	managed := RuntimeManagedFiles()
	binary := filepath.Join(root, managed.Binary)
	control := filepath.Join(root, managed.Control)
	updater := filepath.Join(root, managed.Updater)
	for _, path := range []string{binary, control, updater} {
		if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath := filepath.Join(root, ".update", "install.json")
	manifest, err := EnableManagedUpdates(EnableOptions{
		ManifestPath:  manifestPath,
		InstallRoot:   root,
		BinaryPath:    binary,
		ControlScript: control,
		UpdaterPath:   updater,
		BackupRoot:    filepath.Join(root, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Enabled || manifest.InstallID == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	loaded, err := LoadInstallManifest(manifestPath)
	if err != nil || loaded.InstallID != manifest.InstallID {
		t.Fatalf("LoadInstallManifest() = %#v, %v", loaded, err)
	}
}
