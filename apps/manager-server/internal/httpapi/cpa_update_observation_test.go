package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func cpaUpdateObservationTarget(phase string) string {
	return "/usage-service/runtime/updates/operations/" + phase + "?request_id=web-recover&target_version=7.3.4"
}

func cpaUpdateObservationStatus() model.RuntimeObservedStatus {
	status := cpaUpdateReadyStatus()
	status.Generation = 42
	status.State = model.RuntimeStateStarting
	status.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityObserveUpdateOperation}
	return status
}

func cpaUpdateObservedOperation(request model.RuntimeObserveUpdateOperationRequest) model.RuntimeUpdateOperationObservation {
	created := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	completed := created.Add(time.Second)
	return model.RuntimeUpdateOperationObservation{
		OperationID: request.OperationID, OperationType: request.OperationType,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: 41,
		State: model.RuntimeOperationFailed, FailureCode: "activation_readiness_failed",
		CreatedAt: created, UpdatedAt: completed, CompletedAt: &completed,
	}
}

func TestCPAUpdateObservationEndpointsAuthenticateAndReturnDurableEvidence(t *testing.T) {
	for _, phase := range []string{"prepare", "activate"} {
		t.Run(phase, func(t *testing.T) {
			runtimeClient := &cpaUpdateRuntimeStub{
				status: cpaUpdateObservationStatus(),
				observeFn: func(request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
					digest := sha256.Sum256([]byte("web-recover"))
					wantID := fmt.Sprintf("manager-cpa-update/v1:%s:%x", phase, digest)
					if request.OperationID != wantID || string(request.OperationType) != phase+"_update" ||
						request.ExpectedRuntimeIdentity != "runtime-http-test" || request.ExpectedRuntimeGeneration != 42 ||
						request.TargetVersion != "7.3.4" {
						t.Fatalf("typed Runtime request = %+v", request)
					}
					return cpaUpdateObservedOperation(request), nil
				},
			}
			server := newCPAUpdateServer(t, runtimeClient)
			request := httptest.NewRequest(http.MethodGet, cpaUpdateObservationTarget(phase), nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" ||
				runtimeClient.statusCalls.Load() != 0 || runtimeClient.observeCalls.Load() != 0 {
				t.Fatalf("unauthorized observation = %d %s", response.Code, response.Body.String())
			}
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			response = httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			var got cpaupdateservice.ObservationResult
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
				string(got.Phase) != phase || got.RequestID != "web-recover" || got.TargetVersion != "7.3.4" ||
				got.State != cpaupdateservice.ObservationStateFailed || got.FailureCode != "activation_readiness_failed" ||
				got.CreatedAt == nil || got.UpdatedAt == nil || got.CompletedAt == nil {
				t.Fatalf("normalized observation = %d %+v", response.Code, got)
			}
			if runtimeClient.statusCalls.Load() != 1 || runtimeClient.observeCalls.Load() != 1 ||
				runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
				t.Fatal("observation performed unexpected Runtime calls")
			}
		})
	}
}

