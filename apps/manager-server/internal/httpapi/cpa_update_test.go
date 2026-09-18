package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

const cpaUpdateTestArtifactID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type cpaUpdateRuntimeStub struct {
	mu sync.Mutex

	status     model.RuntimeObservedStatus
	err        error
	prepareFn  func(model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error)
	activateFn func(model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error)
	observeFn  func(model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error)

	statusCalls   atomic.Int32
	prepareCalls  atomic.Int32
	activateCalls atomic.Int32
	observeCalls  atomic.Int32
}

func (s *cpaUpdateRuntimeStub) Status(context.Context) (model.RuntimeObservedStatus, error) {
	s.statusCalls.Add(1)
	return s.status, s.err
}

func (*cpaUpdateRuntimeStub) Start(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Start mutation")
}

func (*cpaUpdateRuntimeStub) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Stop mutation")
}

func (*cpaUpdateRuntimeStub) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Restart mutation")
}

func (s *cpaUpdateRuntimeStub) PrepareUpdate(_ context.Context, request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	s.prepareCalls.Add(1)
	s.mu.Lock()
	fn := s.prepareFn
	s.mu.Unlock()
	if fn == nil {
		panic("unexpected Runtime PrepareUpdate mutation")
	}
	return fn(request)
}

func (s *cpaUpdateRuntimeStub) ActivateUpdate(_ context.Context, request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	s.activateCalls.Add(1)
	s.mu.Lock()
	fn := s.activateFn
	s.mu.Unlock()
	if fn == nil {
		panic("unexpected Runtime ActivateUpdate mutation")
	}
	return fn(request)
}

func (s *cpaUpdateRuntimeStub) ObserveUpdateOperation(_ context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	s.observeCalls.Add(1)
	s.mu.Lock()
	fn := s.observeFn
	s.mu.Unlock()
	if fn == nil {
		panic("unexpected Runtime update operation observation")
	}
	return fn(request)
}

func cpaUpdateOperation(request model.RuntimeMutationRequest, operationType model.RuntimeOperationType) model.RuntimeOperationResult {
	return model.RuntimeOperationResult{
		OperationID:       request.OperationID,
		OperationType:     operationType,
		RuntimeIdentity:   request.ExpectedRuntimeIdentity,
		RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}
}

func newCPAUpdateServer(t *testing.T, runtimeClient runtimeservice.RuntimeClient) *Server {
	t.Helper()
	cfg := config.Config{
		DBPath:      filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:       "usage",
		PopSide:     "right",
		CORSOrigins: []string{"*"},
	}
	database, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	testutil.EnsureAdminCredential(t, database)

	checkedAt := time.Now().UTC()
	state, err := json.Marshal(cpaupdateservice.DiscoveryState{
		SchemaVersion: 1,
		LastAttemptAt: checkedAt,
		LastSuccessAt: checkedAt,
		TargetVersion: "7.3.4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCPAUpdateCheck(t.Context(), state); err != nil {
		t.Fatalf("seed CPA update state: %v", err)
	}

	server := New(cfg, database, collector.NewManager(cfg, database))
	server.AppContext().CPAUpdateService = cpaupdateservice.New(database, runtimeClient, model.RuntimeModeEmbedded)
	return server
}

func cpaUpdateReadyStatus() model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity:           "runtime-http-test",
		Generation:         1,
		ProtocolVersion:    "v1",
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "7.3.3",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
			Engine:     "cpa",
			ArtifactID: cpaUpdateTestArtifactID,
			Version:    "7.3.3",
		},
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	}
}

