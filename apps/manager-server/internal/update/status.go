package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type TransactionState string

const (
	StateDownloading            TransactionState = "downloading"
	StateVerifying              TransactionState = "verifying"
	StateStaged                 TransactionState = "staged"
	StateLaunching              TransactionState = "launching"
	StateStopping               TransactionState = "stopping"
	StateBackingUp              TransactionState = "backing_up"
	StateSwitching              TransactionState = "switching"
	StateStarting               TransactionState = "starting"
	StateValidating             TransactionState = "validating"
	StateSucceeded              TransactionState = "succeeded"
	StateRollingBack            TransactionState = "rolling_back"
	StateRolledBack             TransactionState = "rolled_back"
	StateFailed                 TransactionState = "failed"
	StateManualRecoveryRequired TransactionState = "manual_recovery_required"
)

type TransactionStatus struct {
	SchemaVersion  int              `json:"schemaVersion"`
	TransactionID  string           `json:"transactionId"`
	InstallID      string           `json:"installId"`
	CurrentVersion string           `json:"currentVersion"`
	TargetVersion  string           `json:"targetVersion"`
	State          TransactionState `json:"state"`
	Message        string           `json:"message,omitempty"`
	BackupPath     string           `json:"backupPath,omitempty"`
	UpdaterPID     int              `json:"updaterPid,omitempty"`
	StartedAt      string           `json:"startedAt"`
	UpdatedAt      string           `json:"updatedAt"`
	FinishedAt     string           `json:"finishedAt,omitempty"`
}

func (s TransactionStatus) Terminal() bool {
	switch s.State {
	case StateSucceeded, StateRolledBack, StateFailed, StateManualRecoveryRequired:
		return true
	default:
		return false
	}
}

func ReadTransactionStatus(path string) (TransactionStatus, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return TransactionStatus{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return TransactionStatus{}, errors.New("update status must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return TransactionStatus{}, err
	}
	var status TransactionStatus
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return TransactionStatus{}, fmt.Errorf("parse update status: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return TransactionStatus{}, fmt.Errorf("parse update status: %w", err)
	}
	if status.SchemaVersion != 1 || !validTransactionID(status.TransactionID) ||
		strings.TrimSpace(status.InstallID) == "" || NormalizeVersion(status.CurrentVersion) == "" ||
		NormalizeVersion(status.TargetVersion) == "" || !validTransactionState(status.State) {
		return TransactionStatus{}, errors.New("update status is invalid")
	}
	return status, nil
}

func validTransactionState(state TransactionState) bool {
	switch state {
	case StateDownloading, StateVerifying, StateStaged, StateLaunching, StateStopping, StateBackingUp,
		StateSwitching, StateStarting, StateValidating, StateSucceeded, StateRollingBack, StateRolledBack,
		StateFailed, StateManualRecoveryRequired:
		return true
	default:
		return false
	}
}

func WriteTransactionStatus(path string, status TransactionStatus) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("update status path is empty")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if status.SchemaVersion == 0 {
		status.SchemaVersion = 1
	}
	if status.StartedAt == "" {
		status.StartedAt = now
	}
	status.UpdatedAt = now
	if status.Terminal() && status.FinishedAt == "" {
		status.FinishedAt = now
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePrivateAtomic(path, data)
}
