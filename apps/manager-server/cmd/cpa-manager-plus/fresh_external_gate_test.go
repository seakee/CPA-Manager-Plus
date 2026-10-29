package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// gateAdmin is the browser's Admin header for one legacy-bridge request.
const gateAdmin = "Bearer " + strategyTestAdmin

// gateUpstream is the user's CPA for one gate test: it counts what reached it and
// answers through a handler the test can swap, so a test can decide when the answer
// arrives and whether the body even exists.
type gateUpstream struct {
	requests atomic.Int32
	handle   atomic.Value // func(http.ResponseWriter, *http.Request)
	server   *httptest.Server
}

func newGateUpstream(t *testing.T, management string) *gateUpstream {
	t.Helper()
	upstream := &gateUpstream{}
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+management {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.requests.Add(1)
		upstream.handle.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// gateHTTPRequest is one browser-facing legacy request; the bridge is reached by
// the path the browser sends, never by the `cpamp/` alias.
func gateHTTPRequest(method, path, authorization string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func gateRequest(t *testing.T, app *freshApplication, method, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	app.legacy.ServeHTTP(response, gateHTTPRequest(method, path, authorization))
	return response
}

// statusWatcher reports the moment a handler commits a status, which is the moment
// the credential gate must be released.
type statusWatcher struct {
	*httptest.ResponseRecorder
	committed chan struct{}
	once      sync.Once
}

func (w *statusWatcher) WriteHeader(status int) {
	w.once.Do(func() { close(w.committed) })
	w.ResponseRecorder.WriteHeader(status)
}

// failingStore is a store this Manager cannot read: a saved-key request must fail
// closed rather than guess which credential it would present.
type failingStore struct{ freshstore.Store }

func (failingStore) Load(context.Context) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("store unavailable")
}

// TestExternalModelListDoesNotFileTheCallersRefusal covers the healthy install the
// model list used to break. `/v1/models` forwards the caller's own header, so the
// console's 401 is its own session's, not a refusal of the saved key: filing it
// blocks every typed read until someone rebinds a key that still works.
func TestExternalModelListDoesNotFileTheCallersRefusal(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	models := gateRequest(t, app, http.MethodGet, "/v1/models", "")
	if models.Code != http.StatusUnauthorized {
		t.Fatalf("model list without a caller key = %d %s", models.Code, models.Body.String())
	}
	if app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the caller's own 401 was filed as a refusal of the saved key")
	}
	management := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
	if management.Code != http.StatusOK {
		t.Fatalf("management read after the model list = %d %s", management.Code, management.Body.String())
	}
}

// TestExternalPluginResourceTakesNoCredentialGate keeps the sandboxed plugin
// resource out of the cutover: it authenticates nothing first, so it must neither
// serialize a console read nor file the 401 its own missing credential earns.
func TestExternalPluginResourceTakesNoCredentialGate(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	if !app.legacyCredentialGate.acquire(t.Context()) {
		t.Fatal("could not hold the credential gate")
	}
	defer app.legacyCredentialGate.release()
	request := gateHTTPRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html", "")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Dest", "iframe")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, request)
		done <- response
	}()
	select {
	case response := <-done:
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated plugin resource = %d %s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the unauthenticated plugin resource waited on the credential gate")
	}
	if app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the unauthenticated plugin resource filed a refusal of the saved key")
	}
}

// TestExternalGateCancelledWaiterForwardsNothing keeps a cancelled request out of
// the queue: a caller that stopped waiting never gets a CPA request made on its
// behalf, because the refusal of that request would be filed against a key nobody
// is waiting to hear about.
func TestExternalGateCancelledWaiterForwardsNothing(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	if !app.legacyCredentialGate.acquire(t.Context()) {
		t.Fatal("could not hold the credential gate")
	}
	defer app.legacyCredentialGate.release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.legacy.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request kept waiting for the credential gate")
	}
	if sent := upstream.requests.Load(); sent != 0 {
		t.Fatalf("a cancelled request made %d CPA requests", sent)
	}
}

