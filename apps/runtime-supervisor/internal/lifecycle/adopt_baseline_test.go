package lifecycle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const nextBaselineVersion = "8.0.56"

func prepareBaselineSwitch(t *testing.T, env *acquireTestEnv, running bool) {
	t.Helper()
	op, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive)))
	if err != nil {
		t.Fatal(err)
	}
	if p, provErr := env.selectionStore.LoadProvenance(); provErr != nil ||
		p.ArtifactID != env.pinnedID || p.Origin != selection.OriginBaselineAcquire || p.OperationID != op.ID {
		t.Fatalf("baseline switch provenance = %+v, %v (want op=%s id=%s)", p, provErr, op.ID, env.pinnedID)
	}
	if err := env.executor.EnableDestination(filepath.Join(env.supervisorRoot, "destination"), filepath.Join(env.root, "auth"), "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	if op, err := env.executor.StageDestination(t.Context(), DestinationRequest{
		OperationID: "stage:baseline-test", ExpectedRuntimeIdentity: "runtime-s2a", ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID: env.pinnedID, SourceIdentity: "managed:" + string(env.pinnedID), SourceClass: SourceClassNewManaged,
		APIKeys: []string{initialClientKeyFixture()}, ManagementKey: string(bytes.Repeat([]byte("c"), 64)),
	}); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("stage destination: %+v, %v", op, err)
	}
	env.process.allowStop = true
	if running {
		startInstalledBaseline(t, env, "start:baseline-test", env.pinnedID)
	}
}

func startInstalledBaseline(t *testing.T, env *acquireTestEnv, opID string, id artifact.ID) {
	t.Helper()
	if op, err := env.executor.Start(t.Context(), StartRequest{
		OperationID: opID, ExpectedRuntimeIdentity: "runtime-s2a", ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: id,
	}); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("start baseline: %+v, %v", op, err)
	}
}

func setNextBaseline(t *testing.T, env *acquireTestEnv, bundled bool, opts ...AcquisitionOption) (artifact.ID, []byte) {
	t.Helper()
	binary := []byte("#!/bin/sh\necho newly-pinned-baseline\n")
	id := artifact.ID(fmt.Sprintf("sha256:%x", sha256.Sum256(binary)))
	manifest := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":%q,"artifactId":%q}`, nextBaselineVersion, id)
	if err := os.WriteFile(env.manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if bundled {
		if err := os.WriteFile(env.missingBundled, binary, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts = append([]AcquisitionOption{WithAcquisitionPlatform("linux", "amd64"), WithBundledArtifactExecutable(env.missingBundled)}, opts...)
	if err := env.executor.EnableArtifactAcquisition(env.manifestPath, opts...); err != nil {
		t.Fatal(err)
	}
	return id, buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: binary},
		acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: []byte("MIT License\n")},
	)
}

func assertDestinationArtifact(t *testing.T, env *acquireTestEnv, want artifact.ID) {
	t.Helper()
	staged, err := env.executor.destination.load()
	if err != nil || staged == nil || staged.ExpectedActiveArtifactID != want {
		t.Fatalf("destination digest = %+v, %v; want %s", staged, err, want)
	}
}

