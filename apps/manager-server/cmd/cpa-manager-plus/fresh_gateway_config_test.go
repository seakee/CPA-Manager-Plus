package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

func gatewayRequest(t *testing.T, app *freshApplication, method, resource, body string, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	request := httptest.NewRequest(method, "/control/api/v2/"+resource, reader)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	return response
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body %q: %v", response.Body.String(), err)
	}
	return payload.Code
}

// TestGatewayConfigRequiresAnAuthority proves the page fails closed when this
// installation holds no configuration authority at all.
func TestGatewayConfigRequiresAnAuthority(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.gatewayConfig = nil
	response := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
	if response.Code != http.StatusConflict || errorCode(t, response) != "config_authority_unavailable" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestGatewayConfigSurfaceRejectsAnythingOutsideTheContract covers the request
// shape, the method and the resource surface before any CPA write is possible.
func TestGatewayConfigSurfaceRejectsAnythingOutsideTheContract(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.gatewayConfig = nil
	requestID := strings.Repeat("ab", 16)
	expected := `{"generation":"7","baseline":"` + strings.Repeat("a", 64) + `"}`
	cases := []struct {
		name        string
		method      string
		resource    string
		body        string
		contentType string
		status      int
		code        string
		allow       string
	}{
		{"wrong content type", http.MethodPost, "gateway/config/groups/retry", `{}`, "text/plain", http.StatusUnsupportedMediaType, "", ""},
		{"missing content type", http.MethodPost, "gateway/config/groups/retry", `{}`, "", http.StatusUnsupportedMediaType, "", ""},
		{"unknown body key", http.MethodPost, "gateway/config/groups/retry", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[],"yaml":"x"}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"unknown expectation", http.MethodPost, "gateway/config/groups/retry", `{"requestId":"` + requestID + `","expected":{"generation":"7","baseline":"` + strings.Repeat("a", 64) + `","sha256":"x"},"edits":[]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"unknown group", http.MethodPost, "gateway/config/groups/nope", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"routing.retry.request-retry","action":"set","value":3}]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"foreign leaf", http.MethodPost, "gateway/config/groups/retry", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"server.port","action":"set","value":9000}]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"proxy credentials", http.MethodPost, "gateway/config/groups/request", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"requests.proxy-url","action":"set","value":"http://u:p@proxy.test:8080"}]}`, "application/json", http.StatusUnprocessableEntity, "proxy_url_credentials", ""},
		{"duplicate rule path", http.MethodPost, "gateway/config/rules/filter", `{"requestId":"` + requestID + `","expected":` + expected + `,"rules":[{"models":[{"name":"*"}],"params":["a","a"]}]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"wrong group method", http.MethodGet, "gateway/config/groups/retry", "", "", http.StatusMethodNotAllowed, "", http.MethodPost},
		{"wrong family method", http.MethodPut, "gateway/config/rules/filter", `{}`, "application/json", http.StatusMethodNotAllowed, "", http.MethodPost},
		{"wrong read method", http.MethodPost, "gateway/config", `{}`, "application/json", http.StatusMethodNotAllowed, "", http.MethodGet},
		{"unknown resource", http.MethodGet, "gateway/config/unknown", "", "", http.StatusNotFound, "", ""},
		{"field resource removed", http.MethodGet, "gateway/config/requests/streaming/keepalive-seconds", "", "", http.StatusNotFound, "", ""},
		{"operation without id", http.MethodGet, "gateway/config/operations", "", "", http.StatusNotFound, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := gatewayRequest(t, app, tc.method, tc.resource, tc.body, tc.contentType)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.code != "" && errorCode(t, response) != tc.code {
				t.Fatalf("body=%s", response.Body.String())
			}
			if tc.allow != "" && response.Header().Get("Allow") != tc.allow {
				t.Fatalf("allow=%q", response.Header().Get("Allow"))
			}
		})
	}
	// A query string and a non-canonical escaping are never a resource selector.
	for _, target := range []string{"gateway/config?path=routing", "gateway/config/groups/retry%2F"} {
		request := httptest.NewRequest(http.MethodGet, "/control/api/v2/"+target, nil)
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", target, response.Code)
		}
	}
}

// TestGatewayConfigOperationReadsNeedAnAuthority covers the operation surface: an
// unjournaled request ID is not an unknown result, and the page never writes.
func TestGatewayConfigOperationReadsNeedAnAuthority(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.gatewayConfig = nil
	response := gatewayRequest(t, app, http.MethodGet, "gateway/config/operations/"+strings.Repeat("ab", 16), "", "")
	if response.Code != http.StatusConflict || errorCode(t, response) != "config_authority_unavailable" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// gatewayConfigFixture points one installation at a fake CPA and returns the
// page contract of that installation. The page is read once through the real
// handler so a save echoes the baseline the page itself published.
type gatewayConfigFixture struct {
	app      *freshApplication
	server   *httptest.Server
	document string
}

func newGatewayConfigFixture(t *testing.T, document string, write func(w http.ResponseWriter, r *http.Request) bool) *gatewayConfigFixture {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(document))
		case write != nil && write(w, r):
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	// A managed installation commits the CPA destination and generation it
	// writes, so the page reads exactly this fake through the real transport.
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.NewManaged, Stage: "committed",
			TargetIdentity: "managed-target", TargetGeneration: 7,
			TargetArtifactID:              "sha256:" + strings.Repeat("b", 64),
			DestinationConnectionIdentity: "managed:sha256:" + strings.Repeat("c", 64),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.gatewayConfig = freshconfig.New(app.store, func(context.Context, uint64) (string, error) {
		return "management-key", nil
	}, func(op bootstrap.FreshOperation) bool {
		return op.Strategy.Strategy == bootstrap.NewManaged && op.Strategy.Stage == "committed" &&
			strings.HasPrefix(op.Strategy.DestinationConnectionIdentity, "managed:sha256:")
	})
	app.gatewayConfig.EnableManaged(server.URL)
	return &gatewayConfigFixture{app: app, server: server, document: document}
}

// pageTarget reads the page once and returns the generation and the baseline of
// one group, so a save echoes what the page published instead of a guess.
func (f *gatewayConfigFixture) pageTarget(t *testing.T, group string) (string, string) {
	t.Helper()
	response := gatewayRequest(t, f.app, http.MethodGet, "gateway/config", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var view struct {
		Generation string `json:"generation"`
		Groups     []struct {
			ID       string `json:"id"`
			Baseline string `json:"baseline"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	for _, entry := range view.Groups {
		if entry.ID == group {
			return view.Generation, entry.Baseline
		}
	}
	t.Fatalf("group %q is not on the page", group)
	return "", ""
}

const gatewayTestDocument = `{
  "routing": {"retry": {"request-retry": 2}},
  "requests": {"streaming": {"keepalive-seconds": 15}}
}`

// TestGatewayConfigReplySendsTheBoundedCPARejection proves a real CPA 422 is
// answered with the code this Manager owns plus the field CPA named. CPA's own
// string is evidence in the journal; a page code the Manager does not own would
// be CPA's vocabulary driving browser behaviour.
func TestGatewayConfigReplySendsTheBoundedCPARejection(t *testing.T) {
	fixture := newGatewayConfigFixture(t, gatewayTestDocument, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPatch || r.URL.Path != "/v8/management/config/routing/retry" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"invalid_config","field":"routing.retry.request-retry","message":"cannot unmarshal \"2\" into int"}`))
		return true
	})
	generation, baseline := fixture.pageTarget(t, "retry")
	body := `{"requestId":"` + strings.Repeat("ab", 16) + `","expected":{"generation":"` + generation + `","baseline":"` + baseline + `"},` +
		`"edits":[{"path":"routing.retry.request-retry","action":"set","value":3}]}`
	response := gatewayRequest(t, fixture.app, http.MethodPost, "gateway/config/groups/retry", body, "application/json")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != freshconfig.CodeCPARejected {
		t.Fatalf("wire code = %q, want %q", payload.Code, freshconfig.CodeCPARejected)
	}
	if payload.Code == "invalid_config" {
		t.Fatal("CPA's own code reached the wire")
	}
	if payload.Field != "routing.retry.request-retry" {
		t.Fatalf("field = %q", payload.Field)
	}
	if payload.Message == "" || strings.Contains(payload.Message, `"`) {
		t.Fatalf("message was not the redacted diagnostic: %q", payload.Message)
	}
}

// TestGatewayConfigSaveReportsAMovedTarget proves a save answers the same typed
// blocker the read publishes. The External authority is resolved before anything
// is read or written, so a connection that no longer names the committed target
// is external_target_moved on every save — never a 503 no retry resolves, and
// never a request to a CPA this installation does not own.
func TestGatewayConfigSaveReportsAMovedTarget(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "external:sha256:"+strings.Repeat("c", 64))
	expected := `{"generation":"1","baseline":"` + strings.Repeat("a", 64) + `"}`
	for _, tc := range []struct {
		name     string
		resource string
		body     string
	}{
		{"group", "gateway/config/groups/retry", `{"requestId":"` + strings.Repeat("ab", 16) + `","expected":` + expected +
			`,"edits":[{"path":"routing.retry.request-retry","action":"set","value":3}]}`},
		{"family", "gateway/config/rules/filter", `{"requestId":"` + strings.Repeat("cd", 16) + `","expected":` + expected +
			`,"rules":[{"models":[{"name":"*"}],"params":["a"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := gatewayRequest(t, app, http.MethodPost, tc.resource, tc.body, "application/json")
			if response.Code != http.StatusConflict || errorCode(t, response) != freshconfig.ReasonTargetMoved {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if cpaServer.reads.Load() != 0 {
		t.Fatalf("a moved target was contacted %d times", cpaServer.reads.Load())
	}
}

// TestGatewayConfigReplyKeepsTheManagerPrivateRangeCode proves the private
// profile's own range refusal keeps its code. It is a code this Manager decided
// before CPA was asked, so the page must be able to show it on the field.
func TestGatewayConfigReplyKeepsTheManagerPrivateRangeCode(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.NewManaged, Stage: "committed",
			TargetIdentity: cpa.PrivateKeyRuntime, TargetGeneration: 7, TargetArtifactID: cpa.PrivateKeyArtifact,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.gatewayConfig = freshconfig.New(app.store, func(context.Context, uint64) (string, error) {
		return "management-key", nil
	}, func(op bootstrap.FreshOperation) bool {
		return op.Strategy.Stage == "committed" && op.Strategy.TargetIdentity == cpa.PrivateKeyRuntime
	})
	body := `{"requestId":"` + strings.Repeat("ab", 16) + `","expected":{"generation":"7","baseline":"` + strings.Repeat("a", 64) + `"},` +
		`"edits":[{"path":"requests.streaming.keepalive-seconds","action":"set","value":301}]}`
	response := gatewayRequest(t, app, http.MethodPost, "gateway/config/groups/streaming", body, "application/json")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Code  string `json:"code"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != freshconfig.CodeInvalidConfig {
		t.Fatalf("wire code = %q, want %q", payload.Code, freshconfig.CodeInvalidConfig)
	}
	if payload.Field != "requests.streaming.keepalive-seconds" {
		t.Fatalf("field = %q", payload.Field)
	}
}

// externalPageTarget reads the page of an External installation once and returns
// the generation and the baseline of one group, so a save echoes what the page
// published instead of a guess.
func externalPageTarget(t *testing.T, app *freshApplication, group string) (string, string) {
	t.Helper()
	response := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var view struct {
		Generation string `json:"generation"`
		Groups     []struct {
			ID       string `json:"id"`
			Baseline string `json:"baseline"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	for _, entry := range view.Groups {
		if entry.ID == group {
			return view.Generation, entry.Baseline
		}
	}
	t.Fatalf("group %q is not on the page", group)
	return "", ""
}

// TestExternalGatewaySaveFilesTheFirstRefusal proves a save that meets the CPA's
// 401 records it, exactly as the read and the Key installs do. The page polls the
// config read right after a failed save: a refusal it never recorded would be
// presented again on that poll, and five of them ban the address for thirty
// minutes.
func TestExternalGatewaySaveFilesTheFirstRefusal(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	app.gatewayConfig = gatewayFor(app)
	generation, baseline := externalPageTarget(t, app, "routing")
	// The operator rotated the CPA's management key: every request it receives is
	// now refused with 401.
	cpaServer.reject.Store(true)
	body := `{"requestId":"` + strings.Repeat("ab", 16) + `","expected":{"generation":"` + generation + `","baseline":"` + baseline + `"},` +
		`"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`
	response := gatewayRequest(t, app, http.MethodPost, "gateway/config/groups/routing", body, "application/json")
	if response.Code != http.StatusConflict || errorCode(t, response) != freshconfig.ReasonManagementRejected {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the save's 401 was not recorded")
	}
	reads, probes := cpaServer.reads.Load(), cpaServer.probes.Load()
	read := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
	if read.Code != http.StatusConflict || errorCode(t, read) != freshconfig.ReasonManagementRejected {
		t.Fatalf("read after the recorded refusal = %d %s", read.Code, read.Body.String())
	}
	if cpaServer.reads.Load() != reads || cpaServer.probes.Load() != probes {
		t.Fatal("the recorded refusal did not stop the next read from presenting the key")
	}
}
