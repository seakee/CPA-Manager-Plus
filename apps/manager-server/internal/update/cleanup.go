package update

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

const terminalTransactionRetention = 2

type transactionDirectory struct {
	path    string
	modTime time.Time
}

func CleanupTerminalTransactions(installRoot, activeTransactionID string) error {
	transactionsRoot := filepath.Join(installRoot, ".update", "transactions")
	entries, err := os.ReadDir(transactionsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	candidates := make([]transactionDirectory, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == activeTransactionID {
			continue
		}
		root := filepath.Join(transactionsRoot, entry.Name())
		transaction, err := ReadTransaction(filepath.Join(root, "transaction.json"))
		if err != nil || transaction.TransactionID != entry.Name() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, transactionDirectory{path: root, modTime: info.ModTime()})
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].modTime.After(candidates[right].modTime)
	})
	retention := terminalTransactionRetention
	if retention > len(candidates) {
		retention = len(candidates)
	}
	for _, candidate := range candidates[retention:] {
		if err := os.RemoveAll(candidate.path); err != nil {
			continue
		}
	}
	return nil
}

func CleanupTransactionStaging(transactionPath string) error {
	transaction, err := ReadTransaction(transactionPath)
	if err != nil {
		return err
	}
	transactionRoot := filepath.Dir(transactionPath)
	stagingRoot := filepath.Join(transactionRoot, "staging")
	if !pathWithin(stagingRoot, transaction.PackageRoot) {
		return os.ErrInvalid
	}
	return os.RemoveAll(stagingRoot)
}
