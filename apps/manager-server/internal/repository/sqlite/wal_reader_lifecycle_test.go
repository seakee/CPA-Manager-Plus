package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestWALMaintenanceRecoversAfterReadTransactionContextCancellation(t *testing.T) {
	db, dbPath := openWALMaintenanceTestDB(t)
	now := time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)
	maintenance, err := newWALMaintenance(dbPath, walMaintenanceOptions{
		truncateThresholdBytes: 1,
		now:                    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create WAL maintenance: %v", err)
	}
	t.Cleanup(func() {
		_ = maintenance.Close()
	})

	readerDB, err := sql.Open("sqlite", walMaintenanceDataSourceName(dbPath))
	if err != nil {
		t.Fatalf("open reader database: %v", err)
	}
	readerDB.SetMaxOpenConns(1)
	readerDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = readerDB.Close()
	})

	readerCtx, cancelReader := context.WithCancel(context.Background())
	readerTx, err := readerDB.BeginTx(readerCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		cancelReader()
		t.Fatalf("begin read transaction: %v", err)
	}
	t.Cleanup(func() {
		cancelReader()
		_ = readerTx.Rollback()
	})

	var initialCount int
	if err := readerTx.QueryRowContext(readerCtx, `select count(*) from wal_maintenance_test`).Scan(&initialCount); err != nil {
		t.Fatalf("establish read snapshot: %v", err)
	}

	appendWALMaintenanceRows(t, db, 16)
	maintenance.runOnce(context.Background())
	blocked := maintenance.Snapshot()
	if blocked.Checkpoint.Error != "" {
		t.Fatalf("checkpoint while reader is active: %q", blocked.Checkpoint.Error)
	}
	if blocked.Checkpoint.Mode != WALCheckpointModePassive {
		t.Fatalf("checkpoint mode while reader is active = %q, want passive", blocked.Checkpoint.Mode)
	}
	if blocked.Checkpoint.LogFrames <= 0 || blocked.Checkpoint.CheckpointedFrames >= blocked.Checkpoint.LogFrames {
		t.Fatalf(
			"checkpoint frames while reader is active = %d/%d, want an uncheckpointed tail",
			blocked.Checkpoint.CheckpointedFrames,
			blocked.Checkpoint.LogFrames,
		)
	}
	if blocked.Checkpoint.LastTruncateAttemptAtMS != 0 {
		t.Fatalf("truncate attempted while reader was active at %d", blocked.Checkpoint.LastTruncateAttemptAtMS)
	}

	cancelReader()

	deadline := time.Now().Add(2 * time.Second)
	for {
		now = now.Add(time.Millisecond)
		maintenance.runOnce(context.Background())
		recovered := maintenance.Snapshot()
		if recovered.Checkpoint.Error == "" &&
			recovered.Checkpoint.Mode == WALCheckpointModeTruncate &&
			recovered.Checkpoint.CheckpointedFrames == recovered.Checkpoint.LogFrames &&
			recovered.WALBytes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"checkpoint did not recover after reader cancellation: mode=%q error=%q frames=%d/%d wal=%d",
				recovered.Checkpoint.Mode,
				recovered.Checkpoint.Error,
				recovered.Checkpoint.CheckpointedFrames,
				recovered.Checkpoint.LogFrames,
				recovered.WALBytes,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := readerTx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("read transaction state after cancellation: %v", err)
	}
}
