package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	updatecore "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/update"
)

func TestCapabilityRequiresDataAndSecretsOutsideBackupRoot(t *testing.T) {
	setRuntimeVersion(t, "v1.0.0")
	root := t.TempDir()
	manifestPath := writeManifest(t, root, filepath.Join(root, "backups"))
	dataDir := filepath.Join(root, "data")
	service := New(manifestPath, dataDir, filepath.Join(dataDir, "usage.sqlite"), filepath.Join(dataDir, "data.key"), updatecore.ReleaseClient{})
	if capability := service.Capability(); !capability.Supported {
		t.Fatalf("Capability() = %#v", capability)
	}

	service = New(manifestPath, filepath.Join(root, "backups", "data"), filepath.Join(root, "backups", "data", "usage.sqlite"), filepath.Join(root, "backups", "data", "data.key"), updatecore.ReleaseClient{})
	if capability := service.Capability(); capability.Supported || capability.Reason != "unsupported_backup_layout" {
		t.Fatalf("Capability() = %#v", capability)
	}
}

func TestCapabilityRejectsDatabaseOutsideDataDirectory(t *testing.T) {
	setRuntimeVersion(t, "v1.0.0")
	root := t.TempDir()
	manifestPath := writeManifest(t, root, filepath.Join(root, "backups"))
	dataDir := filepath.Join(root, "data")
	service := New(manifestPath, dataDir, filepath.Join(root, "usage.sqlite"), filepath.Join(dataDir, "data.key"), updatecore.ReleaseClient{})
	if capability := service.Capability(); capability.Supported || capability.Reason != "unsupported_data_layout" {
		t.Fatalf("Capability() = %#v", capability)
	}
}

func TestCapabilityRejectsDevelopmentRuntimeVersion(t *testing.T) {
	setRuntimeVersion(t, "dev")
	root := t.TempDir()
	manifestPath := writeManifest(t, root, filepath.Join(root, "backups"))
	dataDir := filepath.Join(root, "data")
	service := New(manifestPath, dataDir, filepath.Join(dataDir, "usage.sqlite"), filepath.Join(dataDir, "data.key"), updatecore.ReleaseClient{})
	if capability := service.Capability(); capability.Supported || capability.Reason != "runtime_version_unavailable" {
		t.Fatalf("Capability() = %#v", capability)
	}
}

func TestApplyRejectsStagedUpdateFromAnotherRuntimeVersion(t *testing.T) {
	root := t.TempDir()
	manifestPath := writeManifest(t, root, filepath.Join(root, "backups"))
	dataDir := filepath.Join(root, "data")
	service := New(manifestPath, dataDir, filepath.Join(dataDir, "usage.sqlite"), filepath.Join(dataDir, "data.key"), updatecore.ReleaseClient{})
	transactionID := strings.Repeat("a", 32)
	transactionRoot := filepath.Join(root, ".update", "transactions", transactionID)
	transactionPath := filepath.Join(transactionRoot, "transaction.json")
	statusPath := filepath.Join(root, ".update", "status.json")
	transaction := updatecore.Transaction{
		SchemaVersion:   1,
		TransactionID:   transactionID,
		InstallManifest: manifestPath,
		StatusPath:      statusPath,
		PackageRoot:     filepath.Join(transactionRoot, "staging", "package"),
		CurrentVersion:  "v1.0.0",
		TargetVersion:   "v1.2.0",
		ParentPID:       os.Getpid(),
		HealthURL:       "http://127.0.0.1:28317/health",
		InfoURL:         "http://127.0.0.1:28317/usage-service/info",
	}
	if err := os.MkdirAll(transaction.PackageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := updatecore.WriteTransaction(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}
	if err := updatecore.WriteTransactionStatus(statusPath, updatecore.TransactionStatus{
		TransactionID:  transactionID,
		InstallID:      "test",
		CurrentVersion: "v1.0.0",
		TargetVersion:  "v1.2.0",
		State:          updatecore.StateStaged,
	}); err != nil {
		t.Fatal(err)
	}
	setRuntimeVersion(t, "v1.1.0")

	if _, err := service.Apply(func() error { t.Fatal("shutdown must not be requested"); return nil }); err == nil ||
		!strings.Contains(err.Error(), "no longer matches the running version") {
		t.Fatalf("Apply() error = %v", err)
	}
}

func setRuntimeVersion(t *testing.T, version string) {
	t.Helper()
	previous := buildinfo.Version
	buildinfo.Version = version
	t.Cleanup(func() { buildinfo.Version = previous })
}

func writeManifest(t *testing.T, root, backupRoot string) string {
	t.Helper()
	managed := updatecore.RuntimeManagedFiles()
	manifest := updatecore.InstallManifest{
		SchemaVersion: 2,
		InstallID:     "test",
		Mode:          "native-control-script",
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		InstallRoot:   root,
		BinaryPath:    filepath.Join(root, managed.Binary),
		ControlScript: filepath.Join(root, managed.Control),
		UpdaterPath:   filepath.Join(root, managed.Updater),
		BackupRoot:    backupRoot,
		LaunchMode:    "control-script-default",
		Enabled:       true,
	}
	path := filepath.Join(root, ".update", "install.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
