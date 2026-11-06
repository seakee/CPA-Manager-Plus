package main

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/privateconfig"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/protocol"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
)

// This private profile deliberately has no update, selection or adoption path.
// The ordinary qualified Runtime composition remains byte-for-byte unchanged.
func newPrivateRuntimeHandler(ctx context.Context, cfg config) (*runtimeHandler, error) {
	if !cfg.privateConfiguration || os.Geteuid() != 0 || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" ||
		cfg.addr != "127.0.0.1:9081" || cfg.runtimeIdentity != "embedded-auth-proof" ||
		cfg.cpaAddr != "127.0.0.1:8317" || cfg.journalPath != "/runtime/supervisor/operations.sqlite" ||
		cfg.cpaExecutable != "/usr/local/bin/cli-proxy-api" ||
		cfg.cpaArtifactManifest != "/usr/local/share/cpamp/runtime/cpa-artifact.json" ||
		cfg.cpaIdentity == nil || cfg.cpaIdentity.UID != 10001 || cfg.cpaIdentity.GID != 10001 {
		return nil, privateconfig.ErrStop
	}
	configuration := privateconfig.New("/runtime/supervisor/private-config", "/runtime/gateway/config.yaml",
		privateconfig.GrantPath, "/proc/self/ns/net", cfg.runtimeGeneration, 10001)
	observed := artifact.NewObserver(cfg.cpaExecutable, cfg.cpaArtifactManifest)
	refresh := func(spec cpaprocess.StartSpec) error {
		if spec.Executable != cfg.cpaExecutable || len(spec.Args) != 0 {
			return privateconfig.ErrStop
		}
		if e := configuration.CheckStartup(); e != nil {
			return e
		}
		if e := observed.RefreshFrom(cfg.cpaExecutable, cfg.cpaArtifactManifest); e != nil {
			return e
		}
		o := observed.Observation()
		if o == nil || string(o.ArtifactID) != privateconfig.Artifact || o.Version != "8.0.55" {
			return privateconfig.ErrStop
		}
		return nil
	}
	if e := observed.RefreshFrom(cfg.cpaExecutable, cfg.cpaArtifactManifest); e != nil {
		return nil, e
	}
	child, e := cpaprocess.NewManagerWithIdentity(refresh, *cfg.cpaIdentity)
	if e != nil {
		return nil, e
	}
	ready, e := readiness.New(child, cfg.cpaAddr)
	if e != nil {
		return nil, e
	}
	store, e := journal.Open(ctx, cfg.journalPath, journal.Options{})
	if e != nil {
		return nil, e
	}
	executor, e := lifecycle.NewExecutor(journal.Authority{RuntimeIdentity: cfg.runtimeIdentity, RuntimeGeneration: cfg.runtimeGeneration}, store, child, cfg.cpaExecutable)
	if e != nil {
		return nil, errors.Join(e, store.Close())
	}
	boundary, e := lifecycle.NewPrivateConfigurationBoundary(executor, configuration, observed, cfg.cpaArtifactManifest)
	if e != nil {
		return nil, errors.Join(e, executor.Close())
	}
	base, e := protocol.NewHandler(protocol.Config{RuntimeIdentity: cfg.runtimeIdentity, RuntimeGeneration: cfg.runtimeGeneration, Token: cfg.token,
		Start: executor, Stop: executor, Restart: executor, ObserveUpdate: executor, Status: ready, Recovery: executor, Artifact: observed})
	if e != nil {
		return nil, errors.Join(e, executor.Close())
	}
	return &runtimeHandler{Handler: protocol.WithPrivateConfiguration(base, boundary), executor: executor}, nil
}
