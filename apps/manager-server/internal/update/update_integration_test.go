package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInstallManagedFilesAndRollbackRoundTrip(t *testing.T) {
	root, manifest, transaction := testUpdateLayout(t)
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "usage.sqlite"), []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction.DataPaths = []string{dataDir}
	packageRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "staging", "package")
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control} {
		if err := os.WriteFile(filepath.Join(packageRoot, name), []byte("new-"+name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	backupPath := filepath.Join(root, "backups", "snapshot")
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := installManagedFiles(manifest, packageRoot); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(root, managed.Binary), "new-"+managed.Binary)
	if err := os.WriteFile(filepath.Join(dataDir, "usage.sqlite"), []byte("new-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(root, managed.Binary), "old-"+managed.Binary)
	assertFileContent(t, filepath.Join(dataDir, "usage.sqlite"), "old-data")
}

func TestValidateTargetVersionUsesIsolatedHTTPServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			response.WriteHeader(http.StatusOK)
		case "/usage-service/info":
			_ = json.NewEncoder(response).Encode(map[string]string{"runtimeVersion": "v1.2.3"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	transaction := Transaction{
		TargetVersion: "v1.2.3",
		HealthURL:     server.URL + "/health",
		InfoURL:       server.URL + "/usage-service/info",
	}
	manifest := InstallManifest{InstallRoot: t.TempDir(), ControlScript: validationControlScript(t)}
	if err := validateTargetVersion(t.Context(), manifest, transaction); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetVersionRequiresContinuousStability(t *testing.T) {
	checks := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			checks++
			if checks == 2 {
				http.Error(response, "restarting", http.StatusServiceUnavailable)
				return
			}
			response.WriteHeader(http.StatusOK)
		case "/usage-service/info":
			_ = json.NewEncoder(response).Encode(map[string]string{"runtimeVersion": "v1.2.3"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	transaction := Transaction{
		TargetVersion: "v1.2.3",
		HealthURL:     server.URL + "/health",
		InfoURL:       server.URL + "/usage-service/info",
	}
	started := time.Now()
	manifest := InstallManifest{InstallRoot: t.TempDir(), ControlScript: validationControlScript(t)}
	if err := validateTargetVersion(t.Context(), manifest, transaction); err != nil {
		t.Fatal(err)
	}
	if checks < 8 {
		t.Fatalf("health checks = %d, want a reset plus six stable checks", checks)
	}
	if elapsed := time.Since(started); elapsed < 12*time.Second {
		t.Fatalf("validation completed too quickly: %s", elapsed)
	}
}

func TestValidateTargetVersionRequiresControlScriptStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			response.WriteHeader(http.StatusOK)
		case "/usage-service/info":
			_ = json.NewEncoder(response).Encode(map[string]string{"runtimeVersion": "v1.2.3"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	transaction := Transaction{
		TargetVersion: "v1.2.3",
		HealthURL:     server.URL + "/health",
		InfoURL:       server.URL + "/usage-service/info",
	}
	manifest := InstallManifest{InstallRoot: t.TempDir(), ControlScript: validationControlScriptWithExitCode(t, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 13*time.Second)
	defer cancel()
	if err := validateTargetVersion(ctx, manifest, transaction); err == nil {
		t.Fatal("validation accepted a failing control-script status")
	}
}

func validationControlScript(t *testing.T) string {
	return validationControlScriptWithExitCode(t, 0)
}

func validationControlScriptWithExitCode(t *testing.T, exitCode int) string {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(root, "status.ps1")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("exit %d\r\n", exitCode)), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(root, "status.sh")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("#!/usr/bin/env sh\nexit %d\n", exitCode)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagedLayoutNeverUsesLiveDefaultPorts(t *testing.T) {
	if runtime.GOOS == "windows" {
		root, _, transaction := testUpdateLayout(t)
		if filepath.Clean(root) == filepath.Clean(`D:\projects\CPA`) {
			t.Fatal("test layout unexpectedly targets the live CPA directory")
		}
		if transaction.HealthURL != "http://127.0.0.1:28317/health" {
			t.Fatalf("health URL = %q", transaction.HealthURL)
		}
	}
}
