package main

import (
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/artifactproxy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type freshArtifactErrorResponse = artifactproxy.ErrorResponse

func runtimeArtifactResource(resource string) bool {
	return artifactproxy.IsResource(resource)
}

func (a *freshApplication) fixedArtifact(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, bootstrap.FixedBootstrapAPIPath)
	a.runtimeArtifact(w, r, op, resource)
}

func (a *freshApplication) artifactClient(op bootstrap.FreshOperation) *runtimeservice.ArtifactClient {
	if a == nil || op.Strategy.Strategy == bootstrap.ConnectExternal {
		return nil
	}
	return a.artifact
}

func (a *freshApplication) runtimeArtifact(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	var runtime artifactproxy.ArtifactRuntime
	if artifact := a.artifactClient(op); artifact != nil {
		runtime = artifact
	}
	artifactproxy.ServeResource(w, r, runtime, resource)
}
