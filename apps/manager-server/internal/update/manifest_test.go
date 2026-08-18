package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetectCapabilityFailsClosedWithoutManifest(t *testing.T) {
	got := DetectCapability("")
	if got.Supported || got.Reason != "managed_updates_not_enabled" {
		t.Fatalf("DetectCapability() = %#v", got)
	}
}

func TestDetectCapabilityTreatsMissingConfiguredManifestAsNotEnabled(t *testing.T) {
	got := DetectCapability(filepath.Join(t.TempDir(), ".update", "install.json"))
	if got.Supported || got.Reason != "managed_updates_not_enabled" {
		t.Fatalf("DetectCapability() = %#v", got)
	}
}

func TestLoadInstallManifestAcceptsManagedNativeLayout(t *testing.T) {
	root := t.TempDir()
	managed := RuntimeManagedFiles()
	manifestPath := filepath.Join(root, ".update", "install.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := InstallManifest{
		SchemaVersion: installManifestSchemaVersion,
		InstallID:     "test-install",
		Mode:          "native-control-script",
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		InstallRoot:   root,
		BinaryPath:    filepath.Join(root, managed.Binary),
		ControlScript: filepath.Join(root, managed.Control),
		UpdaterPath:   filepath.Join(root, managed.Updater),
		BackupRoot:    filepath.Join(root, "backups"),
		LaunchMode:    "control-script-default",
		Enabled:       true,
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got := DetectCapability(manifestPath)
	if !got.Supported || !got.BackupSupported || !got.RollbackSupport {
		t.Fatalf("DetectCapability() = %#v", got)
	}
}

func TestLoadInstallManifestRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	managed := RuntimeManagedFiles()
	manifest := InstallManifest{
		SchemaVersion: installManifestSchemaVersion,
		InstallID:     "test-install",
		Mode:          "native-control-script",
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		InstallRoot:   root,
		BinaryPath:    filepath.Join(root, managed.Binary),
		ControlScript: filepath.Join(root, "..", "outside"),
		UpdaterPath:   filepath.Join(root, managed.Updater),
		BackupRoot:    filepath.Join(root, "backups"),
		LaunchMode:    "control-script-default",
	}
	if err := validateInstallManifest(manifest); err == nil {
		t.Fatal("validateInstallManifest() accepted an escaping control script")
	}
}

func TestLoadInstallManifestRejectsTrailingJSON(t *testing.T) {
	root := t.TempDir()
	managed := RuntimeManagedFiles()
	manifestPath := filepath.Join(root, ".update", "install.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := InstallManifest{
		SchemaVersion: installManifestSchemaVersion,
		InstallID:     "test-install",
		Mode:          "native-control-script",
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		InstallRoot:   root,
		BinaryPath:    filepath.Join(root, managed.Binary),
		ControlScript: filepath.Join(root, managed.Control),
		UpdaterPath:   filepath.Join(root, managed.Updater),
		BackupRoot:    filepath.Join(root, "backups"),
		LaunchMode:    "control-script-default",
		Enabled:       true,
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, append(data, []byte(` {"extra":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstallManifest(manifestPath); err == nil {
		t.Fatal("LoadInstallManifest accepted trailing JSON")
	}
}