func TestCPAUpdateEndpointsRequirePanelAuthAndDisableCaching(t *testing.T) {
	server := newCPAUpdateServer(t, &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()})
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/usage-service/runtime/updates"},
		{method: http.MethodPost, path: "/usage-service/runtime/updates/check"},
		{method: http.MethodPost, path: "/usage-service/runtime/updates/prepare"},
		{method: http.MethodPost, path: "/usage-service/runtime/updates/activate"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			unauthorized := httptest.NewRecorder()
			server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(test.method, test.path, nil))
			if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unauthorized response = %d, headers=%v", unauthorized.Code, unauthorized.Header())
			}

			var body *strings.Reader
			if strings.HasSuffix(test.path, "/prepare") || strings.HasSuffix(test.path, "/activate") {
				body = strings.NewReader(cpaUpdateMutationBody())
			} else {
				body = strings.NewReader("")
			}
			request := httptest.NewRequest(test.method, test.path, body)
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			response := httptest.NewRecorder()
			if strings.HasSuffix(test.path, "/prepare") {
				server.AppContext().CPAUpdateService = cpaupdateservice.New(server.AppContext().Store, &cpaUpdateRuntimeStub{
					status: cpaUpdateReadyStatus(),
					prepareFn: func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
						return cpaUpdateOperation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate), nil
					},
				}, model.RuntimeModeEmbedded)
			}
			if strings.HasSuffix(test.path, "/activate") {
				server.AppContext().CPAUpdateService = cpaupdateservice.New(server.AppContext().Store, &cpaUpdateRuntimeStub{
					status: cpaUpdateReadyStatus(),
					activateFn: func(request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
						return cpaUpdateOperation(request.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
					},
				}, model.RuntimeModeEmbedded)
			}
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			if strings.HasSuffix(test.path, "/prepare") || strings.HasSuffix(test.path, "/activate") {
				if !strings.Contains(response.Body.String(), `"state":"succeeded"`) ||
					!strings.Contains(response.Body.String(), `"runtime_operation_id":"manager-cpa-update/v1:`) {
					t.Fatalf("response body = %s", response.Body.String())
				}
			} else if !strings.Contains(response.Body.String(), `"state":"update_available"`) ||
				!strings.Contains(response.Body.String(), `"active_artifact_id":"`+cpaUpdateTestArtifactID+`"`) {
				t.Fatalf("response body = %s", response.Body.String())
			}
		})
	}
}

func cpaUpdateMutationBody() string {
	return `{"request_id":"web-0192ac","target_version":"7.3.4","expected_active_artifact_id":"` + cpaUpdateTestArtifactID + `"}`
}

func TestCPAUpdateEndpointReturns503WithoutLeakingRuntimeError(t *testing.T) {
	server := newCPAUpdateServer(t, &cpaUpdateRuntimeStub{err: errors.New("secret Runtime token failure")})
	request := httptest.NewRequest(http.MethodGet, "/usage-service/runtime/updates", nil)
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers=%v, body=%s", response.Code, response.Header(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret Runtime token") {
		t.Fatalf("response leaked Runtime error: %s", response.Body.String())
	}
}