// TestExternalStaleRefusalIsFiledAgainstTheKeyThatWasSent covers the rebind race:
// a slow 401 sent with the replaced key arrives after the rebind committed, and it
// must be filed against the envelope that was actually presented. Filing it against
// the rebound key would block a key nobody has tried yet.
func TestExternalStaleRefusalIsFiledAgainstTheKeyThatWasSent(t *testing.T) {
	const rebound = "aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999"
	enter := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+rebound {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		enter <- struct{}{}
		<-release
		w.WriteHeader(http.StatusUnauthorized)
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	stale := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin))
		stale <- response.Code
	}()
	select {
	case <-enter:
	case <-time.After(5 * time.Second):
		t.Fatal("the stale read never reached CPA")
	}
	// The rebind commits while the old-key request is still in flight.
	protected, err := app.protector.ProtectString(rebound)
	if err != nil {
		t.Fatal(err)
	}
	rebinder, ok := app.store.(freshstore.ConnectionRebinder)
	if !ok {
		t.Fatal("the fixture store cannot rebind")
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rebinder.RebindConnection(t.Context(), record.Operation.Identity, record.Operation.Revision,
		model.ManagerCPAConnectionConfig{CPABaseURL: record.Connection.CPABaseURL, ManagementKey: protected}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case code := <-stale:
		if code != http.StatusConflict {
			t.Fatalf("stale read = %d, want the typed blocker", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stale read never answered")
	}
	if app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the stale refusal was filed against the rebound key")
	}
	if response := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); response.Code != http.StatusOK {
		t.Fatalf("rebound read = %d %s", response.Code, response.Body.String())
	}
}

// TestExternalGateReleasesTheCredentialOnceTheStatusIsKnown bounds the gate: it is
// held until the answer's status exists and not for the body. A slow reader would
// otherwise stall every console CPA read for as long as one response takes to
// stream.
func TestExternalGateReleasesTheCredentialOnceTheStatusIsKnown(t *testing.T) {
	release := make(chan struct{})
	upstream := newGateUpstream(t, externalAuthorityManagement)
	normal := upstream.handle.Load().(func(http.ResponseWriter, *http.Request))
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/config.yaml" {
			normal(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte(`{}`))
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	streamed := &statusWatcher{ResponseRecorder: httptest.NewRecorder(), committed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.legacy.ServeHTTP(streamed, gateHTTPRequest(http.MethodGet, "/v0/management/config.yaml", gateAdmin))
	}()
	select {
	case <-streamed.committed:
	case <-time.After(5 * time.Second):
		t.Fatal("the streamed read never committed a status")
	}
	second := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin))
		second <- response.Code
	}()
	select {
	case code := <-second:
		if code != http.StatusOK {
			t.Fatalf("second read while a body streams = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second console read waited for a streaming body")
	}
	close(release)
	<-done
}

