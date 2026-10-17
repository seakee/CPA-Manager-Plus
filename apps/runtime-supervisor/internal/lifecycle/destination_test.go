package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

func destinationFixture(t *testing.T) (*prepareFixture, DestinationRequest, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("production destination staging is Linux-only; Windows retains ordinary lifecycle")
	}
	data := []byte("exact CPA destination executable for test")
	digest := sha256.Sum256(data)
	id := artifact.ID("sha256:" + hex.EncodeToString(digest[:]))
	f := newPrepareFixture(t, id)
	executable := filepath.Join(filepath.Dir(f.path), "cpa")
	if err := os.WriteFile(executable, data, 0o700); err != nil {
		t.Fatal(err)
	}
	f.starter.executable = executable
	root := filepath.Join(filepath.Dir(f.path), "destination")
	if err := f.starter.EnableDestination(root, "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	r := DestinationRequest{OperationID: "fresh-operation:adopt-destination", ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 41, ExpectedActiveArtifactID: id, SourceIdentity: "external:sha256:" + strings.Repeat("b", 64), SourceClass: "network_existing", APIKeys: []string{"test-client-secret"}, ManagementKey: strings.Repeat("c", 64)}
	return f, r, root
}

func TestDestinationStageReplayReopenAndEverySpawnRetainOwnedConfiguration(t *testing.T) {
	f, r, root := destinationFixture(t)
	staged, err := f.starter.StageDestination(t.Context(), r)
	if err != nil || staged.State != journal.StateSucceeded || f.child.starts != 0 {
		t.Fatalf("stage: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "stage.json"))
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A lost successful reply replays without replacing files or spawning.
	if op, err := f.starter.StageDestination(t.Context(), r); err != nil || !reflect.DeepEqual(op, staged) {
		t.Fatal("stage replay lost durable operation", err)
	}
	if err = f.starter.Close(); err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journalBytes), r.APIKeys[0]) || strings.Contains(string(journalBytes), r.ManagementKey) {
		t.Fatal("resource secret entered operation journal")
	}
	store, err := journal.Open(t.Context(), f.path, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewExecutor(journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41}, store, f.child, f.starter.executable)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.artifact = f.observer
	if err = e.EnableDestination(root, "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StageDestination(t.Context(), r); err != nil {
		t.Fatal("reopened stage", err)
	}
	f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
		if !reflect.DeepEqual(spec.Args, []string{"-config", filepath.Join(root, "config.yaml")}) {
			t.Error("destination configuration lost on spawn")
		}
		return nil
	}
	start := StartRequest{OperationID: r.OperationID + ":start", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration, ExpectedActiveArtifactID: r.ExpectedActiveArtifactID}
	if _, err = e.Start(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(t.Context(), start); err != nil || f.child.starts != 1 {
		t.Fatal("duplicate start spawned again", err)
	}
	if _, err = e.Restart(t.Context(), RestartRequest{OperationID: "later-restart", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Stop(t.Context(), StopRequest{OperationID: "later-stop", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	start.OperationID = "later-start"
	if _, err = e.Start(t.Context(), start); err != nil || f.child.starts != 3 {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "stage.json"))
	configAfter, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
	if string(before) != string(after) || string(configBefore) != string(configAfter) {
		t.Fatal("replay rewrote resource state")
	}
	if err = os.WriteFile(f.starter.executable, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = e.startProcess(t.Context(), f.starter.executable); !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatal("replacement binary spawned", err)
	}
}

func TestDestinationRejectsConflictsBeforeSourceOrChildEffects(t *testing.T) {
	f, r, root := destinationFixture(t)
	for _, change := range []func(*DestinationRequest){func(r *DestinationRequest) { r.ExpectedRuntimeGeneration++ }, func(r *DestinationRequest) { r.ExpectedRuntimeIdentity = "foreign" }, func(r *DestinationRequest) { r.ExpectedActiveArtifactID = activeArtifactB }} {
		request := r
		change(&request)
		if _, err := f.starter.StageDestination(t.Context(), request); err == nil {
			t.Fatal("unfenced stage accepted")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("conflict created destination")
		}
	}
	if _, err := f.starter.StageDestination(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*DestinationRequest){func(r *DestinationRequest) { r.SourceIdentity = "external:sha256:" + strings.Repeat("d", 64) }, func(r *DestinationRequest) { r.SourceClass = "legacy_managed" }, func(r *DestinationRequest) { r.APIKeys = []string{"different-secret"} }, func(r *DestinationRequest) { r.ManagementKey = strings.Repeat("e", 64) }, func(r *DestinationRequest) { r.OperationID = "other-operation" }} {
		request := r
		change(&request)
		if _, err := f.starter.StageDestination(t.Context(), request); !errors.Is(err, journal.ErrOperationIDConflict) {
			t.Fatal("conflicting durable stage replay", err)
		}
	}
	start := startRequest("different-artifact-start")
	start.ExpectedActiveArtifactID = activeArtifactB
	if _, err := f.starter.Start(t.Context(), start); !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatal("different artifact started staged destination", err)
	}
	if f.child.starts != 0 || f.child.stops != 0 {
		t.Fatal("stage touched lifecycle")
	}
}

func TestDestinationPartialPersistenceNeverResumesUncertainStage(t *testing.T) {
	for _, failure := range []string{"running", "complete"} {
		t.Run(failure, func(t *testing.T) {
			f, r, _ := destinationFixture(t)
			f.journal.failAt = failure
			if _, err := f.starter.StageDestination(t.Context(), r); err == nil {
				t.Fatal("persistence failure succeeded")
			}
			f.journal.failAt = ""
			op, err := f.starter.StageDestination(t.Context(), r)
			if err != nil || op.State == journal.StateSucceeded || f.child.starts != 0 {
				t.Fatal("uncertain stage was resumed or committed", err)
			}
		})
	}
}