func TestCPAUpdateEndpointRejectsRequestBody(t *testing.T) {
	server := newCPAUpdateServer(t, &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()})
	request := httptest.NewRequest(http.MethodPost, "/usage-service/runtime/updates/check", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers=%v, body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestCPAUpdateMutationStrictBody(t *testing.T) {
	valid := cpaUpdateMutationBody()
	tests := map[string]string{
		"empty":              "",
		"null":               "null",
		"array":              "[]",
		"unknown field":      strings.TrimSuffix(valid, "}") + `,"latest":true}`,
		"duplicate request":  strings.Replace(valid, `"request_id":"web-0192ac"`, `"request_id":"web-0192ac","request_id":"other"`, 1),
		"duplicate target":   strings.Replace(valid, `"target_version":"7.3.4"`, `"target_version":"7.3.4","target_version":"7.3.5"`, 1),
		"duplicate artifact": strings.Replace(valid, `"expected_active_artifact_id":"`+cpaUpdateTestArtifactID+`"`, `"expected_active_artifact_id":"`+cpaUpdateTestArtifactID+`","expected_active_artifact_id":"`+cpaUpdateTestArtifactID+`"`, 1),
		"missing request":    strings.Replace(valid, `"request_id":"web-0192ac",`, "", 1),
		"missing target":     strings.Replace(valid, `,"target_version":"7.3.4"`, "", 1),
		"missing artifact":   strings.Replace(valid, `,"expected_active_artifact_id":"`+cpaUpdateTestArtifactID+`"`, "", 1),
		"null field":         strings.Replace(valid, `"request_id":"web-0192ac"`, `"request_id":null`, 1),
		"field array":        strings.Replace(valid, `"request_id":"web-0192ac"`, `"request_id":[]`, 1),
		"trailing JSON":      valid + `{}`,
		"multiple values":    valid + ` null`,
		"invalid request ID": strings.Replace(valid, "web-0192ac", "foo/bar", 1),
		"invalid target":     strings.Replace(valid, "7.3.4", "7.3.4-rc.1", 1),
		"invalid artifact":   strings.Replace(valid, cpaUpdateTestArtifactID, "sha256:not-a-digest", 1),
		"invalid UTF-8":      string(append([]byte(`{"request_id":"`), 0xff)),
		"oversized":          `{"request_id":"` + strings.Repeat("a", 600) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
			server := newCPAUpdateServer(t, runtimeClient)
			request := httptest.NewRequest(http.MethodPost, "/usage-service/runtime/updates/prepare", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("response = %d, headers=%v, body=%s", response.Code, response.Header(), response.Body.String())
			}
			if runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("mutation calls prepare=%d activate=%d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
			}
		})
	}
}

