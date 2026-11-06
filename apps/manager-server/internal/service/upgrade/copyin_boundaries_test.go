package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

func requireTargetConflict(t *testing.T, err error) {
	t.Helper()
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorTargetConflict || !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("target refusal = %q/%t/%v, want upgrade_target_conflict", code, ok, err)
	}
}

func plantUnexpectedMember(t *testing.T, path, kind string) {
	t.Helper()
	if kind == "symlink" {
		// A dangling reference must still count as a present target member.
		if err := os.Symlink(filepath.Join(canonicalTempDir(t), "absent"), path); err != nil {
			t.Fatal(err)
		}
		return
	}
	var data []byte
	if kind == "nonempty" {
		data = []byte("preserved unexpected member")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopyInPublicationPreservesArrivingMembers(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		for _, kind := range []string{"empty", "nonempty", "symlink"} {
			t.Run(databaseFileName+suffix+"/"+kind, func(t *testing.T) {
				volume := newLegacyVolume(t)
				target := canonicalTempDir(t)
				options := copyInOptions(volume, target)
				options.Lock = volume.lock(t)
				var before map[string]string
				options.checkpoint = func(phase string) error {
					if phase == "rename-checked" {
						plantUnexpectedMember(t, filepath.Join(target, databaseFileName+suffix), kind)
						before = fingerprintTree(t, target)
					}
					return nil
				}
				_, err := CopyIn(context.Background(), options)
				requireTargetConflict(t, err)
				if before == nil {
					t.Fatal("publication boundary was not exercised")
				}
				assertTreeUnchanged(t, "refused publication", before, fingerprintTree(t, target))
				if suffix != "" {
					if _, err := os.Lstat(filepath.Join(target, databaseFileName)); !os.IsNotExist(err) {
						t.Fatalf("database published beside unexpected sidecar: %v", err)
					}
				}
				if _, found, err := copyinmarker.Read(target); err != nil || !found {
					t.Fatalf("publication lost marker: %t/%v", found, err)
				}
			})
		}
	}
}

func TestUpgradeEntriesRequireCopyInRecovery(t *testing.T) {
	cases := []struct{ name, suffix, kind string }{{name: "published"}}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		for _, kind := range []string{"empty", "nonempty", "symlink"} {
			cases = append(cases, struct{ name, suffix, kind string }{suffix + "/" + kind, suffix, kind})
		}
	}
	for _, name := range []string{"materializing", "different-target", "invalid-marker", "missing-database", "linked-database"} {
		cases = append(cases, struct{ name, suffix, kind string }{name: name})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "recorded")
			targetLock, err := processlock.Acquire(filepath.Join(target, databaseFileName))
			if err != nil {
				t.Fatal(err)
			}
			defer targetLock.Close()
			switch test.name {
			case "materializing", "different-target":
				marker, _, err := copyinmarker.Read(target)
				if err != nil {
					t.Fatal(err)
				}
				if test.name == "materializing" {
					marker.Phase = copyinmarker.PhaseMaterializing
				} else {
					marker.TargetDatabase = filepath.Join(target, "other.sqlite")
				}
				if err := copyinmarker.Write(target, marker); err != nil {
					t.Fatal(err)
				}
			case "invalid-marker":
				if err := os.WriteFile(copyinmarker.Path(target), []byte(`{"version":999,"phase":"future"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-database", "linked-database":
				if err := os.Remove(filepath.Join(target, databaseFileName)); err != nil {
					t.Fatal(err)
				}
				if test.name == "linked-database" {
					plantUnexpectedMember(t, filepath.Join(target, databaseFileName), "symlink")
				}
			}
			if test.suffix != "" {
				plantUnexpectedMember(t, filepath.Join(target, databaseFileName+test.suffix), test.kind)
			}
			before, sourceBefore := fingerprintTree(t, target), fingerprintTree(t, volume.root)
			plugins := false
			entries := []struct {
				name string
				run  func() error
			}{
				{"reconcile", func() error {
					_, err := Reconcile(context.Background(), Options{LegacyRoot: volume.root, TargetRoot: target, BundleDir: options.BundleDir, Lock: options.Lock, TargetLock: targetLock})
					return err
				}},
				{"verify", func() error {
					_, err := Verify(context.Background(), VerifyOptions{LegacyRoot: volume.root, TargetRoot: target})
					return err
				}},
				{"candidate", func() error {
					_, _, snapshot, err := readCopyInCandidate(context.Background(), filepath.Join(target, databaseFileName), volume.root, target, "")
					if snapshot != nil {
						_ = snapshot.Close()
					}
					return err
				}},
				{"recorded-copyin", func() error {
					_, _, err := readRecordedCopyIn(context.Background(), filepath.Join(target, databaseFileName))
					return err
				}},
				{"cpa-import", func() error {
					_, err := ImportCPA(context.Background(), CPAImportOptions{LegacyRoot: volume.root, TargetRoot: target, PluginsEnabled: &plugins, Runtime: &fakeImportRuntime{}})
					return err
				}},
				{"external-connect", func() error {
					_, err := ExternalConnect(context.Background(), ExternalConnectOptions{TargetRoot: target, BaseURL: "http://127.0.0.1:1", ManagementKey: externalTestKey})
					return err
				}},
				{"reconciliation-record", func() error {
					_, _, err := recordReconciliation(context.Background(), filepath.Join(target, databaseFileName), ReconcileEvidence{}, nil)
					return err
				}},
				{"cpa-stage", func() error {
					return enterCPAImportStage(context.Background(), filepath.Join(target, databaseFileName))
				}},
				{"cpa-completion", func() error {
					_, err := recordCPAImportCompletion(context.Background(), filepath.Join(target, databaseFileName), CPAImportResult{})
					return err
				}},
				{"cpa-destination-save", func() error {
					return saveCPAImportDestinationRecord(context.Background(), filepath.Join(target, databaseFileName), filepath.Join(target, dataKeyFileName), cpaImportDestinationRecord{}, "")
				}},
				{"cpa-destination-load", func() error {
					_, _, _, err := loadCPAImportDestinationRecord(context.Background(), filepath.Join(target, databaseFileName), filepath.Join(target, dataKeyFileName))
					return err
				}},
				{"cpa-connection", func() error {
					return handDestinationConnection(context.Background(), filepath.Join(target, databaseFileName), filepath.Join(target, dataKeyFileName), "", "")
				}},
			}
			for _, entry := range entries {
				t.Run(entry.name, func(t *testing.T) { requireTargetConflict(t, entry.run()) })
				assertTreeUnchanged(t, "unrecovered target", before, fingerprintTree(t, target))
			}
			for _, rollbackEntry := range []struct {
				name string
				run  func() error
			}{
				{"rollback-prepare", func() error {
					_, err := PrepareRollback(context.Background(), RollbackOptions{LegacyRoot: volume.root, TargetRoot: target})
					return err
				}},
				{"rollback-record", func() error {
					_, err := RecordRollback(context.Background(), RollbackObservation{LegacyRoot: volume.root, TargetRoot: target})
					return err
				}},
			} {
				t.Run(rollbackEntry.name, func(t *testing.T) {
					err := rollbackEntry.run()
					if test.name == "published" || test.name == "materializing" {
						if !errors.Is(err, ErrRollbackNotApplicable) {
							t.Fatalf("rollback refusal = %v, want %v", err, ErrRollbackNotApplicable)
						}
					} else {
						requireTargetConflict(t, err)
					}
				})
				assertTreeUnchanged(t, "unrecovered target", before, fingerprintTree(t, target))
			}
			assertTreeUnchanged(t, "unrecovered source", sourceBefore, fingerprintTree(t, volume.root))
		})
	}
}

func TestCopyInCandidateRefusesUnexpectedSidecars(t *testing.T) {
	for _, test := range []struct{ suffix, kind string }{{"-journal", "empty"}, {"-journal", "nonempty"}, {"-journal", "symlink"}, {"-wal", "symlink"}, {"-shm", "symlink"}} {
		t.Run(test.suffix+"/"+test.kind, func(t *testing.T) {
			copied := newCopiedVolume(t)
			plantUnexpectedMember(t, copied.databasePath()+test.suffix, test.kind)
			before := fingerprintTree(t, copied.target)
			_, _, snapshot, err := readCopyInCandidate(context.Background(), copied.databasePath(), copied.volume.root, copied.target, "")
			if snapshot != nil {
				_ = snapshot.Close()
			}
			requireTargetConflict(t, err)
			_, err = Reconcile(context.Background(), copied.reconcileOptions())
			requireTargetConflict(t, err)
			_, err = Verify(context.Background(), VerifyOptions{LegacyRoot: copied.volume.root, TargetRoot: copied.target})
			requireTargetConflict(t, err)
			_, err = PrepareRollback(context.Background(), RollbackOptions{LegacyRoot: copied.volume.root, TargetRoot: copied.target})
			requireTargetConflict(t, err)
			_, err = RecordRollback(context.Background(), RollbackObservation{LegacyRoot: copied.volume.root, TargetRoot: copied.target})
			requireTargetConflict(t, err)
			assertTreeUnchanged(t, "unexpected target sidecar", before, fingerprintTree(t, copied.target))
		})
	}
}

func TestVerifyValidatesScratchBeforeItsFirstRead(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, configured := range []bool{false, true} {
			name := "incomplete"
			if completed {
				name = "complete"
			}
			if configured {
				name += "/configured"
			} else {
				name += "/default"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newExternalVerifyFixture(t)
				if completed {
					if result, err := fixture.verify(t); err != nil || !result.Complete {
						t.Fatalf("complete fixture: %+v/%v", result, err)
					}
				}
				scratch := filepath.Join(fixture.volume.root, "scratch")
				if err := os.Mkdir(scratch, 0o700); err != nil {
					t.Fatal(err)
				}
				options := fixture.verifyOptions()
				if configured {
					options.ScratchDir = scratch
				} else {
					setOSTempDir(t, scratch)
				}
				sourceBefore, targetBefore := fingerprintTree(t, fixture.volume.root), fingerprintTree(t, fixture.target)
				_, err := Verify(context.Background(), options)
				if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorSnapshotFailed || !errors.Is(err, ErrSnapshotScratch) {
					t.Fatalf("source scratch refusal = %q/%t/%v", code, ok, err)
				}
				assertTreeUnchanged(t, "verification source", sourceBefore, fingerprintTree(t, fixture.volume.root))
				assertTreeUnchanged(t, "verification target", targetBefore, fingerprintTree(t, fixture.target))
				if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
					t.Fatalf("verification created source scratch: %v/%v", entries, err)
				}
			})
		}
	}
}

func TestCopyInRollbackPreservesChangedMembers(t *testing.T) {
	for _, member := range []string{"data-key", "archive-content", "archive-entry", "archive-symlink"} {
		t.Run(member, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			var before map[string]string
			options.checkpoint = func(phase string) error {
				if phase != "target-restored" {
					return nil
				}
				switch member {
				case "data-key":
					if err := os.WriteFile(filepath.Join(target, dataKeyFileName), []byte("replacement key"), 0o600); err != nil {
						return err
					}
				case "archive-content":
					relative, err := filepath.Rel(volume.root, volume.archives()[0])
					if err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(target, relative), []byte("replacement archive"), 0o600); err != nil {
						return err
					}
				case "archive-entry":
					if err := os.WriteFile(filepath.Join(target, archivesDirName, "unexpected"), []byte("preserved archive entry"), 0o600); err != nil {
						return err
					}
				case "archive-symlink":
					if err := os.RemoveAll(filepath.Join(target, archivesDirName)); err != nil {
						return err
					}
					if err := os.Symlink(filepath.Join(volume.root, archivesDirName), filepath.Join(target, archivesDirName)); err != nil {
						return err
					}
				}
				if err := os.WriteFile(volume.archives()[0], []byte("changed source"), 0o600); err != nil {
					return err
				}
				before = fingerprintTree(t, target)
				return nil
			}
			_, err := CopyIn(context.Background(), options)
			requireTargetConflict(t, err)
			if before == nil {
				t.Fatal("restoration boundary was not exercised")
			}
			assertTreeUnchanged(t, "changed materialized member", before, fingerprintTree(t, target))
			if _, found, err := copyinmarker.Read(target); err != nil || !found {
				t.Fatalf("rollback refusal lost marker: %t/%v", found, err)
			}
		})
	}
}
