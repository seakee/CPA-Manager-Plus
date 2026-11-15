package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// protectedTestEnvelope is a well-formed protected envelope: the store only
// proves the shape, and the External profile resolves its own credential through
// the connection service, which this test replaces.
const protectedTestEnvelope = "enc:v1:AQEBAQEBAQEBAQEB:AgICAgICAgICAgICAgICAg"

// pluginRuntimeFixture is one managed installation over a mutable fake CPA: the
// document the Manager reads is the document its own PATCH built, so a read-back
// proves what the write actually did.
type pluginRuntimeFixture struct {
	app      *freshApplication
	mu       sync.Mutex
	document map[string]any
	patches  []map[string]any
	// patchStatus is CPA's answer to the switch PATCH; anything but 200/204 is a
	// write whose result the Manager cannot claim.
	patchStatus int
	// applyFailedWrite makes the refused PATCH land anyway, which is exactly the
	// lost-reply case a read-back must settle.
	applyFailedWrite bool
}

func newPluginRuntimeFixture(t *testing.T) *pluginRuntimeFixture {
	t.Helper()
	fixture := &pluginRuntimeFixture{
		document:    map[string]any{"plugins": map[string]any{"enabled": false, "dir": "/srv/cpamp/plugins"}},
		patchStatus: http.StatusOK,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			fixture.mu.Lock()
			body, err := json.Marshal(fixture.document)
			fixture.mu.Unlock()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case r.Method == http.MethodPatch && r.URL.Path == "/v8/management/config/plugins":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.mu.Lock()
			fixture.patches = append(fixture.patches, body)
			status := fixture.patchStatus
			if status == http.StatusOK || fixture.applyFailedWrite {
				plugins, _ := fixture.document["plugins"].(map[string]any)
				for key, value := range body {
					plugins[key] = value
				}
			}
			fixture.mu.Unlock()
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
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
	fixture.app = app
	return fixture
}

// pluginRuntimeView is the page contract of the switch. It mirrors the fields
// this test needs instead of reusing the service type, so a wire change fails
// here rather than in a comment.
type pluginRuntimeView struct {
	Capability struct {
		Mode     string `json:"mode"`
		Readable bool   `json:"readable"`
		Writable bool   `json:"writable"`
		Reason   string `json:"reason"`
	} `json:"capability"`
	Generation string `json:"generation"`
	Groups     []struct {
		ID        string `json:"id"`
		Prefix    string `json:"prefix"`
		Baseline  string `json:"baseline"`
		Writable  bool   `json:"writable"`
		Persisted string `json:"persisted"`
		Effective string `json:"effective"`
		Operation *struct {
			RequestID string `json:"requestId"`
			State     string `json:"state"`
		} `json:"operation"`
	} `json:"groups"`
	Fields []struct {
		Path     string `json:"path"`
		Presence string `json:"presence"`
		Value    any    `json:"value"`
		Editable bool   `json:"editable"`
	} `json:"fields"`
}

func readPluginRuntime(t *testing.T, app *freshApplication) pluginRuntimeView {
	t.Helper()
	response := gatewayRequest(t, app, http.MethodGet, "plugins/runtime", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var view pluginRuntimeView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func pluginSwitchBody(generation, baseline, requestID string, enabled bool) string {
	value := "false"
	if enabled {
		value = "true"
	}
	return `{"requestId":"` + requestID + `","expected":{"generation":"` + generation + `","baseline":"` + baseline + `"},` +
		`"edits":[{"path":"plugins.enabled","action":"set","value":` + value + `}]}`
}

// TestPluginRuntimeWritesTheSwitchAndReadsItBack is the whole loop end to end: the
// plugin route reads the switch, writes it through the shared journal, and a new
// read observes the persisted value while the effective value stays unclaimed.
func TestPluginRuntimeWritesTheSwitchAndReadsItBack(t *testing.T) {
	fixture := newPluginRuntimeFixture(t)
	view := readPluginRuntime(t, fixture.app)
	if view.Capability.Mode != "managed" || !view.Capability.Readable || !view.Capability.Writable {
		t.Fatalf("capability %+v", view.Capability)
	}
	if len(view.Groups) != 1 || view.Groups[0].ID != "plugins" || view.Groups[0].Prefix != "plugins" {
		t.Fatalf("groups %+v", view.Groups)
	}
	if len(view.Fields) != 1 || view.Fields[0].Path != "plugins.enabled" || view.Fields[0].Presence != "present" {
		t.Fatalf("fields %+v", view.Fields)
	}
	if value, ok := view.Fields[0].Value.(bool); !ok || value {
		t.Fatalf("initial value %+v", view.Fields[0].Value)
	}
	// The accepted gateway page is untouched by the plugin scope.
	page := gatewayRequest(t, fixture.app, http.MethodGet, "gateway/config", "", "")
	if page.Code != http.StatusOK {
		t.Fatalf("page status=%d", page.Code)
	}
	var pageView struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"groups"`
		Fields []struct {
			Path string `json:"path"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &pageView); err != nil {
		t.Fatal(err)
	}
	if len(pageView.Groups) != 11 || len(pageView.Fields) != 39 {
		t.Fatalf("gateway page sizes %d %d", len(pageView.Groups), len(pageView.Fields))
	}
	for _, group := range pageView.Groups {
		if group.ID == "plugins" {
			t.Fatal("the plugin group is on the gateway page")
		}
	}
	for _, field := range pageView.Fields {
		if strings.HasPrefix(field.Path, "plugins.") {
			t.Fatalf("the plugin leaf is on the gateway page: %s", field.Path)
		}
	}

	requestID := strings.Repeat("a1", 16)
	response := gatewayRequest(t, fixture.app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody(view.Generation, view.Groups[0].Baseline, requestID, true), "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	var saved struct {
		Operation struct {
			RequestID string `json:"requestId"`
			Group     string `json:"group"`
			State     string `json:"state"`
			HTTP200   bool   `json:"http200"`
		} `json:"operation"`
		Effective string `json:"effective"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Operation.State != "persisted" || !saved.Operation.HTTP200 || saved.Effective != "unknown" {
		t.Fatalf("save reply %+v", saved)
	}
	fixture.mu.Lock()
	patches := len(fixture.patches)
	fixture.mu.Unlock()
	if patches != 1 {
		t.Fatalf("patches %d", patches)
	}

	after := readPluginRuntime(t, fixture.app)
	if value, ok := after.Fields[0].Value.(bool); !ok || !value {
		t.Fatalf("read-back value %+v", after.Fields[0].Value)
	}
	if after.Groups[0].Persisted != "persisted" || after.Groups[0].Effective != "unknown" {
		t.Fatalf("group after save %+v", after.Groups[0])
	}

	// The baseline echoed by the first read is now stale, and the switch is not
	// written twice on a moved baseline.
	response = gatewayRequest(t, fixture.app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody(view.Generation, view.Groups[0].Baseline, strings.Repeat("a2", 16), false), "application/json")
	if response.Code != http.StatusConflict || errorCode(t, response) != "source_or_operation_conflict" {
		t.Fatalf("stale baseline status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.mu.Lock()
	patches = len(fixture.patches)
	fixture.mu.Unlock()
	if patches != 1 {
		t.Fatalf("a conflicted save reached CPA: %d patches", patches)
	}
}

// TestPluginRuntimeReconcilesAnUnknownReplyByReadBackOnly proves the switch never
// resends a write whose result it cannot prove: the journal holds it, the read-back
// settles it, and the admin's retry path is the reconcile endpoint.
func TestPluginRuntimeReconcilesAnUnknownReplyByReadBackOnly(t *testing.T) {
	fixture := newPluginRuntimeFixture(t)
	view := readPluginRuntime(t, fixture.app)
	fixture.mu.Lock()
	fixture.patchStatus = http.StatusInternalServerError
	fixture.applyFailedWrite = true
	fixture.mu.Unlock()

	requestID := strings.Repeat("b1", 16)
	response := gatewayRequest(t, fixture.app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody(view.Generation, view.Groups[0].Baseline, requestID, true), "application/json")
	if response.Code != http.StatusConflict || errorCode(t, response) != "original_operation_unknown" {
		t.Fatalf("lost reply status=%d body=%s", response.Code, response.Body.String())
	}

	pending := readPluginRuntime(t, fixture.app)
	group := pending.Groups[0]
	if group.Writable {
		t.Fatalf("an unresolved switch is still writable: %+v", group)
	}
	if group.Operation == nil || group.Operation.RequestID != requestID || group.Operation.State != "unknown" {
		t.Fatalf("journal did not survive the reload: %+v", group.Operation)
	}

	response = gatewayRequest(t, fixture.app, http.MethodPost,
		"plugins/runtime/operations/"+requestID+"/reconcile", `{}`, "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	var reconciled struct {
		Operation struct {
			State   string `json:"state"`
			HTTP200 bool   `json:"http200"`
		} `json:"operation"`
		Effective string `json:"effective"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reconciled); err != nil {
		t.Fatal(err)
	}
	if reconciled.Operation.State != "persisted" || reconciled.Operation.HTTP200 || reconciled.Effective != "unknown" {
		t.Fatalf("reconciled %+v", reconciled)
	}
	fixture.mu.Lock()
	patches := len(fixture.patches)
	fixture.mu.Unlock()
	if patches != 1 {
		t.Fatalf("reconcile resent the write: %d patches", patches)
	}
	settled := readPluginRuntime(t, fixture.app)
	if !settled.Groups[0].Writable || settled.Groups[0].Persisted != "persisted" {
		t.Fatalf("switch stayed locked after reconcile: %+v", settled.Groups[0])
	}
}

// TestPluginRuntimeFailsClosedOutsideTheManagedAuthority proves the route refuses
// a profile that does not own the Supervisor destination, on read and on write.
func TestPluginRuntimeFailsClosedOutsideTheManagedAuthority(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL: "https://external.test", ManagementKey: protectedTestEnvelope,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// External holds no installation authority, so the plugin scope's closed
	// refusal is reached without one.
	app.gatewayConfig = freshconfig.New(app.store, nil, nil)
	app.gatewayConfig.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "https://external.test", "management-key", nil
	})

	response := gatewayRequest(t, app, http.MethodGet, "plugins/runtime", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var view pluginRuntimeView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Capability.Writable || view.Capability.Readable || view.Capability.Reason != freshconfig.ReasonPluginManagedOnly {
		t.Fatalf("external capability %+v", view.Capability)
	}
	for _, group := range view.Groups {
		if group.Writable {
			t.Fatalf("external group writable: %+v", group)
		}
	}
	response = gatewayRequest(t, app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody("1", strings.Repeat("a", 64), strings.Repeat("c1", 16), true), "application/json")
	if response.Code != http.StatusConflict || errorCode(t, response) != freshconfig.ReasonPluginManagedOnly {
		t.Fatalf("write status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestPluginRuntimeSurfaceRejectsAnythingOutsideTheContract covers the request
// shape, the method and the resource surface of the plugin route.
func TestPluginRuntimeSurfaceRejectsAnythingOutsideTheContract(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.gatewayConfig = nil
	requestID := strings.Repeat("ab", 16)
	expected := `{"generation":"7","baseline":"` + strings.Repeat("a", 64) + `"}`
	body := `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"plugins.enabled","action":"set","value":true}]}`
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
		{"no authority", http.MethodGet, "plugins/runtime", "", "", http.StatusConflict, "config_authority_unavailable", ""},
		{"wrong read method", http.MethodPost, "plugins/runtime", `{}`, "application/json", http.StatusMethodNotAllowed, "", http.MethodGet},
		{"wrong write method", http.MethodGet, "plugins/runtime/groups/plugins", "", "", http.StatusMethodNotAllowed, "", http.MethodPost},
		{"wrong content type", http.MethodPost, "plugins/runtime/groups/plugins", body, "text/plain", http.StatusUnsupportedMediaType, "", ""},
		{"unknown body key", http.MethodPost, "plugins/runtime/groups/plugins", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[],"yaml":"x"}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"unknown expectation", http.MethodPost, "plugins/runtime/groups/plugins", `{"requestId":"` + requestID + `","expected":{"generation":"7","baseline":"` + strings.Repeat("a", 64) + `","sha256":"x"},"edits":[]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"unknown group", http.MethodPost, "plugins/runtime/groups/gateway", body, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"gateway leaf", http.MethodPost, "plugins/runtime/groups/plugins", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"routing.strategy","action":"set","value":"round-robin"}]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"plugin root", http.MethodPost, "plugins/runtime/groups/plugins", `{"requestId":"` + requestID + `","expected":` + expected + `,"edits":[{"path":"plugins.dir","action":"set","value":"/tmp"}]}`, "application/json", http.StatusBadRequest, "invalid_config_request", ""},
		{"operation without id", http.MethodGet, "plugins/runtime/operations", "", "", http.StatusNotFound, "", ""},
		{"unknown resource", http.MethodGet, "plugins/runtime/rules/filter", "", "", http.StatusNotFound, "", ""},
		{"unknown reconcile method", http.MethodGet, "plugins/runtime/operations/" + requestID + "/reconcile", "", "", http.StatusMethodNotAllowed, "", http.MethodPost},
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
	for _, target := range []string{"plugins/runtime?path=plugins", "plugins/runtime/groups/plugins%2F"} {
		request := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+""+target, nil)
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", target, response.Code)
		}
	}
}
