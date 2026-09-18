package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/protocol"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

type runtimeHandler struct {
	http.Handler
	executor *lifecycle.Executor
}

func validateLifecycleConfig(journalPath, executable, artifactManifest string) error {
	if (journalPath == "") != (executable == "") {
		return errors.New("CPAMP_RUNTIME_JOURNAL_PATH and CPAMP_CPA_EXECUTABLE must be configured together")
	}
	if strings.ContainsRune(executable, '\x00') {
		return errors.New("CPAMP_CPA_EXECUTABLE must not contain NUL")
	}
	if artifactManifest != "" && executable == "" {
		return errors.New("CPAMP_CPA_ARTIFACT_MANIFEST requires CPAMP_CPA_EXECUTABLE")
	}
	if strings.ContainsRune(artifactManifest, '\x00') {
		return errors.New("CPAMP_CPA_ARTIFACT_MANIFEST must not contain NUL")
	}
	return nil
}

// newRuntimeHandler opens the private journal once at Supervisor startup. HTTP
// submissions share this resource and the same child ownership/serialization.
func newRuntimeHandler(ctx context.Context, cfg config) (*runtimeHandler, error) {
	if err := validateLifecycleConfig(cfg.journalPath, cfg.cpaExecutable, cfg.cpaArtifactManifest); err != nil {
		return nil, err
	}
	if err := readiness.ValidateAddress(cfg.cpaAddr); err != nil {
		return nil, fmt.Errorf("CPAMP_RUNTIME_CPA_ADDR: %w", err)
	}
	settings := protocol.Config{
		RuntimeIdentity:   cfg.runtimeIdentity,
		RuntimeGeneration: cfg.runtimeGeneration,
		Token:             cfg.token,
	}
	runtime := &runtimeHandler{}
	if cfg.journalPath != "" {
		supervisorRoot := filepath.Dir(cfg.journalPath)
		stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
		var stageStore *runtimeupdate.Store
		var err error
		if cfg.cpaIdentity == nil {
			stageStore, err = runtimeupdate.NewStore(stageRoot)
		} else {
			stageStore, err = runtimeupdate.NewStoreWithExecutionGroup(stageRoot, cfg.cpaIdentity.GID)
		}
		if err != nil {
			return nil, fmt.Errorf("configure trusted stage storage: %w", err)
		}
		selectionStore, err := selection.NewStore(filepath.Join(supervisorRoot, "active", "cpa"), stageStore)
		if err != nil {
			return nil, fmt.Errorf("configure active selection storage: %w", err)
		}
		selected, err := selectionStore.Load(selection.Bundled(cfg.cpaExecutable, cfg.cpaArtifactManifest))
		if err != nil {
			return nil, fmt.Errorf("resolve active selection: %w", err)
		}
		artifactObserver := artifact.NewObserver(selected.ExecutablePath, selected.MetadataPath)
		refreshArtifact := func(spec cpaprocess.StartSpec) error {
			metadataPath := cfg.cpaArtifactManifest
			staged := spec.Executable != cfg.cpaExecutable
			if staged {
				metadataPath = filepath.Join(filepath.Dir(spec.Executable), "artifact.json")
			}
			if err := artifactObserver.RefreshFrom(spec.Executable, metadataPath); err != nil {
				log.Printf("active Gateway artifact metadata is incomplete: %v", err)
				if staged {
					return err
				}
			}
			return nil
		}
		initialRefreshErr := artifactObserver.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)
		if selected.IsFinalizedStage() && initialRefreshErr != nil {
			return nil, fmt.Errorf("revalidate persisted active selection: %w", initialRefreshErr)
		}
		if initialRefreshErr != nil {
			log.Printf("active Gateway artifact metadata is incomplete: %v", initialRefreshErr)
		}
		child := cpaprocess.NewManager(refreshArtifact)
		if cfg.cpaIdentity != nil {
			child, err = cpaprocess.NewManagerWithIdentity(refreshArtifact, *cfg.cpaIdentity)
			if err != nil {
				return nil, fmt.Errorf("configure CPA child identity: %w", err)
			}
		}
		observer, err := readiness.New(child, cfg.cpaAddr)
		if err != nil {
			return nil, err
		}
		store, err := journal.Open(ctx, cfg.journalPath, journal.Options{})
		if err != nil {
			return nil, fmt.Errorf("open operation journal: %w", err)
		}
		runtime.executor, err = lifecycle.NewExecutor(journal.Authority{
			RuntimeIdentity:   strings.TrimSpace(cfg.runtimeIdentity),
			RuntimeGeneration: cfg.runtimeGeneration,
		}, store, child, selected.ExecutablePath)
		if err != nil {
			return nil, errors.Join(err, store.Close())
		}
		settings.ObserveUpdate = runtime.executor
		if runtimeupdate.SupportedPlatform(goruntime.GOOS, goruntime.GOARCH) {
			if err := runtime.executor.EnableActivateUpdate(selected, artifactObserver, selectionStore, observer); err != nil {
				return nil, errors.Join(fmt.Errorf("enable update activation: %w", err), runtime.executor.Close())
			}
			settings.ActivateUpdate = runtime.executor
			preparer, prepareErr := runtimeupdate.NewPreparerWithStore(stageStore)
			if prepareErr != nil {
				return nil, errors.Join(fmt.Errorf("configure trusted update staging: %w", prepareErr), runtime.executor.Close())
			}
			if prepareErr := runtime.executor.EnablePrepareUpdate(artifactObserver, preparer); prepareErr != nil {
				return nil, errors.Join(fmt.Errorf("enable trusted update staging: %w", prepareErr), runtime.executor.Close())
			}
			settings.PrepareUpdate = runtime.executor
		}
		settings.Start = runtime.executor
		settings.Stop = runtime.executor
		settings.Restart = runtime.executor
		settings.Status = observer
		settings.Recovery = runtime.executor
		settings.Artifact = artifactObserver
	}
	handler, err := protocol.NewHandler(settings)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("configure Runtime Protocol: %w", err), runtime.Close())
	}
	runtime.Handler = handler
	return runtime, nil
}

func (r *runtimeHandler) Close() error {
	if r.executor == nil {
		return nil
	}
	return r.executor.Close()
}

func (r *runtimeHandler) CloseAdmission() {
	if r.executor != nil {
		r.executor.CloseAdmission()
	}
}
