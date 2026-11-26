package selection

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

func TestSelectionAbsenceUsesBundledAndCommittedStageSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	stages := newStageStore(t, filepath.Join(root, "artifacts"))
	selectionRoot := filepath.Join(root, "active")
	store, err := NewStore(selectionRoot, stages)
	if err != nil {
		t.Fatal(err)
	}
	bundled := Bundled("/usr/local/bin/cli-proxy-api", "/usr/local/share/cpamp/cpa-artifact.json")
	selected, err := store.Load(bundled)
	if err != nil || selected != bundled {
		t.Fatalf("Load(absent) = %+v, %v", selected, err)
	}

	candidate := seedFinalizedStage(t, filepath.Join(root, "artifacts"), "7.3.4", []byte("candidate-b"))
	if err := store.Commit(candidate); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(selectionRoot, stages)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = reopened.Load(bundled)
	if err != nil || selected != candidate {
		t.Fatalf("Load(committed) = %+v, %v, want %+v", selected, err, candidate)
	}
	encoded, err := os.ReadFile(filepath.Join(selectionRoot, selectionName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "path") || strings.Contains(string(encoded), candidate.ExecutablePath) {
		t.Fatalf("selection leaked path authority: %s", encoded)
	}
}

func TestSelectionPresenceFailsClosedForStrictOrInvalidStageState(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string, string){
		"unknown field": func(t *testing.T, selectionRoot, _ string) {
			writeSelection(t, selectionRoot, `{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":"sha256:`+strings.Repeat("a", 64)+`","path":"/tmp/evil"}`)
		},
		"duplicate field": func(t *testing.T, selectionRoot, _ string) {
			writeSelection(t, selectionRoot, `{"schemaVersion":1,"engine":"cpa","version":"7.3.4","version":"7.3.5","artifactId":"sha256:`+strings.Repeat("a", 64)+`"}`)
		},
		"trailing value": func(t *testing.T, selectionRoot, _ string) {
			writeSelection(t, selectionRoot, `{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":"sha256:`+strings.Repeat("a", 64)+`"}{}`)
		},
		"oversized": func(t *testing.T, selectionRoot, _ string) {
			writeSelection(t, selectionRoot, strings.Repeat(" ", selectionLimit+1))
		},
		"missing stage": func(t *testing.T, selectionRoot, _ string) {
			writeSelection(t, selectionRoot, `{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":"sha256:`+strings.Repeat("a", 64)+`"}`)
		},
		"digest mismatch": func(t *testing.T, selectionRoot, stageRoot string) {
			candidate := seedFinalizedStage(t, stageRoot, "7.3.4", []byte("candidate-b"))
			writeSelection(t, selectionRoot, `{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":"sha256:`+strings.Repeat("a", 64)+`"}`)
			if candidate.ArtifactID == artifact.ID("sha256:"+strings.Repeat("a", 64)) {
				t.Fatal("fixture digest collision")
			}
		},
		"stage tamper": func(t *testing.T, selectionRoot, stageRoot string) {
			candidate := seedFinalizedStage(t, stageRoot, "7.3.4", []byte("candidate-b"))
			if err := os.WriteFile(candidate.ExecutablePath, []byte("tampered"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeSelection(t, selectionRoot, selectionJSON(candidate))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stageRoot := filepath.Join(root, "artifacts")
			stages := newStageStore(t, stageRoot)
			selectionRoot := filepath.Join(root, "active")
			if err := os.MkdirAll(selectionRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			mutate(t, selectionRoot, stageRoot)
			store, err := NewStore(selectionRoot, stages)
			if err != nil {
				t.Fatal(err)
			}
			if selected, err := store.Load(Bundled("/bundled", "")); !errors.Is(err, ErrSelectionInvalid) || selected.ExecutablePath != "" {
				t.Fatalf("Load() = %+v, %v", selected, err)
			}
		})
	}
}

func TestSelectionStoreRejectsUntrustedParentShape(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, parent string) {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, parent); err != nil {
				t.Fatal(err)
			}
		},
		"group writable": func(t *testing.T, parent string) {
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o770); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stages := newStageStore(t, filepath.Join(root, "artifacts"))
			parent := filepath.Join(root, "active")
			prepare(t, parent)
			if _, err := NewStore(filepath.Join(parent, "cpa"), stages); !errors.Is(err, ErrSelectionInvalid) {
				t.Fatalf("NewStore() error = %v, want %v", err, ErrSelectionInvalid)
			}
			if _, err := os.Lstat(filepath.Join(parent, "cpa")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("untrusted parent gained selection root: %v", err)
			}
		})
	}
}

func TestSelectionStoreAcceptsPlatformNativeDirectoryPermissions(t *testing.T) {
	root := t.TempDir()
	stages := newStageStore(t, filepath.Join(root, "artifacts"))
	parent := filepath.Join(root, "active")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(parent, "cpa"), stages); err != nil {
		t.Fatalf("NewStore() rejected platform-native directory permissions: %v", err)
	}
}

