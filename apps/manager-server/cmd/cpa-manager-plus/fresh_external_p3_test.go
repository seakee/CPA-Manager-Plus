package main

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// TestExternalReconcileNeverPresentsARejectedKey is the reconcile leftover P3: the
// reconcile POST asks the CPA for the source record, so it presents the stored
// credential exactly like the config read does. A credential the CPA already
// refused must never be presented again, and the plain inspection — which answers
// from the journal alone — must stay available without contacting the CPA either.
func TestExternalReconcileNeverPresentsARejectedKey(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	// The typed read learns the refusal the config page would otherwise repeat.
	fake.management.Store(strings.Repeat("e", 64))
	if response := gatewayRequest(t, app, http.MethodGet, "external/usage", "", ""); response.Code != http.StatusConflict {
		t.Fatalf("usage read = %d %s", response.Code, response.Body.String())
	}
	before := fake.requests.Load()
	reconcile := gatewayRequest(t, app, http.MethodPost,
		"gateway/config/operations/"+strings.Repeat("a", 32)+"/reconcile", `{}`, "application/json")
	if reconcile.Code != http.StatusConflict || errorCode(t, reconcile) != freshconfig.ReasonManagementRejected {
		t.Fatalf("reconcile = %d %s", reconcile.Code, reconcile.Body.String())
	}
	if sent := fake.requests.Load() - before; sent != 0 {
		t.Fatalf("the reconcile presented the refused key %d times", sent)
	}
	// The plain inspection never reconciles, so it stays a journal read.
	before = fake.requests.Load()
	inspect := gatewayRequest(t, app, http.MethodGet,
		"gateway/config/operations/"+strings.Repeat("a", 32), "", "")
	if inspect.Code != http.StatusNotFound {
		t.Fatalf("inspection = %d %s", inspect.Code, inspect.Body.String())
	}
	if sent := fake.requests.Load() - before; sent != 0 {
		t.Fatalf("the inspection presented the refused key %d times", sent)
	}
}

// TestExternalUsageStateAnswersARefusalBeforeResolvingTheSource is the usage
// leftover P3: the refused check must run before the read does its own source
// resolution, so a refused read resolves the installation exactly once — inside
// the grant — and contacts the CPA not at all.
func TestExternalUsageStateAnswersARefusalBeforeResolvingTheSource(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	fake.management.Store(strings.Repeat("d", 64))
	if response := gatewayRequest(t, app, http.MethodGet, "external/usage", "", ""); response.Code != http.StatusConflict {
		t.Fatalf("usage read = %d %s", response.Code, response.Body.String())
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal was not recorded")
	}
	var resolved atomic.Int32
	inner := app.externalSource
	app.externalSource = func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
		resolved.Add(1)
		return inner(ctx)
	}
	requests := fake.requests.Load()
	if _, err := app.externalUsageState(t.Context()); err == nil {
		t.Fatal("a refused credential still produced a usage state")
	}
	if resolved.Load() != 1 {
		t.Fatalf("the refused read resolved the installation %d times, want only the grant's own", resolved.Load())
	}
	if sent := fake.requests.Load() - requests; sent != 0 {
		t.Fatalf("the refused read contacted the CPA %d times", sent)
	}
}
