package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupTerminalTransactionsRetainsNewestAndActive(t *testing.T) {
	root, _, base := testUpdateLayout(t)
	ids := []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32)}
	for index, id := range ids {
		transactionRoot := filepath.Join(root, ".update", "transactions", id)
		transaction := base
		transaction.TransactionID = id
		transaction.StatusPath = filepath.Join(root, ".update", "status.json")
		transaction.PackageRoot = filepath.Join(transactionRoot, "staging", "package")
		if err := os.MkdirAll(transaction.PackageRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := WriteTransaction(filepath.Join(transactionRoot, "transaction.json"), transaction); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(transactionRoot, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupTerminalTransactions(root, ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids[0], ids[2], ids[3]} {
		if _, err := os.Stat(filepath.Join(root, ".update", "transactions", id)); err != nil {
			t.Fatalf("expected transaction %s to remain: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".update", "transactions", ids[1])); !os.IsNotExist(err) {
		t.Fatalf("expected oldest removable transaction to be deleted, got %v", err)
	}
}

func TestCleanupTerminalTransactionsProtectsCurrentGlobalStatusTransaction(t *testing.T) {
	root, _, base := testUpdateLayout(t)
	ids := []string{strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32), strings.Repeat("8", 32)}
	for index, id := range ids {
		transactionRoot := filepath.Join(root, ".update", "transactions", id)
		transaction := base
		transaction.TransactionID = id
		transaction.PackageRoot = filepath.Join(transactionRoot, "staging", "package")
		if err := os.MkdirAll(transaction.PackageRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := WriteTransaction(filepath.Join(transactionRoot, "transaction.json"), transaction); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(transactionRoot, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	protected := ids[0]
	if err := CleanupTerminalTransactions(root, protected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".update", "transactions", protected)); err != nil {
		t.Fatalf("protected transaction was removed: %v", err)
	}
}

func TestCleanupTransactionStagingPreservesMetadata(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transactionRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID)
	transactionPath := filepath.Join(transactionRoot, "transaction.json")
	if err := os.MkdirAll(transaction.PackageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transaction.PackageRoot, "package.bin"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteTransaction(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}
	if err := CleanupTransactionStaging(transactionPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(transactionRoot, "staging")); !os.IsNotExist(err) {
		t.Fatalf("staging still exists: %v", err)
	}
	if _, err := os.Stat(transactionPath); err != nil {
		t.Fatalf("transaction metadata was removed: %v", err)
	}
}

func TestCleanupTransactionStagingRejectsPackageOutsideStaging(t *testing.T) {
	root, _, transaction := testUpdateLayout(t)
	transactionRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID)
	transactionPath := filepath.Join(transactionRoot, "transaction.json")
	transaction.PackageRoot = filepath.Join(transactionRoot, "package")
	if err := os.MkdirAll(transaction.PackageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteTransaction(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}
	if err := CleanupTransactionStaging(transactionPath); err == nil {
		t.Fatal("CleanupTransactionStaging accepted a package outside staging")
	}
}