func TestSelectionCommitClassifiesPrePublishAndPostPublishAmbiguity(t *testing.T) {
	root := t.TempDir()
	stageRoot := filepath.Join(root, "artifacts")
	stages := newStageStore(t, stageRoot)
	candidate := seedFinalizedStage(t, stageRoot, "7.3.4", []byte("candidate-b"))

	preRoot := filepath.Join(root, "pre")
	pre, err := newStore(preRoot, stages, func(string, string) error { return errors.New("rename denied") }, syncDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.Commit(candidate); !errors.Is(err, ErrSelectionNotPublished) {
		t.Fatalf("Commit(pre-publish) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(preRoot, selectionName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-publish selection exists: %v", err)
	}

	ambiguousRoot := filepath.Join(root, "ambiguous")
	ambiguous, err := newStore(ambiguousRoot, stages, os.Rename, func(string) error { return errors.New("sync denied") })
	if err != nil {
		t.Fatal(err)
	}
	if err := ambiguous.Commit(candidate); !errors.Is(err, ErrSelectionPublicationAmbiguous) {
		t.Fatalf("Commit(ambiguous) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ambiguousRoot, selectionName)); err != nil {
		t.Fatalf("ambiguous publication should exist: %v", err)
	}
}

func newStageStore(t *testing.T, root string) *runtimeupdate.Store {
	t.Helper()
	store, err := runtimeupdate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func seedFinalizedStage(t *testing.T, root, version string, executable []byte) Descriptor {
	t.Helper()
	directory := filepath.Join(root, version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executable)
	artifactID := artifact.ID("sha256:" + hex.EncodeToString(digest[:]))
	executablePath := filepath.Join(directory, "cli-proxy-api")
	metadataPath := filepath.Join(directory, "artifact.json")
	if err := os.WriteFile(executablePath, executable, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"schemaVersion":1,"engine":"cpa","version":"` + version + `","artifactId":"` + string(artifactID) + `","sourceArchiveDigest":"sha256:` + strings.Repeat("b", 64) + `"}`
	if err := os.WriteFile(metadataPath, []byte(metadata), 0o444); err != nil {
		t.Fatal(err)
	}
	return Descriptor{
		Source: SourceFinalized, Version: version, ArtifactID: artifactID,
		ExecutablePath: executablePath, MetadataPath: metadataPath,
	}
}

func writeSelection(t *testing.T, root, encoded string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, selectionName), []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
}

func selectionJSON(candidate Descriptor) string {
	return `{"schemaVersion":1,"engine":"cpa","version":"` + candidate.Version + `","artifactId":"` + string(candidate.ArtifactID) + `"}`
}

func TestClearSelectionValidatesBundledAndRemovesSelectionAndProvenance(t *testing.T) {
	root := t.TempDir()
	stageRoot := filepath.Join(root, "artifacts")
	selectionRoot := filepath.Join(root, "active")
	stages := newStageStore(t, stageRoot)
	store, err := NewStore(selectionRoot, stages)
	if err != nil {
		t.Fatal(err)
	}
	candidate := seedFinalizedStage(t, stageRoot, "8.0.55", []byte("finalized"))
	if err := store.Commit(candidate); err != nil {
		t.Fatal(err)
	}
	provenance := Provenance{SchemaVersion: SchemaVersion, ArtifactID: candidate.ArtifactID, Origin: OriginBaselineAcquire, BaselineVersionAtCommit: candidate.Version, OperationID: "selection-test"}
	if err := store.WriteProvenance(provenance); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "bundled")
	bundled := Bundled(path, "")
	for _, prepare := range []func() error{
		func() error { return nil },
		func() error { return os.Symlink(candidate.ExecutablePath, path) },
		func() error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("bundled"), 0o644)
		},
	} {
		if err := prepare(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Clear(bundled); !errors.Is(err, ErrSelectionInvalid) {
			t.Fatalf("Clear invalid executable = %v", err)
		}
		if got, err := store.Load(bundled); err != nil || got != candidate {
			t.Fatalf("invalid clear changed selection: %+v, %v", got, err)
		}
		if got, err := store.LoadProvenance(); err != nil || got != provenance {
			t.Fatalf("invalid clear changed provenance: %+v, %v", got, err)
		}
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got, err := store.Clear(bundled); err != nil || got != bundled {
			t.Fatalf("Clear = %+v, %v", got, err)
		}
	}
	reopened, err := NewStore(selectionRoot, stages)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Load(bundled); err != nil || got != bundled {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if _, err := reopened.LoadProvenance(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provenance still present: %v", err)
	}
}

func TestClearSelectionClassifiesPersistenceFailures(t *testing.T) {
	for _, scenario := range []string{"selection removal", "provenance removal", "directory sync"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(filepath.Join(root, "active"), newStageStore(t, filepath.Join(root, "artifacts")))
			if err != nil {
				t.Fatal(err)
			}
			bundled := Bundled(filepath.Join(root, "bundled"), "")
			if err := os.WriteFile(bundled.ExecutablePath, []byte("bundle"), 0o755); err != nil {
				t.Fatal(err)
			}
			want := ErrSelectionPublicationAmbiguous
			if scenario == "directory sync" {
				store.syncRoot = func(string) error { return os.ErrPermission }
			} else {
				name := provenanceName
				if scenario == "selection removal" {
					name = selectionName
					want = ErrSelectionNotPublished
				}
				if err := os.MkdirAll(filepath.Join(store.root, name, "retained"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Clear(bundled); !errors.Is(err, want) {
				t.Fatalf("Clear error = %v, want %v", err, want)
			}
		})
	}
}
