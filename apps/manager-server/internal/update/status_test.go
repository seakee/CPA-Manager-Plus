package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTransactionStatusRejectsUnknownAndTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	tests := []string{
		`{"schemaVersion":1,"transactionId":"` + strings.Repeat("a", 32) + `","installId":"test","currentVersion":"v1.0.0","targetVersion":"v1.1.0","state":"staged","unknown":true}`,
		`{"schemaVersion":1,"transactionId":"` + strings.Repeat("a", 32) + `","installId":"test","currentVersion":"v1.0.0","targetVersion":"v1.1.0","state":"staged"} {}`,
	}
	for _, content := range tests {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadTransactionStatus(path); err == nil {
			t.Fatal("ReadTransactionStatus() accepted non-canonical JSON")
		}
	}
}

func TestReadTransactionStatusRejectsInvalidIdentityAndState(t *testing.T) {
	tests := []TransactionStatus{
		{TransactionID: "invalid", InstallID: "test", CurrentVersion: "v1.0.0", TargetVersion: "v1.1.0", State: StateStaged},
		{TransactionID: strings.Repeat("a", 32), InstallID: "", CurrentVersion: "v1.0.0", TargetVersion: "v1.1.0", State: StateStaged},
		{TransactionID: strings.Repeat("a", 32), InstallID: "test", CurrentVersion: "dev", TargetVersion: "v1.1.0", State: StateStaged},
		{TransactionID: strings.Repeat("a", 32), InstallID: "test", CurrentVersion: "v1.0.0", TargetVersion: "v1.1.0", State: "unknown"},
	}
	for _, status := range tests {
		path := filepath.Join(t.TempDir(), "status.json")
		if err := WriteTransactionStatus(path, status); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadTransactionStatus(path); err == nil {
			t.Fatalf("ReadTransactionStatus() accepted invalid status %#v", status)
		}
	}
}
