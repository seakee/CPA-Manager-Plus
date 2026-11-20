package maintenancebundle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func buildZeroByteWALWithRolledBackSpill(t *testing.T, truncateTrailing bool) (Layout, string, *processlock.Lock) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{
		DatabasePath:    filepath.Join(root, "usage.sqlite"),
		DataKeyPath:     filepath.Join(root, "data.key"),
		ConfigPath:      filepath.Join(root, "manager.json"),
		ArchivesPath:    filepath.Join(root, "usage-archives"),
		RequireConfig:   true,
		RequireArchives: true,
	}
	rawKey, _, err := security.LoadOrCreateDataKey("", l.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(rawKey)
	if err != nil {
		t.Fatal(err)
	}

	seedDir := filepath.Join(root, "seed-stage")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(seedDir, "seed.sqlite")
	seed, err := sql.Open("sqlite", seedPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.SetMaxOpenConns(1)
	if _, err := seed.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; PRAGMA cache_size=2;"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	// Tx1: commit schema and baseline rows so page 1 is durably committed in WAL.
	saveConnections(t, seed, protector, "both")
	if _, err := seed.Exec(`
		CREATE TABLE test_table (id INTEGER PRIMARY KEY, note TEXT);
		CREATE TABLE spill_table (id INTEGER PRIMARY KEY, blob_text TEXT);
		INSERT INTO test_table VALUES (1, 'tx1-committed');
	`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	statAfterTx1, err := os.Stat(seedPath + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	// Tx2: spill uncommitted pages to -wal under cache_size=2, then ROLLBACK.
	if _, err := seed.Exec("BEGIN IMMEDIATE"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	largeChunk := strings.Repeat("spill-frame-padding-", 300)
	for i := range 24 {
		if _, err := seed.Exec(`INSERT INTO spill_table (id, blob_text) VALUES (?, ?)`, i+1, largeChunk); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
	}
	statDuringTx2, err := os.Stat(seedPath + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if statDuringTx2.Size() <= statAfterTx1.Size() {
		_ = seed.Close()
		t.Fatalf("expected Tx2 cache spill to grow WAL (%d <= %d)", statDuringTx2.Size(), statAfterTx1.Size())
	}
	if _, err := seed.Exec("ROLLBACK"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	// Tx3: commit fewer frames than Tx2 spilled so rolled-back frames remain at the tail
	// with matching salts and a cumulative checksum mismatch after Tx3's commit frame.
	if _, err := seed.Exec(`INSERT INTO test_table VALUES (2, 'tx3-committed')`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	walBytes, err := os.ReadFile(seedPath + "-wal")
	_ = seed.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(seedDir); err != nil {
		t.Fatal(err)
	}

	if truncateTrailing {
		pageSize := int(binary.BigEndian.Uint32(walBytes[8:12]))
		if pageSize == 1 {
			pageSize = 65536
		}
		var trailingHdr [24]byte
		binary.BigEndian.PutUint32(trailingHdr[0:4], 2) // page 2, uncommitted (dbSize=0)
		copy(trailingHdr[8:16], walBytes[16:24])        // active salt1 & salt2
		walBytes = append(walBytes, trailingHdr[:]...)
		walBytes = append(walBytes, bytes.Repeat([]byte{0x5a}, pageSize/4)...)
	}

	put(t, l.DatabasePath, nil)
	put(t, l.DatabasePath+"-wal", walBytes)
	put(t, l.ConfigPath, []byte(`{"dbPath":"`+filepath.ToSlash(l.DatabasePath)+`"}`))
	if err := os.MkdirAll(filepath.Join(l.ArchivesPath, "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(l.ArchivesPath, "2026", "10", "history.jsonl.gz"), []byte("archive-segment\n"))
	lock, err := processlock.Acquire(l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return l, root, lock
}

func TestZeroByteWALRecoveryStopsAtRolledBackSpillAndTruncatedTail(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name             string
		truncateTrailing bool
	}{
		{name: "rolled-back cache spill followed by smaller commit", truncateTrailing: false},
		{name: "rolled-back spill plus truncated uncommitted trailing frame", truncateTrailing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, _, lock := buildZeroByteWALWithRolledBackSpill(t, tc.truncateTrailing)
			scratchRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bundleRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bundleDir := filepath.Join(bundleRoot, "bundle")
			scratchDir := scratchRoot

			if err := ValidateMatchingEvidenceWithScratch(ctx, layout, scratchDir); err != nil {
				t.Fatalf("ValidateMatchingEvidenceWithScratch failed: %v", err)
			}
			if _, err := CreateWithScratch(ctx, layout, bundleDir, lock, scratchDir); err != nil {
				t.Fatalf("CreateWithScratch failed: %v", err)
			}
			if _, err := Verify(ctx, bundleDir); err != nil {
				t.Fatalf("Verify failed: %v", err)
			}

			restoredRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreNewWithScratch(ctx, bundleDir, filepath.Join(restoredRoot, "restored"), scratchDir)
			if err != nil {
				t.Fatalf("RestoreNewWithScratch failed: %v", err)
			}
			if err := ValidateMatchingEvidenceWithScratch(ctx, restored, scratchDir); err != nil {
				t.Fatalf("ValidateMatchingEvidenceWithScratch on restored layout failed: %v", err)
			}
			if err := replayZeroByteScratchWAL(ctx, restored.DatabasePath); err != nil {
				t.Fatalf("replayZeroByteScratchWAL on restored copy failed: %v", err)
			}
			db, err := sql.Open("sqlite", restored.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM test_table`).Scan(&count); err != nil {
				t.Fatalf("query restored test_table: %v", err)
			}
			if count != 2 {
				t.Fatalf("restored test_table row count = %d, want 2 (Tx1 + Tx3 without rolled-back Tx2)", count)
			}
			var spillCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM spill_table`).Scan(&spillCount); err != nil {
				t.Fatalf("query restored spill_table: %v", err)
			}
			if spillCount != 0 {
				t.Fatalf("restored spill_table row count = %d, want 0", spillCount)
			}
		})
	}

	t.Run("zero-commit WAL with only uncommitted frames still fails closed", func(t *testing.T) {
		layout, _, lock := zeroByteWALFixture(t)
		rawWAL, err := os.ReadFile(layout.DatabasePath + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		pageSize := int(binary.BigEndian.Uint32(rawWAL[8:12]))
		if pageSize == 1 {
			pageSize = 65536
		}
		// Keep only the 32-byte WAL header plus a truncated first frame so zero commits exist.
		put(t, layout.DatabasePath+"-wal", rawWAL[:32+24+pageSize/2])

		bundleBase, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		scratchRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bundleDir := filepath.Join(bundleBase, "bundle")
		_, err = CreateWithScratch(ctx, layout, bundleDir, lock, scratchRoot)
		if err == nil {
			t.Fatal("expected zero-commit truncated WAL to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "contains no committed database pages") {
			t.Fatalf("unexpected error for zero-commit truncated WAL: %v", err)
		}
	})
}