func TestCPAUpdateMutationMapsAdmissionExecutionAndUnavailableErrors(t *testing.T) {
	t.Run("admission conflict", func(t *testing.T) {
		status := cpaUpdateReadyStatus()
		status.ActiveGatewayArtifact.ArtifactID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		runtimeClient := &cpaUpdateRuntimeStub{status: status}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/prepare", cpaUpdateMutationBody())
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"active_artifact_mismatch"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if runtimeClient.prepareCalls.Load() != 0 {
			t.Fatalf("PrepareUpdate calls = %d", runtimeClient.prepareCalls.Load())
		}
	})

	t.Run("admission missing activate capability", func(t *testing.T) {
		status := cpaUpdateReadyStatus()
		status.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityPrepareUpdate}
		runtimeClient := &cpaUpdateRuntimeStub{status: status}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/prepare", cpaUpdateMutationBody())
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"runtime_capability_unavailable"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
			t.Fatalf("unexpected calls: prepare=%d, activate=%d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
		}
	})

	t.Run("admission missing prepare capability", func(t *testing.T) {
		status := cpaUpdateReadyStatus()
		status.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityActivateUpdate}
		runtimeClient := &cpaUpdateRuntimeStub{status: status}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/activate", cpaUpdateMutationBody())
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"runtime_capability_unavailable"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
			t.Fatalf("unexpected calls: prepare=%d, activate=%d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
		}
	})

	t.Run("terminal execution failure", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
			result := cpaUpdateOperation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate)
			result.State = model.RuntimeOperationFailed
			result.Failure = &model.RuntimeOperationFailure{Code: "staging_failed", Message: "secret /stage/path"}
			return result, nil
		}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/prepare", cpaUpdateMutationBody())
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"failure_code":"staging_failed"`) ||
			!strings.Contains(response.Body.String(), `"state":"failed"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "/stage/path") {
			t.Fatalf("response leaked Runtime message: %s", response.Body.String())
		}
	})

	t.Run("terminal execution failure on activate", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		runtimeClient.activateFn = func(request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			result := cpaUpdateOperation(request.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate)
			result.State = model.RuntimeOperationFailed
			result.Failure = &model.RuntimeOperationFailure{Code: "activation_readiness_failed", Message: "secret /activate/path"}
			return result, nil
		}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/activate", cpaUpdateMutationBody())
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"failure_code":"activation_readiness_failed"`) ||
			!strings.Contains(response.Body.String(), `"state":"failed"`) ||
			!strings.Contains(response.Body.String(), `"runtime_operation_id":"manager-cpa-update/v1:activate:`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "/activate/path") {
			t.Fatalf("response leaked Runtime message: %s", response.Body.String())
		}
	})

	t.Run("protocol error release_metadata_invalid has no fake failed operation", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				Code:       runtimeservice.ProtocolErrorReleaseMetadataInvalid,
				HTTPStatus: 502,
			}
		}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/prepare", cpaUpdateMutationBody())
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"release_metadata_invalid"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		if strings.Contains(body, `"state"`) || strings.Contains(body, `"runtime_operation_id"`) ||
			strings.Contains(body, `"phase"`) {
			t.Fatalf("response leaked operation evidence: %s", body)
		}
		if runtimeClient.prepareCalls.Load() != 1 {
			t.Fatalf("PrepareUpdate calls = %d", runtimeClient.prepareCalls.Load())
		}
	})

	t.Run("protocol error unsupported_staging_platform has no fake failed operation", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				Code:       runtimeservice.ProtocolErrorUnsupportedStagingPlatform,
				HTTPStatus: 409,
			}
		}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/prepare", cpaUpdateMutationBody())
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"unsupported_staging_platform"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		if strings.Contains(body, `"state"`) || strings.Contains(body, `"runtime_operation_id"`) ||
			strings.Contains(body, `"phase"`) {
			t.Fatalf("response leaked operation evidence: %s", body)
		}
		if runtimeClient.prepareCalls.Load() != 1 {
			t.Fatalf("PrepareUpdate calls = %d", runtimeClient.prepareCalls.Load())
		}
	})

	t.Run("protocol error target_stage_corrupt has no fake failed operation", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		runtimeClient.activateFn = func(request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				Code:       runtimeservice.ProtocolErrorTargetStageCorrupt,
				HTTPStatus: 409,
			}
		}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/activate", cpaUpdateMutationBody())
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"target_stage_corrupt"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		if strings.Contains(body, `"state"`) || strings.Contains(body, `"runtime_operation_id"`) ||
			strings.Contains(body, `"phase"`) {
			t.Fatalf("response leaked operation evidence: %s", body)
		}
		if runtimeClient.activateCalls.Load() != 1 {
			t.Fatalf("ActivateUpdate calls = %d", runtimeClient.activateCalls.Load())
		}
	})

	t.Run("Runtime unavailable", func(t *testing.T) {
		runtimeClient := &cpaUpdateRuntimeStub{err: errors.New("secret Runtime token failure")}
		server := newCPAUpdateServer(t, runtimeClient)
		response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/activate", cpaUpdateMutationBody())
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"runtime_observation_unavailable"`) {
			t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "secret") || runtimeClient.activateCalls.Load() != 0 {
			t.Fatalf("unsafe unavailable response or mutation: %s", response.Body.String())
		}
	})
}

func TestCPAUpdateActivateLostResponseRetryReturnsAlreadyApplied(t *testing.T) {
	status := cpaUpdateReadyStatus()
	status.CPAObservedVersion = "7.3.4"
	status.ActiveGatewayArtifact.Version = "7.3.4"
	status.ActiveGatewayArtifact.ArtifactID = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	runtimeClient := &cpaUpdateRuntimeStub{status: status}
	server := newCPAUpdateServer(t, runtimeClient)
	response := submitCPAUpdateMutation(t, server, "/usage-service/runtime/updates/activate", cpaUpdateMutationBody())
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"already_applied":true`) ||
		!strings.Contains(response.Body.String(), `"state":"succeeded"`) {
		t.Fatalf("response = %d, body=%s", response.Code, response.Body.String())
	}
	if runtimeClient.activateCalls.Load() != 0 {
		t.Fatalf("ActivateUpdate calls = %d", runtimeClient.activateCalls.Load())
	}
}

func submitCPAUpdateMutation(t *testing.T, server *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	return response
}
