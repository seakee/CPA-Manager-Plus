package selection

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProvenanceRoundTripDoesNotChangeSelection(t *testing.T) {
	root := t.TempDir()
	stageRoot := filepath.Join(root, "artifacts")
	active := filepath.Join(root, "active")
	store, err := NewStore(active, newStageStore(t, stageRoot))
	if err != nil {
		t.Fatal(err)
	}
	selected := seedFinalizedStage(t, stageRoot, "8.0.55", []byte("acquired binary"))
	if err := store.Commit(selected); err != nil {
		t.Fatal(err)
	}
	selectionPath := filepath.Join(active, selectionName)
	before, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadProvenance(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing provenance: %v", err)
	}
	record := Provenance{SchemaVersion: 1, ArtifactID: selected.ArtifactID, Origin: OriginBaselineAcquire,
		BaselineVersionAtCommit: "8.0.55", OperationID: "acquire-1"}
	if err := store.WriteProvenance(record); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadProvenance(); err != nil || got != record {
		t.Fatalf("provenance = %+v, %v", got, err)
	}
	path := filepath.Join(active, provenanceName)
	if stat, err := os.Lstat(path); err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0o600 {
		t.Fatalf("untrusted provenance file: %v, %v", stat, err)
	}
	record.Origin = OriginUserUpdate
	if err := store.WriteProvenance(record); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadProvenance(); err != nil || got != record {
		t.Fatalf("replacement = %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{} {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadProvenance(); err == nil {
		t.Fatal("accepted malformed provenance")
	}
	after, err := os.ReadFile(selectionPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("selection changed: %v", err)
	}
	if got, err := store.Load(Bundled("/bundled/cli-proxy-api", "/bundled/artifact.json")); err != nil || got != selected {
		t.Fatalf("selection with damaged provenance = %+v, %v", got, err)
	}
}

func TestLoadPersistedSelectionValidatesAbsencePermissionsAndContent(t *testing.T) {
	root := t.TempDir()
	stageRoot := filepath.Join(root, "artifacts")
	active := filepath.Join(root, "active")
	store, err := NewStore(active, newStageStore(t, stageRoot))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.LoadPersistedSelection(); err != nil || ok || got != (Descriptor{}) {
		t.Fatalf("missing selection = (%+v, %v, %v), want empty/false/nil", got, ok, err)
	}

	selected := seedFinalizedStage(t, stageRoot, "8.1.0", []byte("staged-8.1.0-binary"))
	if err := store.Commit(selected); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadPersistedSelection()
	if err != nil || !ok || got.Source != SourceFinalized || got.Version != "8.1.0" || got.ArtifactID != selected.ArtifactID {
		t.Fatalf("persisted selection = (%+v, %v, %v)", got, ok, err)
	}

	selectionPath := filepath.Join(active, selectionName)
	if err := os.Chmod(selectionPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadPersistedSelection(); err == nil || ok {
		t.Fatalf("0644 selection = (ok=%v, err=%v), want untrusted error", ok, err)
	}
	if err := os.Chmod(selectionPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(selectionPath, []byte("{malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadPersistedSelection(); err == nil || ok {
		t.Fatalf("malformed selection = (ok=%v, err=%v), want decode error", ok, err)
	}
}
