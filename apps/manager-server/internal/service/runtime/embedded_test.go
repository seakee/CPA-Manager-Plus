package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const testRuntimeToken = "test-runtime-token"

func TestNewEmbeddedClientDisablesEnvironmentProxy(t *testing.T) {
	client := NewEmbeddedClient("http://cpamp-runtime:18318", testRuntimeToken)
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("Transport.Proxy must be nil for direct Supervisor connections")
	}
	if transport == http.DefaultTransport {
		t.Fatal("Transport reused http.DefaultTransport instead of cloning it")
	}
}

func TestEmbeddedClientStatusMapsSupervisorObservation(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		wantState    model.RuntimeState
		wantVersion  model.CPAObservedVersion
		wantCaps     model.RuntimeCapabilities
		wantSupports model.RuntimeCapability
		wantRecovery *model.RuntimeRecoveryObservation
	}{
		{
			name:      "unknown before CPA observation",
			response:  `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"unknown","cpaObservedVersion":"","capabilities":[]}`,
			wantState: model.RuntimeStateUnknown,
			wantCaps:  model.RuntimeCapabilities{},
		},
		{
			name:         "ready with CPA version and capabilities",
			response:     `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","cpaObservedVersion":"v7.2.130","capabilities":["capability-a","capability-b"]}`,
			wantState:    model.RuntimeStateReady,
			wantVersion:  "v7.2.130",
			wantCaps:     model.RuntimeCapabilities{"capability-a", "capability-b"},
			wantSupports: "capability-b",
		},
		{
			name:         "ready with no safe observed version",
			response:     `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","cpaObservedVersion":"","capabilities":["start","stop","restart"],"recovery":{"state":"armed","attemptsRemaining":2}}`,
			wantState:    model.RuntimeStateReady,
			wantCaps:     model.RuntimeCapabilities{"start", "stop", "restart"},
			wantSupports: "restart",
			wantRecovery: &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateArmed, AttemptsRemaining: 2},
		},
		{
			name:      "ready with version omitted",
			response:  `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","capabilities":[]}`,
			wantState: model.RuntimeStateReady,
			wantCaps:  model.RuntimeCapabilities{},
		},
		{
			name:        "nonempty observed version is preserved verbatim",
			response:    `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","cpaObservedVersion":" custom build ","capabilities":[]}`,
			wantState:   model.RuntimeStateReady,
			wantVersion: " custom build ",
			wantCaps:    model.RuntimeCapabilities{},
		},
		{
			name:      "authoritative offline observation",
			response:  `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"offline","cpaObservedVersion":"","capabilities":[]}`,
			wantState: model.RuntimeStateOffline,
			wantCaps:  model.RuntimeCapabilities{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != embeddedRuntimeStatusPath || r.URL.RawQuery != "" {
					t.Errorf("URL = %q, want %q without query", r.URL.String(), embeddedRuntimeStatusPath)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+testRuntimeToken {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Accept"); got != "application/json" {
					t.Errorf("Accept = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			status, err := NewEmbeddedClient(server.URL+"/", testRuntimeToken).Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
			if status.ProtocolVersion != "v1" || status.Identity != "runtime-01" || status.Generation != 7 {
				t.Fatalf("Status() protocol metadata = %#v", status)
			}
			if status.State != test.wantState || status.CPAObservedVersion != test.wantVersion {
				t.Fatalf("Status() = %#v", status)
			}
			if !reflect.DeepEqual(status.Capabilities, test.wantCaps) {
				t.Fatalf("Status() capabilities = %#v, want %#v", status.Capabilities, test.wantCaps)
			}
			if !reflect.DeepEqual(status.Recovery, test.wantRecovery) {
				t.Fatalf("Status() recovery = %#v, want %#v", status.Recovery, test.wantRecovery)
			}
			if test.wantSupports != "" && !status.Capabilities.Supports(test.wantSupports) {
				t.Fatalf("Status() capabilities do not support %q", test.wantSupports)
			}
		})
	}
}

func TestEmbeddedClientStatusRejectsHTTPFailureWithoutLeakingToken(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		location string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "non-2xx", status: http.StatusInternalServerError},
		{name: "redirect", status: http.StatusFound, location: "/redirected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requestCount++
				if test.location != "" {
					w.Header().Set("Location", test.location)
				}
				http.Error(w, "rejected "+testRuntimeToken, test.status)
			}))
			defer server.Close()

			status, err := NewEmbeddedClient(server.URL, testRuntimeToken).Status(t.Context())
			assertZeroStatusError(t, status, err)
			if strings.Contains(err.Error(), testRuntimeToken) {
				t.Fatalf("Status() error leaked runtime token: %v", err)
			}
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
		})
	}
}

func TestEmbeddedClientStatusUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, err := NewEmbeddedClient("http://127.0.0.1:1", testRuntimeToken).Status(ctx)
	assertZeroStatusError(t, status, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Status() error = %v, want context canceled", err)
	}
}

func TestEmbeddedClientStatusTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewEmbeddedClient(server.URL, testRuntimeToken)
	client.httpClient.Timeout = 20 * time.Millisecond
	status, err := client.Status(t.Context())
	assertZeroStatusError(t, status, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Status() error = %v, want deadline exceeded", err)
	}
}