func TestCPAUpdateObservationStrictQueryAndEmptyBody(t *testing.T) {
	base := cpaUpdateObservationTarget("prepare")
	tests := map[string]struct{ target, body, method string }{
		"missing request":        {target: strings.Replace(base, "request_id=web-recover&", "", 1)},
		"missing target":         {target: strings.Replace(base, "&target_version=7.3.4", "", 1)},
		"duplicate request":      {target: base + "&request_id=other"},
		"duplicate target":       {target: base + "&target_version=7.3.5"},
		"raw operation ID":       {target: base + "&operationId=raw-op"},
		"raw Runtime generation": {target: base + "&expectedRuntimeGeneration=42"},
		"artifact guard":         {target: base + "&expected_active_artifact_id=" + cpaUpdateTestArtifactID},
		"bad escape":             {target: base + "&extra=%zz"},
		"invalid ID":             {target: strings.Replace(base, "web-recover", url.QueryEscape("foo/bar"), 1)},
		"oversized ID":           {target: strings.Replace(base, "web-recover", strings.Repeat("a", 65), 1)},
		"invalid version":        {target: strings.Replace(base, "7.3.4", "latest", 1)},
		"non-empty body":         {target: base, body: "{}"},
		"POST":                   {target: base, method: http.MethodPost},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateObservationStatus()}
			server := newCPAUpdateServer(t, runtimeClient)
			method := test.method
			if method == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, test.target, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			wantStatus := http.StatusBadRequest
			if method != http.MethodGet {
				wantStatus = http.StatusMethodNotAllowed
			}
			if response.Code != wantStatus || response.Header().Get("Cache-Control") != "no-store" ||
				runtimeClient.statusCalls.Load() != 0 || runtimeClient.observeCalls.Load() != 0 ||
				runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("invalid observation = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCPAUpdateObservationNormalizesMissingAndRedactsUnavailableEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"missing", &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorOperationNotFound, HTTPStatus: 404}, 200, "\"state\":\"not_found\""},
		{"conflict", &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorOperationIDConflict, HTTPStatus: 409}, 409, "\"code\":\"operation_id_conflict\""},
		{"identity", &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorRuntimeIdentityMismatch, HTTPStatus: 409}, 409, "\"code\":\"runtime_identity_mismatch\""},
		{"generation", &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorStaleRuntimeGeneration, HTTPStatus: 409}, 409, "\"code\":\"stale_runtime_generation\""},
		{"raw transport", errors.New("/private/runtime journal token=secret"), 503, "\"code\":\"runtime_transport_unavailable\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateObservationStatus(),
				observeFn: func(model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
					return model.RuntimeUpdateOperationObservation{}, test.err
				},
			}
			server := newCPAUpdateServer(t, runtimeClient)
			request := httptest.NewRequest(http.MethodGet, cpaUpdateObservationTarget("activate"), nil)
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" ||
				!strings.Contains(response.Body.String(), test.body) || strings.Contains(response.Body.String(), "secret") ||
				strings.Contains(response.Body.String(), "/private/") || strings.Contains(response.Body.String(), "already_applied") {
				t.Fatalf("observation error = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCPAUpdateObservationExternalMakesNoRuntimeCalls(t *testing.T) {
	runtimeClient := &cpaUpdateRuntimeStub{}
	server := newCPAUpdateServer(t, runtimeClient)
	server.AppContext().CPAUpdateService = cpaupdateservice.New(server.AppContext().Store, runtimeClient, model.RuntimeModeExternal)
	request := httptest.NewRequest(http.MethodGet, cpaUpdateObservationTarget("prepare"), nil)
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "\"code\":\"managed_externally\"") ||
		runtimeClient.statusCalls.Load() != 0 || runtimeClient.observeCalls.Load() != 0 ||
		runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
		t.Fatalf("External observation = %d %s", response.Code, response.Body.String())
	}
}

func TestCPAUpdateObservationUsesEmbeddedGETAndHidesRuntimeMessage(t *testing.T) {
	var paths []string
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private-runtime-token" {
			t.Error("Manager did not use authenticated read-only transport")
		}
		if r.URL.Path == "/v1/runtime/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion": "v1", "runtimeIdentity": "runtime-01", "runtimeGeneration": 7,
				"state": "starting", "capabilities": []string{"observe_update_operation"},
			})
			return
		}
		if r.URL.Path != "/v1/runtime/operations/update" || r.URL.Query().Get("expectedRuntimeGeneration") != "7" ||
			r.URL.Query().Get("operationType") != "activate_update" || r.URL.Query().Get("targetVersion") != "7.3.4" {
			t.Errorf("unexpected Runtime operation request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operationId": r.URL.Query().Get("operationId"), "operationType": "activate_update",
			"runtimeIdentity": "runtime-01", "runtimeGeneration": 41, "state": "failed",
			"createdAt": "2026-09-18T09:00:00Z", "updatedAt": "2026-09-18T09:00:01Z",
			"completedAt": "2026-09-18T09:00:01Z",
			"error":       map[string]string{"code": "activation_readiness_failed", "message": "/private/runtime private-runtime-token"},
		})
	}))
	defer supervisor.Close()
	server := newCPAUpdateServer(t, runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token"))
	request := httptest.NewRequest(http.MethodGet, cpaUpdateObservationTarget("activate"), nil)
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request.WithContext(context.Background()))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "\"state\":\"failed\"") ||
		strings.Contains(response.Body.String(), "private-runtime-token") || strings.Contains(response.Body.String(), "/private/") ||
		strings.Join(paths, ",") != "/v1/runtime/status,/v1/runtime/operations/update" {
		t.Fatalf("Embedded observation = %d %s, paths=%v", response.Code, response.Body.String(), paths)
	}
}
