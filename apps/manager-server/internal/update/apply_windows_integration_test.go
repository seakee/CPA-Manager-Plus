//go:build windows

package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestApplyTransactionWindowsEndToEndWithPrivateBackupAndCleanup(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	managed := RuntimeManagedFiles()
	controlLog := filepath.Join(root, "control.log")
	controlScript := `param([string]$Command)
Add-Content -LiteralPath '` + strings.ReplaceAll(controlLog, "'", "''") + `' -Value $Command
exit 0
`
	if err := os.WriteFile(manifest.ControlScript, []byte(controlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(dataDir, "usage.sqlite")
	if err := os.WriteFile(dataPath, []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction.DataPaths = []string{dataDir}
	parentCommand := exec.Command("cmd.exe", "/c", "exit", "0")
	if err := parentCommand.Start(); err != nil {
		t.Fatal(err)
	}
	transaction.ParentPID = parentCommand.Process.Pid
	_ = parentCommand.Wait()
	packageRoot := transaction.PackageRoot
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control} {
		content := "new-" + name
		if name == managed.Control {
			content = controlScript
		}
		if err := os.WriteFile(filepath.Join(packageRoot, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			response.WriteHeader(http.StatusOK)
		case "/usage-service/info":
			_ = json.NewEncoder(response).Encode(map[string]string{"runtimeVersion": transaction.TargetVersion})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	transaction.HealthURL = server.URL + "/health"
	transaction.InfoURL = server.URL + "/usage-service/info"
	transactionPath := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
	if err := WriteTransaction(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}
	if err := WriteTransactionStatus(transaction.StatusPath, TransactionStatus{
		TransactionID:  transaction.TransactionID,
		InstallID:      manifest.InstallID,
		CurrentVersion: transaction.CurrentVersion,
		TargetVersion:  transaction.TargetVersion,
		State:          StateStaged,
	}); err != nil {
		t.Fatal(err)
	}

	if err := ApplyTransaction(context.Background(), transactionPath); err != nil {
		t.Fatal(err)
	}
	status, err := ReadTransactionStatus(transaction.StatusPath)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateSucceeded || status.BackupPath == "" {
		t.Fatalf("status = %#v", status)
	}
	assertFileContent(t, filepath.Join(root, managed.Binary), "new-"+managed.Binary)
	assertFileContent(t, dataPath, "old-data")
	if _, err := os.Stat(filepath.Join(filepath.Dir(transactionPath), "staging")); !os.IsNotExist(err) {
		t.Fatalf("staging remains after success: %v", err)
	}
	for _, path := range []string{
		status.BackupPath,
		filepath.Join(status.BackupPath, "backup-manifest.json"),
		filepath.Join(status.BackupPath, "data", "data", "usage.sqlite"),
	} {
		assertPrivateWindowsACL(t, path)
	}
	logBytes, err := os.ReadFile(controlLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(logBytes)) != "start\r\nstatus" && strings.TrimSpace(string(logBytes)) != "start\nstatus" {
		t.Fatalf("control log = %q", logBytes)
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
		t.Fatal("live data was left with backup-only private ACL")
	}
}
