package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func authorityResponse(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority", nil))
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private authority response is cacheable")
	}
	return response
}

func TestFreshAuthorityAdmissionIsCommittedAndProvenOnly(t *testing.T) {
	op := bootstrap.FreshOperation{
		Identity: bootstrap.FreshIdentity{InstallationID: "install-one", Generation: "generation-one", OperationID: "fresh-operation"},
		Routing:  bootstrap.FreshRoutingCheckpoint{OperationID: "fresh-operation", Revision: 4, Target: "/control", Committed: true},
	}
	for _, test := range []struct {
		name     string
		op       bootstrap.FreshOperation
		proven   bool
		admitted bool
	}{
		{name: "committed-and-proven", op: op, proven: true, admitted: true},
		{name: "committed-but-unproven", op: op, proven: false, admitted: false},
		{name: "proven-but-uncommitted", op: func() bootstrap.FreshOperation {
			pending := op
			pending.Routing.Committed = false
			return pending
		}(), proven: true, admitted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reply := freshAuthorityReplyFor(test.op, test.proven)
			if reply.GatewayAdmission.Admitted != test.admitted {
				t.Fatalf("admitted = %v, want %v", reply.GatewayAdmission.Admitted, test.admitted)
			}
			// The posture is always bound to the installation generation it was
			// computed from, admitted or not.
			if reply.GatewayAdmission.InstallationID != test.op.Identity.InstallationID ||
				reply.GatewayAdmission.Generation != test.op.Identity.Generation {
				t.Fatalf("admission is not bound to the checkpoint: %+v", reply.GatewayAdmission)
			}
			if reply.Target != test.op.Routing.Target || reply.Revision != test.op.Routing.Revision || reply.Committed != test.op.Routing.Committed {
				t.Fatalf("routing projection changed: %+v", reply.State)
			}
		})
	}
}

