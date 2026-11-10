package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// The System database card reports the checkpoint the Manager observed for its
// own database. The Fresh service starts that maintenance itself, so the card
// has to show a real checkpoint instead of the inspect-only "unobserved" state
// it reported for the whole life of a v2 installation.
func TestFreshDatabaseStatusObservesCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("create table if not exists probe(id integer primary key, value text)"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startFreshDatabaseMaintenance(ctx, path)
	// A second call is the completion-replay path and must not open another
	// maintainer for the same database.
	startFreshDatabaseMaintenance(ctx, path)

	provider := freshDatabaseStatusProvider{dbPath: path}
	deadline := time.Now().Add(20 * time.Second)
	for {
		snapshot := provider.Snapshot()
		if snapshot.Checkpoint.Mode != sqliterepo.WALCheckpointModeUnobserved && snapshot.Checkpoint.ExecutedAtMS > 0 {
			if snapshot.DatabaseBytes <= 0 {
				t.Fatalf("observed checkpoint without database files: %+v", snapshot)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("database status never observed a checkpoint: %+v", snapshot.Checkpoint)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