func readBaselineStateFile(t *testing.T, env *acquireTestEnv, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(env.selectionRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAdoptBaselinePreservesStoppedStateAndStartsBundledWithDestinationFence(t *testing.T) {
	env := newSlimAcquireTestEnv(t)
	prepareBaselineSwitch(t, env, false)
	id, _ := setNextBaseline(t, env, true)
	status, err := env.executor.ArtifactStatus()
	if err != nil || !status.BundledAvailable || !status.AdoptBaselineAvailable || status.BaselineUpgradeAvailable || status.Required || status.Active == nil || status.Active.ArtifactID != env.pinnedID {
		t.Fatalf("before adoption: %+v, %v", status, err)
	}
	if _, err := env.executor.AdoptBaseline(t.Context(), id); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("stale expected digest: %v", err)
	}
	status, err = env.executor.AdoptBaseline(t.Context(), env.pinnedID)
	if err != nil || !status.BundledAvailable || status.AdoptBaselineAvailable || status.Required || status.Active == nil || status.Active.Origin != ArtifactOriginBundled || status.Active.ArtifactID != id {
		t.Fatalf("after adoption: %+v, %v", status, err)
	}
	for _, name := range []string{"selection.json", "selection-provenance.json"} {
		if _, err := os.Lstat(filepath.Join(env.selectionRoot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still present: %v", name, err)
		}
	}
	if env.process.starts != 0 || env.process.stops != 0 || env.process.Observe().State != cpaprocess.StateNotStarted {
		t.Fatal("adoption must not launch a stopped CPA")
	}
	assertDestinationArtifact(t, env, id)
	// Simulate the original stage fence being restored for a journal replay.
	if err := env.executor.restoreOriginStageRequestLocked(); err != nil {
		t.Fatal(err)
	}
	assertDestinationArtifact(t, env, env.pinnedID)
	startInstalledBaseline(t, env, "start:adopted-bundle", id)
	assertDestinationArtifact(t, env, id)
	if spec := env.process.startSpec; spec.Executable != env.missingBundled || len(spec.Args) != 2 || spec.Args[0] != "-config" {
		t.Fatalf("bundled process start spec: %+v", spec)
	}
}

func TestAdoptBaselineRunningCandidateOwnsProcessOnlyAfterReadiness(t *testing.T) {
	for _, trial := range []string{"ready", "unready", "spawn failure", "changed after readiness"} {
		t.Run(trial, func(t *testing.T) {
			env := newSlimAcquireTestEnv(t)
			prepareBaselineSwitch(t, env, true)
			prior := env.executor.active
			before := readBaselineStateFile(t, env, "selection.json")
			provenance := readBaselineStateFile(t, env, "selection-provenance.json")
			id, _ := setNextBaseline(t, env, true)
			if trial == "spawn failure" {
				onStart := env.process.onStart
				env.process.onStart = func(spec cpaprocess.StartSpec) error {
					if spec.Executable == env.missingBundled {
						return errors.New("candidate could not start")
					}
					return onStart(spec)
				}
			}
			env.executor.readiness = readinessFunc(func(context.Context) readiness.State {
				if env.process.startSpec.Executable == env.missingBundled {
					if !bytes.Equal(before, readBaselineStateFile(t, env, "selection.json")) {
						t.Fatal("candidate was selected before readiness")
					}
					assertDestinationArtifact(t, env, env.pinnedID)
					if trial == "unready" {
						return readiness.Offline
					}
					if trial == "changed after readiness" {
						if err := os.WriteFile(env.missingBundled, []byte("changed"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
				return readiness.Ready
			})
			status, err := env.executor.AdoptBaseline(t.Context(), env.pinnedID)
			want := env.pinnedID
			if trial == "ready" {
				want = id
				if err != nil || status.Active == nil || status.Active.ArtifactID != id || env.process.startSpec.Executable != env.missingBundled || env.process.starts != 2 || env.process.stops != 1 {
					t.Fatalf("running adoption = %+v, %v, starts=%d stops=%d", status, err, env.process.starts, env.process.stops)
				}
			} else {
				if ArtifactErrorCode(err) != ArtifactCodeInstallFailed || env.process.startSpec.Executable != prior.ExecutablePath || env.executor.active != prior {
					t.Fatalf("expected install failure and prior process, got %+v, %v, active=%+v spec=%+v", status, err, env.executor.active, env.process.startSpec)
				}
				if !bytes.Equal(before, readBaselineStateFile(t, env, "selection.json")) || !bytes.Equal(provenance, readBaselineStateFile(t, env, "selection-provenance.json")) {
					t.Fatal("failed candidate altered persistent selection or provenance")
				}
				if trial != "spawn failure" && (env.process.starts != 3 || env.process.stops != 2) {
					t.Fatalf("candidate must be reaped before rollback: starts=%d stops=%d", env.process.starts, env.process.stops)
				}
			}
			assertDestinationArtifact(t, env, want)
			if recovery := env.executor.RecoveryStatus(); recovery.State != RecoveryStateArmed || env.process.Observe().State != cpaprocess.StateRunning {
				t.Fatalf("recovery not armed for running owner: %+v", recovery)
			}
			if observed := env.observer.Observation(); observed == nil || observed.ArtifactID != want {
				t.Fatalf("observer artifact = %+v, want %s", observed, want)
			}
		})
	}
}

func TestCompareCPAVersionUsesReleasePrecedence(t *testing.T) {
	for _, versions := range [][2]string{
		{"8.0.100", "8.0.99"},
		{"8.1.0", "8.0.999"},
		{"8.1.0", "8.1.0-rc.999"},
		{"8.1.0-rc.100", "8.1.0-rc.99"},
		{"8.1.0-rc", "8.1.0-100"},
		{"8.1.0-rc.1", "8.1.0-rc"},
	} {
		if compareCPAVersion(versions[0], versions[1]) <= 0 || compareCPAVersion(versions[1], versions[0]) >= 0 {
			t.Errorf("expected %s > %s", versions[0], versions[1])
		}
	}
	if compareCPAVersion("8.1.0+bundled", "8.1.0+acquired") != 0 {
		t.Error("build metadata must not affect release precedence")
	}
}

func TestAdoptBaselineRejectsUserUpdateAndUntrustedProvenance(t *testing.T) {
	t.Run("user_update via ActivateUpdate blocks baseline adoption", func(t *testing.T) {
		env := newSlimAcquireTestEnv(t)
		prepareBaselineSwitch(t, env, true)

		userVersion := "8.0.56"
		userBinary := []byte("#!/bin/sh\necho user-updated-8.0.56\n")
		userSum := sha256.Sum256(userBinary)
		userID := artifact.ID("sha256:" + hex.EncodeToString(userSum[:]))
		userArchive := buildAcquireTarGzip(t,
			acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: userBinary},
			acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: []byte("MIT License\n")},
		)
		archiveSum := sha256.Sum256(userArchive)
		if _, err := env.stageStore.Stage(t.Context(), runtimeupdate.Release{
			Version:       userVersion,
			ArchiveDigest: fmt.Sprintf("sha256:%x", archiveSum),
		}, func(_ context.Context, _ runtimeupdate.Release, dst io.Writer) error {
			_, writeErr := dst.Write(userArchive)
			return writeErr
		}); err != nil {
			t.Fatal(err)
		}
		actOp, err := env.executor.ActivateUpdate(t.Context(), ActivateUpdateRequest{
			OperationID:               "activate-user-8.0.56",
			ExpectedRuntimeIdentity:   "runtime-s2a",
			ExpectedRuntimeGeneration: 1,
			ExpectedActiveArtifactID:  env.pinnedID,
			TargetVersion:             userVersion,
		})
		if err != nil || actOp.State != journal.StateSucceeded {
			t.Fatalf("ActivateUpdate(%s) = %+v, %v", userVersion, actOp, err)
		}

		// Newer bundled release 8.0.57 becomes available on disk, but active 8.0.56 has user_update provenance.
		bundledVersion := "8.0.57"
		bundledBinary := []byte("#!/bin/sh\necho bundled-8.0.57\n")
		bundledSum := sha256.Sum256(bundledBinary)
		bundledID := artifact.ID("sha256:" + hex.EncodeToString(bundledSum[:]))
		bundledManifest := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":%q,"artifactId":%q}`, bundledVersion, bundledID)
		if err := os.WriteFile(env.manifestPath, []byte(bundledManifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(env.missingBundled, bundledBinary, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := env.executor.EnableArtifactAcquisition(
			env.manifestPath,
			WithAcquisitionPlatform("linux", "amd64"),
			WithBundledArtifactExecutable(env.missingBundled),
		); err != nil {
			t.Fatal(err)
		}

		status, err := env.executor.ArtifactStatus()
		if err != nil {
			t.Fatalf("ArtifactStatus() error = %v", err)
		}
		if !status.BundledAvailable || status.AdoptBaselineAvailable || status.BaselineUpgradeAvailable || status.Required ||
			status.Active == nil || status.Active.ArtifactID != userID || status.Active.Origin != ArtifactOriginAcquired {
			t.Fatalf("ArtifactStatus() for user_update = %+v; want AdoptBaselineAvailable=false and Active=%s", status, userID)
		}

		beforeSel := readBaselineStateFile(t, env, "selection.json")
		beforeProv := readBaselineStateFile(t, env, "selection-provenance.json")
		destStagePath := filepath.Join(env.supervisorRoot, "destination", "stage.json")
		beforeDest, err := os.ReadFile(destStagePath)
		if err != nil {
			t.Fatal(err)
		}
		originStagePath := filepath.Join(env.supervisorRoot, "destination", originStageRequestFile)
		beforeOrigin, err := os.ReadFile(originStagePath)
		if err != nil {
			t.Fatal(err)
		}
		beforeProc := env.process.Observe()
		beforeStarts, beforeStops := env.process.starts, env.process.stops

		_, adoptErr := env.executor.AdoptBaseline(t.Context(), userID)
		if ArtifactErrorCode(adoptErr) != ArtifactCodeArtifactConflict || !errors.Is(adoptErr, runtimeupdate.ErrArtifactConflict) {
			t.Fatalf("AdoptBaseline() for user_update = %v, want %s/%v", adoptErr, ArtifactCodeArtifactConflict, runtimeupdate.ErrArtifactConflict)
		}
		if !bytes.Equal(beforeSel, readBaselineStateFile(t, env, "selection.json")) {
			t.Fatal("selection.json was modified after rejected AdoptBaseline")
		}
		if !bytes.Equal(beforeProv, readBaselineStateFile(t, env, "selection-provenance.json")) {
			t.Fatal("selection-provenance.json was modified after rejected AdoptBaseline")
		}
		if afterDest, readErr := os.ReadFile(destStagePath); readErr != nil || !bytes.Equal(beforeDest, afterDest) {
			t.Fatalf("destination/stage.json changed after rejected AdoptBaseline: err=%v", readErr)
		}
		if afterOrigin, readErr := os.ReadFile(originStagePath); readErr != nil || !bytes.Equal(beforeOrigin, afterOrigin) {
			t.Fatalf("destination/.stage-request.json changed after rejected AdoptBaseline: err=%v", readErr)
		}
		if env.process.Observe() != beforeProc || env.process.starts != beforeStarts || env.process.stops != beforeStops {
			t.Fatalf("CPA process disturbed on rejected AdoptBaseline: got=%+v starts=%d stops=%d", env.process.Observe(), env.process.starts, env.process.stops)
		}
	})

	for _, tc := range []struct {
		name    string
		running bool
		mutate  func(t *testing.T, env *acquireTestEnv)
	}{
		{
			name: "missing provenance",
			mutate: func(t *testing.T, env *acquireTestEnv) {
				t.Helper()
				if err := os.Remove(filepath.Join(env.selectionRoot, "selection-provenance.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "corrupt provenance",
			running: true,
			mutate: func(t *testing.T, env *acquireTestEnv) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(env.selectionRoot, "selection-provenance.json"), []byte("{corrupt\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "mismatched provenance artifactId",
			running: true,
			mutate: func(t *testing.T, env *acquireTestEnv) {
				t.Helper()
				other := sha256.Sum256([]byte("other-artifact"))
				if err := env.selectionStore.WriteProvenance(selection.Provenance{
					SchemaVersion:           selection.SchemaVersion,
					ArtifactID:              artifact.ID("sha256:" + hex.EncodeToString(other[:])),
					Origin:                  selection.OriginBaselineAcquire,
					BaselineVersionAtCommit: env.pinnedVersion,
					OperationID:             "tampered-op",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSlimAcquireTestEnv(t)
			prepareBaselineSwitch(t, env, tc.running)
			tc.mutate(t, env)
			setNextBaseline(t, env, true)

			status, err := env.executor.ArtifactStatus()
			if err != nil {
				t.Fatalf("ArtifactStatus() error = %v", err)
			}
			if !status.BundledAvailable || status.AdoptBaselineAvailable || status.BaselineUpgradeAvailable ||
				status.Active == nil || status.Active.ArtifactID != env.pinnedID {
				t.Fatalf("ArtifactStatus() = %+v; want AdoptBaselineAvailable=false", status)
			}

			beforeSel := readBaselineStateFile(t, env, "selection.json")
			provPath := filepath.Join(env.selectionRoot, "selection-provenance.json")
			beforeProv, provReadErr := os.ReadFile(provPath)
			destStagePath := filepath.Join(env.supervisorRoot, "destination", "stage.json")
			beforeDest, err := os.ReadFile(destStagePath)
			if err != nil {
				t.Fatal(err)
			}

			_, adoptErr := env.executor.AdoptBaseline(t.Context(), env.pinnedID)
			if ArtifactErrorCode(adoptErr) != ArtifactCodeArtifactConflict || !errors.Is(adoptErr, runtimeupdate.ErrArtifactConflict) {
				t.Fatalf("AdoptBaseline() = %v, want %s/%v", adoptErr, ArtifactCodeArtifactConflict, runtimeupdate.ErrArtifactConflict)
			}
			if !bytes.Equal(beforeSel, readBaselineStateFile(t, env, "selection.json")) {
				t.Fatal("selection.json was modified")
			}
			afterProv, afterProvErr := os.ReadFile(provPath)
			if errors.Is(provReadErr, os.ErrNotExist) != errors.Is(afterProvErr, os.ErrNotExist) ||
				(!errors.Is(provReadErr, os.ErrNotExist) && (provReadErr != nil || afterProvErr != nil)) {
				t.Fatalf("provenance existence changed: before=%v after=%v", provReadErr, afterProvErr)
			}
			if provReadErr == nil && !bytes.Equal(beforeProv, afterProv) {
				t.Fatal("selection-provenance.json was modified")
			}
			if afterDest, readErr := os.ReadFile(destStagePath); readErr != nil || !bytes.Equal(beforeDest, afterDest) {
				t.Fatalf("destination/stage.json changed: err=%v", readErr)
			}
			if _, originErr := os.Lstat(filepath.Join(env.supervisorRoot, "destination", originStageRequestFile)); !errors.Is(originErr, os.ErrNotExist) {
				t.Fatalf("unexpected .stage-request.json created: %v", originErr)
			}
		})
	}
}

func TestStartReconcilesBundledDestinationAfterRestartWithoutOriginSnapshot(t *testing.T) {
	root := t.TempDir()
	supervisorRoot := filepath.Join(root, "supervisor")
	stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
	selectionRoot := filepath.Join(supervisorRoot, "active", "cpa")
	destRoot := filepath.Join(supervisorRoot, "destination")
	gatewayAuth := filepath.Join(root, "gateway", "auth")
	journalPath := filepath.Join(supervisorRoot, "operations.sqlite")
	bundledPath := filepath.Join(root, "usr-local-bin-cli-proxy-api")
	manifestPath := filepath.Join(root, "cpa-artifact.json")

	writeBundled := func(version string, binary []byte) artifact.ID {
		t.Helper()
		sum := sha256.Sum256(binary)
		id := artifact.ID("sha256:" + hex.EncodeToString(sum[:]))
		if err := os.WriteFile(bundledPath, binary, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf("{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":%q,\"artifactId\":%q}\n", version, id)
		if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		return id
	}

	bootSupervisor := func(t *testing.T, generation uint64) (*Executor, *recordingAcquireProcess, *artifact.Observer) {
		t.Helper()
		stageStore, err := runtimeupdate.NewStore(stageRoot)
		if err != nil {
			t.Fatal(err)
		}
		selectionStore, err := selection.NewStore(selectionRoot, stageStore)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectionStore.Load(selection.Bundled(bundledPath, manifestPath))
		if err != nil {
			t.Fatal(err)
		}
		if selected.IsFinalizedStage() {
			t.Fatalf("unexpected finalized selection %+v on bundled docker-full path", selected)
		}
		observer := artifact.NewObserver(selected.ExecutablePath, selected.MetadataPath)
		if err := observer.RefreshFrom(selected.ExecutablePath, selected.MetadataPath); err != nil {
			t.Fatalf("initial observer refresh: %v", err)
		}
		proc := &recordingAcquireProcess{allowStop: true}
		proc.onStart = func(spec cpaprocess.StartSpec) error {
			return observer.RefreshFrom(spec.Executable, manifestPath)
		}
		store, err := journal.Open(t.Context(), journalPath, journal.Options{})
		if err != nil {
			t.Fatal(err)
		}
		exec, err := NewExecutor(
			journal.Authority{RuntimeIdentity: "runtime-docker-full", RuntimeGeneration: generation},
			store,
			proc,
			selected.ExecutablePath,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := exec.EnableActivateUpdate(selected, observer, selectionStore, readinessFunc(func(context.Context) readiness.State {
			return readiness.Ready
		})); err != nil {
			t.Fatal(err)
		}
		if err := exec.EnableDestination(destRoot, gatewayAuth, "127.0.0.1:8317", nil); err != nil {
			t.Fatal(err)
		}
		if err := exec.EnableArtifactAcquisition(
			manifestPath,
			WithAcquisitionPlatform("linux", "amd64"),
			WithBundledArtifactExecutable(bundledPath),
		); err != nil {
			t.Fatal(err)
		}
		return exec, proc, observer
	}

	// 1. Fresh docker-full boot with oldBundled: StageDestination + Start only (no UploadArtifact, ActivateUpdate, or AdoptBaseline).
	oldID := writeBundled("8.0.55", []byte("#!/bin/sh\necho bundled-8.0.55\n"))
	exec1, proc1, _ := bootSupervisor(t, 1)
	t.Cleanup(func() { _ = exec1.Close() })
	oldDestReq := DestinationRequest{
		OperationID:               "stage:docker-full-init",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  oldID,
		SourceIdentity:            "managed:" + string(oldID),
		SourceClass:               SourceClassNewManaged,
		APIKeys:                   []string{initialClientKeyFixture()},
		ManagementKey:             string(bytes.Repeat([]byte("c"), 64)),
	}
	if op, err := exec1.StageDestination(t.Context(), oldDestReq); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("initial StageDestination = %+v, %v", op, err)
	}
	if op, err := exec1.Start(t.Context(), StartRequest{
		OperationID:               "start:docker-full-v1",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  oldID,
	}); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("initial Start = %+v, %v", op, err)
	}
	if proc1.starts != 1 || proc1.startSpec.Executable != bundledPath {
		t.Fatalf("initial spawn = starts:%d spec:%+v", proc1.starts, proc1.startSpec)
	}
	if _, err := os.Lstat(filepath.Join(destRoot, originStageRequestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".stage-request.json must not exist before baseline mismatch: %v", err)
	}
	for _, name := range []string{"selection.json", "selection-provenance.json"} {
		if _, err := os.Lstat(filepath.Join(selectionRoot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not exist on fresh docker-full install: %v", name, err)
		}
	}
	if err := exec1.Close(); err != nil {
		t.Fatal(err)
	}

	// 2. Restart Supervisor with newBundled + cpa-artifact.json(newBundled).
	//    Executor.Start must reconcile stage.json cleanly without ErrActiveArtifactMismatch.
	newID := writeBundled("8.0.56", []byte("#!/bin/sh\necho bundled-8.0.56\n"))
	exec2, proc2, obs2 := bootSupervisor(t, 1)
	t.Cleanup(func() { _ = exec2.Close() })

	status, err := exec2.ArtifactStatus()
	if err != nil || !status.BundledAvailable || status.AdoptBaselineAvailable || status.Required ||
		status.Active == nil || status.Active.Origin != ArtifactOriginBundled || status.Active.ArtifactID != newID {
		t.Fatalf("ArtifactStatus after bundled upgrade = %+v, %v", status, err)
	}
	if op, err := exec2.Start(t.Context(), StartRequest{
		OperationID:               "start:docker-full-v2",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  newID,
	}); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("upgraded bundled Start() error = %v, op = %+v", err, op)
	}
	wantArgs := []string{"-config", filepath.Join(destRoot, "config.yaml")}
	if proc2.starts != 1 || proc2.startSpec.Executable != bundledPath || !reflect.DeepEqual(proc2.startSpec.Args, wantArgs) {
		t.Fatalf("upgraded bundled spawn = starts:%d spec:%+v; want exe=%q args=%v", proc2.starts, proc2.startSpec, bundledPath, wantArgs)
	}
	if got := obs2.Observation(); got == nil || got.ArtifactID != newID {
		t.Fatalf("observer after upgraded Start = %+v, want %s", got, newID)
	}
	stagedAfterV2, err := exec2.destination.load()
	if err != nil || stagedAfterV2 == nil || stagedAfterV2.ExpectedActiveArtifactID != newID || stagedAfterV2.OperationID != oldDestReq.OperationID {
		t.Fatalf("reconciled stage.json = %+v, %v; want %s", stagedAfterV2, err, newID)
	}
	originAfterV2, err := exec2.loadOriginStageRequestLocked()
	if err != nil || originAfterV2 == nil || !reflect.DeepEqual(*originAfterV2, oldDestReq) {
		t.Fatalf(".stage-request.json = %+v, %v; want %+v", originAfterV2, err, oldDestReq)
	}

	// 3. Idempotent replay of the original StageDestination(oldBundled) still succeeds, and Restart reconciles back to newBundled.
	if replayOp, err := exec2.StageDestination(t.Context(), oldDestReq); err != nil || replayOp.State != journal.StateSucceeded || replayOp.OperationID != oldDestReq.OperationID {
		t.Fatalf("StageDestination replay after bundled reconciliation = %+v, %v", replayOp, err)
	}
	if restartOp, err := exec2.Restart(t.Context(), RestartRequest{
		OperationID:               "restart:docker-full-v2",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 1,
	}); err != nil || restartOp.State != journal.StateSucceeded {
		t.Fatalf("Restart after StageDestination replay = %+v, %v", restartOp, err)
	}
	if stagedAfterRestart, err := exec2.destination.load(); err != nil || stagedAfterRestart == nil || stagedAfterRestart.ExpectedActiveArtifactID != newID {
		t.Fatalf("stage.json after Restart = %+v, %v; want %s", stagedAfterRestart, err, newID)
	}
	if err := exec2.Close(); err != nil {
		t.Fatal(err)
	}

	// 4. Subsequent bundled upgrade (v2 -> v3) across a new generation with existing .stage-request.json also reconciles and preserves oldBundled origin.
	v3ID := writeBundled("8.0.57", []byte("#!/bin/sh\necho bundled-8.0.57\n"))
	exec3, proc3, _ := bootSupervisor(t, 2)
	t.Cleanup(func() { _ = exec3.Close() })
	if op, err := exec3.Start(t.Context(), StartRequest{
		OperationID:               "start:docker-full-v3",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 2,
		ExpectedActiveArtifactID:  v3ID,
	}); err != nil || op.State != journal.StateSucceeded {
		t.Fatalf("second bundled upgrade Start = %+v, %v", op, err)
	}
	if proc3.starts != 1 || proc3.startSpec.Executable != bundledPath {
		t.Fatalf("v3 spawn = starts:%d spec:%+v", proc3.starts, proc3.startSpec)
	}
	if stagedAfterV3, err := exec3.destination.load(); err != nil || stagedAfterV3 == nil || stagedAfterV3.ExpectedActiveArtifactID != v3ID {
		t.Fatalf("v3 stage.json = %+v, %v; want %s", stagedAfterV3, err, v3ID)
	}
	if originAfterV3, err := exec3.loadOriginStageRequestLocked(); err != nil || originAfterV3 == nil || !reflect.DeepEqual(*originAfterV3, oldDestReq) {
		t.Fatalf("v3 .stage-request.json = %+v, %v; want %+v", originAfterV3, err, oldDestReq)
	}

	// 5. Tampered bundled executable fails closed on Start without launching CPA or altering .stage-request.json.
	if err := os.WriteFile(bundledPath, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc3.state = cpaprocess.Observation{State: cpaprocess.StateExited, InstanceID: proc3.state.InstanceID}
	beforeTamperStarts := proc3.starts
	if _, err := exec3.Start(t.Context(), StartRequest{
		OperationID:               "start:tampered-bundled",
		ExpectedRuntimeIdentity:   "runtime-docker-full",
		ExpectedRuntimeGeneration: 2,
	}); err == nil {
		t.Fatal("expected Start to fail closed on tampered bundled executable")
	}
	if proc3.starts != beforeTamperStarts {
		t.Fatalf("tampered bundled binary was spawned: starts=%d (want %d)", proc3.starts, beforeTamperStarts)
	}
	if originAfterTamper, err := exec3.loadOriginStageRequestLocked(); err != nil || originAfterTamper == nil || !reflect.DeepEqual(*originAfterTamper, oldDestReq) {
		t.Fatalf(".stage-request.json mutated after tampered Start: %+v, %v", originAfterTamper, err)
	}
}
