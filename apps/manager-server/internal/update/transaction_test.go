package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTransactionRejectsPathsOutsideInstallRoot(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transaction.StatusPath = filepath.Join(t.TempDir(), "status.json")
	path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
	if err := WriteTransaction(path, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTransaction(path); err == nil {
		t.Fatal("ReadTransaction() accepted an escaping status path")
	}
}

func TestReadTransactionRequiresCanonicalManagedLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(root string, transaction *Transaction, path *string)
	}{
		{
			name: "transaction file outside its ID directory",
			mutate: func(root string, transaction *Transaction, path *string) {
				*path = filepath.Join(root, ".update", "transactions", strings.Repeat("b", 32), "transaction.json")
			},
		},
		{
			name: "transaction ID differs from directory",
			mutate: func(_ string, transaction *Transaction, _ *string) {
				transaction.TransactionID = strings.Repeat("b", 32)
			},
		},
		{
			name: "status path is another install-root file",
			mutate: func(root string, transaction *Transaction, _ *string) {
				transaction.StatusPath = filepath.Join(root, ".update", "other-status.json")
			},
		},
		{
			name: "package belongs to another transaction",
			mutate: func(root string, transaction *Transaction, _ *string) {
				transaction.PackageRoot = filepath.Join(root, ".update", "transactions", strings.Repeat("b", 32), "staging", "package")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _, transaction := testUpdateLayout(t)
			path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
			test.mutate(root, &transaction, &path)
			if err := WriteTransaction(path, transaction); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadTransaction(path); err == nil {
				t.Fatal("ReadTransaction() accepted a transaction outside the canonical managed layout")
			}
		})
	}
}

func TestReadTransactionRejectsReservedDataPaths(t *testing.T) {
	tests := []struct {
		name string
		path func(root string, manifest InstallManifest) string
	}{
		{name: "install root", path: func(root string, _ InstallManifest) string { return root }},
		{name: "update metadata", path: func(root string, _ InstallManifest) string { return filepath.Join(root, ".update") }},
		{name: "backup root", path: func(_ string, manifest InstallManifest) string { return manifest.BackupRoot }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, manifest, transaction := testUpdateLayout(t)
			transaction.DataPaths = []string{test.path(root, manifest)}
			path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
			if err := WriteTransaction(path, transaction); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadTransaction(path); err == nil {
				t.Fatal("ReadTransaction() accepted a reserved data path")
			}
		})
	}
}

func TestReadTransactionRejectsOverlappingDataPaths(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transaction.DataPaths = []string{filepath.Join(root, "data"), filepath.Join(root, "data", "nested")}
	path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
	if err := WriteTransaction(path, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTransaction(path); err == nil {
		t.Fatal("ReadTransaction() accepted overlapping data paths")
	}
}

func TestReadTransactionRejectsUntrustedStatusURLs(t *testing.T) {
	tests := []struct {
		name   string
		health string
		info   string
	}{
		{name: "HTTPS", health: "https://127.0.0.1:28317/health", info: "https://127.0.0.1:28317/usage-service/info"},
		{name: "different origin", health: "http://127.0.0.1:28317/health", info: "http://127.0.0.1:28318/usage-service/info"},
		{name: "unexpected health path", health: "http://127.0.0.1:28317/other", info: "http://127.0.0.1:28317/usage-service/info"},
		{name: "credentials", health: "http://user@127.0.0.1:28317/health", info: "http://127.0.0.1:28317/usage-service/info"},
		{name: "query", health: "http://127.0.0.1:28317/health?token=value", info: "http://127.0.0.1:28317/usage-service/info"},
		{name: "missing port", health: "http://127.0.0.1/health", info: "http://127.0.0.1/usage-service/info"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _, transaction := testUpdateLayout(t)
			transaction.HealthURL = test.health
			transaction.InfoURL = test.info
			path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
			if err := WriteTransaction(path, transaction); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadTransaction(path); err == nil {
				t.Fatal("ReadTransaction() accepted untrusted status URLs")
			}
		})
	}
}

func TestReadTransactionRejectsEscapingDataPath(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transaction.DataPaths = []string{filepath.Join(t.TempDir(), "data")}
	path := filepath.Join(root, ".update", "transactions", transaction.TransactionID, "transaction.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteTransaction(path, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTransaction(path); err == nil {
		t.Fatal("ReadTransaction() accepted an escaping data path")
	}
}
