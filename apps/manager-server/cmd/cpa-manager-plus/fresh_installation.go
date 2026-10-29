package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// Observe through the existing Host-configured transport. Browser input cannot
// choose a Runtime endpoint, credential, generation or installation authority.
func (a *freshApplication) observeInstallationRuntime(ctx context.Context) (model.RuntimeObservedStatus, error) {
	if a.runtime == nil {
		return model.RuntimeObservedStatus{}, errors.New("installation Runtime is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := a.runtime.Status(ctx)
	if err == nil {
		err = s.Validate()
	}
	return s, err
}

func (a *freshApplication) installationRuntime(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if op.Strategy.Strategy != "" && !freshkeyapply.ManagerOwnedStrategy(op.Strategy.Strategy) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	s, err := a.observeInstallationRuntime(r.Context())
	if err != nil || s.ProtocolVersion != "v1" || s.ActiveGatewayArtifact == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// Deliberately omit transport tokens, filesystem paths and raw diagnostics.
	writeFreshJSON(w, installationRuntimeObservation{
		Identity: string(s.Identity), Generation: uint64(s.Generation),
		Artifact: installationArtifact{ID: string(s.ActiveGatewayArtifact.ArtifactID)}, State: s.State,
	})
}

type installationArtifact struct {
	ID string `json:"artifactId"`
}

type installationRuntimeObservation struct {
	Identity   string               `json:"runtimeIdentity"`
	Generation uint64               `json:"runtimeGeneration"`
	Artifact   installationArtifact `json:"activeGatewayArtifact"`
	State      model.RuntimeState   `json:"state"`
}

func (a *freshApplication) managedInstallationReady(ctx context.Context, op bootstrap.FreshOperation) bool {
	if !freshkeyapply.ManagerOwnedStrategy(op.Strategy.Strategy) || op.Strategy.Stage != "committed" {
		return false
	}
	s, err := a.observeInstallationRuntime(ctx)
	return err == nil && s.ProtocolVersion == "v1" && s.State == model.RuntimeStateReady && s.ActiveGatewayArtifact != nil &&
		string(s.Identity) == op.Strategy.TargetIdentity && uint64(s.Generation) == op.Strategy.TargetGeneration &&
		string(s.ActiveGatewayArtifact.ArtifactID) == op.Strategy.TargetArtifactID
}
