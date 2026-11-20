package maintenancebundle

import (
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func zeroByteWALFixture(t *testing.T) (Layout, string, *processlock.Lock) {
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
	if _, err := seed.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	saveConnections(t, seed, protector, "both")
	if _, err := seed.Exec(`CREATE TABLE test_table (id INTEGER PRIMARY KEY, note TEXT); INSERT INTO test_table VALUES (1, 'uncheckpointed-wal');`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	// Copy the uncheckpointed 0-byte main file and populated -wal while the
	// connection is still open so SQLite has not checkpointed on close.
	// Read the populated -wal (which contains page 1 + all subsequent pages
	// written after PRAGMA journal_mode=WAL) before seed.Close() checkpoints it,
	// and pair it with a 0-byte main database file.
	walBytes, err := os.ReadFile(seedPath + "-wal")
	_ = seed.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(seedDir); err != nil {
		t.Fatal(err)
	}
	if len(walBytes) <= 32 {
		t.Fatalf("expected populated WAL frames (>32 bytes), got %d bytes", len(walBytes))
	}
	put(t, l.DatabasePath, nil)
	put(t, l.DatabasePath+"-wal", walBytes)
	put(t, l.ConfigPath, []byte(`{"dbPath":"`+filepath.ToSlash(l.DatabasePath)+`"}`))
	if err := os.MkdirAll(filepath.Join(l.ArchivesPath, "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(l.ArchivesPath, "2026", "10", "history.jsonl.gz"), []byte("opaque archive bytes\x00\xff"))
	lock, err := processlock.Acquire(l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return l, root, lock
}

func validEmptyWALHeaderBytes() []byte {
	var header [32]byte
	binary.BigEndian.PutUint32(header[0:4], 0x377f0682)
	binary.BigEndian.PutUint32(header[4:8], 3007000)
	binary.BigEndian.PutUint32(header[8:12], 4096)
	binary.BigEndian.PutUint32(header[16:20], 0x11223344)
	binary.BigEndian.PutUint32(header[20:24], 0x55667788)
	var s1, s2 uint32
	for i := 0; i < 24; i += 8 {
		s1 += binary.LittleEndian.Uint32(header[i:i+4]) + s2
		s2 += binary.LittleEndian.Uint32(header[i+4:i+8]) + s1
	}
	binary.BigEndian.PutUint32(header[24:28], s1)
	binary.BigEndian.PutUint32(header[28:32], s2)
	return header[:]
}

func TestValidateMatchingEvidenceZeroByteDatabaseWithPopulatedWAL(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts 0-byte main database with valid populated WAL across create and restore", func(t *testing.T) {
		l, root, lock := zeroByteWALFixture(t)
		if err := ValidateMatchingEvidence(ctx, l); err != nil {
			t.Fatalf("ValidateMatchingEvidence rejected valid 0-byte+WAL layout: %v", err)
		}
		bundleDir := filepath.Join(root, "bundle")
		if _, err := Create(ctx, l, bundleDir, lock); err != nil {
			t.Fatalf("Create rejected valid 0-byte+WAL layout: %v", err)
		}
		restoredDir := filepath.Join(root, "restored")
		restored, err := RestoreNew(ctx, bundleDir, restoredDir)
		if err != nil {
			t.Fatalf("RestoreNew rejected valid 0-byte+WAL bundle: %v", err)
		}
		if err := ValidateMatchingEvidence(ctx, restored); err != nil {
			t.Fatalf("ValidateMatchingEvidence rejected restored 0-byte+WAL layout: %v", err)
		}
	})

	t.Run("rejects 0-byte main database without non-empty regular WAL or with truncated main file", func(t *testing.T) {
		l, _, _ := zeroByteWALFixture(t)
		walCopy, err := os.ReadFile(l.DatabasePath + "-wal")
		if err != nil {
			t.Fatal(err)
		}

		// Absent WAL.
		if err := os.Remove(l.DatabasePath + "-wal"); err != nil {
			t.Fatal(err)
		}
		if err := ValidateMatchingEvidence(ctx, l); err == nil || !strings.Contains(err.Error(), "too small") {
			t.Fatalf("0-byte main without WAL error = %v, want too small", err)
		}

		// Empty 0-byte WAL.
		put(t, l.DatabasePath+"-wal", nil)
		if err := ValidateMatchingEvidence(ctx, l); err == nil || !strings.Contains(err.Error(), "too small") {
			t.Fatalf("0-byte main with 0-byte WAL error = %v, want too small", err)
		}

		// Header-only 32-byte WAL with zero committed frames.
		put(t, l.DatabasePath+"-wal", validEmptyWALHeaderBytes())
		if err := ValidateMatchingEvidence(ctx, l); err == nil {
			t.Fatalf("expected 0-byte main with 0-frame WAL header to fail")
		}

		// Symlinked WAL.
		outsideWAL := filepath.Join(t.TempDir(), "outside.wal")
		put(t, outsideWAL, walCopy)
		if err := os.Remove(l.DatabasePath + "-wal"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideWAL, l.DatabasePath+"-wal"); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := ValidateMatchingEvidence(ctx, l); err == nil || !strings.Contains(err.Error(), "WAL is not a regular file") {
			t.Fatalf("symlinked WAL error = %v, want WAL is not a regular file", err)
		}
		if err := os.Remove(l.DatabasePath + "-wal"); err != nil {
			t.Fatal(err)
		}

		// 1..99-byte truncated main file even with valid WAL.
		put(t, l.DatabasePath+"-wal", walCopy)
		put(t, l.DatabasePath, []byte("SQLite format 3\x00truncated"))
		if err := ValidateMatchingEvidence(ctx, l); err == nil || !strings.Contains(err.Error(), "too small") {
			t.Fatalf("truncated 1..99-byte main database error = %v, want too small", err)
		}
	})

	t.Run("enforces raw persisted CPA connection storage guard on 0-byte main database with WAL", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		l := Layout{
			DatabasePath:    filepath.Join(root, "usage.sqlite"),
			DataKeyPath:     filepath.Join(root, "data.key"),
			ArchivesPath:    filepath.Join(root, "usage-archives"),
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
		seedPath := filepath.Join(t.TempDir(), "seed.sqlite")
		seed, err := sql.Open("sqlite", seedPath)
		if err != nil {
			t.Fatal(err)
		}
		seed.SetMaxOpenConns(1)
		if _, err := seed.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at_ms INTEGER NOT NULL);"); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
		repo := setting.New(seed, protector)
		if err := repo.SaveBootstrapState(ctx, model.BootstrapState{ConnectionStorageMigrationVersion: 2}); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
		// Write unsealed plaintext managementKey under migration version 2.
		if _, err := seed.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('manager_config_v1', '{"cpaConnection":{"cpaBaseUrl":"http://127.0.0.1:8317","managementKey":"plaintext-secret"}}', 1)`); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
		walBytes, err := os.ReadFile(seedPath + "-wal")
		_ = seed.Close()
		if err != nil {
			t.Fatal(err)
		}
		put(t, l.DatabasePath, nil)
		put(t, l.DatabasePath+"-wal", walBytes)
		if err := os.MkdirAll(l.ArchivesPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ValidateMatchingEvidence(ctx, l); err == nil || !strings.Contains(err.Error(), "inspect persisted CPA connection storage") {
			t.Fatalf("expected raw storage guard failure on 0-byte+WAL database, got %v", err)
		}
	})
}

func TestMaintenanceBundleRejectsOverlappingScratchAndTMPDIR(t *testing.T) {
	ctx := context.Background()

	t.Run("ValidateMatchingEvidenceWithScratch rejects scratchDir and TMPDIR inside component roots", func(t *testing.T) {
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dbDir := filepath.Join(base, "db")
		keyDir := filepath.Join(base, "keys")
		cfgDir := filepath.Join(base, "cfg")
		archDir := filepath.Join(base, "archives")
		for _, dir := range []string{dbDir, keyDir, cfgDir, archDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		srcLayout, _, _ := fixture(t)
		for _, suffix := range []string{"", "-wal"} {
			if data, err := os.ReadFile(srcLayout.DatabasePath + suffix); err == nil {
				put(t, filepath.Join(dbDir, "usage.sqlite"+suffix), data)
			}
		}
		keyBytes, err := os.ReadFile(srcLayout.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(keyDir, "data.key"), keyBytes)
		put(t, filepath.Join(cfgDir, "manager.json"), []byte(`{"ok":true}`))

		l := Layout{
			DatabasePath:    filepath.Join(dbDir, "usage.sqlite"),
			DataKeyPath:     filepath.Join(keyDir, "data.key"),
			ConfigPath:      filepath.Join(cfgDir, "manager.json"),
			ArchivesPath:    archDir,
			RequireConfig:   true,
			RequireArchives: true,
		}

		for _, badScratch := range []string{
			dbDir,
			filepath.Join(dbDir, "nested"),
			keyDir,
			cfgDir,
			archDir,
		} {
			if err := ValidateMatchingEvidenceWithScratch(ctx, l, badScratch); err == nil {
				t.Fatalf("ValidateMatchingEvidenceWithScratch accepted scratch inside %s", badScratch)
			}
		}

		t.Setenv("TMPDIR", keyDir)
		t.Setenv("TMP", keyDir)
		t.Setenv("TEMP", keyDir)
		if err := ValidateMatchingEvidence(ctx, l); err == nil {
			t.Fatalf("ValidateMatchingEvidence accepted TMPDIR inside keyDir")
		}
	})

	t.Run("CreateWithScratch, RestoreNewWithScratch, and RecoverPublication reject overlapping scratch before staging", func(t *testing.T) {
		l, root, lock := fixture(t)
		bundleParent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bundleDir := filepath.Join(bundleParent, "bundle")

		for _, badScratch := range []string{
			root,
			l.ArchivesPath,
			bundleDir,
			filepath.Join(bundleDir, "sub"),
			operationRoot(bundleDir),
		} {
			if _, err := CreateWithScratch(ctx, l, bundleDir, lock, badScratch); err == nil {
				t.Fatalf("CreateWithScratch accepted overlapping scratch %s", badScratch)
			}
			noPublished(t, bundleParent, bundleDir)
		}

		if _, err := Create(ctx, l, bundleDir, lock); err != nil {
			t.Fatal(err)
		}

		restoreParent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		targetDir := filepath.Join(restoreParent, "restored")
		for _, badScratch := range []string{
			bundleDir,
			filepath.Join(bundleDir, "nested"),
			targetDir,
			operationRoot(targetDir),
		} {
			if _, err := RestoreNewWithScratch(ctx, bundleDir, targetDir, badScratch); err == nil {
				t.Fatalf("RestoreNewWithScratch accepted overlapping scratch %s", badScratch)
			}
			noPublished(t, restoreParent, targetDir)
		}

		// Simulate an unfinished publication with published target and verify
		// RecoverPublication rejects TMPDIR inside dest before staging.
		if _, err := RestoreNew(ctx, bundleDir, targetDir); err != nil {
			t.Fatal(err)
		}
		m, err := Verify(ctx, targetDir)
		if err != nil {
			t.Fatal(err)
		}
		rec := publication{
			dest: targetDir,
			root: operationRoot(targetDir),
			record: publicationRecord{
				Version:     1,
				Destination: filepath.Base(targetDir),
				ID:          "0123456789abcdef0123456789abcdef",
				State:       "preparing",
				Manifest:    &m,
			},
		}
		if err := os.MkdirAll(rec.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := rec.writeRecord(); err != nil {
			t.Fatal(err)
		}
		if _, err := RecoverPublicationWithScratch(ctx, targetDir, targetDir); err == nil {
			t.Fatalf("RecoverPublicationWithScratch accepted scratch inside targetDir")
		}
		if _, err := RecoverPublicationWithScratch(ctx, targetDir, rec.root); err == nil {
			t.Fatalf("RecoverPublicationWithScratch accepted scratch inside publication root")
		}
		state, err := RecoverPublication(ctx, targetDir)
		if err != nil || state != "committed" {
			t.Fatalf("RecoverPublication with valid default scratch = (%q, %v), want committed", state, err)
		}
	})
}