func TestFreshAuthorityWithoutManagedAuthorityNeverAdmits(t *testing.T) {
	app := newInstallationTestApplication(t)
	// A Fresh application without an Embedded Runtime holds no managed CPA
	// authority: an External one may exist, but admission is a managed fact and
	// the External profile never answers it.
	if app.keyApply != nil && app.keyApply.Profile() == freshkeyapply.ProfileManaged {
		t.Fatal("test application unexpectedly has a managed key authority")
	}
	response := authorityResponse(t, app)
	if response.Code != http.StatusOK {
		t.Fatalf("authority status = %d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"installationId", "generation", "operationId", "revision", "target", "committed", "gatewayAdmission"}
	if len(body) != len(want) {
		t.Fatalf("authority fields = %v, want %v", body, want)
	}
	for _, field := range want {
		if _, ok := body[field]; !ok {
			t.Fatalf("authority is missing %q: %v", field, body)
		}
	}
	admission, ok := body["gatewayAdmission"].(map[string]any)
	if !ok || len(admission) != 3 {
		t.Fatalf("admission posture = %v", body["gatewayAdmission"])
	}
	if admission["admitted"] != false {
		t.Fatalf("admission without an install-derived authority = %v", admission)
	}
	if admission["installationId"] != body["installationId"] || admission["generation"] != body["generation"] {
		t.Fatalf("admission is not bound to the reply: %v / %v", admission, body)
	}
	if _, leaked := admission["managementKey"]; leaked {
		t.Fatal("admission exposed a credential")
	}
	if app.gatewayAdmissionProven(t.Context()) {
		t.Fatal("nil managed authority proved Gateway admission")
	}
}

func TestFreshAuthorityRequiresAPublishedRoutingProjection(t *testing.T) {
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	response := authorityResponse(t, app)
	if response.Code != http.StatusNotFound {
		t.Fatalf("authority before routing = %d, want 404", response.Code)
	}
	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/internal/routing/fresh-authority", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("authority mutation = %d, want 405", response.Code)
	}
	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority?legacy=true", nil))
	if response.Code == http.StatusOK {
		t.Fatal("authority accepted a query selector")
	}
}

func TestFreshAuthorityAdmissionRemainsProvenAcrossConsoleFileLoggingToggle(t *testing.T) {
	adoptInstall := func(r *freshstore.Record, env managedInstallEnv) {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.AdoptExisting,
			SourceClass:                   bootstrap.AdoptNetworkExisting,
			SourceIdentity:                "external:sha256:" + strings.Repeat("b", 64),
			Stage:                         "committed",
			TargetIdentity:                string(env.status.Identity),
			TargetGeneration:              uint64(env.status.Generation),
			TargetArtifactID:              string(env.status.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.AdoptedDestinationIdentity(env.endpoint),
			AdoptionResources:             []bootstrap.ResourceRef{bootstrap.AdoptionRefClientAPIKeys},
			AdoptionDecisions: []bootstrap.AdoptionResourceDecision{
				{
					ResourceRef:       bootstrap.AdoptionRefClientAPIKeys,
					Kind:              bootstrap.APIKeys,
					Scope:             bootstrap.ClientAPIKeys,
					ReadEvidenceRef:   "preflight:keys-read",
					ReplayEvidenceRef: "preflight:keys-replay",
					VerifyEvidenceRef: "preflight:keys-verify",
					Qualification:     bootstrap.CapabilityQualified,
					Disposition:       bootstrap.Migrate,
				},
			},
		}
		r.AdoptionCiphertext = env.protected
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: env.endpoint, ManagementKey: env.protected}
		r.Operation.ProductComplete = true
	}

	for _, tc := range []struct {
		name  string
		setup managedInstallSetup
	}{
		{name: "new_managed", setup: completedManagedInstall},
		{name: "adopt_existing", setup: adoptInstall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newManagedInstallHarnessWith(t, tc.setup, true)
			assertAdmission := func(step string) {
				t.Helper()
				authority := authorityResponse(t, harness.app)
				if authority.Code != http.StatusOK {
					t.Fatalf("%s: fresh-authority status = %d %s", step, authority.Code, authority.Body.String())
				}
				var reply struct {
					GatewayAdmission struct {
						Admitted bool `json:"admitted"`
					} `json:"gatewayAdmission"`
				}
				if err := json.Unmarshal(authority.Body.Bytes(), &reply); err != nil {
					t.Fatalf("%s: decode fresh-authority: %v", step, err)
				}
				if !reply.GatewayAdmission.Admitted {
					t.Fatalf("%s: gatewayAdmission.admitted = false, want true", step)
				}
				keysRes := harness.call(t, http.MethodGet, "api-keys", "")
				if keysRes.Code != http.StatusOK {
					t.Fatalf("%s: GET api-keys = %d %s", step, keysRes.Code, keysRes.Body.String())
				}
				var keysView struct {
					GatewayPending bool `json:"gatewayPending"`
				}
				if err := json.Unmarshal(keysRes.Body.Bytes(), &keysView); err != nil {
					t.Fatalf("%s: decode api-keys: %v", step, err)
				}
				if keysView.GatewayPending {
					t.Fatalf("%s: api-keys gatewayPending = true, want false", step)
				}
			}

			assertAdmission("baseline")

			enableRes := harness.call(t, http.MethodPut, "cpamp/v0/management/logging-to-file", `{"value":true}`)
			if enableRes.Code != http.StatusOK {
				t.Fatalf("PUT cpamp/v0/management/logging-to-file true = %d %s", enableRes.Code, enableRes.Body.String())
			}
			harness.destination.mu.Lock()
			gotEnabled := harness.destination.loggingToFile
			harness.destination.mu.Unlock()
			if !gotEnabled {
				t.Fatal("destination CPA did not receive logging-to-file=true")
			}
			assertAdmission("after enabling file logging")

			disableRes := harness.call(t, http.MethodPut, "cpamp/v0/management/logging-to-file", `{"value":false}`)
			if disableRes.Code != http.StatusOK {
				t.Fatalf("PUT cpamp/v0/management/logging-to-file false = %d %s", disableRes.Code, disableRes.Body.String())
			}
			harness.destination.mu.Lock()
			gotDisabled := !harness.destination.loggingToFile
			harness.destination.mu.Unlock()
			if !gotDisabled {
				t.Fatal("destination CPA did not receive logging-to-file=false")
			}
			assertAdmission("after disabling file logging")
		})
	}
}