func TestEmbeddedClientStatusReturnsErrorWhenSupervisorIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()

	status, err := NewEmbeddedClient(serverURL, testRuntimeToken).Status(t.Context())
	assertZeroStatusError(t, status, err)
	if status.State == model.RuntimeStateOffline {
		t.Fatal("Status() converted transport failure to offline")
	}
}

func TestEmbeddedClientStatusRejectsInvalidProtocolResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "malformed JSON", response: `{"protocolVersion":`},
		{name: "multiple JSON values", response: `{}` + `{}`},
		{name: "missing protocol metadata", response: `{"state":"unknown","capabilities":[]}`},
		{name: "missing identity", response: `{"protocolVersion":"v1","runtimeGeneration":7,"state":"unknown","capabilities":[]}`},
		{name: "missing generation", response: `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","state":"unknown","capabilities":[]}`},
		{name: "unsupported protocol version", response: `{"protocolVersion":"v2","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"unknown","capabilities":[]}`},
		{name: "invalid runtime state", response: `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"broken","capabilities":[]}`},
		{name: "invalid recovery state", response: `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","capabilities":[],"recovery":{"state":"retrying","attemptsRemaining":1}}`},
		{name: "invalid recovery attempts", response: `{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","capabilities":[],"recovery":{"state":"armed","attemptsRemaining":4}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			status, err := NewEmbeddedClient(server.URL, testRuntimeToken).Status(t.Context())
			assertZeroStatusError(t, status, err)
			if strings.Contains(err.Error(), testRuntimeToken) {
				t.Fatalf("Status() error leaked runtime token: %v", err)
			}
		})
	}
}

func assertZeroStatusError(t *testing.T, status model.RuntimeObservedStatus, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Status() error = nil")
	}
	if !reflect.DeepEqual(status, model.RuntimeObservedStatus{}) {
		t.Fatalf("Status() = %#v, want zero value", status)
	}
}

func TestEmbeddedClientSubmitsTypedLifecycleMutations(t *testing.T) {
	request := model.RuntimeMutationRequest{
		OperationID:               "runtime-reconcile/v1:abc",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 7,
	}
	tests := []struct {
		name          string
		path          string
		operationType model.RuntimeOperationType
		call          func(*EmbeddedClient, context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	}{
		{name: "start", path: embeddedRuntimeStartPath, operationType: model.RuntimeOperationStart, call: (*EmbeddedClient).Start},
		{name: "stop", path: embeddedRuntimeStopPath, operationType: model.RuntimeOperationStop, call: (*EmbeddedClient).Stop},
		{name: "restart", path: embeddedRuntimeRestartPath, operationType: model.RuntimeOperationRestart, call: (*EmbeddedClient).Restart},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != test.path {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+testRuntimeToken {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				var body embeddedMutationRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if body.OperationID != request.OperationID || body.ExpectedRuntimeIdentity != "runtime-01" ||
					body.ExpectedRuntimeGeneration != 7 {
					t.Errorf("request body = %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(embeddedOperationResponse{
					OperationID:       request.OperationID,
					OperationType:     string(test.operationType),
					RuntimeIdentity:   "runtime-01",
					RuntimeGeneration: 7,
					State:             string(model.RuntimeOperationSucceeded),
				})
			}))
			defer server.Close()

			result, err := test.call(NewEmbeddedClient(server.URL, testRuntimeToken), t.Context(), request)
			if err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			if result.OperationID != request.OperationID || result.OperationType != test.operationType ||
				result.State != model.RuntimeOperationSucceeded {
				t.Fatalf("mutation result = %#v", result)
			}
		})
	}
}

func TestEmbeddedClientMutationPreservesStableProtocolErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"stale_runtime_generation","message":"do not branch on this message"}}`))
	}))
	defer server.Close()
	_, err := NewEmbeddedClient(server.URL, testRuntimeToken).Start(t.Context(), model.RuntimeMutationRequest{
		OperationID:               "operation-1",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 7,
	})
	code, ok := ErrorCode(err)
	if !ok || code != ProtocolErrorStaleRuntimeGeneration {
		t.Fatalf("mutation error = %v code=%q ok=%v", err, code, ok)
	}
	if strings.Contains(err.Error(), "do not branch") || strings.Contains(err.Error(), testRuntimeToken) {
		t.Fatalf("mutation error exposed unstable or secret text: %v", err)
	}
}

func TestFileRuntimeTokenSourceRecoversAfterLateFileCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-token")
	client := NewEmbeddedClientWithTokenSource("http://127.0.0.1:1", NewFileRuntimeTokenSource(path))
	if _, err := client.Status(t.Context()); err == nil || !strings.Contains(err.Error(), "token file") {
		t.Fatalf("missing token Status error = %v", err)
	}
	if err := os.WriteFile(path, []byte(testRuntimeToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testRuntimeToken {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"offline","capabilities":[],"recovery":{"state":"inactive","attemptsRemaining":0}}`))
	}))
	defer server.Close()
	client.baseURL = server.URL
	if _, err := client.Status(t.Context()); err != nil {
		t.Fatalf("Status after token creation: %v", err)
	}
}

func TestEmbeddedClientRejectsUnknownMutationResultFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"operationId":"operation-1","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"succeeded","unexpected":true}`))
	}))
	defer server.Close()
	_, err := NewEmbeddedClient(server.URL, testRuntimeToken).Start(t.Context(), model.RuntimeMutationRequest{
		OperationID:               "operation-1",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 7,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("mutation error = %v", err)
	}
}
