package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// TestExternalConnectStrictInputAndTransactionalSettingsR8 verifies R8 #1:
//   - explicit caller-supplied BaseURL preserves /v0/management and rejects schemeless host:port
//   - legacy setup-only settings with PopSide="left" and /v0/management preserve collector fields
//     when canonicalized inside the ExternalConnect transaction
//   - stage validation precedes settings mutation and LoadManagerConfig/LoadSetup errors fail closed
//   - FencedExternalBaseURLForStore (with and without protector) and Verify agree on /v0/management drift.
func TestExternalConnectStrictInputAndTransactionalSettingsR8(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit /v0/management stays verbatim and explicit schemeless host:port is rejected", func(t *testing.T) {
		f := newVerifyFixture(t)
		f.reconcile(t)

		// Explicit schemeless host:port must be rejected.
		_, err := ExternalConnect(ctx, ExternalConnectOptions{
			TargetRoot:    f.target,
			BaseURL:       "127.0.0.1:8317",
			ManagementKey: externalTestKey,
		})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorExternalAuthorityUnavailable {
			t.Fatalf("ExternalConnect(schemeless) = (%v, %q), want %s", err, code, UpgradeErrorExternalAuthorityUnavailable)
		}

		// Explicit /v0/management URL must stay /v0/management end-to-end.
		const explicitPrefixed = "http://127.0.0.1:8317/v0/management"
		var observedProbeURL string
		connRes, err := ExternalConnect(ctx, ExternalConnectOptions{
			TargetRoot:    f.target,
			BaseURL:       explicitPrefixed,
			ManagementKey: externalTestKey,
			observeExternalVersion: func(_ context.Context, baseURL, _ string) (cpa.ExternalVersionReport, error) {
				observedProbeURL = baseURL
				return cpa.ClassifyExternalVersion("v8.0.55"), nil
			},
			readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
				return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
			},
			readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
				return cpa.V8ConfigObservation{BaseURL: explicitPrefixed, ClientKeyCount: 1}, nil
			},
			proveModelSurface: func(context.Context, string, string) error { return nil },
		})
		if err != nil {
			t.Fatalf("ExternalConnect(%q) failed: %v", explicitPrefixed, err)
		}
		if observedProbeURL != explicitPrefixed {
			t.Fatalf("observed probe URL = %q, want %q", observedProbeURL, explicitPrefixed)
		}
		if connRes.Stage != model.UpgradeStageExternalConnect {
			t.Fatalf("stage = %q, want external_connect", connRes.Stage)
		}

		f.handManagerConnection(t, explicitPrefixed, externalTestKey)

		db, err := managersqlite.Open(f.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		key, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		protector, err := security.NewProtector(key)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		op, opOK, err := upgradeoperation.New(db).Get(ctx)
		if err != nil || !opOK {
			_ = db.Close()
			t.Fatalf("load op: ok=%v err=%v", opOK, err)
		}
		binding, bindOK, err := upgradeoperation.New(db).ExternalBinding(ctx)
		if err != nil || !bindOK {
			_ = db.Close()
			t.Fatalf("load binding: ok=%v err=%v", bindOK, err)
		}
		if binding.BaseURL != explicitPrefixed {
			_ = db.Close()
			t.Fatalf("binding.BaseURL = %q, want %q", binding.BaseURL, explicitPrefixed)
		}
		if got := FencedExternalBaseURLForStore(ctx, db, protector, op, binding, true); got != explicitPrefixed {
			_ = db.Close()
			t.Fatalf("FencedExternalBaseURLForStore(with protector) = %q, want %q", got, explicitPrefixed)
		}
		if got := FencedExternalBaseURLForStore(ctx, db, nil, op, binding, true); got != explicitPrefixed {
			_ = db.Close()
			t.Fatalf("FencedExternalBaseURLForStore(nil protector) = %q, want %q", got, explicitPrefixed)
		}
		_ = db.Close()
		if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
			TargetRoot:      f.target,
			ControlBasePath: "/cpamp-admin",
		}); err != nil {
			t.Fatalf("ConfigureAccessRouting failed: %v", err)
		}

		verRes, err := Verify(ctx, VerifyOptions{
			LegacyRoot: f.volume.root,
			TargetRoot: f.target,
			observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v8.0.55"), nil
			},
			readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
				return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
			},
			readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
				return cpa.V8ConfigObservation{BaseURL: explicitPrefixed, ClientKeyCount: 1}, nil
			},
			proveModelSurface: func(context.Context, string, string) error { return nil },
		})
		if err != nil || !verRes.Complete {
			t.Fatalf("Verify(explicit /v0/management) = (%+v, %v), want complete", verRes, err)
		}
	})

	t.Run("setup-only legacy store with PopSide=left and /v0/management preserves collector settings inside tx", func(t *testing.T) {
		f := newVerifyFixture(t)
		f.reconcile(t)

		key, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(key)
		if err != nil {
			t.Fatal(err)
		}
		db, err := managersqlite.Open(f.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM settings WHERE key = 'manager_config_v1'`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db, protector).SaveSetup(ctx, model.Setup{
			CPAUpstreamURL: "http://127.0.0.1:8317/v0/management",
			ManagementKey:  externalTestKey,
			Queue:          "legacy:queue",
			PopSide:        "left",
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		_ = db.Close()

		_, err = ExternalConnect(ctx, ExternalConnectOptions{
			TargetRoot:    f.target,
			BaseURL:       "http://127.0.0.1:8317",
			ManagementKey: externalTestKey,
			observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v8.0.55"), nil
			},
			readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
				return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
			},
			readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
				return cpa.V8ConfigObservation{BaseURL: "http://127.0.0.1:8317", ClientKeyCount: 1}, nil
			},
			proveModelSurface: func(context.Context, string, string) error { return nil },
		})
		if err != nil {
			t.Fatalf("ExternalConnect failed: %v", err)
		}

		db2, err := managersqlite.Open(f.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db2.Close() }()
		st := store.New(db2, protector)
		cfg, ok, err := st.LoadManagerConfig(ctx)
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (ok=%v, err=%v)", ok, err)
		}
		if cfg.CPAConnection.CPABaseURL != "http://127.0.0.1:8317" {
			t.Fatalf("CPABaseURL = %q, want http://127.0.0.1:8317", cfg.CPAConnection.CPABaseURL)
		}
		if cfg.Collector.PopSide != "left" || cfg.Collector.Queue != "legacy:queue" {
			t.Fatalf("Collector = %+v, want PopSide=left Queue=legacy:queue", cfg.Collector)
		}
		setup, setupOK, err := st.LoadSetup(ctx)
		if err != nil || !setupOK {
			t.Fatalf("LoadSetup = (ok=%v, err=%v)", setupOK, err)
		}
		if setup.CPAUpstreamURL != "http://127.0.0.1:8317" || setup.PopSide != "left" || setup.Queue != "legacy:queue" {
			t.Fatalf("Setup = %+v, want canonical URL + preserved collector fields", setup)
		}
	})

	t.Run("stage validation precedes settings canonicalization and corrupted settings fail closed", func(t *testing.T) {
		// 1. Before reconcile, ExternalConnect is ineligible and must never mutate legacy settings.
		fUnreconciled := newVerifyFixture(t)
		key, _, err := security.LoadOrCreateDataKey("", fUnreconciled.paths.DataKey)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(key)
		if err != nil {
			t.Fatal(err)
		}
		db, err := managersqlite.Open(fUnreconciled.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if err := setting.New(db, protector).SaveSetup(ctx, model.Setup{
			CPAUpstreamURL: "http://127.0.0.1:8317/v0/management",
			ManagementKey:  externalTestKey,
			PopSide:        "left",
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		_ = db.Close()

		_, err = ExternalConnect(ctx, ExternalConnectOptions{
			TargetRoot:    fUnreconciled.target,
			BaseURL:       "http://127.0.0.1:8317",
			ManagementKey: externalTestKey,
		})
		if err == nil {
			t.Fatal("expected ExternalConnect before reconcile to fail")
		}
		dbAfter, err := managersqlite.Open(fUnreconciled.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		setupAfter, setupOK, err := store.New(dbAfter, protector).LoadSetup(ctx)
		_ = dbAfter.Close()
		if err != nil || !setupOK || setupAfter.CPAUpstreamURL != "http://127.0.0.1:8317/v0/management" {
			t.Fatalf("ineligible ExternalConnect mutated setup: ok=%v err=%v setup=%+v", setupOK, err, setupAfter)
		}

		// 2. Corrupted settings JSON fails closed and rolls back the ExternalConnect transaction.
		fCorrupt := newVerifyFixture(t)
		fCorrupt.reconcile(t)
		dbCorrupt, err := managersqlite.Open(fCorrupt.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dbCorrupt.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at_ms) VALUES ('setup', '{not-valid-json', 1000)`); err != nil {
			_ = dbCorrupt.Close()
			t.Fatal(err)
		}
		_ = dbCorrupt.Close()

		_, err = ExternalConnect(ctx, ExternalConnectOptions{
			TargetRoot:    fCorrupt.target,
			BaseURL:       "http://127.0.0.1:8317",
			ManagementKey: externalTestKey,
			observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v8.0.55"), nil
			},
			readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
				return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
			},
			readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
				return cpa.V8ConfigObservation{BaseURL: "http://127.0.0.1:8317", ClientKeyCount: 1}, nil
			},
			proveModelSurface: func(context.Context, string, string) error { return nil },
		})
		if err == nil {
			t.Fatal("expected corrupted setup JSON to fail ExternalConnect, got nil")
		}
		dbVerifyRollback, err := managersqlite.Open(fCorrupt.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		opRollback, _, err := upgradeoperation.New(dbVerifyRollback).Get(ctx)
		_, bindFound, bindErr := upgradeoperation.New(dbVerifyRollback).ExternalBinding(ctx)
		_ = dbVerifyRollback.Close()
		if err != nil || bindErr != nil {
			t.Fatalf("inspect rolled-back tx: opErr=%v bindErr=%v", err, bindErr)
		}
		if opRollback.Stage == model.UpgradeStageExternalConnect || bindFound {
			t.Fatalf("failed settings canonicalization committed ExternalConnect: stage=%q bindFound=%v", opRollback.Stage, bindFound)
		}
	})

	t.Run("FencedExternalBaseURLForStore with and without protector and Verify agree on /v0/management drift", func(t *testing.T) {
		f := newExternalVerifyFixture(t)
		// Complete Verify once against canonical http://127.0.0.1:8317.
		verRes, err := f.verify(t)
		if err != nil || !verRes.Complete {
			t.Fatalf("initial verify = (%+v, %v)", verRes, err)
		}

		// Rebind stored settings post-upgrade to /v0/management.
		f.handManagerConnection(t, "http://127.0.0.1:8317/v0/management", externalTestKey)

		key, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(key)
		if err != nil {
			t.Fatal(err)
		}
		db, err := managersqlite.Open(f.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		op, _, err := upgradeoperation.New(db).Get(ctx)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		binding, _, err := upgradeoperation.New(db).ExternalBinding(ctx)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		gotWithProtector := FencedExternalBaseURLForStore(ctx, db, protector, op, binding, true)
		gotNilProtector := FencedExternalBaseURLForStore(ctx, db, nil, op, binding, true)
		_ = db.Close()
		if gotWithProtector != "" || gotNilProtector != "" {
			t.Fatalf("FencedExternalBaseURLForStore on /v0/management drift = (withProtector=%q, nilProtector=%q), want both empty",
				gotWithProtector, gotNilProtector)
		}

		// Completed Verify replay must also detect the /v0/management drift.
		if _, err := f.verify(t); err == nil {
			t.Fatal("expected Verify on completed store with /v0/management drift to fail, got nil")
		}
	})
}