// TestExternalFirstRefusalBodySurvivesARealServer covers the response the operator
// actually reads. Behind a real server the proxy has already copied CPA's entity
// headers, so a local refusal that keeps them is truncated or empty: the empty 401
// and the 34-byte 401 must both answer one readable typed body.
func TestExternalFirstRefusalBodySurvivesARealServer(t *testing.T) {
	for _, scenario := range []struct {
		name string
		body string
	}{
		{"empty refusal", ""},
		{"refusal with a body", `{"error":{"message":"invalid api key"}}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			upstream := newGateUpstream(t, externalAuthorityManagement)
			upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(scenario.body)))
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(scenario.body))
			})
			app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
			server := httptest.NewServer(app.legacy)
			t.Cleanup(server.Close)
			request, err := http.NewRequest(http.MethodGet, server.URL+"/v0/management/api-keys", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", gateAdmin)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("the refusal body is unreadable: %v", err)
			}
			var payload struct {
				Code string `json:"code"`
			}
			if err = json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("the refusal body %q is unreadable: %v", body, err)
			}
			if response.StatusCode != http.StatusConflict || payload.Code != freshconfig.ReasonManagementRejected {
				t.Fatalf("status=%d code=%q body=%q", response.StatusCode, payload.Code, body)
			}
			// The framing must describe this local body, never the refused one.
			if length := response.Header.Get("Content-Length"); length != "" && length != strconv.Itoa(len(body)) {
				t.Fatalf("Content-Length %q describes %d bytes, not CPA's refused body", length, len(body))
			}
			if encoding := response.Header.Get("Content-Encoding"); encoding != "" {
				t.Fatalf("the local refusal kept CPA's Content-Encoding %q", encoding)
			}
		})
	}
}

// TestExternalGateTranslatesACPAForbidden is the second refusal shape: CPA answers
// 403 to a client it refuses (a ban, or remote management disabled), and the bridge
// names it exactly as the 2a routes do, with the detail that says the ban expires.
func TestExternalGateTranslatesACPAForbidden(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	response := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
	if response.Code != http.StatusConflict {
		t.Fatalf("forbidden read = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != freshconfig.ReasonManagementRejected || payload.Detail != externalRebindForbiddenDetail {
		t.Fatalf("code=%q detail=%q", payload.Code, payload.Detail)
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the 403 was not recorded as a refusal")
	}
}

// TestExternalGateFailsClosedWhenTheInstructionCannotBeRead: a Manager that cannot
// read its own committed instruction cannot prove which credential the request
// would present, so it answers the typed unavailability and forwards nothing.
func TestExternalGateFailsClosedWhenTheInstructionCannotBeRead(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	app.store = failingStore{Store: app.store}
	response := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable instruction = %d %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != "external_cpa_authority_unavailable" {
		t.Fatalf("code = %q", code)
	}
	if sent := upstream.requests.Load(); sent != 0 {
		t.Fatalf("a failed-closed request made %d CPA requests", sent)
	}
}

// TestExternalWritesAnswerTheRecordedRefusalLocally covers the Fresh writes, not
// just the bridge: a gateway save and a key install would each present a credential
// the CPA already refused, and five refusals ban the address for thirty minutes.
// Both answer the typed blocker without a CPA request.
func TestExternalWritesAnswerTheRecordedRefusalLocally(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	app.noteExternalRejection(t.Context(), app.externalReadGrant(t.Context()), cpa.ErrManagementRejected)
	before := upstream.requests.Load()
	save := gatewayRequest(t, app, http.MethodPost, "gateway/config/groups/retry",
		`{"requestId":"`+strings.Repeat("a", 32)+`","expected":{"generation":"1","baseline":"`+strings.Repeat("b", 64)+`"},"edits":[{"path":"routing.retry.request-retry","action":"set","value":3}]}`,
		"application/json")
	if save.Code != http.StatusConflict || errorCode(t, save) != freshconfig.ReasonManagementRejected {
		t.Fatalf("gateway save = %d %s", save.Code, save.Body.String())
	}
	created := gatewayRequest(t, app, http.MethodPost, "api-keys",
		`{"requestId":"`+strings.Repeat("c", 32)+`"}`, "application/json")
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("key create = %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil || body.Key.ID == "" {
		t.Fatalf("key create body = %s", created.Body.String())
	}
	install := gatewayRequest(t, app, http.MethodPost, "api-keys/"+body.Key.ID+"/initial-install", `{}`, "application/json")
	if install.Code != http.StatusConflict || errorCode(t, install) != freshconfig.ReasonManagementRejected {
		t.Fatalf("key install = %d %s", install.Code, install.Body.String())
	}
	if sent := upstream.requests.Load() - before; sent != 0 {
		t.Fatalf("writes presented the refused key %d times", sent)
	}
}

// TestExternalPluginManagementIsGatedAfterARefusal keeps the plugin management
// proxy inside the cutover: it presents the saved key, so a refusal must answer it
// locally rather than spend one more failed authentication.
func TestExternalPluginManagementIsGatedAfterARefusal(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	first := gateRequest(t, app, http.MethodGet, "/v0/management/codex-invite/invite", gateAdmin)
	if first.Code != http.StatusConflict {
		t.Fatalf("plugin management = %d %s", first.Code, first.Body.String())
	}
	before := upstream.requests.Load()
	repeated := gateRequest(t, app, http.MethodGet, "/v0/management/codex-invite/invite", gateAdmin)
	if repeated.Code != http.StatusConflict || errorCode(t, repeated) != freshconfig.ReasonManagementRejected {
		t.Fatalf("repeated plugin management = %d %s", repeated.Code, repeated.Body.String())
	}
	if sent := upstream.requests.Load() - before; sent != 0 {
		t.Fatalf("the bridge presented the refused key %d more times", sent)
	}
}

// TestExternalGateLeavesAdoptAndNewManagedAlone is the cutover's scope: on those
// profiles the bridge is the Console's own contract, so neither the replaced-write
// refusal nor the External blocker may answer their legacy requests.
func TestExternalGateLeavesAdoptAndNewManagedAlone(t *testing.T) {
	managed := newInstallationTestApplication(t)
	markFreshProductComplete(t, managed)
	scenarios := []struct {
		name string
		app  *freshApplication
	}{
		{"new managed", managed},
		{"adopt", newAdoptRetryFixture(t).app},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			write := gateRequest(t, scenario.app, http.MethodPut, "/v0/management/api-keys", gateAdmin)
			if code := gateErrorCode(write); code == externalLegacyWriteReplaced || code == freshconfig.ReasonManagementRejected {
				t.Fatalf("%s answered the External cutover: %d %s", scenario.name, write.Code, write.Body.String())
			}
			read := gateRequest(t, scenario.app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
			if code := gateErrorCode(read); code == freshconfig.ReasonManagementRejected {
				t.Fatalf("%s answered the External blocker: %d %s", scenario.name, read.Code, read.Body.String())
			}
		})
	}
}

// gateErrorCode reads the typed code of one answer, and reports an empty string for
// a body that is not this Manager's JSON: those profiles can answer with CPA's own
// shape, which is exactly what "unchanged" means here.
func gateErrorCode(response *httptest.ResponseRecorder) string {
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		return ""
	}
	return payload.Code
}
