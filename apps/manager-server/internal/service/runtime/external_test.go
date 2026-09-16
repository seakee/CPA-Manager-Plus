package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestExternalClientStatus(t *testing.T) {
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("X-CPA-Version", "v7.2.130")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	status, err := NewExternalClient(server.URL, "management-key").Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Status() returned invalid observation: %v", err)
	}
	if status.State != model.RuntimeStateReady || status.CPAObservedVersion != "v7.2.130" {
		t.Fatalf("Status() = %#v", status)
	}
	if status.ProtocolVersion != "" || status.Identity != "" || status.Generation != 0 {
		t.Fatalf("Status() invented Runtime Protocol metadata: %#v", status)
	}
	if status.Capabilities != nil {
		t.Fatalf("Status() capabilities = %#v, want nil", status.Capabilities)
	}
	if gotAuthorization != "Bearer management-key" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestExternalClientStatusRequiresReliableObservation(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "missing CPA version",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			want: "CPA version header is missing",
		},
		{
			name: "authentication failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			},
			want: "401 Unauthorized",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()

			status, err := NewExternalClient(server.URL, "management-key").Status(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Status() error = %v, want %q", err, test.want)
			}
			if !reflect.DeepEqual(status, model.RuntimeObservedStatus{}) {
				t.Fatalf("Status() = %#v, want zero value", status)
			}
		})
	}
}

func TestExternalClientStatusUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, err := NewExternalClient("http://127.0.0.1:1", "management-key").Status(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Status() error = %v, want context canceled", err)
	}
	if !reflect.DeepEqual(status, model.RuntimeObservedStatus{}) {
		t.Fatalf("Status() = %#v, want zero value", status)
	}
}

func TestExternalClientLifecycleMutationsFailLocally(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	defer server.Close()
	client := NewExternalClient(server.URL, "management-key")
	request := model.RuntimeMutationRequest{
		OperationID:               "operation-1",
		ExpectedRuntimeIdentity:   "runtime-1",
		ExpectedRuntimeGeneration: 1,
	}
	mutations := []struct {
		name string
		run  func(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	}{
		{name: "start", run: client.Start},
		{name: "stop", run: client.Stop},
		{name: "restart", run: client.Restart},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			result, err := mutation.run(t.Context(), request)
			if !errors.Is(err, ErrRuntimeMutationUnsupported) {
				t.Fatalf("mutation error = %v", err)
			}
			if !reflect.DeepEqual(result, model.RuntimeOperationResult{}) {
				t.Fatalf("mutation result = %#v", result)
			}
		})
	}
	if requestCount != 0 {
		t.Fatalf("external lifecycle mutations made %d network requests", requestCount)
	}
}
