package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func runtimeObservationRequest() model.RuntimeObserveUpdateOperationRequest {
	return model.RuntimeObserveUpdateOperationRequest{
		RuntimeMutationRequest: model.RuntimeMutationRequest{
			OperationID: "manager-cpa-update/v1:prepare:abc", ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 7,
		},
		OperationType: model.RuntimeOperationPrepareUpdate, TargetVersion: "7.3.4",
	}
}

func runtimeObservationWire() embeddedObservedOperationResponse {
	created := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	return embeddedObservedOperationResponse{
		OperationID: "manager-cpa-update/v1:prepare:abc", OperationType: "prepare_update",
		RuntimeIdentity: "runtime-01", RuntimeGeneration: 6, State: "accepted",
		CreatedAt: created, UpdatedAt: created,
	}
}

func TestEmbeddedClientObservesTypedUpdateWithoutMutation(t *testing.T) {
	for _, phase := range []model.RuntimeOperationType{model.RuntimeOperationPrepareUpdate, model.RuntimeOperationActivateUpdate} {
		for _, state := range []string{"accepted", "running", "succeeded", "failed"} {
			t.Run(string(phase)+"/"+state, func(t *testing.T) {
				request := runtimeObservationRequest()
				request.OperationType = phase
				wire := runtimeObservationWire()
				wire.OperationType, wire.State = string(phase), state
				if state == "succeeded" || state == "failed" {
					completed := wire.UpdatedAt
					wire.CompletedAt = &completed
				}
				if state == "failed" {
					wire.Error = &embeddedProtocolError{Code: "update_stage_failed", Message: "/private/runtime token=" + testRuntimeToken}
				}
				var calls atomic.Int32
				var expectedGeneration atomic.Uint64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					wantQuery := url.Values{
						"operationId": {request.OperationID}, "operationType": {string(phase)}, "targetVersion": {"7.3.4"},
						"expectedRuntimeIdentity": {"runtime-01"}, "expectedRuntimeGeneration": {fmt.Sprint(expectedGeneration.Load())},
					}
					if r.Method != http.MethodGet || r.URL.Path != embeddedRuntimeObserveUpdatePath ||
						!reflect.DeepEqual(r.URL.Query(), wantQuery) || err != nil || len(body) != 0 {
						t.Errorf("request = %s %s body=%q err=%v", r.Method, r.URL, body, err)
					}
					if r.Header.Get("Authorization") != "Bearer "+testRuntimeToken || r.Header.Get("Cache-Control") != "no-store" {
						t.Error("observation omitted private authentication or cache policy")
					}
					_ = json.NewEncoder(w).Encode(wire)
				}))
				defer server.Close()
				client := NewEmbeddedClient(server.URL, testRuntimeToken)
				for _, generation := range []model.RuntimeGeneration{7, 2, 1 << 63} {
					request.ExpectedRuntimeGeneration = generation
					expectedGeneration.Store(uint64(generation))
					got, err := client.ObserveUpdateOperation(t.Context(), request)
					if err != nil || got.OperationID != request.OperationID || got.OperationType != phase ||
						got.State != model.RuntimeOperationState(state) || got.RuntimeGeneration != 6 ||
						!got.CreatedAt.Equal(wire.CreatedAt) || !reflect.DeepEqual(got.CompletedAt, wire.CompletedAt) {
						t.Fatalf("ObserveUpdateOperation() = %+v, %v", got, err)
					}
					encoded, err := json.Marshal(got)
					if err != nil || strings.Contains(string(encoded), "/private/") || strings.Contains(string(encoded), testRuntimeToken) {
						t.Fatalf("typed observation leaked remote message: %s, %v", encoded, err)
					}
				}
				if calls.Load() != 3 {
					t.Fatalf("HTTP calls = %d, want one GET per observation", calls.Load())
				}
			})
		}
	}
}

func TestEmbeddedClientObservationRejectsInvalidEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*embeddedObservedOperationResponse){
		"wrong ID":                  func(r *embeddedObservedOperationResponse) { r.OperationID = "other" },
		"wrong phase":               func(r *embeddedObservedOperationResponse) { r.OperationType = "activate_update" },
		"wrong identity":            func(r *embeddedObservedOperationResponse) { r.RuntimeIdentity = "other" },
		"zero generation":           func(r *embeddedObservedOperationResponse) { r.RuntimeGeneration = 0 },
		"unknown state":             func(r *embeddedObservedOperationResponse) { r.State = "already_applied" },
		"missing created timestamp": func(r *embeddedObservedOperationResponse) { r.CreatedAt = time.Time{} },
		"missing updated timestamp": func(r *embeddedObservedOperationResponse) { r.UpdatedAt = time.Time{} },
		"timestamps reversed":       func(r *embeddedObservedOperationResponse) { r.UpdatedAt = r.CreatedAt.Add(-time.Second) },
		"completed accepted":        func(r *embeddedObservedOperationResponse) { completed := r.UpdatedAt; r.CompletedAt = &completed },
		"accepted with empty error": func(r *embeddedObservedOperationResponse) { r.Error = &embeddedProtocolError{} },
		"failed without code": func(r *embeddedObservedOperationResponse) {
			r.State = "failed"
			completed := r.UpdatedAt
			r.CompletedAt = &completed
		},
		"failed with raw code": func(r *embeddedObservedOperationResponse) {
			r.State = "failed"
			completed := r.UpdatedAt
			r.CompletedAt = &completed
			r.Error = &embeddedProtocolError{Code: "/private/runtime token=secret"}
		},
		"succeeded without completion": func(r *embeddedObservedOperationResponse) { r.State = "succeeded" },
	} {
		t.Run(name, func(t *testing.T) {
			wire := runtimeObservationWire()
			mutate(&wire)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(wire) }))
			defer server.Close()
			got, err := NewEmbeddedClient(server.URL, testRuntimeToken).ObserveUpdateOperation(t.Context(), runtimeObservationRequest())
			if err == nil || !reflect.DeepEqual(got, model.RuntimeUpdateOperationObservation{}) ||
				strings.Contains(err.Error(), "/private/") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid response = %+v, %v", got, err)
			}
		})
	}
}