func buildZeroByteWALLegacyVolumeWithRolledBackSpill(t *testing.T, truncateTrailing bool) legacyVolume {
	t.Helper()
	vol := newZeroByteWALLegacyVolume(t, "")
	// Re-build -wal from a live SQLite connection that executes Tx1 commit -> Tx2 spill + ROLLBACK -> Tx3 commit.
	stageDir := canonicalTempDir(t)
	seedPath := filepath.Join(stageDir, "spill.sqlite")
	seed, err := sql.Open("sqlite", seedPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.SetMaxOpenConns(1)
	if _, err := seed.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; PRAGMA cache_size=2;"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	// Attach a checkpointed clone of vol to copy schema + rows into Tx1.
	cloneDir := canonicalTempDir(t)
	cloneDB := filepath.Join(cloneDir, "clone.sqlite")
	rawWAL, err := os.ReadFile(vol.databasePath() + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(cloneDB, nil, 0o600); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(cloneDB+"-wal", rawWAL, 0o600); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	page1, err := extractCommittedPage1FromWAL(cloneDB + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(cloneDB, page1, 0o600); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	ckpt, err := sql.Open("sqlite", cloneDB)
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if _, err := ckpt.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = ckpt.Close()
		_ = seed.Close()
		t.Fatal(err)
	}
	_ = ckpt.Close()

	escapedClone := strings.ReplaceAll(cloneDB, "'", "''")
	if _, err := seed.Exec(fmt.Sprintf("ATTACH DATABASE '%s' AS tpl;", escapedClone)); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	rows, err := seed.Query(`SELECT sql, name FROM tpl.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'usage_monitoring_event_search_v1_%' ORDER BY rowid`)
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	type tblDef struct{ ddl, name string }
	var tables []tblDef
	for rows.Next() {
		var d tblDef
		if err := rows.Scan(&d.ddl, &d.name); err != nil {
			_ = rows.Close()
			_ = seed.Close()
			t.Fatal(err)
		}
		tables = append(tables, d)
	}
	_ = rows.Close()
	for _, tbl := range tables {
		if _, err := seed.Exec(tbl.ddl); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
		quoted := `"` + strings.ReplaceAll(tbl.name, `"`, `""`) + `"`
		if _, err := seed.Exec(fmt.Sprintf("INSERT INTO main.%s SELECT * FROM tpl.%s", quoted, quoted)); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec("DETACH DATABASE tpl"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	sizeAfterTx1, err := os.Stat(seedPath + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	// Tx2: spill uncommitted frames into -wal, then ROLLBACK without adding any new schema table.
	if _, err := seed.Exec("BEGIN IMMEDIATE"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	padding := strings.Repeat("rolled-back-spill-data-", 250)
	for i := range 25 {
		if _, err := seed.Exec(
			`INSERT INTO settings (key, value, updated_at_ms) VALUES (?, ?, 2000)`,
			fmt.Sprintf("spill_temp_%02d", i), padding,
		); err != nil {
			_ = seed.Close()
			t.Fatal(err)
		}
	}
	sizeDuringTx2, err := os.Stat(seedPath + "-wal")
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if sizeDuringTx2.Size() <= sizeAfterTx1.Size() {
		_ = seed.Close()
		t.Fatalf("expected Tx2 spill to grow WAL (%d <= %d)", sizeDuringTx2.Size(), sizeAfterTx1.Size())
	}
	if _, err := seed.Exec("ROLLBACK"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	// Tx3: commit 1 small update so rolled-back frames remain after Tx3's commit with checksum mismatch.
	if _, err := seed.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at_ms) VALUES ('tx3_marker', 'ok', 3000)`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}

	walBytes, err := os.ReadFile(seedPath + "-wal")
	_ = seed.Close()
	if err != nil {
		t.Fatal(err)
	}

	if truncateTrailing {
		pageSize := int(binary.BigEndian.Uint32(walBytes[8:12]))
		if pageSize == 1 {
			pageSize = 65536
		}
		var trailingHdr [24]byte
		binary.BigEndian.PutUint32(trailingHdr[0:4], 2)
		copy(trailingHdr[8:16], walBytes[16:24])
		walBytes = append(walBytes, trailingHdr[:]...)
		walBytes = append(walBytes, bytes.Repeat([]byte{0x3c}, pageSize/4)...)
	}

	if err := os.WriteFile(vol.databasePath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vol.databasePath()+"-wal", walBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(vol.databasePath() + "-shm")
	return vol
}

// TestUpgradeZeroByteWALRolledBackSpillAndTruncatedTailR8 verifies R8 #3 in service/upgrade.
func TestUpgradeZeroByteWALRolledBackSpillAndTruncatedTailR8(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name             string
		truncateTrailing bool
	}{
		{name: "rolled-back spill followed by smaller commit", truncateTrailing: false},
		{name: "rolled-back spill plus truncated uncommitted trailing frame", truncateTrailing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vol := buildZeroByteWALLegacyVolumeWithRolledBackSpill(t, tc.truncateTrailing)
			target := canonicalTempDir(t)
			bundle := DefaultBundleDirectory(target)

			report, err := Preflight(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetDir:       target,
				BundleDir:       bundle,
			})
			if err != nil {
				t.Fatalf("Preflight returned error: %v", err)
			}
			if !report.Ready {
				t.Fatalf("Preflight Ready=false blocked=%v checks=%+v", report.Blocked, report.Checks)
			}

			srcLock := vol.lock(t)
			res, err := CopyIn(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetRoot:      target,
				BundleDir:       bundle,
				Lock:            srcLock,
			})
			if err != nil {
				t.Fatalf("CopyIn failed: %v", err)
			}
			if res.Stage != model.UpgradeStageCopyIn {
				t.Fatalf("stage = %q, want copy_in", res.Stage)
			}

			tgtLock, err := processlock.Acquire(TargetDatabasePath(target))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tgtLock.Close() }()
			rec, err := Reconcile(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetRoot:      target,
				BundleDir:       bundle,
				Lock:            srcLock,
				TargetLock:      tgtLock,
			})
			if err != nil {
				t.Fatalf("Reconcile failed: %v", err)
			}
			if !rec.Recorded {
				t.Fatalf("expected Recorded=true, got %+v", rec)
			}
		})
	}
}

// TestNonCanonicalBundleInsideTargetRejectedR8 verifies R8 #4 across Preflight,
// CopyIn, Reconcile, and ImportCPA while preserving <targetRoot>/upgrade-backup.
func TestNonCanonicalBundleInsideTargetRejectedR8(t *testing.T) {
	ctx := context.Background()
	vol := newLegacyVolume(t)
	target := canonicalTempDir(t)

	// Canonical <targetRoot>/upgrade-backup remains accepted.
	canonicalBundle := DefaultBundleDirectory(target)
	if report, err := Preflight(ctx, Options{
		LegacyRoot:      vol.root,
		DeclaredVersion: bootstrap.PlannedDirectRelease,
		TargetDir:       target,
		BundleDir:       canonicalBundle,
	}); err != nil || !report.Ready {
		t.Fatalf("Preflight rejected canonical upgrade-backup: ready=%v err=%v", report.Ready, err)
	}

	nonCanonicalSubdirs := []string{
		filepath.Join(target, "custom-backup"),
		filepath.Join(target, archivesDirName),
		filepath.Join(target, copyInWorkDirectoryName, "sub"),
	}
	for _, badBundle := range nonCanonicalSubdirs {
		t.Run(filepath.Base(badBundle), func(t *testing.T) {
			if _, err := Preflight(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetDir:       target,
				BundleDir:       badBundle,
			}); err == nil || !strings.Contains(err.Error(), "canonical upgrade-backup") {
				t.Fatalf("Preflight(%s) = %v, want canonical upgrade-backup refusal", badBundle, err)
			}

			lock := vol.lock(t)
			_, copyErr := CopyIn(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetRoot:      target,
				BundleDir:       badBundle,
				Lock:            lock,
			})
			_ = lock.Close()
			if copyErr == nil || !strings.Contains(copyErr.Error(), "canonical upgrade-backup") {
				t.Fatalf("CopyIn(%s) = %v, want canonical upgrade-backup refusal", badBundle, copyErr)
			}

			srcLock := vol.lock(t)
			_, recErr := Reconcile(ctx, Options{
				LegacyRoot:      vol.root,
				DeclaredVersion: bootstrap.PlannedDirectRelease,
				TargetRoot:      target,
				BundleDir:       badBundle,
				Lock:            srcLock,
			})
			_ = srcLock.Close()
			if recErr == nil || !strings.Contains(recErr.Error(), "canonical upgrade-backup") {
				t.Fatalf("Reconcile(%s) = %v, want canonical upgrade-backup refusal", badBundle, recErr)
			}

			// ImportCPA both with LegacyRoot and without LegacyRoot.
			for _, legacyArg := range []string{vol.root, ""} {
				pluginsDisabled := false
				_, impErr := ImportCPA(ctx, CPAImportOptions{
					LegacyRoot:     legacyArg,
					TargetRoot:     target,
					BundleDir:      badBundle,
					Runtime:        newFakeImportRuntime(model.RuntimeStateOffline),
					DestinationURL: "http://127.0.0.1:28317",
					PluginsEnabled: &pluginsDisabled,
				})
				if impErr == nil || !strings.Contains(impErr.Error(), "canonical upgrade-backup") {
					t.Fatalf("ImportCPA(legacy=%q, bundle=%s) = %v, want canonical upgrade-backup refusal", legacyArg, badBundle, impErr)
				}
			}
		})
	}
}

// TestCopyInPostMaterializationCancelRollsBackCleanlyR8 verifies R8 #5:
// cancellation during staged compatibility check rolls back materialized members
// via context.WithoutCancel(ctx) and returns context.Canceled rather than upgrade_target_conflict.
func TestCopyInPostMaterializationCancelRollsBackCleanlyR8(t *testing.T) {
	vol := newLegacyVolume(t)
	target := canonicalTempDir(t)
	bundle := DefaultBundleDirectory(target)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prevHook := afterSnapshotCopy
	t.Cleanup(func() { afterSnapshotCopy = prevHook })
	snapshotCount := 0
	afterSnapshotCopy = func() {
		snapshotCount++
		if snapshotCount == 2 {
			// Cancel during the post-materialization staged compatibility snapshot.
			cancel()
		}
	}

	lock := vol.lock(t)
	defer func() { _ = lock.Close() }()

	_, err := CopyIn(ctx, Options{
		LegacyRoot:      vol.root,
		DeclaredVersion: bootstrap.PlannedDirectRelease,
		TargetRoot:      target,
		BundleDir:       bundle,
		Lock:            lock,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyIn error = %v, want context.Canceled", err)
	}
	if code, ok := UpgradeErrorCodeOf(err); ok && code == UpgradeErrorTargetConflict {
		t.Fatalf("CopyIn returned %s on cancellation instead of clean rollback: %v", code, err)
	}

	// Verify materialized members and work directory were rolled back cleanly.
	for _, member := range []string{
		filepath.Join(target, databaseFileName),
		filepath.Join(target, dataKeyFileName),
		filepath.Join(target, archivesDirName),
		filepath.Join(target, copyInWorkDirectoryName),
	} {
		if _, statErr := os.Lstat(member); !os.IsNotExist(statErr) {
			t.Fatalf("canceled CopyIn left %s behind (err=%v)", member, statErr)
		}
	}

	// Reset hook and confirm a fresh CopyIn succeeds without target conflict.
	afterSnapshotCopy = prevHook
	res, retryErr := CopyIn(context.Background(), Options{
		LegacyRoot:      vol.root,
		DeclaredVersion: bootstrap.PlannedDirectRelease,
		TargetRoot:      target,
		BundleDir:       bundle,
		Lock:            lock,
	})
	if retryErr != nil {
		t.Fatalf("retry CopyIn after canceled rollback failed: %v", retryErr)
	}
	if res.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("retry stage = %q, want copy_in", res.Stage)
	}
}

// TestRootScratchAndCanonicalizationHardeningR8 verifies R8 #6 and R8 #9 (compareStores).
func TestRootScratchAndCanonicalizationHardeningR8(t *testing.T) {
	ctx := context.Background()

	t.Run("relative --legacy . resolves sibling cpa directory and ConfiguredLegacyRoots are enforced", func(t *testing.T) {
		parent := canonicalTempDir(t)
		legacyDir := filepath.Join(parent, "legacy-v1")
		siblingCPA := filepath.Join(parent, cpaSiblingDirectoryName)
		if err := os.MkdirAll(legacyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(siblingCPA, 0o700); err != nil {
			t.Fatal(err)
		}

		origWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(legacyDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(origWD) })

		cpaDir, _ := ResolveCPAImportRoots(".", "", "")
		if canonicalPath(cpaDir) != canonicalPath(siblingCPA) {
			t.Fatalf("ResolveCPAImportRoots(\".\") = %q, want sibling %q", cpaDir, siblingCPA)
		}
		if err := RefuseCPAImportOverlappingRoots(".", "", "", filepath.Join(siblingCPA, "nested"), ""); err == nil {
			t.Fatal("expected RefuseCPAImportOverlappingRoots(\".\") to refuse target inside sibling cpa dir")
		}

		// ConfiguredLegacyRoots enforced even when legacyRoot argument is empty.
		t.Setenv("CPAMP_UPGRADE_LEGACY_ROOT", legacyDir)
		if err := RefuseCPAImportOverlappingRoots("", "", "", filepath.Join(legacyDir, "nested-target"), ""); err == nil {
			t.Fatal("expected RefuseCPAImportOverlappingRoots to enforce CPAMP_UPGRADE_LEGACY_ROOT")
		}
	})

	t.Run("RequireActionStage enforces CPAMP_UPGRADE_LEGACY_ROOT and CPAMP_UPGRADE_BUNDLE_DIR", func(t *testing.T) {
		f := newVerifyFixture(t)
		cfgLegacy := canonicalTempDir(t)
		cfgBundle := canonicalTempDir(t)
		t.Setenv("CPAMP_UPGRADE_LEGACY_ROOT", cfgLegacy)
		t.Setenv("CPAMP_UPGRADE_BUNDLE_DIR", cfgBundle)

		scratchInLegacy := filepath.Join(cfgLegacy, "scratch")
		if err := os.MkdirAll(scratchInLegacy, 0o700); err != nil {
			t.Fatal(err)
		}
		scratchInBundle := filepath.Join(cfgBundle, "scratch")
		if err := os.MkdirAll(scratchInBundle, 0o700); err != nil {
			t.Fatal(err)
		}

		t.Setenv("TMPDIR", scratchInLegacy)
		errLegacy := RequireActionStage(ctx, f.databasePath(), "reconcile", nil)
		if code, ok := UpgradeErrorCodeOf(errLegacy); !ok || code != UpgradeErrorSnapshotFailed {
			t.Fatalf("RequireActionStage(TMPDIR inside CPAMP_UPGRADE_LEGACY_ROOT) = (%v, %q), want %s",
				errLegacy, code, UpgradeErrorSnapshotFailed)
		}

		t.Setenv("TMPDIR", scratchInBundle)
		errBundle := RequireActionStage(ctx, f.databasePath(), "reconcile", nil)
		if code, ok := UpgradeErrorCodeOf(errBundle); !ok || code != UpgradeErrorSnapshotFailed {
			t.Fatalf("RequireActionStage(TMPDIR inside CPAMP_UPGRADE_BUNDLE_DIR) = (%v, %q), want %s",
				errBundle, code, UpgradeErrorSnapshotFailed)
		}
	})

	t.Run("dangling symlink --bundle and --target-dir, regular-file --target-dir, and invalid explicit --scratch-dir fail closed", func(t *testing.T) {
		vol := newLegacyVolume(t)
		dir := canonicalTempDir(t)

		danglingTarget := filepath.Join(dir, "dangling-target")
		if err := os.Symlink(filepath.Join(vol.root, "nonexistent-target"), danglingTarget); err != nil {
			t.Fatal(err)
		}
		if _, err := Preflight(ctx, Options{
			LegacyRoot: vol.root,
			TargetDir:  danglingTarget,
		}); err == nil {
			t.Fatal("expected Preflight to refuse dangling symlink --target-dir")
		}

		danglingBundle := filepath.Join(dir, "dangling-bundle")
		if err := os.Symlink(filepath.Join(vol.root, "nonexistent-bundle"), danglingBundle); err != nil {
			t.Fatal(err)
		}
		if _, err := Preflight(ctx, Options{
			LegacyRoot: vol.root,
			TargetDir:  canonicalTempDir(t),
			BundleDir:  danglingBundle,
		}); err == nil {
			t.Fatal("expected Preflight to refuse dangling symlink --bundle")
		}
		if _, err := Preflight(ctx, Options{
			LegacyRoot: vol.root,
			BundleDir:  danglingBundle,
		}); err == nil {
			t.Fatal("expected Preflight without TargetDir to refuse dangling symlink --bundle")
		}

		regularFileTarget := filepath.Join(dir, "regular-target-file")
		if err := os.WriteFile(regularFileTarget, []byte("not-a-dir"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Preflight(ctx, Options{
			LegacyRoot: vol.root,
			TargetDir:  regularFileTarget,
		}); err == nil {
			t.Fatal("expected Preflight to refuse regular-file --target-dir")
		}

		// nearestExistingDir must skip regular files and return the enclosing directory.
		if got := nearestExistingDir(regularFileTarget); canonicalPath(got) != canonicalPath(dir) {
			t.Fatalf("nearestExistingDir(regularFile) = %q, want parent directory %q", got, dir)
		}

		// Nonexistent, regular-file, and dangling-symlink explicit --scratch-dir must fail closed.
		nonexistentScratch := filepath.Join(dir, "missing-scratch")
		if err := ValidateScratchDir(nonexistentScratch, vol.root, canonicalTempDir(t)); !errors.Is(err, ErrSnapshotFailed) {
			t.Fatalf("ValidateScratchDir(nonexistent) = %v, want ErrSnapshotFailed", err)
		}
		if err := ValidateScratchDir(regularFileTarget, vol.root, canonicalTempDir(t)); !errors.Is(err, ErrSnapshotFailed) {
			t.Fatalf("ValidateScratchDir(regular file) = %v, want ErrSnapshotFailed", err)
		}
		if err := ValidateScratchDir(danglingTarget, vol.root, canonicalTempDir(t)); !errors.Is(err, ErrSnapshotFailed) {
			t.Fatalf("ValidateScratchDir(dangling symlink) = %v, want ErrSnapshotFailed", err)
		}

		repMissingScratch, err := Preflight(ctx, Options{
			LegacyRoot:      vol.root,
			DeclaredVersion: bootstrap.PlannedDirectRelease,
			ScratchDir:      nonexistentScratch,
		})
		if err != nil {
			t.Fatalf("Preflight(nonexistent scratch) returned error: %v", err)
		}
		if repMissingScratch.Ready || !slices.Contains(repMissingScratch.Blocked, CodeSnapshotFailed) {
			t.Fatalf("Preflight(nonexistent scratch) Ready=%v Blocked=%v, want %s",
				repMissingScratch.Ready, repMissingScratch.Blocked, CodeSnapshotFailed)
		}
	})

	t.Run("compareStores maps OpenSnapshot errors to typed snapshot and ownership codes", func(t *testing.T) {
		vol := newLegacyVolume(t)
		outsideDB := filepath.Join(canonicalTempDir(t), "outside.sqlite")
		if err := os.WriteFile(outsideDB, []byte("sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(vol.databasePath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideDB, vol.databasePath()); err != nil {
			t.Fatal(err)
		}

		_, _, err := compareStores(ctx, vol.root, sideObservation{}, canonicalTempDir(t))
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCode(CodeSourceSymlinked) {
			t.Fatalf("compareStores(symlinked db) = (%v, %q), want %s", err, code, CodeSourceSymlinked)
		}

		if runtime.GOOS != "windows" && os.Geteuid() != 0 {
			vol2 := newLegacyVolume(t)
			roScratch := canonicalTempDir(t)
			if err := os.Chmod(roScratch, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(roScratch, 0o700) })

			_, _, snapErr := compareStores(ctx, vol2.root, sideObservation{}, roScratch)
			if code, ok := UpgradeErrorCodeOf(snapErr); !ok || code != UpgradeErrorSnapshotFailed {
				t.Fatalf("compareStores(read-only scratch) = (%v, %q), want %s", snapErr, code, UpgradeErrorSnapshotFailed)
			}
		}
	})
}

// TestExternalConnectCanonicalizesPrefixedLegacySetupR9 verifies R9 #1:
// a setup-only plaintext-key legacy store with an explicit /v0/management URL
// and PopSide="left" is canonicalized, sealed into manager_config_v1 + setup,
// and given a cpa_connection_revision_v1 when ExternalConnect receives the
// matching caller-supplied /v0/management URL.
func TestExternalConnectCanonicalizesPrefixedLegacySetupR9(t *testing.T) {
	ctx := context.Background()
	f := newVerifyFixture(t)
	f.reconcile(t)

	const (
		rawPrefixed       = "https://EXAMPLE.COM:443/v0/management"
		canonicalPrefixed = "https://example.com/v0/management"
	)
	db, err := managersqlite.Open(f.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM settings WHERE key IN ('manager_config_v1', 'cpa_connection_revision_v1')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := setting.New(db).SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: rawPrefixed,
		ManagementKey:  externalTestKey,
		Queue:          "legacy:queue",
		PopSide:        "left",
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	var observedProbeURL string
	res, err := ExternalConnect(ctx, ExternalConnectOptions{
		TargetRoot:    f.target,
		BaseURL:       rawPrefixed,
		ManagementKey: externalTestKey,
		observeExternalVersion: func(_ context.Context, baseURL, _ string) (cpa.ExternalVersionReport, error) {
			observedProbeURL = baseURL
			return cpa.ClassifyExternalVersion("v8.0.55"), nil
		},
		readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("c", 64)}, nil
		},
		readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			return cpa.V8ConfigObservation{BaseURL: canonicalPrefixed, ClientKeyCount: 1}, nil
		},
		proveModelSurface: func(context.Context, string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("ExternalConnect(%q) failed: %v", rawPrefixed, err)
	}
	if observedProbeURL != canonicalPrefixed {
		t.Fatalf("observed probe URL = %q, want %q", observedProbeURL, canonicalPrefixed)
	}
	if res.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("stage = %q, want external_connect", res.Stage)
	}

	db, err = managersqlite.Open(f.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rawKey, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := setting.New(db)
	rawMgr, ok, err := repo.LoadManagerConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig raw: ok=%v err=%v", ok, err)
	}
	if rawMgr.CPAConnection.CPABaseURL != canonicalPrefixed {
		t.Fatalf("manager_config_v1 CPABaseURL = %q, want %q", rawMgr.CPAConnection.CPABaseURL, canonicalPrefixed)
	}
	if !security.IsValidProtectedEnvelope(rawMgr.CPAConnection.ManagementKey) {
		t.Fatalf("manager_config_v1 ManagementKey was not sealed: %q", rawMgr.CPAConnection.ManagementKey)
	}
	if rawMgr.Collector.PopSide != "left" || rawMgr.Collector.Queue != "legacy:queue" {
		t.Fatalf("collector settings lost: popSide=%q queue=%q", rawMgr.Collector.PopSide, rawMgr.Collector.Queue)
	}
	rawSetup, setupOK, err := repo.LoadSetup(ctx)
	if err != nil || !setupOK {
		t.Fatalf("LoadSetup raw: ok=%v err=%v", setupOK, err)
	}
	if rawSetup.CPAUpstreamURL != canonicalPrefixed {
		t.Fatalf("setup CPAUpstreamURL = %q, want %q", rawSetup.CPAUpstreamURL, canonicalPrefixed)
	}
	if !security.IsValidProtectedEnvelope(rawSetup.ManagementKey) {
		t.Fatalf("setup ManagementKey was not sealed: %q", rawSetup.ManagementKey)
	}
	protectedRepo := setting.New(db, protector)
	decryptedMgr, _, err := protectedRepo.LoadManagerConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if decryptedMgr.CPAConnection.ManagementKey != externalTestKey {
		t.Fatalf("decrypted manager key = %q, want %q", decryptedMgr.CPAConnection.ManagementKey, externalTestKey)
	}
	var rev string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'cpa_connection_revision_v1'`).Scan(&rev); err != nil {
		t.Fatalf("expected cpa_connection_revision_v1 row: %v", err)
	}
	if !isValidConnectionRevisionHex(rev) {
		t.Fatalf("cpa_connection_revision_v1 = %q, want valid 64-hex revision", rev)
	}
}

// TestVerifyManagerCredentialPrioritizesSourceTimeoutOverUnknownRootR9 verifies R9 #4:
// when a mount lookup times out before naming the configured source root
// (SourceKnown=false, SourceTimedOut=true), VerifyManagerCredential reports
// ErrSnapshotRootTimedOut rather than ErrSnapshotRootUnknown.
func TestVerifyManagerCredentialPrioritizesSourceTimeoutOverUnknownRootR9(t *testing.T) {
	vol := newLegacyVolume(t)
	err := VerifyManagerCredential(context.Background(), vol.databasePath(), CredentialSnapshotRoots{
		Target:         vol.root,
		SourceKnown:    false,
		SourceTimedOut: true,
		PreserveTarget: true,
	}, externalTestKey)
	if !errors.Is(err, ErrSnapshotRootTimedOut) {
		t.Fatalf("VerifyManagerCredential(SourceKnown=false, SourceTimedOut=true) = %v, want ErrSnapshotRootTimedOut", err)
	}
	if errors.Is(err, ErrSnapshotRootUnknown) {
		t.Fatalf("VerifyManagerCredential returned ErrSnapshotRootUnknown instead of ErrSnapshotRootTimedOut: %v", err)
	}
}
