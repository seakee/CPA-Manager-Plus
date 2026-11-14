package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/ingress/internal/ingress"
)

func freshAuthorityJSON(state ingress.FreshRouteState, admission string) string {
	body := map[string]any{
		"installationId":     state.InstallationID,
		"generation":         state.Generation,
		"operationId":        state.OperationID,
		"revision":           state.Revision,
		"target":             state.Target,
		"committed":          state.Committed,
		"inferenceTarget":    state.InferenceTarget,
		"sharedEntry":        state.SharedEntry,
		"inferenceCommitted": state.InferenceCommitted,
	}
	if admission != "" {
		body["gatewayAdmission"] = json.RawMessage(admission)
	}
	data, _ := json.Marshal(body)
	return string(data)
}

// publishFreshProjection writes the one private routing projection through the
// control listener, exactly as Manager does, so the admission decision is bound
// to an Ingress-owned projection instead of a test field.
func publishFreshProjection(t *testing.T, routes *ingress.FreshRoutes, state ingress.FreshRouteState) {
	t.Helper()
	handler := routes.ControlHandler(func(context.Context) (ingress.FreshRouteState, error) { return state, nil })
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, ingress.FreshRoutingControlPath, bytes.NewReader(data)))
	if response.Code != http.StatusOK {
		t.Fatalf("projection publish = %d", response.Code)
	}
}

func TestFreshAuthorityClientDecodesAdmissionAndRejectsUnknownShape(t *testing.T) {
	state := ingress.FreshRouteState{InstallationID: "install-one", Generation: "generation-one", OperationID: "fresh-operation", Revision: 3, Target: "/control", Committed: true}
	admission := `{"installationId":"install-one","generation":"generation-one","admitted":true}`
	var body atomic.Value
	var status atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ingress.FreshRoutingAuthorityPath || r.Method != http.MethodGet || r.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("unexpected authority request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Cache-Control"))
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == http.StatusOK {
			_, _ = io.WriteString(w, body.Load().(string))
		}
	}))
	defer manager.Close()
	target, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	authority := managerFreshAuthority(target)
	status.Store(http.StatusOK)
	body.Store(freshAuthorityJSON(state, admission))
	reply, err := authority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reply.FreshRouteState != state || reply.GatewayAdmission == nil || !reply.GatewayAdmission.Admitted ||
		reply.GatewayAdmission.InstallationID != state.InstallationID || reply.GatewayAdmission.Generation != state.Generation {
		t.Fatalf("decoded authority = %+v", reply)
	}

	for _, test := range []struct {
		name   string
		status int
		body   string
		legacy bool
	}{
		{name: "legacy-manager", status: http.StatusNoContent, legacy: true},
		{name: "unknown-field", status: http.StatusOK, body: `{"installationId":"install-one","generation":"generation-one","operationId":"fresh-operation","revision":3,"target":"/control","committed":true,"gatewayAdmission":{"installationId":"install-one","generation":"generation-one","admitted":true},"admitted":true}`},
		{name: "trailing-data", status: http.StatusOK, body: freshAuthorityJSON(state, admission) + "{}"},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "redirect", status: http.StatusFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			status.Store(int32(test.status))
			body.Store(test.body)
			_, err := authority(t.Context())
			if test.legacy {
				if !errors.Is(err, errLegacyManager) {
					t.Fatalf("legacy authority error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("malformed authority accepted")
			}
		})
	}
}

func TestFreshGatewayAdmissionClientIntegration(t *testing.T) {
	state := ingress.FreshRouteState{InstallationID: "install-one", Generation: "generation-one", OperationID: "fresh-operation", Revision: 3, Target: "/control", InferenceTarget: "/inference", Committed: true, InferenceCommitted: true}
	admitted := `{"installationId":"install-one","generation":"generation-one","admitted":true}`
	foreignGeneration := `{"installationId":"install-one","generation":"generation-two","admitted":true}`
	denied := `{"installationId":"install-one","generation":"generation-one","admitted":false}`
	var body atomic.Value
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer manager.Close()
	target, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	authority := managerFreshAuthority(target)
	routes := &ingress.FreshRoutes{}
	publishFreshProjection(t, routes, state)

	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{name: "admitted", body: freshAuthorityJSON(state, admitted), want: true},
		{name: "denied", body: freshAuthorityJSON(state, denied)},
		{name: "foreign-generation", body: freshAuthorityJSON(state, foreignGeneration)},
		{name: "older-manager", body: freshAuthorityJSON(state, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			body.Store(test.body)
			if got := ingress.NewGatewayAdmitter(routes, authority, ingress.DefaultGatewayAdmissionTTL).Admitted(t.Context()); got != test.want {
				t.Fatalf("admitted = %v, want %v", got, test.want)
			}
		})
	}

	// The real authority client and the real handler: an unreachable Manager
	// closes Gateway traffic, and a positive bound answer opens it.
	gatewayHits := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	gatewayURL, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := ingress.NewFreshHandler(routes, target, gatewayURL, log.New(io.Discard, "", 0), nil)
	handler.EnableFreshGatewayAdmission(ingress.NewGatewayAdmitter(routes, authority, ingress.DefaultGatewayAdmissionTTL).Admitted)
	request := func() int {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/inference/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer cpamp_sk_client")
		handler.ServeHTTP(response, req)
		return response.Code
	}
	body.Store(freshAuthorityJSON(state, admitted))
	if code := request(); code != http.StatusOK || gatewayHits != 1 {
		t.Fatalf("admitted model request = %d hits=%d", code, gatewayHits)
	}
	manager.Close()
	handler = ingress.NewFreshHandler(routes, target, gatewayURL, log.New(io.Discard, "", 0), nil)
	handler.EnableFreshGatewayAdmission(ingress.NewGatewayAdmitter(routes, authority, ingress.DefaultGatewayAdmissionTTL).Admitted)
	if code := request(); code != http.StatusServiceUnavailable || gatewayHits != 1 {
		t.Fatalf("Manager outage model request = %d hits=%d", code, gatewayHits)
	}
}

func TestFreshRoutingOnlyNarrowsTheAuthorityToTheProjection(t *testing.T) {
	state := ingress.FreshRouteState{InstallationID: "install-one", Generation: "generation-one", OperationID: "fresh-operation", Revision: 1, Target: "/control"}
	reply := ingress.FreshAuthority{FreshRouteState: state, GatewayAdmission: &ingress.GatewayAdmission{InstallationID: "install-one", Generation: "generation-one", Admitted: true}}
	narrowed := freshRoutingOnly(func(context.Context) (ingress.FreshAuthority, error) { return reply, nil })
	got, err := narrowed(t.Context())
	if err != nil || got != state {
		t.Fatalf("narrowed authority = %+v %v", got, err)
	}
	failing := freshRoutingOnly(func(context.Context) (ingress.FreshAuthority, error) {
		return ingress.FreshAuthority{}, errors.New("unavailable")
	})
	if _, err := failing(t.Context()); err == nil {
		t.Fatal("narrowed authority swallowed a transport error")
	}
}
