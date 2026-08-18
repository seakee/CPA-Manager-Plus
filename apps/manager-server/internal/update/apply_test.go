package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRollbackSnapshotRecordsAndVerifiesEveryFile(t *testing.T) {
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
	backupManifest, err := readAndVerifyBackupManifest(manifest, transaction, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(backupManifest.Files) < 4 {
		t.Fatalf("backup manifest files = %#v", backupManifest.Files)
	}
	for _, file := range backupManifest.Files {
		if file.Size <= 0 || len(file.SHA256) != 64 {
			t.Fatalf("invalid backup file metadata: %#v", file)
		}
	}
	if err := os.WriteFile(filepath.Join(backupPath, "data", "data", "usage.sqlite"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAndVerifyBackupManifest(manifest, transaction, backupPath); err == nil {
		t.Fatal("backup verification accepted modified data")
	}
	if err := os.WriteFile(filepath.Join(backupPath, "data", "data", "usage.sqlite"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "data", "unexpected.txt"), []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAndVerifyBackupManifest(manifest, transaction, backupPath); err == nil {
		t.Fatal("backup verification accepted an unlisted file")
	}
}

func TestRestoreRollbackSnapshotRemovesPathsThatDidNotExist(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	dataDir := filepath.Join(root, "data")
	transaction.DataPaths = []string{dataDir}
	backupPath := filepath.Join(root, "backups", "snapshot")
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "new.sqlite"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte("new config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("new data path remains after rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("new config remains after rollback: %v", err)
	}
}

func TestRecoverInterruptedUpdateRestoresSnapshot(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "usage.sqlite"), []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction.DataPaths = []string{dataDir}
	backupPath := filepath.Join(root, "backups", "snapshot")
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	managed := RuntimeManagedFiles()
	if err := os.WriteFile(filepath.Join(root, managed.Binary), []byte("new-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "usage.sqlite"), []byte("new-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transactionRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID)
	transaction.StatusPath = filepath.Join(root, ".update", "status.json")
	if err := WriteTransaction(filepath.Join(transactionRoot, "transaction.json"), transaction); err != nil {
		t.Fatal(err)
	}
	status := TransactionStatus{
		TransactionID:  transaction.TransactionID,
		InstallID:      manifest.InstallID,
		CurrentVersion: transaction.CurrentVersion,
		TargetVersion:  transaction.TargetVersion,
		State:          StateSwitching,
		BackupPath:     backupPath,
		UpdaterPID:     999999,
	}
	if err := WriteTransactionStatus(transaction.StatusPath, status); err != nil {
		t.Fatal(err)
	}
	got, recovered, err := RecoverInterruptedUpdate(transaction.InstallManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered || got.State != StateRolledBack {
		t.Fatalf("RecoverInterruptedUpdate() = (%#v, %v)", got, recovered)
	}
	assertFileContent(t, filepath.Join(root, managed.Binary), "old-"+managed.Binary)
	assertFileContent(t, filepath.Join(dataDir, "usage.sqlite"), "old-data")
}

func TestRecoverInterruptedPreparationDoesNotBlockStartup(t *testing.T) {
	_, manifest, transaction := testUpdateLayout(t)
	for _, state := range []TransactionState{StateDownloading, StateVerifying} {
		status := TransactionStatus{
			TransactionID:  transaction.TransactionID,
			InstallID:      manifest.InstallID,
			CurrentVersion: transaction.CurrentVersion,
			TargetVersion:  transaction.TargetVersion,
			State:          state,
		}
		if err := WriteTransactionStatus(transaction.StatusPath, status); err != nil {
			t.Fatal(err)
		}
		got, recovered, err := RecoverInterruptedUpdate(transaction.InstallManifest)
		if err != nil {
			t.Fatal(err)
		}
		if !recovered || got.State != StateFailed {
			t.Fatalf("RecoverInterruptedUpdate(%s) = (%#v, %v)", state, got, recovered)
		}
	}
}

func TestRecoverInterruptedPreSwitchUpdateDoesNotRequireTransactionMetadata(t *testing.T) {
	_, manifest, transaction := testUpdateLayout(t)
	for _, state := range []TransactionState{StateLaunching, StateStopping, StateBackingUp} {
		status := TransactionStatus{
			TransactionID:  transaction.TransactionID,
			InstallID:      manifest.InstallID,
			CurrentVersion: transaction.CurrentVersion,
			TargetVersion:  transaction.TargetVersion,
			State:          state,
			UpdaterPID:     999999,
		}
		if err := WriteTransactionStatus(transaction.StatusPath, status); err != nil {
			t.Fatal(err)
		}
		got, recovered, err := RecoverInterruptedUpdate(transaction.InstallManifest)
		if err != nil {
			t.Fatal(err)
		}
		if !recovered || got.State != StateFailed || got.UpdaterPID != 0 {
			t.Fatalf("RecoverInterruptedUpdate(%s) = (%#v, %v)", state, got, recovered)
		}
	}
}

func TestRecoverInterruptedUpdateRejectsStatusFromAnotherInstallation(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transactionRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID)
	if err := WriteTransaction(filepath.Join(transactionRoot, "transaction.json"), transaction); err != nil {
		t.Fatal(err)
	}
	status := TransactionStatus{
		TransactionID:  transaction.TransactionID,
		InstallID:      "another-install",
		CurrentVersion: transaction.CurrentVersion,
		TargetVersion:  transaction.TargetVersion,
		State:          StateSwitching,
		UpdaterPID:     999999,
	}
	if err := WriteTransactionStatus(transaction.StatusPath, status); err != nil {
		t.Fatal(err)
	}
	got, recovered, err := RecoverInterruptedUpdate(transaction.InstallManifest)
	if err == nil || recovered || got.State != StateManualRecoveryRequired {
		t.Fatalf("RecoverInterruptedUpdate() = (%#v, %v, %v)", got, recovered, err)
	}
}

func testUpdateLayout(t *testing.T) (string, InstallManifest, Transaction) {
	t.Helper()
	root := t.TempDir()
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old-"+name), 0o700); err != nil {
			t.Fatal(err)
		}
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
	manifestPath := filepath.Join(root, ".update", "install.json")
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	transaction := Transaction{
		SchemaVersion:   1,
		TransactionID:   strings.Repeat("a", 32),
		InstallManifest: manifestPath,
		StatusPath:      filepath.Join(root, ".update", "status.json"),
		PackageRoot:     filepath.Join(root, ".update", "transactions", strings.Repeat("a", 32), "staging", "package"),
		CurrentVersion:  "v1.0.0",
		TargetVersion:   "v1.1.0",
		ParentPID:       os.Getpid(),
		HealthURL:       "http://127.0.0.1:28317/health",
		InfoURL:         "http://127.0.0.1:28317/usage-service/info",
	}
	return root, manifest, transaction
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
