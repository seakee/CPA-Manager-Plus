package main

import (
	"context"
	"io"
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

type gatedEmbeddedArtifactRuntime struct {
	app *freshApplication
}

func (g gatedEmbeddedArtifactRuntime) enterRead(ctx context.Context) (*runtimeservice.ArtifactClient, context.Context, func(), error) {
	if g.app == nil || g.app.artifact == nil {
		return nil, nil, nil, errEmbeddedRuntimeDisabled
	}
	callCtx, release, err := g.app.enterEmbeddedRead(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if g.app.artifact == nil {
		release()
		return nil, nil, nil, errEmbeddedRuntimeDisabled
	}
	return g.app.artifact, callCtx, release, nil
}

func (g gatedEmbeddedArtifactRuntime) enterMutation(ctx context.Context) (*runtimeservice.ArtifactClient, func(), error) {
	if g.app == nil || g.app.artifact == nil {
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	release, err := g.app.enterEmbeddedMutation(ctx)
	if err != nil {
		return nil, nil, err
	}
	if g.app.artifact == nil {
		release()
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	return g.app.artifact, release, nil
}

func (g gatedEmbeddedArtifactRuntime) Status(ctx context.Context) (runtimeservice.ArtifactStatus, error) {
	client, callCtx, release, err := g.enterRead(ctx)
	if err != nil {
		return runtimeservice.ArtifactStatus{}, err
	}
	out, callErr := client.Status(callCtx)
	release()
	return out, g.app.wrapEmbeddedReadErr(ctx, callErr)
}

func (g gatedEmbeddedArtifactRuntime) Probe(ctx context.Context) (runtimeservice.ArtifactProbeResult, error) {
	client, callCtx, release, err := g.enterRead(ctx)
	if err != nil {
		return runtimeservice.ArtifactProbeResult{}, err
	}
	out, callErr := client.Probe(callCtx)
	release()
	return out, g.app.wrapEmbeddedReadErr(ctx, callErr)
}

func (g gatedEmbeddedArtifactRuntime) Acquire(ctx context.Context, source string, cdnPrefix string) (runtimeservice.ArtifactOperationEnvelope, error) {
	client, release, err := g.enterMutation(ctx)
	if err != nil {
		return runtimeservice.ArtifactOperationEnvelope{}, err
	}
	defer release()
	return client.Acquire(ctx, source, cdnPrefix)
}

func (g gatedEmbeddedArtifactRuntime) Upload(ctx context.Context, body io.Reader, contentLength int64) (runtimeservice.ArtifactOperationEnvelope, error) {
	client, release, err := g.enterMutation(ctx)
	if err != nil {
		return runtimeservice.ArtifactOperationEnvelope{}, err
	}
	defer release()
	return client.Upload(ctx, body, contentLength)
}

func (a *freshApplication) artifactRuntime(op bootstrap.FreshOperation) artifactproxy.ArtifactRuntime {
	if a == nil || a.artifact == nil || op.Strategy.Strategy == bootstrap.ConnectExternal {
		return nil
	}
	return gatedEmbeddedArtifactRuntime{app: a}
}

func (a *freshApplication) runtimeArtifact(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	artifactproxy.ServeResource(w, r, a.artifactRuntime(op), resource)
}