func TestEmbeddedClientObservationAcceptsRetainedTombstoneTimestamps(t *testing.T) {
	wire := runtimeObservationWire()
	wire.State = "succeeded"
	completed := wire.CreatedAt.Add(time.Second)
	wire.CompletedAt = &completed
	wire.UpdatedAt = completed.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(wire) }))
	defer server.Close()
	got, err := NewEmbeddedClient(server.URL, testRuntimeToken).ObserveUpdateOperation(t.Context(), runtimeObservationRequest())
	if err != nil || got.State != model.RuntimeOperationSucceeded || !got.UpdatedAt.Equal(wire.UpdatedAt) {
		t.Fatalf("retained tombstone = %+v, %v", got, err)
	}
}

func TestEmbeddedClientObservationRejectsUntrustedJSONWithoutLeakingIt(t *testing.T) {
	valid, err := json.Marshal(runtimeObservationWire())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		string(valid) + " {}",
		strings.TrimSuffix(string(valid), "}") + ",\"private-token-path\":\"secret\"}",
		"{\"createdAt\":\"/private/runtime token=secret\"}",
		"null",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		got, err := NewEmbeddedClient(server.URL, testRuntimeToken).ObserveUpdateOperation(t.Context(), runtimeObservationRequest())
		server.Close()
		if err == nil || got.State != "" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("untrusted response = %+v, %v", got, err)
		}
	}
}

func TestEmbeddedClientObservationPreservesStableErrorsOnly(t *testing.T) {
	for _, test := range []struct {
		code   ProtocolErrorCode
		status int
	}{
		{ProtocolErrorOperationNotFound, 404}, {ProtocolErrorOperationIDConflict, 409},
		{ProtocolErrorRuntimeIdentityMismatch, 409}, {ProtocolErrorStaleRuntimeGeneration, 409},
		{ProtocolErrorOperationPersistenceUnavailable, 503}, {"/private/runtime token=secret", 500},
	} {
		t.Run(string(test.code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(embeddedErrorEnvelope{Error: embeddedProtocolError{
					Code: string(test.code), Message: "/private/runtime token=" + testRuntimeToken,
				}})
			}))
			defer server.Close()
			got, err := NewEmbeddedClient(server.URL, testRuntimeToken).ObserveUpdateOperation(t.Context(), runtimeObservationRequest())
			if err == nil || got.State != "" || strings.Contains(err.Error(), "/private/") ||
				strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), testRuntimeToken) {
				t.Fatalf("error response = %+v, %v", got, err)
			}
			if test.status != 500 {
				var protocolErr *ProtocolError
				if !errors.As(err, &protocolErr) || protocolErr.Code != test.code || protocolErr.HTTPStatus != test.status {
					t.Fatalf("lost protocol code/status: %v", err)
				}
			}
		})
	}
}

func TestEmbeddedClientObservationUsesCallerCancellationAndRejectsInvalidRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := NewEmbeddedClient("http://127.0.0.1:1", testRuntimeToken)
	if _, err := client.ObserveUpdateOperation(ctx, runtimeObservationRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation = %v", err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	request := runtimeObservationRequest()
	request.OperationType = model.RuntimeOperationRestart
	if _, err := NewEmbeddedClient(server.URL, testRuntimeToken).ObserveUpdateOperation(t.Context(), request); err == nil || calls.Load() != 0 {
		t.Fatalf("invalid request reached Runtime: err=%v calls=%d", err, calls.Load())
	}
}

func TestExternalClientObservationIsUnsupportedWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	resolverCalls := 0
	client := NewExternalClientWithConnectionSource(func(context.Context) (string, string, error) {
		resolverCalls++
		return server.URL, "management-key", nil
	})
	got, err := client.ObserveUpdateOperation(t.Context(), runtimeObservationRequest())
	if !errors.Is(err, ErrRuntimeObservationUnsupported) || got.State != "" || calls.Load() != 0 || resolverCalls != 0 {
		t.Fatalf("External observation = %+v, %v; network=%d resolver=%d", got, err, calls.Load(), resolverCalls)
	}
}
