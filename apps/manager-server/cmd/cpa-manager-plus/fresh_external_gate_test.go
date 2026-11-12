package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/router"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	codexsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	modelpricesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/modelprice"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
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
// is waiting to hear about. It is answered locally instead of silently dropped: no
// status at all is indistinguishable from a Manager that hung.
func TestExternalGateCancelledWaiterForwardsNothing(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	if !app.legacyCredentialGate.acquire(t.Context()) {
		t.Fatal("could not hold the credential gate")
	}
	defer app.legacyCredentialGate.release()
	// The decision reads this Manager's own store before it reaches the gate, and a
	// cancelled context would fail that read instead of the wait. The store here
	// answers regardless of the caller, so the request really queues on the gate
	// and the cancellation is what ends it.
	app.store = uncancellableStore{app.store}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.legacy.ServeHTTP(response, request)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request kept waiting for the credential gate")
	}
	if response.Code != statusClientClosedRequest {
		t.Fatalf("cancelled waiter = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != externalCredentialWaitCancelled {
		t.Fatalf("cancelled waiter body = %q (%v)", response.Body.String(), err)
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

// uncancellableStore answers its own reads regardless of the caller, so a test can
// cancel a request before it reaches the gate without failing the decision first.
type uncancellableStore struct{ freshstore.Store }

func (s uncancellableStore) Load(ctx context.Context) (freshstore.Record, error) {
	return s.Store.Load(context.WithoutCancel(ctx))
}

// gateIsFree probes the credential gate without waiting for it: the slot has one
// seat, so a held gate is a full channel.
func gateIsFree(app *freshApplication) bool {
	select {
	case app.legacyCredentialGate.slot <- struct{}{}:
		<-app.legacyCredentialGate.slot
		return true
	default:
		return false
	}
}

// statusCapture keeps every status it is handed, including the informational ones
// a plain recorder would swallow. Tests about which status decides need both.
type statusCapture struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
}

func newStatusCapture() *statusCapture { return &statusCapture{header: http.Header{}} }

func (w *statusCapture) Header() http.Header { return w.header }

func (w *statusCapture) WriteHeader(status int) { w.codes = append(w.codes, status) }

func (w *statusCapture) Write(body []byte) (int, error) { return w.body.Write(body) }

func (w *statusCapture) Flush() {}

// hijackCapture is the same writer plus the upgrade surface a hijacked response
// needs.
type hijackCapture struct {
	*statusCapture
	hijacked bool
}

func (w *hijackCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

// gateRecorder builds the recorder exactly as the guard does, over an already
// acquired gate, so a test can drive one status at a time.
func gateRecorder(t *testing.T, app *freshApplication, w http.ResponseWriter) *externalLegacyRecorder {
	t.Helper()
	if !app.legacyCredentialGate.acquire(t.Context()) {
		t.Fatal("could not hold the credential gate")
	}
	markContext, mark := proxysvc.WithSavedKeyMark(t.Context())
	// The proxy marks the request in its Director when it attaches the saved key;
	// a recorder built outside the proxy marks it here, so a 401 is decided as one.
	proxysvc.NoteSavedManagementKey(markContext)
	hold, requestCtx := beginExternalCredentialHold(markContext, &app.legacyCredentialGate, time.Minute)
	t.Cleanup(hold.finish)
	return &externalLegacyRecorder{
		ResponseWriter: w, ctx: requestCtx, app: app, hold: hold, mark: mark,
	}
}

// TestExternalRecorderIgnoresInformationalStatus keeps an interim status from
// deciding anything: a 103 says nothing about the credential, so the gate stays
// held until the CPA's final status exists.
func TestExternalRecorderIgnoresInformationalStatus(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	capture := newStatusCapture()
	recorder := gateRecorder(t, app, capture)
	recorder.WriteHeader(http.StatusEarlyHints)
	if gateIsFree(app) {
		t.Fatal("an informational status released the credential gate")
	}
	recorder.WriteHeader(http.StatusUnauthorized)
	if len(capture.codes) != 2 || capture.codes[0] != http.StatusEarlyHints || capture.codes[1] != http.StatusConflict {
		t.Fatalf("statuses = %v", capture.codes)
	}
	if !strings.Contains(capture.body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("body = %q", capture.body.String())
	}
	if !gateIsFree(app) {
		t.Fatal("the final status did not release the credential gate")
	}
}

// TestExternalRecorderReleasesTheGateOnHijack covers the upgrade: a hijacked
// connection never carries a status this gate can decide, so the lock must not
// outlive the handshake.
func TestExternalRecorderReleasesTheGateOnHijack(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	capture := &hijackCapture{statusCapture: newStatusCapture()}
	recorder := gateRecorder(t, app, capture)
	recorder.WriteHeader(http.StatusSwitchingProtocols)
	if _, _, err := recorder.Hijack(); err != nil {
		t.Fatalf("hijack: %v", err)
	}
	if !capture.hijacked {
		t.Fatal("the hijack never reached the real writer")
	}
	if !gateIsFree(app) {
		t.Fatal("a hijacked response kept the credential gate")
	}
}

// TestExternalRecorderRefusesToHijackWithoutAnUpgradeableWriter proves the gate is
// not released by a hijack that never happened: the caller keeps its tenure.
func TestExternalRecorderRefusesToHijackWithoutAnUpgradeableWriter(t *testing.T) {
	upstream := newGateUpstream(t, externalAuthorityManagement)
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	recorder := gateRecorder(t, app, newStatusCapture())
	if _, _, err := recorder.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("hijack error = %v", err)
	}
	if gateIsFree(app) {
		t.Fatal("an unsupported hijack released the credential gate")
	}
}

// TestExternalGateBudgetFreesAHungCPA bounds the wait for the CPA's headers. A CPA
// that accepts the connection and never answers must not hold every other console
// read behind it.
func TestExternalGateBudgetFreesAHungCPA(t *testing.T) {
	block := make(chan struct{})
	var first atomic.Bool
	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(false, true) {
			<-block
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	app.credentialHeaderBudget = 100 * time.Millisecond
	hung := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin))
		hung <- response.Code
	}()
	waitForCondition(t, func() bool { return upstream.requests.Load() == 1 })
	second := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin))
		second <- response.Code
	}()
	select {
	case code := <-second:
		if code != http.StatusOK {
			t.Fatalf("the read behind a hung CPA = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a hung CPA held the credential gate past its %s budget", app.credentialHeaderBudget)
	}
	close(block)
	select {
	case code := <-hung:
		if code != http.StatusBadGateway {
			t.Fatalf("the hung read = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hung read never ended")
	}
}

// externalProviderCallDelay is how long the fake provider takes to send its
// headers: several times the console-read budget the same test installs.
const externalProviderCallDelay = 400 * time.Millisecond

// TestExternalProviderCallOutlivesTheHeaderBudget covers the regression the read
// budget introduced: `v0/management/api-call` forwards a request to an upstream
// provider, and a non-streaming completion can take far longer than any console
// read to send its headers. Cutting it at the read budget turned a healthy slow
// answer into a 502. The console read keeps its budget.
func TestExternalProviderCallOutlivesTheHeaderBudget(t *testing.T) {
	// The blocked read below outlives the assertion, so the handler is released
	// before the upstream server's own cleanup closes it.
	block := make(chan struct{})
	defer close(block)
	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/api-call":
			time.Sleep(externalProviderCallDelay)
		case "/v0/management/api-keys":
			// This read never answers, so only the budget can end it.
			<-block
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":200}`))
	})
	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	app.credentialHeaderBudget = 100 * time.Millisecond

	call := gateRequest(t, app, http.MethodPost, "/v0/management/api-call", gateAdmin)
	if call.Code != http.StatusOK {
		t.Fatalf("a provider call answered after %s = %d %s", externalProviderCallDelay, call.Code, call.Body.String())
	}
	if body := call.Body.String(); !strings.Contains(body, `"status_code":200`) {
		t.Fatalf("provider call body = %q", body)
	}
	if !gateIsFree(app) {
		t.Fatal("the provider call kept the credential gate after its status")
	}

	read := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.legacy.ServeHTTP(response, gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin))
		read <- response.Code
	}()
	select {
	case code := <-read:
		if code != http.StatusBadGateway {
			t.Fatalf("a management read past the %s budget = %d", app.credentialHeaderBudget, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a management read survived its %s budget", app.credentialHeaderBudget)
	}
}

// TestExternalProviderBoundPathsOutliveHeaderBudgetWhileConsoleReadsExpire
// verifies through real HTTP proxy requests (with zero sleeps) that provider-bound
// paths (/v0/management/api-call/ with trailing slash and /v0/management/oauth-callback)
// survive past two full console-read header-budget expirations, while CPA console
// reads (/v0/management/config.yaml) expire with 502 and /v1/models forwards
// without saved-key budget gating.
func TestExternalProviderBoundPathsOutliveHeaderBudgetWhileConsoleReadsExpire(t *testing.T) {
	const budget = 50 * time.Millisecond

	releaseHungReads := make(chan struct{})
	defer close(releaseHungReads)
	type providerGateRun struct {
		entered chan struct{}
		ready   chan struct{}
		once    sync.Once
	}
	var activeProviderRun atomic.Value
	activeProviderRun.Store(&providerGateRun{
		entered: make(chan struct{}),
		ready:   make(chan struct{}),
	})

	upstream := newGateUpstream(t, externalAuthorityManagement)
	upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/api-call/", "/v0/management/oauth-callback":
			run := activeProviderRun.Load().(*providerGateRun)
			run.once.Do(func() { close(run.entered) })
			<-run.ready
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v0/management/config.yaml":
			<-releaseHungReads
		case "/v1/models":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	app.credentialHeaderBudget = budget

	refAppA := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	refAppA.credentialHeaderBudget = budget
	refAppB := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
	refAppB.credentialHeaderBudget = budget

	for _, providerPath := range []string{"/v0/management/api-call/", "/v0/management/oauth-callback"} {
		t.Run(strings.Trim(providerPath, "/"), func(t *testing.T) {
			run := &providerGateRun{
				entered: make(chan struct{}),
				ready:   make(chan struct{}),
			}
			activeProviderRun.Store(run)
			providerDone := make(chan int, 1)
			go func() {
				providerDone <- gateRequest(t, app, http.MethodPost, providerPath, gateAdmin).Code
			}()

			select {
			case <-run.entered:
			case <-time.After(5 * time.Second):
				close(run.ready)
				t.Fatalf("%s did not enter upstream handler before reference budget expirations", providerPath)
			}

			// Drive two real sequential console-read header-budget expirations (>= 2*budget
			// elapsed wall-clock time) after provider entry before allowing response headers.
			if code := gateRequest(t, refAppA, http.MethodGet, "/v0/management/config.yaml", gateAdmin).Code; code != http.StatusBadGateway {
				close(run.ready)
				t.Fatalf("first hung config.yaml read = %d, want 502", code)
			}
			if code := gateRequest(t, refAppB, http.MethodGet, "/v0/management/config.yaml", gateAdmin).Code; code != http.StatusBadGateway {
				close(run.ready)
				t.Fatalf("second hung config.yaml read = %d, want 502", code)
			}
			close(run.ready)
			select {
			case code := <-providerDone:
				if code != http.StatusOK {
					t.Fatalf("%s cut by header budget: status = %d, want 200", providerPath, code)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not complete after provider answered", providerPath)
			}
		})
	}

	if code := gateRequest(t, app, http.MethodGet, "/v0/management/config.yaml", gateAdmin).Code; code != http.StatusBadGateway {
		t.Fatalf("console config.yaml read past %s budget = %d, want 502", budget, code)
	}
	if code := gateRequest(t, app, http.MethodGet, "/v1/models", "").Code; code != http.StatusUnauthorized {
		t.Fatalf("/v1/models without client key = %d, want 401", code)
	}
	if app.externalReadGrant(t.Context()).Refused {
		t.Fatal("/v1/models 401 or timed-out console read falsely marked saved management key refused")
	}
}

func TestExternalRuntimeUpdatesSerializesAndRecordsRejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			upstream := newGateUpstream(t, externalAuthorityManagement)
			upstream.handle.Store(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered) })
				<-release
				w.WriteHeader(status)
			})
			app := newExternalApp(t, upstream.server.URL, externalAuthorityManagement)
			before := upstream.requests.Load()

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				firstDone <- gateRequest(t, app, http.MethodGet, "/usage-service/runtime/updates", gateAdmin)
			}()
			<-entered
			go func() {
				secondDone <- gateRequest(t, app, http.MethodGet, "/usage-service/runtime/updates", gateAdmin)
			}()
			time.Sleep(25 * time.Millisecond)
			if got := upstream.requests.Load() - before; got != 1 {
				t.Fatalf("concurrent runtime/updates probes reached upstream %d times while first was in flight, want 1", got)
			}
			close(release)
			first := <-firstDone
			second := <-secondDone
			if first.Code != http.StatusServiceUnavailable && first.Code != http.StatusConflict {
				t.Fatalf("first runtime/updates status = %d body=%s", first.Code, first.Body.String())
			}
			if second.Code != http.StatusServiceUnavailable {
				t.Fatalf("second runtime/updates status = %d body=%s", second.Code, second.Body.String())
			}
			if !app.externalReadGrant(t.Context()).Refused {
				t.Fatal("runtime/updates rejection did not record externalReadGrant.Refused")
			}
			management := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
			if management.Code != http.StatusConflict || errorCode(t, management) != freshconfig.ReasonManagementRejected {
				t.Fatalf("subsequent management read after runtime/updates refusal = %d body=%s", management.Code, management.Body.String())
			}
			if got := upstream.requests.Load() - before; got != 1 {
				t.Fatalf("total upstream requests = %d, want 1", got)
			}
		})
	}
}

// newStatefulExternalCPA models a real External CPA v8/v0 management surface
// with live client-key and configuration state so multi-step key operations,
// gateway saves/reconciles, runtime status, and model/usage reads can be
// exercised end-to-end across credential rotations and rebinds.
type statefulExternalCPA struct {
	mu           sync.Mutex
	activeKey    string
	refuseStatus int
	clientKeys   []string
	authSeen     []string
	requests     atomic.Int32
	onRequest    func(r *http.Request) bool
	server       *httptest.Server
}

func newStatefulExternalCPA(t *testing.T, initialKey string) *statefulExternalCPA {
	t.Helper()
	s := &statefulExternalCPA{
		activeKey:    initialKey,
		refuseStatus: http.StatusUnauthorized,
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		authHeader := r.Header.Get("Authorization")
		s.mu.Lock()
		s.authSeen = append(s.authSeen, r.Method+" "+r.URL.Path+" "+authHeader)
		expected := "Bearer " + s.activeKey
		refuse := s.refuseStatus
		keysSnapshot := append([]string(nil), s.clientKeys...)
		hook := s.onRequest
		s.mu.Unlock()
		if hook != nil && hook(r) {
			return
		}
		if authHeader != expected {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(refuse)
			_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.13")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":false,"redis-usage-queue-retention-seconds":60}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"codex-1.json","name":"codex-1.json","auth_index":"idx-1","type":"codex","account":"acct-1@example.com","disabled":true},{"id":"codex-2.json","name":"codex-2.json","auth_index":"idx-2","type":"codex","account":"acct-2@example.com","disabled":false}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"plan_type":"plus"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			rawKeys, _ := json.Marshal(keysSnapshot)
			_, _ = w.Write([]byte(`{"routing":{"strategy":"round-robin","retry":{"request-retry":3}},"plugins":{"dir":"/var/lib/cpa/plugins"},"access":{"api-keys":` + string(rawKeys) + `}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config.yaml":
			rawKeys, _ := json.Marshal(keysSnapshot)
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":` + string(rawKeys) + `}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys":
			rawKeys, _ := json.Marshal(keysSnapshot)
			_, _ = w.Write(rawKeys)
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys":
			var updated []string
			if json.NewDecoder(r.Body).Decode(&updated) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.clientKeys = updated
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func stageExternalKeyForGateTest(t *testing.T, app *freshApplication, reqChar string, installOnCPA bool) string {
	t.Helper()
	created := gatewayRequest(t, app, http.MethodPost, "api-keys",
		`{"requestId":"`+strings.Repeat(reqChar, 32)+`"}`, "application/json")
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("create key = %d %s", created.Code, created.Body.String())
	}
	var decoded struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &decoded); err != nil || decoded.Key.ID == "" {
		t.Fatalf("decode created key %q: %v", created.Body.String(), err)
	}
	if installOnCPA {
		installed := gatewayRequest(t, app, http.MethodPost, "api-keys/"+decoded.Key.ID+"/initial-install", `{}`, "application/json")
		if installed.Code != http.StatusOK {
			t.Fatalf("initial-install key %s = %d %s", decoded.Key.ID, installed.Code, installed.Body.String())
		}
	}
	return decoded.Key.ID
}

// TestExternalFreshNativeSurfacesSerializeAndRecordFirstRejection proves that
// every Fresh-native External CPA surface (gateway config read/save/reconcile,
// client key list/observe/apply/revocation-prepare/check/continue, runtime
// status, and plugin capability probing) serializes outbound stored-credential
// requests on legacyCredentialGate and records the first 401/403 refusal before
// releasing the gate, so a concurrent cross-surface burst sends only 1 request
// to CPA instead of tripping CPA's five-strike address ban.
func TestExternalFreshNativeSurfacesSerializeAndRecordFirstRejection(t *testing.T) {
	const rotatedKey = "rotated-key-1111222233334444555566667777888899990000aaaabbbbcccc"
	leaders := []string{
		"gateway_config_read",
		"gateway_config_save",
		"gateway_config_reconcile",
		"api_keys_list",
		"api_keys_observe",
		"api_keys_apply",
		"api_keys_revocation_prepare",
		"api_keys_operation_check",
		"api_keys_operation_continue",
		"runtime_status",
		"plugin_capability_probe",
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, leader := range leaders {
			t.Run(http.StatusText(status)+"/"+leader, func(t *testing.T) {
				cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
				app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
				app.gatewayConfig = gatewayFor(app)

				installedA := stageExternalKeyForGateTest(t, app, "1", true)
				installedB := stageExternalKeyForGateTest(t, app, "2", true)
				unstagedApplyKey := stageExternalKeyForGateTest(t, app, "3", false)
				generation, baseline := externalPageTarget(t, app, "routing")

				var opID string
				if leader == "api_keys_operation_check" || leader == "api_keys_operation_continue" {
					opID = strings.Repeat("4", 32)
					prep := gatewayRequest(t, app, http.MethodPost, "api-keys/"+installedA+"/revocation",
						`{"requestId":"`+opID+`","keeperId":"`+installedB+`","mode":"revoke"}`, "application/json")
					if prep.Code != http.StatusOK {
						t.Fatalf("prepare revocation = %d %s", prep.Code, prep.Body.String())
					}
				}
				reconcileID := strings.Repeat("5", 32)
				if leader == "gateway_config_reconcile" {
					rec, err := app.store.Load(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(v *freshstore.Record) error {
						if v.ConfigGroups == nil {
							v.ConfigGroups = map[string]*freshstore.ConfigGroupJournal{}
						}
						v.ConfigGroups["routing"] = &freshstore.ConfigGroupJournal{
							Latest: reconcileID,
							Operations: map[string]freshstore.ConfigGroupOperation{
								reconcileID: {
									SourceID:   rec.Operation.Identity.InstallationID,
									Generation: generation,
									RequestID:  reconcileID,
									Group:      "routing",
									Target:     "group",
									Prefix:     "routing",
									Baseline:   baseline,
									Edits:      []freshstore.ConfigGroupEdit{{Path: "routing.strategy", Action: "set", Value: "fill-first"}},
									State:      "unknown",
								},
							},
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}

				firstEntered := make(chan struct{})
				secondEntered := make(chan struct{})
				releaseFirst := make(chan struct{})
				var enterCount atomic.Int32
				cpaSrv.mu.Lock()
				cpaSrv.activeKey = rotatedKey
				cpaSrv.refuseStatus = status
				cpaSrv.onRequest = func(_ *http.Request) bool {
					n := enterCount.Add(1)
					if n == 1 {
						close(firstEntered)
						<-releaseFirst
					} else if n == 2 {
						close(secondEntered)
					}
					return false
				}
				cpaSrv.mu.Unlock()

				const followerCount = 5
				var parkedWaiters atomic.Int32
				allWaitersParked := make(chan struct{})
				var parkedOnce sync.Once
				app.legacyCredentialGate.onWait = func() {
					if parkedWaiters.Add(1) >= followerCount {
						parkedOnce.Do(func() { close(allWaitersParked) })
					}
				}
				defer func() { app.legacyCredentialGate.onWait = nil }()

				before := cpaSrv.requests.Load()
				leaderErr := make(chan string, 1)
				go func() {
					switch leader {
					case "gateway_config_read":
						res := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "gateway/config read = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "gateway_config_save":
						body := `{"requestId":"` + strings.Repeat("6", 32) + `","expected":{"generation":"` + generation + `","baseline":"` + baseline + `"},"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`
						res := gatewayRequest(t, app, http.MethodPost, "gateway/config/groups/routing", body, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "gateway/config save = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "gateway_config_reconcile":
						res := gatewayRequest(t, app, http.MethodPost, "gateway/config/operations/"+reconcileID+"/reconcile", `{}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "gateway/config reconcile = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_list":
						res := gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys list = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_observe":
						res := gatewayRequest(t, app, http.MethodGet, "api-keys/"+unstagedApplyKey+"/initial-install", "", "")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys observe = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_apply":
						res := gatewayRequest(t, app, http.MethodPost, "api-keys/"+unstagedApplyKey+"/initial-install", `{}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys apply = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_revocation_prepare":
						res := gatewayRequest(t, app, http.MethodPost, "api-keys/"+installedA+"/revocation",
							`{"requestId":"`+strings.Repeat("7", 32)+`","keeperId":"`+installedB+`","mode":"revoke"}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys revocation prepare = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_operation_check":
						res := gatewayRequest(t, app, http.MethodGet, "api-keys/operations/"+opID, "", "")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys operation check = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "api_keys_operation_continue":
						res := gatewayRequest(t, app, http.MethodPost, "api-keys/operations/"+opID+"/continue", `{}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "api-keys operation continue = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "runtime_status":
						res := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
						var body freshRuntimeStatusResponse
						if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.ErrorCode != freshconfig.ReasonManagementRejected {
							leaderErr <- "runtime/status = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "plugin_capability_probe":
						if app.v8PluginCapable(t.Context()) {
							leaderErr <- "v8PluginCapable = true on rejected credential"
							return
						}
					}
					leaderErr <- ""
				}()

				select {
				case <-firstEntered:
				case <-time.After(5 * time.Second):
					t.Fatalf("leader %s never reached CPA", leader)
				}

				followerErrs := make(chan string, followerCount)
				go func() {
					res := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower gateway/config = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower api-keys = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodPost, "api-keys/"+unstagedApplyKey+"/initial-install", `{}`, "application/json")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower initial-install = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
					var body freshRuntimeStatusResponse
					if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.ErrorCode != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower runtime/status = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", "")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower cpamp/v1/models = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()

				select {
				case <-allWaitersParked:
				case <-secondEntered:
				case <-time.After(5 * time.Second):
				}
				close(releaseFirst)

				if msg := <-leaderErr; msg != "" {
					t.Fatal(msg)
				}
				for range followerCount {
					if msg := <-followerErrs; msg != "" {
						t.Fatal(msg)
					}
				}
				if !app.externalReadGrant(t.Context()).Refused {
					t.Fatalf("leader %s did not record durable credential refusal", leader)
				}
				if got := cpaSrv.requests.Load() - before; got != 1 {
					t.Fatalf("External CPA received %d management requests across leader %s + concurrent burst, want 1", got, leader)
				}
			})
		}
	}
}

// TestExternalFreshNativeSurfacesBindSingleSnapshotAcrossRebind proves that a
// Fresh-native External caller queued behind legacyCredentialGate during a
// concurrent rebind resolves both its refusal grant and outbound credential
// from one post-gate snapshot (never mixing a pre-gate grant digest with a
// post-rebind credential or overwriting a newer refusal with a stale outer
// grant).
func TestExternalFreshNativeSurfacesBindSingleSnapshotAcrossRebind(t *testing.T) {
	const reboundKey = "rebound-key-aaaabbbbccccddddeeeeffff0000111122223333444455556666"
	const rotatedAgainKey = "rotated-again-9999888877776666555544443333222211110000ffffeeee"
	surfaces := []string{
		"gateway_config_read",
		"api_keys_list",
		"api_keys_revocation_prepare",
		"runtime_status",
		"runtime_updates",
	}
	for _, surface := range surfaces {
		t.Run(surface, func(t *testing.T) {
			cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
			app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
			app.gatewayConfig = gatewayFor(app)

			keyA := stageExternalKeyForGateTest(t, app, "a", true)
			keyB := stageExternalKeyForGateTest(t, app, "b", true)

			holderEntered := make(chan struct{})
			releaseHolder := make(chan struct{})
			var holderOnce sync.Once
			cpaSrv.mu.Lock()
			cpaSrv.onRequest = func(r *http.Request) bool {
				if r.URL.Path == "/v0/management/api-keys" {
					holderOnce.Do(func() {
						close(holderEntered)
						<-releaseHolder
					})
				}
				return false
			}
			cpaSrv.mu.Unlock()

			holderDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				holderDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
			}()
			select {
			case <-holderEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("gate holder never reached CPA")
			}

			waiterParked := make(chan struct{})
			var waiterOnce sync.Once
			app.legacyCredentialGate.onWait = func() {
				waiterOnce.Do(func() { close(waiterParked) })
			}
			defer func() { app.legacyCredentialGate.onWait = nil }()

			waiterDone := make(chan string, 1)
			go func() {
				switch surface {
				case "gateway_config_read":
					res := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						waiterDone <- "gateway/config = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
				case "api_keys_list":
					res := gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						waiterDone <- "api-keys = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
				case "api_keys_revocation_prepare":
					res := gatewayRequest(t, app, http.MethodPost, "api-keys/"+keyA+"/revocation",
						`{"requestId":"`+strings.Repeat("c", 32)+`","keeperId":"`+keyB+`","mode":"revoke"}`, "application/json")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						waiterDone <- "revocation prepare = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
				case "runtime_status":
					res := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
					var body freshRuntimeStatusResponse
					if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.ErrorCode != freshconfig.ReasonManagementRejected {
						waiterDone <- "runtime/status = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
				case "runtime_updates":
					res := gateRequest(t, app, http.MethodGet, "/usage-service/runtime/updates", gateAdmin)
					if res.Code != http.StatusServiceUnavailable && res.Code != http.StatusConflict {
						waiterDone <- "runtime/updates = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
				}
				waiterDone <- ""
			}()

			select {
			case <-waiterParked:
			case msg := <-waiterDone:
				close(releaseHolder)
				<-holderDone
				t.Fatalf("surface %s did not wait on legacyCredentialGate (%s)", surface, msg)
			case <-time.After(5 * time.Second):
				close(releaseHolder)
				t.Fatalf("surface %s never reached legacyCredentialGate", surface)
			}

			// Rebind to reboundKey while waiter is parked on the gate, then rotate
			// CPA again so reboundKey itself meets a 401 when waiter proceeds.
			cpaSrv.mu.Lock()
			cpaSrv.activeKey = reboundKey
			cpaSrv.mu.Unlock()
			rebindRes := gatewayRequest(t, app, http.MethodPost, "external/rebind",
				`{"requestId":"`+strings.Repeat("d", 32)+`","managementKey":"`+reboundKey+`"}`, "application/json")
			if rebindRes.Code != http.StatusOK {
				close(releaseHolder)
				t.Fatalf("rebind = %d %s", rebindRes.Code, rebindRes.Body.String())
			}

			cpaSrv.mu.Lock()
			cpaSrv.activeKey = rotatedAgainKey
			cpaSrv.authSeen = nil
			cpaSrv.mu.Unlock()

			close(releaseHolder)
			if holder := <-holderDone; holder.Code != http.StatusOK {
				t.Fatalf("holder = %d %s", holder.Code, holder.Body.String())
			}
			if msg := <-waiterDone; msg != "" {
				t.Fatal(msg)
			}

			cpaSrv.mu.Lock()
			seen := append([]string(nil), cpaSrv.authSeen...)
			cpaSrv.mu.Unlock()
			for _, line := range seen {
				if strings.Contains(line, externalAuthorityManagement) {
					t.Fatalf("surface %s presented pre-rebind credential after rebind: %v", surface, seen)
				}
			}
			if !app.externalReadGrant(t.Context()).Refused {
				t.Fatalf("surface %s filed refusal against stale pre-gate grant instead of rebound credential (seen=%v)", surface, seen)
			}
			beforeFollowUp := cpaSrv.requests.Load()
			followUp := gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
			if followUp.Code != http.StatusConflict || errorCode(t, followUp) != freshconfig.ReasonManagementRejected {
				t.Fatalf("follow-up external/usage = %d %s", followUp.Code, followUp.Body.String())
			}
			if sent := cpaSrv.requests.Load() - beforeFollowUp; sent != 0 {
				t.Fatalf("follow-up sent %d CPA requests after rebound credential was refused", sent)
			}
		})
	}
	t.Run("runtime_updates_split_snapshot_during_status", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, rotatedAgainKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		rebinder := app.store.(freshstore.ConnectionRebinder)
		baseSource := app.externalSource
		var rebindOnce sync.Once
		app.externalSource = func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
			install, err := baseSource(ctx)
			rebindOnce.Do(func() {
				protected, protectErr := app.protector.ProtectString(reboundKey)
				if protectErr != nil {
					t.Errorf("protect reboundKey: %v", protectErr)
					return
				}
				rec, loadErr := app.store.Load(context.Background())
				if loadErr != nil {
					t.Errorf("load store: %v", loadErr)
					return
				}
				if _, rebindErr := rebinder.RebindConnection(context.Background(), rec.Operation.Identity, rec.Operation.Revision,
					model.ManagerCPAConnectionConfig{CPABaseURL: rec.Connection.CPABaseURL, ManagementKey: protected}); rebindErr != nil {
					t.Errorf("rebind at source-resolution boundary: %v", rebindErr)
				}
			})
			return install, err
		}

		res := gateRequest(t, app, http.MethodGet, "/usage-service/runtime/updates", gateAdmin)
		if res.Code != http.StatusServiceUnavailable && res.Code != http.StatusConflict {
			t.Fatalf("runtime/updates = %d %s", res.Code, res.Body.String())
		}
		cpaSrv.mu.Lock()
		seen := append([]string(nil), cpaSrv.authSeen...)
		cpaSrv.mu.Unlock()
		if len(seen) != 1 || !strings.Contains(seen[0], "Bearer "+externalAuthorityManagement) {
			t.Fatalf("runtime/updates sent credential different from in-gate grant snapshot: %v", seen)
		}
		if app.externalReadGrant(t.Context()).Refused {
			t.Fatal("refusal of pre-rebind snapshot marked reboundKey refused")
		}
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = reboundKey
		cpaSrv.mu.Unlock()
		followUp := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
		var statusBody freshRuntimeStatusResponse
		if followUp.Code != http.StatusOK || json.Unmarshal(followUp.Body.Bytes(), &statusBody) != nil || statusBody.ErrorCode != "" {
			t.Fatalf("follow-up runtime/status on reboundKey = %d %s", followUp.Code, followUp.Body.String())
		}
	})
}
// TestExternalLegacyGateBindsPostWaitSnapshotAcrossRebind proves that
// guardFreshExternalLegacy re-reads freshstore.Record inside
// legacyCredentialGate and forces the downstream legacy proxy to send the
// exact credential snapshot whose envelope digest is in recorder.grant, across
// both pre-gate wait rebinds and post-load/pre-proxy rebinds.
func TestExternalLegacyGateBindsPostWaitSnapshotAcrossRebind(t *testing.T) {
	t.Run("rebind while queued on legacyCredentialGate", func(t *testing.T) {
		const reboundKey = "legacy-rebound-0000111122223333444455556666777788889999aaaabbbb"
		const rotatedAgain = "legacy-rotated-ffff111122223333444455556666777788889999aaaabbbb"

		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)

		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		var firstOnce sync.Once
		cpaSrv.mu.Lock()
		cpaSrv.onRequest = func(_ *http.Request) bool {
			firstOnce.Do(func() {
				close(firstEntered)
				<-releaseFirst
			})
			return false
		}
		cpaSrv.mu.Unlock()

		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			firstDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		}()
		select {
		case <-firstEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("first legacy read never reached CPA")
		}

		secondParked := make(chan struct{})
		var parkedOnce sync.Once
		app.legacyCredentialGate.onWait = func() {
			parkedOnce.Do(func() { close(secondParked) })
		}
		defer func() { app.legacyCredentialGate.onWait = nil }()

		secondDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			secondDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		}()
		select {
		case <-secondParked:
		case <-time.After(5 * time.Second):
			t.Fatal("second legacy read never parked on legacyCredentialGate")
		}

		// Rebind while second request is parked inside gate.acquire(ctx), then
		// rotate CPA again so reboundKey receives 401 when second proceeds.
		protected, err := app.protector.ProtectString(reboundKey)
		if err != nil {
			t.Fatal(err)
		}
		rebinder := app.store.(freshstore.ConnectionRebinder)
		record, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rebinder.RebindConnection(t.Context(), record.Operation.Identity, record.Operation.Revision,
			model.ManagerCPAConnectionConfig{CPABaseURL: record.Connection.CPABaseURL, ManagementKey: protected}); err != nil {
			t.Fatal(err)
		}
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = rotatedAgain
		cpaSrv.authSeen = nil
		cpaSrv.mu.Unlock()

		close(releaseFirst)
		if first := <-firstDone; first.Code != http.StatusOK {
			t.Fatalf("first legacy read = %d %s", first.Code, first.Body.String())
		}
		second := <-secondDone
		if second.Code != http.StatusConflict || errorCode(t, second) != freshconfig.ReasonManagementRejected {
			t.Fatalf("second legacy read = %d %s", second.Code, second.Body.String())
		}
		if !app.externalReadGrant(t.Context()).Refused {
			t.Fatal("second legacy read filed 401 against pre-gate K1 grant instead of post-gate reboundKey")
		}
		beforeThird := cpaSrv.requests.Load()
		third := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		if third.Code != http.StatusConflict || errorCode(t, third) != freshconfig.ReasonManagementRejected {
			t.Fatalf("third legacy read = %d %s", third.Code, third.Body.String())
		}
		if sent := cpaSrv.requests.Load() - beforeThird; sent != 0 {
			t.Fatalf("third legacy read sent %d CPA requests after reboundKey refusal", sent)
		}
	})

	t.Run("rebind after in-gate source resolution before proxy setup resolution", func(t *testing.T) {
		const reboundKey = "split-rebound-0000111122223333444455556666777788889999aaaabbbbcc"
		const rotatedAgain = "split-rotated-9999888877776666555544443333222211110000ffffeeeedd"

		cpaSrv := newStatefulExternalCPA(t, rotatedAgain)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		rebinder := app.store.(freshstore.ConnectionRebinder)
		baseSource := app.externalSource
		var rebindOnce sync.Once
		app.externalSource = func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
			install, err := baseSource(ctx)
			rebindOnce.Do(func() {
				protected, protectErr := app.protector.ProtectString(reboundKey)
				if protectErr != nil {
					t.Errorf("protect reboundKey: %v", protectErr)
					return
				}
				rec, loadErr := app.store.Load(context.Background())
				if loadErr != nil {
					t.Errorf("load store: %v", loadErr)
					return
				}
				if _, rebindErr := rebinder.RebindConnection(context.Background(), rec.Operation.Identity, rec.Operation.Revision,
					model.ManagerCPAConnectionConfig{CPABaseURL: rec.Connection.CPABaseURL, ManagementKey: protected}); rebindErr != nil {
					t.Errorf("rebind at source-resolution boundary: %v", rebindErr)
				}
			})
			return install, err
		}

		res := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
			t.Fatalf("legacy read = %d %s", res.Code, res.Body.String())
		}
		cpaSrv.mu.Lock()
		seen := append([]string(nil), cpaSrv.authSeen...)
		cpaSrv.mu.Unlock()
		if len(seen) != 1 || !strings.Contains(seen[0], "Bearer "+externalAuthorityManagement) {
			t.Fatalf("proxy sent credential different from in-gate grant snapshot: %v", seen)
		}
		if app.externalReadGrant(t.Context()).Refused {
			t.Fatal("refusal of pre-rebind snapshot marked reboundKey refused")
		}
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = reboundKey
		cpaSrv.mu.Unlock()
		followUp := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		if followUp.Code != http.StatusOK {
			t.Fatalf("follow-up legacy read on reboundKey = %d %s", followUp.Code, followUp.Body.String())
		}
	})
}

// TestExternalBridgedManagementServicesSerializeAndRecordRejection proves that
// the cpamp-bridged Manager services that present the stored External
// management credential internally (model-prices/sync, account-action-candidates
// enable + delete auth-file, and codex-inspection run + actions) serialize
// their outbound CPA calls on legacyCredentialGate, bind grant + credential to
// one post-gate snapshot across rebinds, and record the first 401/403 refusal
// before releasing the gate.
func TestExternalBridgedManagementServicesSerializeAndRecordRejection(t *testing.T) {
	const rotatedKey = "bridged-rotated-1111222233334444555566667777888899990000aaaabbbb"
	const reboundKey = "bridged-rebound-aaaabbbbccccddddeeeeffff000011112222333344445555"

	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-4o":{"id":"gpt-4o","cost":{"input":2.5,"output":10}}}}}`))
	}))
	t.Cleanup(catalogServer.Close)

	newBridgedExternalApp := func(t *testing.T, cpaURL, managementKey string) *freshApplication {
		t.Helper()
		app := newExternalApp(t, cpaURL, managementKey)
		catalogURL := catalogServer.URL
		disabledURL := ""
		app.legacyApp.ModelPriceService = modelpricesvc.NewMultiSourceWithModelsDev(
			app.legacyApp.Store, &catalogURL, &disabledURL, &disabledURL, app.legacyApp.ManagerConfigService,
		)
		app.legacy = guardFreshExternalLegacy(guardFreshLegacyWrites(router.New(app.legacyApp), nil), app)
		app.gatewayConfig = gatewayFor(app)
		t.Cleanup(func() { _ = app.legacyApp.CodexInspectionService.StopAndWait(context.Background()) })
		return app
	}

	leaders := []string{
		"model_prices_sync",
		"account_action_enable",
		"account_action_delete_auth_file",
		"codex_inspection_run_async",
		"codex_inspection_execute_actions",
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, leader := range leaders {
			t.Run(http.StatusText(status)+"/"+leader, func(t *testing.T) {
				cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
				app := newBridgedExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)

				cand, err := app.legacyApp.Store.UpsertAccountActionCandidate(t.Context(), model.AccountActionCandidateUpsert{
					ActionType:        model.AccountActionTypeReview,
					Provider:          "codex",
					AuthFileName:      "codex-1.json",
					AuthIndex:         "idx-1",
					AccountSnapshot:   "acct-1@example.com",
					ReasonCode:        "quota_exhausted",
					Reason:            "test candidate",
					SeenAtMS:          time.Now().UnixMilli(),
				})
				if err != nil {
					t.Fatal(err)
				}
				seededRun, err := app.legacyApp.Store.CreateCodexInspectionRun(t.Context(), model.CodexInspectionRun{
					TriggerType: model.CodexInspectionTriggerManual,
					Status:      model.CodexInspectionStatusCompleted,
					StartedAtMS: time.Now().UnixMilli(),
				})
				if err != nil {
					t.Fatal(err)
				}
				seededResult, err := app.legacyApp.Store.InsertCodexInspectionResult(t.Context(), model.CodexInspectionResult{
					RunID:           seededRun.ID,
					AccountKey:      "codex:codex-2.json:idx-2",
					FileName:        "codex-2.json",
					DisplayAccount:  "acct-2@example.com",
					AccountSnapshot: "acct-2@example.com",
					AuthIndex:       "idx-2",
					Provider:        "codex",
					Action:          "disable",
					ActionReason:    "quota check",
				})
				if err != nil {
					t.Fatal(err)
				}

				firstEntered := make(chan struct{})
				secondEntered := make(chan struct{})
				releaseFirst := make(chan struct{})
				var enterCount atomic.Int32
				cpaSrv.mu.Lock()
				cpaSrv.activeKey = rotatedKey
				cpaSrv.refuseStatus = status
				cpaSrv.onRequest = func(_ *http.Request) bool {
					n := enterCount.Add(1)
					if n == 1 {
						close(firstEntered)
						<-releaseFirst
					} else if n == 2 {
						close(secondEntered)
					}
					return false
				}
				cpaSrv.mu.Unlock()

				const followerCount = 3
				var parkedWaiters atomic.Int32
				allWaitersParked := make(chan struct{})
				var parkedOnce sync.Once
				app.legacyCredentialGate.onWait = func() {
					if parkedWaiters.Add(1) >= followerCount {
						parkedOnce.Do(func() { close(allWaitersParked) })
					}
				}
				defer func() { app.legacyCredentialGate.onWait = nil }()

				before := cpaSrv.requests.Load()
				leaderErr := make(chan string, 1)
				go func() {
					switch leader {
					case "model_prices_sync":
						res := gatewayRequest(t, app, http.MethodPost, "cpamp/v0/management/model-prices/sync", `{}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "model-prices/sync = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "account_action_enable":
						res := gatewayRequest(t, app, http.MethodPost,
							"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/enable", `{}`, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "account-action enable = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "account_action_delete_auth_file":
						res := gatewayRequest(t, app, http.MethodDelete,
							"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/auth-file", "", "")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "account-action delete = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "codex_inspection_run_async":
						res := gatewayRequest(t, app, http.MethodPost, "cpamp/v0/management/codex-inspection/run", `{}`, "application/json")
						if res.Code != http.StatusOK && res.Code != http.StatusAccepted && res.Code != http.StatusConflict {
							leaderErr <- "codex-inspection/run = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					case "codex_inspection_execute_actions":
						actionBody := `{"resultIds":[` + strconv.FormatInt(seededResult.ID, 10) + `],"action":"disable"}`
						res := gatewayRequest(t, app, http.MethodPost,
							"cpamp/v0/management/codex-inspection/runs/"+strconv.FormatInt(seededRun.ID, 10)+"/actions", actionBody, "application/json")
						if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
							leaderErr <- "codex-inspection actions = " + strconv.Itoa(res.Code) + " " + res.Body.String()
							return
						}
					}
					leaderErr <- ""
				}()

				select {
				case <-firstEntered:
				case <-time.After(5 * time.Second):
					t.Fatalf("leader %s never reached CPA", leader)
				}

				followerErrs := make(chan string, followerCount)
				go func() {
					res := gatewayRequest(t, app, http.MethodPost, "cpamp/v0/management/model-prices/sync", `{}`, "application/json")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower model-prices/sync = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodPost,
						"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/enable", `{}`, "application/json")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower account-action enable = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()
				go func() {
					res := gatewayRequest(t, app, http.MethodPost, "cpamp/v0/management/codex-inspection/run", `{}`, "application/json")
					if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
						followerErrs <- "follower codex-inspection/run = " + strconv.Itoa(res.Code) + " " + res.Body.String()
						return
					}
					followerErrs <- ""
				}()

				select {
				case <-allWaitersParked:
				case <-secondEntered:
					close(releaseFirst)
					t.Fatalf("follower reached CPA while %s held legacyCredentialGate", leader)
				case <-time.After(5 * time.Second):
					close(releaseFirst)
					t.Fatalf("timed out waiting for %d followers to park on legacyCredentialGate behind %s", followerCount, leader)
				}
				close(releaseFirst)

				if msg := <-leaderErr; msg != "" {
					t.Fatal(msg)
				}
				for range followerCount {
					if msg := <-followerErrs; msg != "" {
						t.Fatal(msg)
					}
				}
				// Ensure any background inspection task has finished before checking state.
				_ = app.legacyApp.CodexInspectionService.StopAndWait(t.Context())

				if !app.externalReadGrant(t.Context()).Refused {
					t.Fatalf("leader %s did not record durable credential refusal", leader)
				}
				if got := cpaSrv.requests.Load() - before; got != 1 {
					t.Fatalf("External CPA received %d management requests across %s + concurrent bridged burst, want 1", got, leader)
				}
			})
		}
	}

	t.Run("rebind while queued binds post-gate snapshot and preserves authFileMutations lock order", func(t *testing.T) {
		const rotatedAgain = "bridged-rotated-again-9999888877776666555544443333222211110000"
		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app := newBridgedExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)

		cand, err := app.legacyApp.Store.UpsertAccountActionCandidate(t.Context(), model.AccountActionCandidateUpsert{
			ActionType:      model.AccountActionTypeReview,
			Provider:        "codex",
			AuthFileName:    "codex-1.json",
			AuthIndex:       "idx-1",
			AccountSnapshot: "acct-1@example.com",
			ReasonCode:      "quota_exhausted",
			Reason:          "test candidate",
			SeenAtMS:        time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}

		holderEntered := make(chan struct{})
		releaseHolder := make(chan struct{})
		var holderOnce sync.Once
		cpaSrv.mu.Lock()
		cpaSrv.onRequest = func(r *http.Request) bool {
			if r.URL.Path == "/v0/management/api-keys" {
				holderOnce.Do(func() {
					close(holderEntered)
					<-releaseHolder
				})
			}
			return false
		}
		cpaSrv.mu.Unlock()

		holderDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			holderDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		}()
		select {
		case <-holderEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("holder never reached CPA")
		}

		waiterParked := make(chan struct{})
		var waiterOnce sync.Once
		app.legacyCredentialGate.onWait = func() {
			waiterOnce.Do(func() { close(waiterParked) })
		}
		defer func() { app.legacyCredentialGate.onWait = nil }()

		enableDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			enableDone <- gatewayRequest(t, app, http.MethodPost,
				"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/enable", `{}`, "application/json")
		}()
		select {
		case <-waiterParked:
		case res := <-enableDone:
			close(releaseHolder)
			<-holderDone
			t.Fatalf("account-action enable did not wait on legacyCredentialGate: %d %s", res.Code, res.Body.String())
		case <-time.After(5 * time.Second):
			close(releaseHolder)
			t.Fatal("account-action enable never parked on legacyCredentialGate")
		}

		// Rebind to reboundKey while waiter is parked, then rotate CPA again so
		// reboundKey receives 401 when waiter proceeds.
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = reboundKey
		cpaSrv.mu.Unlock()
		rebindRes := gatewayRequest(t, app, http.MethodPost, "external/rebind",
			`{"requestId":"`+strings.Repeat("e", 32)+`","managementKey":"`+reboundKey+`"}`, "application/json")
		if rebindRes.Code != http.StatusOK {
			close(releaseHolder)
			t.Fatalf("rebind = %d %s", rebindRes.Code, rebindRes.Body.String())
		}
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = rotatedAgain
		cpaSrv.authSeen = nil
		cpaSrv.mu.Unlock()

		close(releaseHolder)
		if holder := <-holderDone; holder.Code != http.StatusOK {
			t.Fatalf("holder = %d %s", holder.Code, holder.Body.String())
		}
		res := <-enableDone
		if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
			t.Fatalf("account-action enable = %d %s", res.Code, res.Body.String())
		}
		cpaSrv.mu.Lock()
		seen := append([]string(nil), cpaSrv.authSeen...)
		cpaSrv.mu.Unlock()
		for _, line := range seen {
			if strings.Contains(line, externalAuthorityManagement) {
				t.Fatalf("account-action enable presented pre-rebind credential: %v", seen)
			}
		}
		if !app.externalReadGrant(t.Context()).Refused {
			t.Fatalf("account-action enable did not record refusal against reboundKey (seen=%v)", seen)
		}
	})
}

// TestExternalPrecheckPropagatesContextCancellationWhileWaitingOnGate proves
// that when a caller's request context is cancelled while waiting on
// legacyCredentialGate in the precheck, the route fails closed with
// external_credential_wait_cancelled (499) rather than falling through to
// local journal lookups or detaching collector startup onto runCtx.
func TestExternalPrecheckPropagatesContextCancellationWhileWaitingOnGate(t *testing.T) {
	cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
	app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
	app.gatewayConfig = gatewayFor(app)
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	app.runCtx = hostCtx

	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	var holderOnce sync.Once
	cpaSrv.mu.Lock()
	cpaSrv.onRequest = func(_ *http.Request) bool {
		holderOnce.Do(func() {
			close(holderEntered)
			<-releaseHolder
		})
		return false
	}
	cpaSrv.mu.Unlock()

	holderDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		holderDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
	}()
	select {
	case <-holderEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("holder never reached CPA")
	}

	sendCancelledWhileWaiting := func(method, resource, body string) *httptest.ResponseRecorder {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var waits atomic.Int32
		app.legacyCredentialGate.onWait = func() {
			if waits.Add(1) == 1 {
				cancel()
			} else {
				stopHost()
			}
		}
		defer func() { app.legacyCredentialGate.onWait = nil }()

		var reader *bytes.Reader
		if body == "" {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader([]byte(body))
		}
		req := httptest.NewRequest(method, "/control/api/v2/"+resource, reader).WithContext(ctx)
		req.Header.Set("Authorization", gateAdmin)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		return rec
	}

	beforeCPA := cpaSrv.requests.Load()
	reconcileRes := sendCancelledWhileWaiting(http.MethodPost,
		"gateway/config/operations/"+strings.Repeat("a", 32)+"/reconcile", `{}`)
	if reconcileRes.Code != statusClientClosedRequest || errorCode(t, reconcileRes) != externalCredentialWaitCancelled {
		close(releaseHolder)
		<-holderDone
		t.Fatalf("cancelled reconcile = %d %s, want %d %s", reconcileRes.Code, reconcileRes.Body.String(), statusClientClosedRequest, externalCredentialWaitCancelled)
	}

	keyOpRes := sendCancelledWhileWaiting(http.MethodGet,
		"api-keys/operations/"+strings.Repeat("9", 32), "")
	if keyOpRes.Code != statusClientClosedRequest || errorCode(t, keyOpRes) != externalCredentialWaitCancelled {
		close(releaseHolder)
		<-holderDone
		t.Fatalf("cancelled key operation check = %d %s, want %d %s", keyOpRes.Code, keyOpRes.Body.String(), statusClientClosedRequest, externalCredentialWaitCancelled)
	}

	usageConfirmRes := sendCancelledWhileWaiting(http.MethodPost,
		"external/usage", `{"requestId":"`+strings.Repeat("b", 32)+`","confirmEnableStatistics":true}`)
	if usageConfirmRes.Code != statusClientClosedRequest || errorCode(t, usageConfirmRes) != externalCredentialWaitCancelled {
		close(releaseHolder)
		<-holderDone
		t.Fatalf("cancelled external/usage confirm = %d %s, want %d %s", usageConfirmRes.Code, usageConfirmRes.Body.String(), statusClientClosedRequest, externalCredentialWaitCancelled)
	}
	if app.externalUsageConfirmed.Load() || app.usageCollectionStarted() {
		close(releaseHolder)
		<-holderDone
		t.Fatal("cancelled external/usage confirm mutated host collector state")
	}
	if got := cpaSrv.requests.Load() - beforeCPA; got != 0 {
		close(releaseHolder)
		<-holderDone
		t.Fatalf("cancelled prechecks sent %d CPA requests, want 0", got)
	}

	close(releaseHolder)
	if holder := <-holderDone; holder.Code != http.StatusOK {
		t.Fatalf("holder = %d %s", holder.Code, holder.Body.String())
	}
}

// TestExternalFreshNativeHeaderReceiptReleasesGateBeforeSlowBody proves that
// once an upstream 200 response header arrives on a Fresh-native gated call,
// legacyCredentialGate is released immediately before the response body
// finishes streaming so one slow body cannot block other External surfaces.
func TestExternalFreshNativeHeaderReceiptReleasesGateBeforeSlowBody(t *testing.T) {
	headersFlushed := make(chan struct{})
	releaseBody := make(chan struct{})
	var slowOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.13")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v8/management/config" {
			slowOnce.Do(func() {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"routing":`))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				close(headersFlushed)
				<-releaseBody
				_, _ = w.Write([]byte(`{"strategy":"round-robin"}}`))
			})
			return
		}
		_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
	}))
	t.Cleanup(server.Close)

	app := newExternalApp(t, server.URL, externalAuthorityManagement)
	app.gatewayConfig = gatewayFor(app)

	slowDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		slowDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
	}()
	select {
	case <-headersFlushed:
	case <-time.After(5 * time.Second):
		t.Fatal("slow gateway/config never flushed upstream headers")
	}

	siblingDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		siblingDone <- gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
	}()

	select {
	case sibling := <-siblingDone:
		var body freshRuntimeStatusResponse
		if sibling.Code != http.StatusOK || json.Unmarshal(sibling.Body.Bytes(), &body) != nil || body.ErrorCode != "" {
			close(releaseBody)
			t.Fatalf("sibling runtime/status = %d %s", sibling.Code, sibling.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseBody)
		t.Fatal("sibling runtime/status remained blocked on legacyCredentialGate while slow body streamed after 200 headers")
	}

	close(releaseBody)
	slow := <-slowDone
	if slow.Code != http.StatusOK {
		t.Fatalf("slow gateway/config = %d %s", slow.Code, slow.Body.String())
	}
}

func writeTruncatedHTTPStatus(w http.ResponseWriter, status int, extraHeaderKey, extraHeaderVal string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(status)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		w.WriteHeader(status)
		return
	}
	extra := ""
	if extraHeaderKey != "" {
		extra = fmt.Sprintf("%s: %s\r\n", extraHeaderKey, extraHeaderVal)
	}
	_, _ = fmt.Fprintf(
		rw,
		"HTTP/1.1 %d %s\r\nContent-Type: application/json\r\n%sContent-Length: 512\r\nConnection: close\r\n\r\n{\"err",
		status,
		http.StatusText(status),
		extra,
	)
	_ = rw.Flush()
	_ = conn.Close()
}

// TestExternalTruncatedAuthRefusalPersistsAndReturnsTypedBlocker proves that
// 401/403 responses with truncated or unexpectedly encoded bodies still persist
// durable External credential refusal and return external_management_key_rejected
// (409) to the first caller across Collector startup (stats GET, enable PUT,
// readback) and Fresh-native config/key endpoints.
func TestExternalTruncatedAuthRefusalPersistsAndReturnsTypedBlocker(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run("collector_startup_stats_get_truncated_"+strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/v0/management/config":
					w.Header().Set("X-CPA-Version", "v8.0.55")
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
				case "/v0/management/usage-statistics-enabled":
					writeTruncatedHTTPStatus(w, status, "", "")
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			app := newExternalApp(t, server.URL, externalAuthorityManagement)

			outcome := app.startExternalUsageCollector(t.Context())
			if outcome.Started || outcome.Reason != freshconfig.ReasonManagementRejected {
				t.Fatalf("startExternalUsageCollector outcome = %+v, want Reason=%s", outcome, freshconfig.ReasonManagementRejected)
			}
			if !app.externalReadGrant(t.Context()).Refused {
				t.Fatalf("truncated %d on usage-statistics GET did not persist durable refusal", status)
			}
			beforeFollowUp := requests.Load()
			followUp := gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
			if followUp.Code != http.StatusConflict || errorCode(t, followUp) != freshconfig.ReasonManagementRejected {
				t.Fatalf("follow-up external/usage = %d %s", followUp.Code, followUp.Body.String())
			}
			if legacy := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); legacy.Code != http.StatusConflict {
				t.Fatalf("follow-up legacy api-keys = %d %s", legacy.Code, legacy.Body.String())
			}
			if sent := requests.Load() - beforeFollowUp; sent != 0 {
				t.Fatalf("sent %d saved-key requests after truncated %d refusal, want 0", sent, status)
			}
		})

		t.Run("collector_confirm_put_and_readback_truncated_"+strconv.Itoa(status), func(t *testing.T) {
			for _, failStep := range []string{"put", "readback"} {
				t.Run(failStep, func(t *testing.T) {
					var requests atomic.Int32
					var statsGets atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						switch {
						case r.URL.Path == "/v0/management/config":
							w.Header().Set("X-CPA-Version", "v8.0.55")
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
						case r.URL.Path == "/v0/management/usage-statistics-enabled" && r.Method == http.MethodGet:
							if statsGets.Add(1) == 1 {
								w.Header().Set("Content-Type", "application/json")
								_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
								return
							}
							writeTruncatedHTTPStatus(w, status, "", "")
						case r.URL.Path == "/v0/management/usage-statistics-enabled" && r.Method == http.MethodPut:
							if failStep == "put" {
								writeTruncatedHTTPStatus(w, status, "", "")
								return
							}
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"status":"ok"}`))
						default:
							http.NotFound(w, r)
						}
					}))
					t.Cleanup(server.Close)

					app := newExternalApp(t, server.URL, externalAuthorityManagement)
					hostCtx, stopHost := context.WithCancel(t.Context())
					defer stopHost()
					app.runCtx = hostCtx

					confirm := gatewayRequest(t, app, http.MethodPost, "external/usage",
						`{"requestId":"`+strings.Repeat("c", 32)+`","confirmEnableStatistics":true}`, "application/json")
					if confirm.Code != http.StatusConflict || errorCode(t, confirm) != freshconfig.ReasonManagementRejected {
						t.Fatalf("confirm external/usage (%s truncated %d) = %d %s, want 409 %s",
							failStep, status, confirm.Code, confirm.Body.String(), freshconfig.ReasonManagementRejected)
					}
					if !app.externalReadGrant(t.Context()).Refused {
						t.Fatalf("truncated %d on %s did not persist durable refusal", status, failStep)
					}
					beforeFollowUp := requests.Load()
					if followUp := gatewayRequest(t, app, http.MethodGet, "external/usage", "", ""); followUp.Code != http.StatusConflict {
						t.Fatalf("follow-up external/usage = %d %s", followUp.Code, followUp.Body.String())
					}
					if sent := requests.Load() - beforeFollowUp; sent != 0 {
						t.Fatalf("sent %d saved-key requests after %s truncated %d refusal, want 0", sent, failStep, status)
					}
				})
			}
		})

		t.Run("native_config_and_keys_truncated_and_encoded_"+strconv.Itoa(status), func(t *testing.T) {
			cases := []struct {
				name      string
				extraHdrK string
				extraHdrV string
				oversized bool
				call      func(t *testing.T, app *freshApplication, keyID string) *httptest.ResponseRecorder
			}{
				{
					name: "gateway_config_get_truncated",
					call: func(t *testing.T, app *freshApplication, _ string) *httptest.ResponseRecorder {
						return gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
					},
				},
				{
					name:      "gateway_config_get_unexpected_encoding",
					extraHdrK: "Content-Encoding",
					extraHdrV: "gzip",
					call: func(t *testing.T, app *freshApplication, _ string) *httptest.ResponseRecorder {
						return gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
					},
				},
				{
					name:      "gateway_config_get_oversized",
					oversized: true,
					call: func(t *testing.T, app *freshApplication, _ string) *httptest.ResponseRecorder {
						return gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
					},
				},
				{
					name: "api_keys_list_truncated",
					call: func(t *testing.T, app *freshApplication, _ string) *httptest.ResponseRecorder {
						return gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
					},
				},
				{
					name: "api_keys_apply_truncated",
					call: func(t *testing.T, app *freshApplication, keyID string) *httptest.ResponseRecorder {
						return gatewayRequest(t, app, http.MethodPost, "api-keys/"+keyID+"/initial-install", `{}`, "application/json")
					},
				},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.URL.Path == "/v0/management/config" {
							w.Header().Set("X-CPA-Version", "v8.0.55")
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{}`))
							return
						}
						if tc.oversized {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(status)
							_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
							return
						}
						writeTruncatedHTTPStatus(w, status, tc.extraHdrK, tc.extraHdrV)
					}))
					t.Cleanup(server.Close)

					app := newExternalApp(t, server.URL, externalAuthorityManagement)
					app.gatewayConfig = gatewayFor(app)
					stagedKey := stageExternalKeyForGateTest(t, app, "3", false)

					first := tc.call(t, app, stagedKey)
					if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
						t.Fatalf("%s first response = %d %s, want 409 %s",
							tc.name, first.Code, first.Body.String(), freshconfig.ReasonManagementRejected)
					}
					if !app.externalReadGrant(t.Context()).Refused {
						t.Fatalf("%s did not persist durable refusal", tc.name)
					}
					beforeFollowUp := requests.Load()
					followUp := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
					if followUp.Code != http.StatusConflict || errorCode(t, followUp) != freshconfig.ReasonManagementRejected {
						t.Fatalf("%s follow-up = %d %s", tc.name, followUp.Code, followUp.Body.String())
					}
					if sent := requests.Load() - beforeFollowUp; sent != 0 {
						t.Fatalf("%s sent %d saved-key requests after refusal, want 0", tc.name, sent)
					}
				})
			}
		})
	}
}

// TestExternalHeaderClaimBlocksBudgetExpiryFromReleasingGateBeforeRefusalPersists
// pauses refusal persistence immediately after upstream 401/403 response headers
// are observed, lets the credential hold's tenure budget expire deterministically,
// and proves a queued saved-key request cannot acquire legacyCredentialGate or
// reach CPA before the refusal is durably visible.
func TestExternalHeaderClaimBlocksBudgetExpiryFromReleasingGateBeforeRefusalPersists(t *testing.T) {
	surfaces := []struct {
		name string
		run  func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder
		ok   func(t *testing.T, res *httptest.ResponseRecorder)
	}{
		{
			name: "legacy_proxy",
			run: func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
				return gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
			},
			ok: func(t *testing.T, res *httptest.ResponseRecorder) {
				if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
					t.Fatalf("legacy_proxy = %d %s", res.Code, res.Body.String())
				}
			},
		},
		{
			name: "fresh_gateway_config",
			run: func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
				return gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
			},
			ok: func(t *testing.T, res *httptest.ResponseRecorder) {
				if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
					t.Fatalf("fresh_gateway_config = %d %s", res.Code, res.Body.String())
				}
			},
		},
		{
			name: "fresh_runtime_status",
			run: func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
				return gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
			},
			ok: func(t *testing.T, res *httptest.ResponseRecorder) {
				var body freshRuntimeStatusResponse
				if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.ErrorCode != freshconfig.ReasonManagementRejected {
					t.Fatalf("fresh_runtime_status = %d %s", res.Code, res.Body.String())
				}
			},
		},
		{
			name: "fresh_console_models",
			run: func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
				return gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", "")
			},
			ok: func(t *testing.T, res *httptest.ResponseRecorder) {
				if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
					t.Fatalf("fresh_console_models = %d %s", res.Code, res.Body.String())
				}
			},
		},
		{
			name: "fresh_external_usage",
			run: func(t *testing.T, app *freshApplication) *httptest.ResponseRecorder {
				return gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
			},
			ok: func(t *testing.T, res *httptest.ResponseRecorder) {
				if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
					t.Fatalf("fresh_external_usage = %d %s", res.Code, res.Body.String())
				}
			},
		},
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, surface := range surfaces {
			t.Run(http.StatusText(status)+"/"+surface.name, func(t *testing.T) {
				var upstreamHits atomic.Int32
				secondReachedCPA := make(chan struct{})
				var secondOnce sync.Once

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if upstreamHits.Add(1) >= 2 {
						secondOnce.Do(func() { close(secondReachedCPA) })
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"refused"}`))
				}))
				t.Cleanup(server.Close)

				app := newExternalApp(t, server.URL, externalAuthorityManagement)
				app.gatewayConfig = gatewayFor(app)
				app.credentialHeaderBudget = 80 * time.Millisecond

				atBeforeRecord := make(chan struct{})
				allowRecord := make(chan struct{})
				var recordOnce sync.Once
				app.rejection.beforeRecord = func() {
					recordOnce.Do(func() {
						close(atBeforeRecord)
						<-allowRecord
					})
				}

				budgetExpired := make(chan struct{})
				var expiryOnce sync.Once
				app.afterCredentialHoldBudgetExpiry = func() {
					expiryOnce.Do(func() { close(budgetExpired) })
				}

				firstDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					firstDone <- surface.run(t, app)
				}()

				select {
				case <-atBeforeRecord:
				case <-time.After(5 * time.Second):
					t.Fatal("leader never reached refusal recording")
				}

				select {
				case <-budgetExpired:
				case <-time.After(5 * time.Second):
					close(allowRecord)
					t.Fatal("credential hold budget timer did not fire while recording was paused")
				}

				// After the budget timer has fired while refusal persistence is paused,
				// a subsequent saved-key request must still park on legacyCredentialGate
				// rather than acquiring the gate and hitting CPA with the unrecorded key.
				secondParked := make(chan struct{})
				var parkedOnce sync.Once
				app.legacyCredentialGate.onWait = func() {
					parkedOnce.Do(func() { close(secondParked) })
				}
				defer func() { app.legacyCredentialGate.onWait = nil }()

				secondDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					secondDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
				}()

				select {
				case <-secondParked:
				case <-secondReachedCPA:
					close(allowRecord)
					t.Fatal("tenure budget expiry released legacyCredentialGate before refusal recording finished, allowing queued request to hit CPA")
				case <-time.After(5 * time.Second):
					close(allowRecord)
					t.Fatal("subsequent request neither parked on legacyCredentialGate nor reached CPA")
				}
				close(allowRecord)

				first := <-firstDone
				surface.ok(t, first)

				second := <-secondDone
				if second.Code != http.StatusConflict || errorCode(t, second) != freshconfig.ReasonManagementRejected {
					t.Fatalf("queued request after refusal = %d %s, want 409 %s",
						second.Code, second.Body.String(), freshconfig.ReasonManagementRejected)
				}
				if !app.externalReadGrant(t.Context()).Refused {
					t.Fatal("durable refusal was not persisted")
				}
				if got := upstreamHits.Load(); got != 1 {
					t.Fatalf("External CPA received %d saved-key requests across budget-expiry race, want 1", got)
				}
			})
		}
	}
}

// TestExternalRefusalSQLiteWriteFailureFailsClosedAcrossFamiliesAndRecoversOnRebind
// verifies that when SQLite refuses to persist the fresh_external_rejection_v1
// row, the first 401/403 still installs a process-local fail-closed blocker for
// that (identity, credential) grant so subsequent calls across gateway config,
// console models, keys, and codex inspection make zero additional CPA requests,
// and that rebind clears the blocker and restores service.
func TestExternalRefusalSQLiteWriteFailureFailsClosedAcrossFamiliesAndRecoversOnRebind(t *testing.T) {
	const rotatedKey = "sqlite-fault-rotated-0000111122223333444455556666777788889999aa"
	const rotatedSecondKey = "sqlite-fault-second-aaaabbbbccccddddeeeeffff000011112222333344"

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cpaSrv := newStatefulExternalCPA(t, rotatedKey)
			cpaSrv.refuseStatus = status
			app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
			app.gatewayConfig = gatewayFor(app)
			stagedKey := stageExternalKeyForGateTest(t, app, "5", false)

			// Configure SQLite triggers so any INSERT or UPDATE of fresh_external_rejection_v1
			// fails inside SQLite while all other tables and DELETE recovery work normally.
			for _, ddl := range []string{
				`CREATE TRIGGER fail_external_refusal_insert BEFORE INSERT ON settings
				 WHEN NEW.key = 'fresh_external_rejection_v1'
				 BEGIN SELECT RAISE(ABORT, 'simulated sqlite write failure'); END;`,
				`CREATE TRIGGER fail_external_refusal_update BEFORE UPDATE ON settings
				 WHEN NEW.key = 'fresh_external_rejection_v1'
				 BEGIN SELECT RAISE(ABORT, 'simulated sqlite write failure'); END;`,
			} {
				if _, err := app.rejection.db.ExecContext(t.Context(), ddl); err != nil {
					t.Fatalf("install sqlite trigger: %v", err)
				}
			}

			// First saved-key request observes 401/403; SQLite write fails.
			first := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
			var statusBody freshRuntimeStatusResponse
			if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &statusBody) != nil || statusBody.ErrorCode != freshconfig.ReasonManagementRejected {
				t.Fatalf("first runtime/status = %d %s", first.Code, first.Body.String())
			}

			var durableCount int
			if err := app.rejection.db.QueryRowContext(
				t.Context(),
				`SELECT count(*) FROM settings WHERE key = ?`,
				externalRejectionKey,
			).Scan(&durableCount); err != nil {
				t.Fatalf("query settings count: %v", err)
			}
			if durableCount != 0 {
				t.Fatalf("expected 0 durable refusal rows under write fault, got %d", durableCount)
			}
			if got := cpaSrv.requests.Load(); got != 1 {
				t.Fatalf("initial refusal made %d CPA requests, want 1", got)
			}

			// Subsequent calls across gateway config, console models, keys, and
			// codex inspection must fail closed locally without hitting CPA.
			if cfgRes := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); cfgRes.Code != http.StatusConflict || errorCode(t, cfgRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("gateway/config after SQLite write failure = %d %s", cfgRes.Code, cfgRes.Body.String())
			}
			if modelsRes := gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", ""); modelsRes.Code != http.StatusConflict || errorCode(t, modelsRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("cpamp/v1/models after SQLite write failure = %d %s", modelsRes.Code, modelsRes.Body.String())
			}
			if keysRes := gatewayRequest(t, app, http.MethodGet, "api-keys", "", ""); keysRes.Code != http.StatusConflict || errorCode(t, keysRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("api-keys list after SQLite write failure = %d %s", keysRes.Code, keysRes.Body.String())
			}
			if applyRes := gatewayRequest(t, app, http.MethodPost, "api-keys/"+stagedKey+"/initial-install", `{}`, "application/json"); applyRes.Code != http.StatusConflict || errorCode(t, applyRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("api-keys initial-install after SQLite write failure = %d %s", applyRes.Code, applyRes.Body.String())
			}
			inspectReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/cpamp/v0/management/codex-inspection/run", strings.NewReader(`{"async":false}`))
			inspectReq.Header.Set("Content-Type", "application/json")
			inspectReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			inspectRes := httptest.NewRecorder()
			app.canonicalAPI(inspectRes, inspectReq)
			if inspectRes.Code != http.StatusConflict || errorCode(t, inspectRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("codex-inspection/run after SQLite write failure = %d %s", inspectRes.Code, inspectRes.Body.String())
			}

			if got := cpaSrv.requests.Load(); got != 1 {
				t.Fatalf("CPA received %d rejected-auth requests across all families, want 1", got)
			}

			// Rebind via canonical HTTP endpoint must clear the process-local blocker and recover.
			rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
				cpaSrv.server.URL, rotatedKey, strings.Repeat("d", 32))
			rebindRes := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
			if rebindRes.Code != http.StatusOK {
				t.Fatalf("external/rebind = %d %s", rebindRes.Code, rebindRes.Body.String())
			}

			if cfgAfter := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); cfgAfter.Code != http.StatusOK {
				t.Fatalf("gateway/config after rebind = %d %s", cfgAfter.Code, cfgAfter.Body.String())
			}
			if modelsAfter := gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", ""); modelsAfter.Code != http.StatusOK {
				t.Fatalf("cpamp/v1/models after rebind = %d %s", modelsAfter.Code, modelsAfter.Body.String())
			}
			if keysAfter := gatewayRequest(t, app, http.MethodGet, "api-keys", "", ""); keysAfter.Code != http.StatusOK {
				t.Fatalf("api-keys after rebind = %d %s", keysAfter.Code, keysAfter.Body.String())
			}
			inspectAfterReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/cpamp/v0/management/codex-inspection/run", strings.NewReader(`{"async":false}`))
			inspectAfterReq.Header.Set("Content-Type", "application/json")
			inspectAfterReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			inspectAfterRes := httptest.NewRecorder()
			app.canonicalAPI(inspectAfterRes, inspectAfterReq)
			if inspectAfterRes.Code != http.StatusOK {
				t.Fatalf("codex-inspection/run after rebind = %d %s", inspectAfterRes.Code, inspectAfterRes.Body.String())
			}

			// Once SQLite recovers (triggers dropped), a new refusal persists durably as usual.
			for _, ddl := range []string{
				`DROP TRIGGER fail_external_refusal_insert;`,
				`DROP TRIGGER fail_external_refusal_update;`,
			} {
				if _, err := app.rejection.db.ExecContext(t.Context(), ddl); err != nil {
					t.Fatalf("drop sqlite trigger: %v", err)
				}
			}
			cpaSrv.mu.Lock()
			cpaSrv.activeKey = rotatedSecondKey
			cpaSrv.mu.Unlock()
			if afterDrop := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); afterDrop.Code != http.StatusConflict {
				t.Fatalf("gateway/config after second rotation = %d %s", afterDrop.Code, afterDrop.Body.String())
			}
			if err := app.rejection.db.QueryRowContext(
				t.Context(),
				`SELECT count(*) FROM settings WHERE key = ?`,
				externalRejectionKey,
			).Scan(&durableCount); err != nil || durableCount != 1 {
				t.Fatalf("expected durable refusal row after SQLite recovery, count=%d err=%v", durableCount, err)
			}
		})
	}
}

// TestExternalWedgedRefusalPersistenceFailsClosedWithinBoundAndFreesGate proves
// that when refusal persistence hangs indefinitely (either inside SQLite I/O or
// waiting on an already-wedged persistence lock) after response headers claimed
// the credential hold, a bounded persistence deadline installs the process-local
// fail-closed blocker and frees legacyCredentialGate so waiters receive local
// 409 external_management_key_rejected within bounded time and CPA sees only 1
// rejected-auth request.
func TestExternalWedgedRefusalPersistenceFailsClosedWithinBoundAndFreesGate(t *testing.T) {
	t.Run("controlled_clock_channel", func(t *testing.T) {
		for _, surface := range []string{"legacy_proxy", "fresh_gateway_config"} {
			t.Run(surface, func(t *testing.T) {
				var upstreamHits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					upstreamHits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"refused"}`))
				}))
				t.Cleanup(server.Close)

				app := newExternalApp(t, server.URL, externalAuthorityManagement)
				app.gatewayConfig = gatewayFor(app)
				app.credentialHeaderBudget = 30 * time.Millisecond

				releaseOnCleanup := make(chan struct{})
				t.Cleanup(func() { close(releaseOnCleanup) })

				atBeforeRecord := make(chan struct{})
				var recordOnce sync.Once
				app.rejection.beforeRecord = func() {
					recordOnce.Do(func() { close(atBeforeRecord) })
					<-releaseOnCleanup
				}

				persistClockCh := make(chan time.Time, 1)
				app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
					return persistClockCh, func() {}
				}

				budgetExpired := make(chan struct{})
				var expiryOnce sync.Once
				app.afterCredentialHoldBudgetExpiry = func() {
					expiryOnce.Do(func() { close(budgetExpired) })
				}

				leaderCtx, cancelLeader := context.WithCancel(t.Context())
				defer cancelLeader()
				leaderDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					res := httptest.NewRecorder()
					switch surface {
					case "legacy_proxy":
						req := gateHTTPRequest(http.MethodGet, "/v0/management/api-keys", gateAdmin).WithContext(leaderCtx)
						app.legacy.ServeHTTP(res, req)
					case "fresh_gateway_config":
						req := httptest.NewRequest(http.MethodGet, "/control/api/v2/gateway/config", nil).WithContext(leaderCtx)
						req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
						app.canonicalAPI(res, req)
					}
					leaderDone <- res
				}()

				select {
				case <-atBeforeRecord:
				case <-time.After(5 * time.Second):
					t.Fatal("leader never entered refusal persistence")
				}

				waiterParked := make(chan struct{})
				var parkedOnce sync.Once
				app.legacyCredentialGate.onWait = func() {
					parkedOnce.Do(func() { close(waiterParked) })
				}
				defer func() { app.legacyCredentialGate.onWait = nil }()

				waiterDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					waiterDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
				}()

				select {
				case <-waiterParked:
				case <-time.After(5 * time.Second):
					t.Fatal("waiter never queued on legacyCredentialGate")
				}

				// Cancel caller context and let tenure header budget expire while
				// persistence remains blocked forever; gate must stay held until
				// the persistence deadline triggers local fail-closed installation.
				cancelLeader()
				select {
				case <-budgetExpired:
				case <-time.After(5 * time.Second):
					t.Fatal("tenure header budget did not expire")
				}

				// Expire the controlled persistence clock: local fail-closed blocker
				// installs and releases legacyCredentialGate without waiting for SQLite.
				persistClockCh <- time.Now()

				select {
				case waiterRes := <-waiterDone:
					if waiterRes.Code != http.StatusConflict || errorCode(t, waiterRes) != freshconfig.ReasonManagementRejected {
						t.Fatalf("waiter response = %d %s, want 409 %s",
							waiterRes.Code, waiterRes.Body.String(), freshconfig.ReasonManagementRejected)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("waiter remained wedged on legacyCredentialGate after persistence deadline expired")
				}

				select {
				case <-leaderDone:
				case <-time.After(2 * time.Second):
					t.Fatal("leader handler remained wedged after persistence deadline expired")
				}

				if got := upstreamHits.Load(); got != 1 {
					t.Fatalf("CPA received %d requests under wedged persistence, want 1", got)
				}
			})
		}
	})

	t.Run("small_persist_budget_and_mutex_wait_contention", func(t *testing.T) {
		const secondKey = "wedged-second-key-0000111122223333444455556666777788889999aaaabb"
		const thirdKey = "wedged-third-key-aaaabbbbccccddddeeeeffff0000111122223333444455"

		cpaSrv := newStatefulExternalCPA(t, secondKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)
		app.rejection.persistBudget = 25 * time.Millisecond

		releaseOnCleanup := make(chan struct{})
		t.Cleanup(func() { close(releaseOnCleanup) })

		atBeforeRecord := make(chan struct{})
		var recordOnce sync.Once
		app.rejection.beforeRecord = func() {
			recordOnce.Do(func() { close(atBeforeRecord) })
			<-releaseOnCleanup
		}

		// Wave 1: first refusal wedges inside persistence forever; small persistBudget
		// installs local blocker and unblocks waiter within bounded time.
		wave1Leader := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			wave1Leader <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		}()
		select {
		case <-atBeforeRecord:
		case <-time.After(5 * time.Second):
			t.Fatal("wave 1 leader never entered persistence")
		}

		wave1Waiter := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			wave1Waiter <- gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
		}()
		select {
		case res := <-wave1Waiter:
			if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
				t.Fatalf("wave 1 waiter = %d %s, want 409 %s", res.Code, res.Body.String(), freshconfig.ReasonManagementRejected)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("wave 1 waiter hung when persistence wedged")
		}
		<-wave1Leader
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("wave 1 CPA requests = %d, want 1", got)
		}

		// Wave 2: while the persistence slot is STILL held forever by Wave 1,
		// rebind to secondKey commits the replacement credential and reports
		// retryable 503 external_rebind_unavailable because durable DELETE cannot
		// acquire the wedged slot, while in-process reads on secondKey still work
		// via pendingClear and a subsequent rotation to thirdKey hits semaphore-wait
		// contention on record() yet still bounds the wait, installs local
		// fail-closed state for secondKey, and frees the gate.
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, secondKey, strings.Repeat("e", 32))
		if rebindRes := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json"); rebindRes.Code != http.StatusServiceUnavailable || errorCode(t, rebindRes) != "external_rebind_unavailable" {
			t.Fatalf("rebind while persistence slot wedged = %d %s, want 503 external_rebind_unavailable", rebindRes.Code, rebindRes.Body.String())
		}
		if okRes := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); okRes.Code != http.StatusOK {
			t.Fatalf("gateway/config on rebound secondKey = %d %s", okRes.Code, okRes.Body.String())
		}

		cpaSrv.mu.Lock()
		cpaSrv.activeKey = thirdKey
		cpaSrv.mu.Unlock()
		beforeWave2Refusal := cpaSrv.requests.Load()

		wave2Leader := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			wave2Leader <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		}()
		select {
		case res := <-wave2Leader:
			if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
				t.Fatalf("wave 2 leader under persistence-lock contention = %d %s", res.Code, res.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("wave 2 leader hung waiting on wedged persistence lock")
		}
		if followUp := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); followUp.Code != http.StatusConflict || errorCode(t, followUp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("wave 2 follow-up = %d %s", followUp.Code, followUp.Body.String())
		}
		if sent := cpaSrv.requests.Load() - beforeWave2Refusal; sent != 1 {
			t.Fatalf("wave 2 sent %d refused secondKey requests to CPA, want 1", sent)
		}
	})
}

type rejectionSQLController struct {
	inFlight      atomic.Int32
	maxConcurrent atomic.Int32
	selectCalls   atomic.Int32

	mu          sync.Mutex
	onSelect    func() error
	onDelete    func() error
	afterInsert func(error)
	afterDelete func(error)
}

func (c *rejectionSQLController) trackEnter() func() {
	cur := c.inFlight.Add(1)
	for {
		prev := c.maxConcurrent.Load()
		if cur <= prev || c.maxConcurrent.CompareAndSwap(prev, cur) {
			break
		}
	}
	return func() {
		c.inFlight.Add(-1)
	}
}

type controlledSQLiteConnector struct {
	base driver.Driver
	dsn  string
	ctrl *rejectionSQLController
}

func (c controlledSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &controlledSQLiteConn{raw: raw, ctrl: c.ctrl}, nil
}

func (c controlledSQLiteConnector) Driver() driver.Driver { return c.base }

type controlledSQLiteConn struct {
	raw  driver.Conn
	ctrl *rejectionSQLController
}

func (c *controlledSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	return c.raw.Prepare(query)
}

func (c *controlledSQLiteConn) Close() error { return c.raw.Close() }

func (c *controlledSQLiteConn) Begin() (driver.Tx, error) { return c.raw.Begin() }

func (c *controlledSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	done := c.ctrl.trackEnter()
	defer done()
	if strings.Contains(strings.ToLower(query), "from settings") {
		c.ctrl.selectCalls.Add(1)
		c.ctrl.mu.Lock()
		hook := c.ctrl.onSelect
		c.ctrl.mu.Unlock()
		if hook != nil {
			if err := hook(); err != nil {
				return nil, err
			}
		}
	}
	if qc, ok := c.raw.(driver.QueryerContext); ok {
		rows, err := qc.QueryContext(ctx, query, args)
		if !errors.Is(err, driver.ErrSkip) {
			return rows, err
		}
	}
	stmt, err := c.raw.Prepare(query)
	if err != nil {
		return nil, err
	}
	if sq, ok := stmt.(driver.StmtQueryContext); ok {
		rows, qErr := sq.QueryContext(ctx, args)
		if qErr != nil {
			_ = stmt.Close()
			return nil, qErr
		}
		return &closingDriverRows{Rows: rows, stmt: stmt}, nil
	}
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	rows, qErr := stmt.Query(vals)
	if qErr != nil {
		_ = stmt.Close()
		return nil, qErr
	}
	return &closingDriverRows{Rows: rows, stmt: stmt}, nil
}

func (c *controlledSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	done := c.ctrl.trackEnter()
	defer done()
	lower := strings.ToLower(strings.TrimSpace(query))
	var (
		afterDelete func(error)
		afterInsert func(error)
	)
	switch {
	case strings.HasPrefix(lower, "delete from settings"):
		c.ctrl.mu.Lock()
		hook := c.ctrl.onDelete
		afterDelete = c.ctrl.afterDelete
		c.ctrl.mu.Unlock()
		if hook != nil {
			if err := hook(); err != nil {
				if afterDelete != nil {
					afterDelete(err)
				}
				return nil, err
			}
		}
	case strings.HasPrefix(lower, "insert into settings"):
		c.ctrl.mu.Lock()
		afterInsert = c.ctrl.afterInsert
		c.ctrl.mu.Unlock()
	}
	runExec := func() (driver.Result, error) {
		if ec, ok := c.raw.(driver.ExecerContext); ok {
			res, err := ec.ExecContext(ctx, query, args)
			if !errors.Is(err, driver.ErrSkip) {
				return res, err
			}
		}
		stmt, err := c.raw.Prepare(query)
		if err != nil {
			return nil, err
		}
		defer func() { _ = stmt.Close() }()
		if se, ok := stmt.(driver.StmtExecContext); ok {
			return se.ExecContext(ctx, args)
		}
		vals := make([]driver.Value, len(args))
		for i, a := range args {
			vals[i] = a.Value
		}
		return stmt.Exec(vals)
	}
	res, err := runExec()
	if afterInsert != nil {
		afterInsert(err)
	}
	if afterDelete != nil {
		afterDelete(err)
	}
	return res, err
}

type closingDriverRows struct {
	driver.Rows
	stmt driver.Stmt
}

func (r *closingDriverRows) Close() error {
	err1 := r.Rows.Close()
	err2 := r.stmt.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func openControlledRejectionDB(t *testing.T, app *freshApplication, ctrl *rejectionSQLController) *sql.DB {
	t.Helper()
	rec, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatalf("load fresh record for db path: %v", err)
	}
	dbPath := rec.Operation.Identity.DatabasePath
	dsn := "file:" + filepath.ToSlash(dbPath) + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db := sql.OpenDB(controlledSQLiteConnector{
		base: app.rejection.db.Driver(),
		dsn:  dsn,
		ctrl: ctrl,
	})
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestExternalRejectionReadFailureFailsClosedAndRetainsPositiveBlocker verifies
// that (1) a durable refusal write retains positive refusal state in memory so
// subsequent requests in the same process answer 409 external_management_key_rejected
// even if SQLite SELECT later fails, and (2) on cold start (empty in-memory
// cache), a SQLite SELECT failure or corrupt refusal row fails closed across
// native and bridged routes with zero additional CPA requests rather than
// treating lookup failure as "not refused".
func TestExternalRejectionReadFailureFailsClosedAndRetainsPositiveBlocker(t *testing.T) {
	const rotatedKey = "read-fault-rotated-0000111122223333444455556666777788889999aa"

	t.Run("in_process_retains_positive_blocker_when_sqlite_select_fails", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, rotatedKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)

		ctrl := &rejectionSQLController{}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))

		first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("first gateway/config = %d %s, want 409 %s", first.Code, first.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("initial refusal CPA requests = %d, want 1", got)
		}

		ctrl.mu.Lock()
		ctrl.onSelect = func() error {
			return errors.New("simulated post-write sqlite select failure")
		}
		ctrl.mu.Unlock()
		beforeSelects := ctrl.selectCalls.Load()

		if cfgRes := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); cfgRes.Code != http.StatusConflict || errorCode(t, cfgRes) != freshconfig.ReasonManagementRejected {
			t.Fatalf("gateway/config under post-write select fault = %d %s, want 409", cfgRes.Code, cfgRes.Body.String())
		}
		if modelsRes := gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", ""); modelsRes.Code != http.StatusConflict || errorCode(t, modelsRes) != freshconfig.ReasonManagementRejected {
			t.Fatalf("cpamp/v1/models under post-write select fault = %d %s, want 409", modelsRes.Code, modelsRes.Body.String())
		}
		if legacyRes := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); legacyRes.Code != http.StatusConflict || errorCode(t, legacyRes) != freshconfig.ReasonManagementRejected {
			t.Fatalf("legacy api-keys under post-write select fault = %d %s, want 409", legacyRes.Code, legacyRes.Body.String())
		}
		if got := ctrl.selectCalls.Load() - beforeSelects; got != 0 {
			t.Fatalf("expected retained positive blocker to avoid SQLite SELECT, got %d SELECT calls", got)
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("CPA requests after post-write select fault = %d, want 1", got)
		}
	})

	t.Run("cold_start_select_error_and_corrupt_row_fail_closed_across_surfaces", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, rotatedKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)
		stagedKey := stageExternalKeyForGateTest(t, app, "6", false)

		baseRejectionDB := app.rejection.db
		first := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
		var statusBody freshRuntimeStatusResponse
		if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &statusBody) != nil || statusBody.ErrorCode != freshconfig.ReasonManagementRejected {
			t.Fatalf("initial runtime/status = %d %s", first.Code, first.Body.String())
		}
		var savedValue string
		if err := baseRejectionDB.QueryRowContext(
			t.Context(),
			`SELECT value FROM settings WHERE key = ?`,
			externalRejectionKey,
		).Scan(&savedValue); err != nil {
			t.Fatalf("read persisted refusal row: %v", err)
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("initial refusal CPA requests = %d, want 1", got)
		}

		assertAllSurfacesFailClosed := func(label string) {
			t.Helper()
			const wantUnavailable = "external_cpa_authority_unavailable"
			if cfgRes := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); cfgRes.Code != http.StatusServiceUnavailable || errorCode(t, cfgRes) != wantUnavailable {
				t.Fatalf("%s: gateway/config = %d %s, want 503 %s", label, cfgRes.Code, cfgRes.Body.String(), wantUnavailable)
			}
			for _, modelRoute := range []string{"cpamp/v1/models", "cpamp/models"} {
				if modelsRes := gatewayRequest(t, app, http.MethodGet, modelRoute, "", ""); modelsRes.Code != http.StatusServiceUnavailable || errorCode(t, modelsRes) != wantUnavailable {
					t.Fatalf("%s: %s = %d %s, want 503 %s", label, modelRoute, modelsRes.Code, modelsRes.Body.String(), wantUnavailable)
				}
			}
			if keysRes := gatewayRequest(t, app, http.MethodGet, "api-keys", "", ""); keysRes.Code != http.StatusServiceUnavailable || errorCode(t, keysRes) != wantUnavailable {
				t.Fatalf("%s: api-keys list = %d %s, want 503 %s", label, keysRes.Code, keysRes.Body.String(), wantUnavailable)
			}
			if applyRes := gatewayRequest(t, app, http.MethodPost, "api-keys/"+stagedKey+"/initial-install", `{}`, "application/json"); applyRes.Code != http.StatusServiceUnavailable || errorCode(t, applyRes) != wantUnavailable {
				t.Fatalf("%s: api-keys initial-install = %d %s, want 503 %s", label, applyRes.Code, applyRes.Body.String(), wantUnavailable)
			}
			if usageRes := gatewayRequest(t, app, http.MethodGet, "external/usage", "", ""); usageRes.Code != http.StatusServiceUnavailable || errorCode(t, usageRes) != wantUnavailable {
				t.Fatalf("%s: GET external/usage = %d %s, want 503 %s", label, usageRes.Code, usageRes.Body.String(), wantUnavailable)
			}
			if confirmRes := gatewayRequest(t, app, http.MethodPost, "external/usage", `{"requestId":"`+strings.Repeat("b", 32)+`","confirmEnableStatistics":true}`, "application/json"); confirmRes.Code != http.StatusServiceUnavailable || errorCode(t, confirmRes) != wantUnavailable {
				t.Fatalf("%s: POST external/usage = %d %s, want 503 %s", label, confirmRes.Code, confirmRes.Body.String(), wantUnavailable)
			}
			rtRes := gatewayRequest(t, app, http.MethodGet, "runtime/status", "", "")
			var rtBody freshRuntimeStatusResponse
			if rtRes.Code != http.StatusOK || json.Unmarshal(rtRes.Body.Bytes(), &rtBody) != nil || rtBody.Status != "unknown" || rtBody.ErrorCode != "runtime_unreachable" {
				t.Fatalf("%s: runtime/status = %d %s, want 200 status=unknown errorCode=runtime_unreachable", label, rtRes.Code, rtRes.Body.String())
			}
			if legacyRes := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); legacyRes.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: legacy /v0/management/api-keys = %d %s, want 503", label, legacyRes.Code, legacyRes.Body.String())
			}
			inspectReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/cpamp/v0/management/codex-inspection/run", strings.NewReader(`{"async":false}`))
			inspectReq.Header.Set("Content-Type", "application/json")
			inspectReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			inspectRes := httptest.NewRecorder()
			app.canonicalAPI(inspectRes, inspectReq)
			if inspectRes.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: codex-inspection/run = %d %s, want 503", label, inspectRes.Code, inspectRes.Body.String())
			}
			if got := cpaSrv.requests.Load(); got != 1 {
				t.Fatalf("%s: CPA received %d total requests, want 1 (zero additional requests)", label, got)
			}
		}

		// 1. Cold-start instance with empty localBlocked and failing SQLite SELECT.
		ctrl := &rejectionSQLController{
			onSelect: func() error {
				return errors.New("simulated cold-start sqlite select I/O error")
			},
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		assertAllSurfacesFailClosed("cold_start_select_error")

		// 2. Cold-start instance with corrupt JSON in fresh_external_rejection_v1.
		ctrl.mu.Lock()
		ctrl.onSelect = nil
		ctrl.mu.Unlock()
		if _, err := baseRejectionDB.ExecContext(
			t.Context(),
			`UPDATE settings SET value = '{corrupt' WHERE key = ?`,
			externalRejectionKey,
		); err != nil {
			t.Fatalf("corrupt refusal row: %v", err)
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		assertAllSurfacesFailClosed("cold_start_corrupt_row")

		// 3. Restore valid durable refusal row: cold-start read returns 409 rejected with 0 CPA hits.
		if _, err := baseRejectionDB.ExecContext(
			t.Context(),
			`UPDATE settings SET value = ? WHERE key = ?`,
			savedValue,
			externalRejectionKey,
		); err != nil {
			t.Fatalf("restore refusal row: %v", err)
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		if recovered := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); recovered.Code != http.StatusConflict || errorCode(t, recovered) != freshconfig.ReasonManagementRejected {
			t.Fatalf("gateway/config after restoring durable row = %d %s, want 409", recovered.Code, recovered.Body.String())
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("CPA requests after restoring durable row = %d, want 1", got)
		}
	})
}

// TestExternalNonReturningRejectionSelectAndDeleteCompleteWithinBoundWithoutSlotReuse
// exercises real driver-level non-returning SELECT and DELETE calls plus an
// already-running INSERT finishing after rebind, proving bounded handler
// completion, zero premature dbSlot reuse, and zero additional refused-key CPA
// requests.
func TestExternalNonReturningRejectionSelectAndDeleteCompleteWithinBoundWithoutSlotReuse(t *testing.T) {
	t.Run("non_returning_select_bounds_caller_frees_gate_and_prevents_slot_reuse", func(t *testing.T) {
		const rotatedKey = "nonreturning-select-rotated-0000111122223333444455556666777788"
		cpaSrv := newStatefulExternalCPA(t, rotatedKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)

		// Persist durable refusal first.
		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict {
			t.Fatalf("initial refusal = %d %s, want 409", first.Code, first.Body.String())
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("initial CPA requests = %d, want 1", got)
		}

		allowSelectReturn := make(chan struct{})
		var allowOnce sync.Once
		releaseSelect := func() { allowOnce.Do(func() { close(allowSelectReturn) }) }
		t.Cleanup(releaseSelect)

		selectEntered := make(chan struct{})
		var selectOnce sync.Once
		ctrl := &rejectionSQLController{
			onSelect: func() error {
				selectOnce.Do(func() { close(selectEntered) })
				<-allowSelectReturn
				return errors.New("stalled select released during cleanup")
			},
		}

		// Simulate cold start so lookup must consult SQLite SELECT.
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		persistClockCh := make(chan time.Time, 2)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			return persistClockCh, func() {}
		}

		leaderDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			leaderDone <- gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin)
		}()

		select {
		case <-selectEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("cold-start lookup never entered SQLite SELECT")
		}

		waiterParked := make(chan struct{})
		var waiterOnce sync.Once
		app.legacyCredentialGate.onWait = func() {
			waiterOnce.Do(func() { close(waiterParked) })
		}
		defer func() { app.legacyCredentialGate.onWait = nil }()

		waiterDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			waiterDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		}()

		select {
		case <-waiterParked:
		case <-time.After(5 * time.Second):
			t.Fatal("waiter never parked on legacyCredentialGate while SELECT was blocked")
		}

		// Expire leader's SELECT deadline: leader must return 503 locally and
		// release legacyCredentialGate while the stalled SELECT still holds dbSlot.
		persistClockCh <- time.Now()
		select {
		case leaderRes := <-leaderDone:
			if leaderRes.Code != http.StatusServiceUnavailable {
				t.Fatalf("leader under non-returning SELECT = %d %s, want 503", leaderRes.Code, leaderRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("leader remained stuck in non-returning SELECT after persist deadline")
		}

		// Expire waiter's dbSlot acquisition deadline: waiter must also return 503
		// without spawning a second SQL operation while the first SELECT remains blocked.
		persistClockCh <- time.Now()
		select {
		case waiterRes := <-waiterDone:
			if waiterRes.Code != http.StatusServiceUnavailable {
				t.Fatalf("waiter under wedged SELECT dbSlot = %d %s, want 503", waiterRes.Code, waiterRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiter remained stuck waiting on wedged SELECT dbSlot")
		}

		if got := ctrl.maxConcurrent.Load(); got != 1 {
			t.Fatalf("max concurrent rejection SQL operations = %d, want 1 (no premature slot reuse)", got)
		}
		if got := ctrl.selectCalls.Load(); got != 1 {
			t.Fatalf("total SQLite SELECT calls while first remained wedged = %d, want 1", got)
		}
		if got := cpaSrv.requests.Load(); got != 1 {
			t.Fatalf("CPA received %d total requests, want 1 (zero additional requests)", got)
		}
	})

	t.Run("non_returning_delete_on_rebind_completes_within_bound_without_slot_reuse", func(t *testing.T) {
		const secondKey = "nonreturning-delete-second-0000111122223333444455556666777788"
		const thirdKey = "nonreturning-delete-third-aaaabbbbccccddddeeeeffff000011112222"
		cpaSrv := newStatefulExternalCPA(t, secondKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)

		allowDeleteReturn := make(chan struct{})
		var allowOnce sync.Once
		releaseDelete := func() { allowOnce.Do(func() { close(allowDeleteReturn) }) }
		t.Cleanup(releaseDelete)

		deleteEntered := make(chan struct{})
		var deleteOnce sync.Once
		ctrl := &rejectionSQLController{}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))

		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict {
			t.Fatalf("initial refusal = %d %s, want 409", first.Code, first.Body.String())
		}

		ctrl.mu.Lock()
		ctrl.onDelete = func() error {
			deleteOnce.Do(func() { close(deleteEntered) })
			<-allowDeleteReturn
			return errors.New("stalled delete released during cleanup")
		}
		ctrl.mu.Unlock()

		persistClockCh := make(chan time.Time, 2)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			return persistClockCh, func() {}
		}

		rebindDone := make(chan *httptest.ResponseRecorder, 1)
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, secondKey, strings.Repeat("c", 32))
		go func() {
			rebindDone <- gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		}()

		select {
		case <-deleteEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("rebind clear never entered SQLite DELETE")
		}

		persistClockCh <- time.Now()
		select {
		case rebindRes := <-rebindDone:
			if rebindRes.Code != http.StatusServiceUnavailable || errorCode(t, rebindRes) != "external_rebind_unavailable" {
				t.Fatalf("external/rebind under non-returning DELETE = %d %s, want 503 external_rebind_unavailable", rebindRes.Code, rebindRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("external/rebind remained stuck in non-returning DELETE after persist deadline")
		}

		// While DELETE still holds dbSlot, reads on the rebound secondKey must
		// succeed without blocking on dbSlot or resurrecting the stale refusal.
		if cfgOK := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); cfgOK.Code != http.StatusOK {
			t.Fatalf("gateway/config after rebind with wedged DELETE = %d %s, want 200", cfgOK.Code, cfgOK.Body.String())
		}
		if modelsOK := gatewayRequest(t, app, http.MethodGet, "cpamp/v1/models", "", ""); modelsOK.Code != http.StatusOK {
			t.Fatalf("cpamp/v1/models after rebind with wedged DELETE = %d %s, want 200", modelsOK.Code, modelsOK.Body.String())
		}

		// Rotate CPA to thirdKey while DELETE still holds dbSlot: refusal of secondKey
		// times out on dbSlot, installs localBlocked, and prevents secondKey retries.
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = thirdKey
		cpaSrv.mu.Unlock()
		beforeRefusal := cpaSrv.requests.Load()

		refuseDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			refuseDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		}()
		persistClockCh <- time.Now()
		select {
		case refuseRes := <-refuseDone:
			if refuseRes.Code != http.StatusConflict || errorCode(t, refuseRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("refusal under wedged DELETE slot = %d %s, want 409", refuseRes.Code, refuseRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("refusal hung waiting on wedged DELETE dbSlot")
		}

		if followUp := gateRequest(t, app, http.MethodGet, "/v0/management/api-keys", gateAdmin); followUp.Code != http.StatusConflict || errorCode(t, followUp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("follow-up after refusal under wedged DELETE = %d %s, want 409", followUp.Code, followUp.Body.String())
		}
		if sent := cpaSrv.requests.Load() - beforeRefusal; sent != 1 {
			t.Fatalf("refused secondKey sent %d times to CPA, want 1", sent)
		}
		if got := ctrl.maxConcurrent.Load(); got != 1 {
			t.Fatalf("max concurrent rejection SQL operations = %d, want 1 (no premature slot reuse)", got)
		}
	})

	t.Run("late_running_insert_completing_after_rebind_is_flushed_before_cold_start", func(t *testing.T) {
		const secondKey = "late-insert-second-0000111122223333444455556666777788889999aa"
		cpaSrv := newStatefulExternalCPA(t, secondKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)
		baseDB := app.rejection.db

		allowInsertFinish := make(chan struct{})
		var allowOnce sync.Once
		releaseInsert := func() { allowOnce.Do(func() { close(allowInsertFinish) }) }
		t.Cleanup(releaseInsert)

		insertEntered := make(chan struct{})
		var insertOnce sync.Once
		deleteCompleted := make(chan error, 1)
		var deleteDoneOnce sync.Once

		ctrl := &rejectionSQLController{
			afterInsert: func(err error) {
				if err == nil {
					insertOnce.Do(func() { close(insertEntered) })
					<-allowInsertFinish
				}
			},
			afterDelete: func(err error) {
				deleteDoneOnce.Do(func() { deleteCompleted <- err })
			},
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))

		persistClockCh := make(chan time.Time, 2)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			return persistClockCh, func() {}
		}

		// 1. Trigger refusal on K1; pause inside driver ExecContext(INSERT) after epoch check.
		refuseDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			refuseDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		}()
		select {
		case <-insertEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("refusal never entered driver ExecContext(INSERT)")
		}

		// Expire record() persistence deadline so Request 1 finishes while INSERT is still in flight.
		persistClockCh <- time.Now()
		select {
		case refuseRes := <-refuseDone:
			if refuseRes.Code != http.StatusConflict {
				t.Fatalf("initial refusal = %d %s, want 409", refuseRes.Code, refuseRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("initial refusal did not complete on persist deadline")
		}

		// 2. Rebind to secondKey while K1's INSERT is still running inside the driver.
		rebindDone := make(chan *httptest.ResponseRecorder, 1)
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, secondKey, strings.Repeat("f", 32))
		go func() {
			rebindDone <- gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		}()
		// Expire clear()'s slot wait so rebind finishes while INSERT still holds dbSlot.
		persistClockCh <- time.Now()
		select {
		case rebindRes := <-rebindDone:
			if rebindRes.Code != http.StatusServiceUnavailable || errorCode(t, rebindRes) != "external_rebind_unavailable" {
				t.Fatalf("rebind while INSERT in flight = %d %s, want 503 external_rebind_unavailable", rebindRes.Code, rebindRes.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("rebind did not complete while INSERT held dbSlot")
		}

		var rowsWhilePaused int
		if err := baseDB.QueryRowContext(
			t.Context(),
			`SELECT count(*) FROM settings WHERE key = ?`,
			externalRejectionKey,
		).Scan(&rowsWhilePaused); err != nil || rowsWhilePaused != 1 {
			t.Fatalf("expected late INSERT to have committed 1 row before return, count=%d err=%v", rowsWhilePaused, err)
		}

		// 3. Let the late INSERT return so deferred pending-clear cleanup deletes the row under dbSlot.
		releaseInsert()
		select {
		case delErr := <-deleteCompleted:
			if delErr != nil {
				t.Fatalf("deferred pending-clear DELETE failed: %v", delErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deferred pending-clear DELETE never ran after late INSERT finished")
		}

		var durableRows int
		if err := baseDB.QueryRowContext(
			t.Context(),
			`SELECT count(*) FROM settings WHERE key = ?`,
			externalRejectionKey,
		).Scan(&durableRows); err != nil {
			t.Fatalf("query settings after deferred cleanup: %v", err)
		}
		if durableRows != 0 {
			t.Fatalf("late pre-rebind INSERT left %d durable refusal rows after deferred cleanup, want 0", durableRows)
		}
		retryRes := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		if retryRes.Code != http.StatusOK {
			t.Fatalf("retry rebind after deferred cleanup = %d %s, want 200", retryRes.Code, retryRes.Body.String())
		}
	})
}

// TestExternalPostPrecheckRejectionReadFailureFailsClosedAcrossWireSurfaces
// verifies that when a route-level precheck succeeds (SELECT #1) and the
// subsequent wire-phase refusal lookup fails (SELECT #2 or #3), every two-phase
// native and bridged synchronous surface fails closed with HTTP 503
// external_cpa_authority_unavailable, sends zero CPA requests on the failing
// wire call, and performs zero fallback work (such as unproxied catalog
// downloads, HTTP 200 degraded capability payloads, HTTP 200 needs_review
// outcomes, or generic 500s).
func TestExternalPostPrecheckRejectionReadFailureFailsClosedAcrossWireSurfaces(t *testing.T) {
	const wantUnavailable = "external_cpa_authority_unavailable"
	var catalogHits atomic.Int32
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		catalogHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-4o":{"id":"gpt-4o","cost":{"input":2.5,"output":10}}}}}`))
	}))
	t.Cleanup(catalogServer.Close)

	newApp := func(t *testing.T, cpaURL string) (*freshApplication, *rejectionSQLController) {
		t.Helper()
		app := newExternalApp(t, cpaURL, externalAuthorityManagement)
		catalogURL := catalogServer.URL
		disabledURL := ""
		app.legacyApp.ModelPriceService = modelpricesvc.NewMultiSourceWithModelsDev(
			app.legacyApp.Store, &catalogURL, &disabledURL, &disabledURL, app.legacyApp.ManagerConfigService,
		)
		app.legacy = guardFreshExternalLegacy(guardFreshLegacyWrites(router.New(app.legacyApp), nil), app)
		app.gatewayConfig = gatewayFor(app)
		t.Cleanup(func() { _ = app.legacyApp.CodexInspectionService.StopAndWait(context.Background()) })
		ctrl := &rejectionSQLController{}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		return app, ctrl
	}

	armFailOnSelectFrom := func(ctrl *rejectionSQLController, failFrom int32) *atomic.Int32 {
		seq := &atomic.Int32{}
		ctrl.mu.Lock()
		ctrl.onSelect = func() error {
			if seq.Add(1) >= failFrom {
				return errors.New("simulated post-precheck sqlite select I/O error")
			}
			return nil
		}
		ctrl.mu.Unlock()
		return seq
	}

	t.Run("model_prices_sync_aborts_without_catalog_download", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app, ctrl := newApp(t, cpaSrv.server.URL)
		catalogHits.Store(0)
		armFailOnSelectFrom(ctrl, 2)

		res := gatewayRequest(t, app, http.MethodPost, "cpamp/v0/management/model-prices/sync", `{}`, "application/json")
		if res.Code != http.StatusServiceUnavailable || errorCode(t, res) != wantUnavailable {
			t.Errorf("model-prices/sync = %d %s, want 503 %s", res.Code, res.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load(); got != 0 {
			t.Errorf("model-prices/sync CPA requests = %d, want 0", got)
		}
		if got := catalogHits.Load(); got != 0 {
			t.Errorf("model-prices/sync catalog downloads = %d, want 0 (no unproxied fallback)", got)
		}
	})

	t.Run("gateway_config_read_and_save_fail_closed_with_503", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app, ctrl := newApp(t, cpaSrv.server.URL)

		initial := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		if initial.Code != http.StatusOK {
			t.Fatalf("initial gateway/config = %d %s, want 200", initial.Code, initial.Body.String())
		}
		var view freshconfig.View
		if err := json.Unmarshal(initial.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode gateway/config: %v", err)
		}
		var routingBaseline string
		for _, g := range view.Groups {
			if g.ID == "routing" {
				routingBaseline = g.Baseline
				break
			}
		}
		cpaBefore := cpaSrv.requests.Load()

		// SELECT #1 = route precheck (ok), SELECT #2 = ReadConfigDocument wire gate (fails).
		armFailOnSelectFrom(ctrl, 2)
		resRead := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		if resRead.Code != http.StatusServiceUnavailable || errorCode(t, resRead) != wantUnavailable {
			t.Errorf("gateway/config GET on ReadConfigDocument failure = %d %s, want 503 %s", resRead.Code, resRead.Body.String(), wantUnavailable)
		}

		// SELECT #1 = route precheck (ok), SELECT #2 = SaveGroup wire gate (fails).
		armFailOnSelectFrom(ctrl, 2)
		saveBody := `{"requestId":"` + strings.Repeat("8", 32) + `","expected":{"generation":"` + view.Generation + `","baseline":"` + routingBaseline + `"},"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`
		resSave := gatewayRequest(t, app, http.MethodPost, "gateway/config/groups/routing", saveBody, "application/json")
		if resSave.Code != http.StatusServiceUnavailable || errorCode(t, resSave) != wantUnavailable {
			t.Errorf("gateway/config POST save on wire gate failure = %d %s, want 503 %s", resSave.Code, resSave.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load() - cpaBefore; got != 0 {
			t.Errorf("gateway/config additional CPA requests = %d, want 0", got)
		}
	})

	t.Run("account_action_enable_and_delete_return_503", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app, ctrl := newApp(t, cpaSrv.server.URL)
		cand, err := app.legacyApp.Store.UpsertAccountActionCandidate(t.Context(), model.AccountActionCandidateUpsert{
			ActionType:      model.AccountActionTypeReview,
			Provider:        "codex",
			AuthFileName:    "codex-1.json",
			AuthIndex:       "idx-1",
			AccountSnapshot: "acct-1@example.com",
			ReasonCode:      "quota_exhausted",
			Reason:          "test candidate",
			SeenAtMS:        time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}

		armFailOnSelectFrom(ctrl, 2)
		enableRes := gatewayRequest(t, app, http.MethodPost,
			"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/enable", `{}`, "application/json")
		if enableRes.Code != http.StatusServiceUnavailable || errorCode(t, enableRes) != wantUnavailable {
			t.Errorf("account-action enable = %d %s, want 503 %s", enableRes.Code, enableRes.Body.String(), wantUnavailable)
		}

		armFailOnSelectFrom(ctrl, 2)
		delRes := gatewayRequest(t, app, http.MethodDelete,
			"cpamp/v0/management/account-action-candidates/"+strconv.FormatInt(cand.ID, 10)+"/auth-file", "", "")
		if delRes.Code != http.StatusServiceUnavailable || errorCode(t, delRes) != wantUnavailable {
			t.Errorf("account-action delete = %d %s, want 503 %s", delRes.Code, delRes.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load(); got != 0 {
			t.Errorf("account-action CPA requests = %d, want 0", got)
		}
	})

	t.Run("codex_inspection_actions_return_503_and_run_marks_failed", func(t *testing.T) {
		var authListCalls atomic.Int32
		var probeCalls atomic.Int32
		var mutationCalls atomic.Int32
		cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
				authListCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-2","name":"codex-2.json","provider":"codex","account":"acct-2@example.com","auth_index":"idx-2","disabled":false,"status":"ok","state":"ready"}]}`))
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v0/management/api-call"):
				probeCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status_code":200,"body":{}}`))
			default:
				mutationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		}))
		t.Cleanup(cpaServer.Close)

		app, ctrl := newApp(t, cpaServer.URL)
		seededRun, err := app.legacyApp.Store.CreateCodexInspectionRun(t.Context(), model.CodexInspectionRun{
			TriggerType: model.CodexInspectionTriggerManual,
			Status:      model.CodexInspectionStatusCompleted,
			StartedAtMS: time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
		seededResult, err := app.legacyApp.Store.InsertCodexInspectionResult(t.Context(), model.CodexInspectionResult{
			RunID:           seededRun.ID,
			AccountKey:      "codex:codex-2.json:idx-2",
			FileName:        "codex-2.json",
			DisplayAccount:  "acct-2@example.com",
			AccountSnapshot: "acct-2@example.com",
			AuthIndex:       "idx-2",
			Provider:        "codex",
			Action:          "disable",
			ActionReason:    "quota check",
		})
		if err != nil {
			t.Fatal(err)
		}
		actionBody := `{"resultIds":[` + strconv.FormatInt(seededResult.ID, 10) + `]}`

		// 1. Fail on SELECT #2 (validateActionItems -> fetchAuthFiles).
		armFailOnSelectFrom(ctrl, 2)
		actRes1 := gatewayRequest(t, app, http.MethodPost,
			"cpamp/v0/management/codex-inspection/runs/"+strconv.FormatInt(seededRun.ID, 10)+"/actions", actionBody, "application/json")
		if actRes1.Code != http.StatusServiceUnavailable || errorCode(t, actRes1) != wantUnavailable {
			t.Errorf("codex-inspection actions (validate phase) = %d %s, want 503 %s", actRes1.Code, actRes1.Body.String(), wantUnavailable)
		}
		if got := authListCalls.Load(); got != 0 {
			t.Errorf("codex-inspection actions (validate phase) auth-files calls = %d, want 0", got)
		}

		// 2. Fail on SELECT #3 (validateActionItems succeeds, executeAction target resolution fails before any mutation).
		armFailOnSelectFrom(ctrl, 3)
		actRes2 := gatewayRequest(t, app, http.MethodPost,
			"cpamp/v0/management/codex-inspection/runs/"+strconv.FormatInt(seededRun.ID, 10)+"/actions", actionBody, "application/json")
		if actRes2.Code != http.StatusServiceUnavailable || errorCode(t, actRes2) != wantUnavailable {
			t.Errorf("codex-inspection actions (execute phase) = %d %s, want 503 %s", actRes2.Code, actRes2.Body.String(), wantUnavailable)
		}
		if got := mutationCalls.Load(); got != 0 {
			t.Errorf("codex-inspection actions mutation calls = %d, want 0", got)
		}

		// 3. Inspection run failing on probe wire gate after fetchAuthFiles succeeds:
		// must record status="failed" (never "completed") with 0 probe calls.
		armFailOnSelectFrom(ctrl, 2)
		runDetail, runErr := app.legacyApp.CodexInspectionService.Run(
			app.withExternalSavedCredentialGate(t.Context()),
			codexsvc.RunRequest{TriggerType: "manual", TriggerKey: "post-auth-files-fault"},
		)
		if runErr == nil || runDetail.Run.Status != model.CodexInspectionStatusFailed {
			t.Errorf("codex-inspection run after probe wire-gate fault = (status=%q, err=%v), want status=%q and non-nil error",
				runDetail.Run.Status, runErr, model.CodexInspectionStatusFailed)
		}
		if got := probeCalls.Load(); got != 0 {
			t.Errorf("codex-inspection probe calls = %d, want 0", got)
		}
	})

	t.Run("api_keys_list_and_initial_install_return_503", func(t *testing.T) {
		cpaSrv := newStatefulExternalCPA(t, externalAuthorityManagement)
		app, ctrl := newApp(t, cpaSrv.server.URL)
		stagedKey := stageExternalKeyForGateTest(t, app, "7", false)

		armFailOnSelectFrom(ctrl, 2)
		listRes := gatewayRequest(t, app, http.MethodGet, "api-keys", "", "")
		if listRes.Code != http.StatusServiceUnavailable || errorCode(t, listRes) != wantUnavailable {
			t.Errorf("api-keys list = %d %s, want 503 %s", listRes.Code, listRes.Body.String(), wantUnavailable)
		}

		armFailOnSelectFrom(ctrl, 2)
		installRes := gatewayRequest(t, app, http.MethodPost, "api-keys/"+stagedKey+"/initial-install", `{}`, "application/json")
		if installRes.Code != http.StatusServiceUnavailable || errorCode(t, installRes) != wantUnavailable {
			t.Errorf("api-keys initial-install = %d %s, want 503 %s", installRes.Code, installRes.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load(); got != 0 {
			t.Errorf("api-keys CPA requests = %d, want 0", got)
		}
	})
}

// TestExternalKeyOperationPreflightAndPrepareEnforceGateAndRejectionUnavailability
// verifies #1183 x #1173 integration on GET api-keys/{target}/revocation/{keeper}/{mode}
// and POST api-keys/{target}/revocation:
//  1. Concurrent External preflight calls serialize behind legacyCredentialGate so
//     a newly rejected management key is presented to CPA once and answered locally
//     for the waiter.
//  2. Cold-start precheck and post-precheck wire-phase refusal-read unavailability
//     answer HTTP 503 external_cpa_authority_unavailable on both preflight and
//     prepare with zero additional CPA requests.
func TestExternalKeyOperationPreflightAndPrepareEnforceGateAndRejectionUnavailability(t *testing.T) {
	const wantUnavailable = "external_cpa_authority_unavailable"

	t.Run("concurrent_preflight_serializes_and_records_rejection_once", func(t *testing.T) {
		var upstreamCalls atomic.Int32
		entered := make(chan struct{})
		var enterOnce sync.Once
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamCalls.Add(1)
			enterOnce.Do(func() { close(entered) })
			<-release
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(cpaServer.Close)

		app := newExternalApp(t, cpaServer.URL, externalAuthorityManagement)
		targetKey := stageExternalKeyForGateTest(t, app, "a", false)
		keeperKey := stageExternalKeyForGateTest(t, app, "b", false)
		path := "api-keys/" + targetKey + "/revocation/" + keeperKey + "/replace"

		waiterParked := make(chan struct{})
		var waiterOnce sync.Once
		app.legacyCredentialGate.onWait = func() {
			waiterOnce.Do(func() { close(waiterParked) })
		}
		defer func() { app.legacyCredentialGate.onWait = nil }()

		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			firstDone <- gatewayRequest(t, app, http.MethodGet, path, "", "")
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("first preflight never reached CPA")
		}

		secondDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			secondDone <- gatewayRequest(t, app, http.MethodGet, path, "", "")
		}()
		select {
		case <-waiterParked:
		case <-time.After(500 * time.Millisecond):
			t.Errorf("second preflight did not park behind legacyCredentialGate")
		}

		releaseOnce.Do(func() { close(release) })
		for i, ch := range []chan *httptest.ResponseRecorder{firstDone, secondDone} {
			res := <-ch
			if res.Code != http.StatusOK {
				t.Fatalf("preflight #%d = %d %s, want 200", i+1, res.Code, res.Body.String())
			}
			var reply struct {
				Ready  bool   `json:"ready"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &reply); err != nil || reply.Ready || reply.Reason != freshconfig.ReasonManagementRejected {
				t.Errorf("preflight #%d = %s, want ready=false reason=%s", i+1, res.Body.String(), freshconfig.ReasonManagementRejected)
			}
		}
		if !app.externalReadGrant(t.Context()).Refused {
			t.Errorf("preflight rejection did not record durable refusal")
		}
		if got := upstreamCalls.Load(); got != 1 {
			t.Errorf("concurrent preflights sent %d CPA requests, want 1", got)
		}
	})

	t.Run("preflight_and_prepare_fail_closed_with_503_on_rejection_read_failure", func(t *testing.T) {
		const rotatedKey = "preflight-fault-rotated-00001111222233334444555566667777888899"
		cpaSrv := newStatefulExternalCPA(t, rotatedKey)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)
		targetKey := stageExternalKeyForGateTest(t, app, "c", false)
		keeperKey := stageExternalKeyForGateTest(t, app, "d", false)
		preflightPath := "api-keys/" + targetKey + "/revocation/" + keeperKey + "/replace"

		// Persist durable refusal row first.
		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict {
			t.Fatalf("initial refusal = %d %s, want 409", first.Code, first.Body.String())
		}
		cpaBefore := cpaSrv.requests.Load()

		// 1. Cold-start precheck SELECT failure: preflight must answer 503 with 0 CPA hits.
		ctrl := &rejectionSQLController{
			onSelect: func() error {
				return errors.New("simulated cold-start preflight select error")
			},
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		preRes := gatewayRequest(t, app, http.MethodGet, preflightPath, "", "")
		if preRes.Code != http.StatusServiceUnavailable || errorCode(t, preRes) != wantUnavailable {
			t.Errorf("cold-start preflight on SELECT error = %d %s, want 503 %s", preRes.Code, preRes.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load() - cpaBefore; got != 0 {
			t.Errorf("cold-start preflight contacted CPA %d times, want 0", got)
		}

		// 2. Rebind to rotatedKey, then fail on SELECT #2 (wire phase after precheck passes)
		// for both preflight and prepare.
		ctrl.mu.Lock()
		ctrl.onSelect = nil
		ctrl.mu.Unlock()
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, rotatedKey, strings.Repeat("9", 32))
		if rb := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json"); rb.Code != http.StatusOK {
			t.Fatalf("rebind = %d %s, want 200", rb.Code, rb.Body.String())
		}
		cpaBefore = cpaSrv.requests.Load()

		var seq atomic.Int32
		ctrl.mu.Lock()
		ctrl.onSelect = func() error {
			if seq.Add(1) >= 2 {
				return errors.New("simulated wire-phase preflight/prepare select error")
			}
			return nil
		}
		ctrl.mu.Unlock()
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))

		wirePreRes := gatewayRequest(t, app, http.MethodGet, preflightPath, "", "")
		if wirePreRes.Code != http.StatusServiceUnavailable || errorCode(t, wirePreRes) != wantUnavailable {
			t.Errorf("wire-phase preflight on SELECT error = %d %s, want 503 %s", wirePreRes.Code, wirePreRes.Body.String(), wantUnavailable)
		}

		seq.Store(0)
		prepBody := `{"requestId":"` + strings.Repeat("e", 32) + `","keeperId":"` + keeperKey + `","mode":"replace"}`
		prepRes := gatewayRequest(t, app, http.MethodPost, "api-keys/"+targetKey+"/revocation", prepBody, "application/json")
		if prepRes.Code != http.StatusServiceUnavailable || errorCode(t, prepRes) != wantUnavailable {
			t.Errorf("wire-phase prepare on SELECT error = %d %s, want 503 %s", prepRes.Code, prepRes.Body.String(), wantUnavailable)
		}
		if got := cpaSrv.requests.Load() - cpaBefore; got != 0 {
			t.Errorf("wire-phase preflight/prepare contacted CPA %d times, want 0", got)
		}
	})
}

// TestExternalSameKeyReverifyPropagatesDeleteFailureAcrossRestartAndRecoversOnRetry
// verifies that when a proven re-verify or rotation cannot commit the durable
// refusal DELETE in SQLite:
//  1. POST external/rebind returns a retryable 503 external_rebind_unavailable
//     rather than claiming 200 reverified/rebound.
//  2. Reopening the exact same SQLite database in a fresh Manager instance
//     exhibits state consistent with that non-success verdict (same-K1 remains
//     blocked until clear commits; K2 rotation retains its already-committed
//     replacement credential).
//  3. Retrying POST external/rebind after the DELETE fault clears removes the
//     durable refusal row and succeeds across subsequent restarts.
func TestExternalSameKeyReverifyPropagatesDeleteFailureAcrossRestartAndRecoversOnRetry(t *testing.T) {
	newRestartableExternalApp := func(t *testing.T, cpaURL, management string) (*freshApplication, func() *freshApplication) {
		t.Helper()
		currentApp, closeCurrent := newA3TestApp(t)
		var closeMu sync.Mutex
		closeActive := func() {
			closeMu.Lock()
			fn := closeCurrent
			closeCurrent = nil
			closeMu.Unlock()
			if fn != nil {
				fn()
			}
		}
		t.Cleanup(closeActive)

		baseURL, err := cpa.NormalizeV8BaseURL(cpaURL)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := currentApp.protector.ProtectString(management)
		if err != nil {
			t.Fatal(err)
		}
		record, err := currentApp.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = currentApp.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Strategy = record.Operation.Strategy
			r.Operation.Strategy.Strategy = "connect_external"
			r.Operation.Strategy.Stage = "committed"
			r.Operation.Strategy.TargetIdentity = freshkeyapply.ExternalIdentity(baseURL)
			r.Operation.Strategy.TargetGeneration = 1
			r.Operation.ProductComplete = true
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		currentApp.externalSource = externalInstallSource(currentApp.store, currentApp.protector)
		currentApp.keyApply = freshkeyapply.NewExternal(currentApp.keyRepo, currentApp.keys, currentApp.externalSource)
		currentApp.gatewayConfig = gatewayFor(currentApp)

		paths := legacypreflight.DefaultPaths(record.Operation.Identity.DatabasePath)
		paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")

		restart := func() *freshApplication {
			t.Helper()
			closeActive()
			reopened, openErr := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
			if openErr != nil {
				t.Fatalf("reopen same sqlite database: %v", openErr)
			}
			closeMu.Lock()
			closeCurrent = func() { _ = reopened.Close() }
			closeMu.Unlock()
			restarted := newFreshApplication(reopened, config.Config{}, nil)
			restarted.externalSource = externalInstallSource(restarted.store, restarted.protector)
			restarted.keyApply = freshkeyapply.NewExternal(restarted.keyRepo, restarted.keys, restarted.externalSource)
			restarted.gatewayConfig = gatewayFor(restarted)
			return restarted
		}
		return currentApp, restart
	}

	t.Run("same_key_reverify_delete_fault_reports_503_matches_reopened_sqlite_and_recovers_on_retry", func(t *testing.T) {
		const temporaryBanKey = "temporary-ban-placeholder-000011112222333344445555666677778888"
		cpaSrv := newStatefulExternalCPA(t, temporaryBanKey)
		app, restart := newRestartableExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)

		// 1. Persist durable refusal for K1 (externalAuthorityManagement).
		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("initial refusal = %d %s, want 409 %s", first.Code, first.Body.String(), freshconfig.ReasonManagementRejected)
		}

		// 2. CPA accepts K1 again, while SQLite DELETE on fresh_external_rejection_v1 fails.
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = externalAuthorityManagement
		cpaSrv.mu.Unlock()

		if _, err := app.rejection.db.ExecContext(
			t.Context(),
			`CREATE TRIGGER block_rejection_delete BEFORE DELETE ON settings WHEN OLD.key = 'fresh_external_rejection_v1' BEGIN SELECT RAISE(ABORT, 'simulated rejection delete fault'); END;`,
		); err != nil {
			t.Fatalf("install delete fault trigger: %v", err)
		}

		reverifyBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, externalAuthorityManagement, strings.Repeat("1", 32))
		reverifyFault := gatewayRequest(t, app, http.MethodPost, "external/rebind", reverifyBody, "application/json")
		if reverifyFault.Code != http.StatusServiceUnavailable || errorCode(t, reverifyFault) != "external_rebind_unavailable" {
			t.Errorf("same-key re-verify under DELETE fault = %d %s, want 503 external_rebind_unavailable", reverifyFault.Code, reverifyFault.Body.String())
		}

		// 3. Restart on the exact same SQLite file: because durable DELETE did not
		// commit (and rebind reported 503, not 200), the restarted Manager still sees
		// the durable refusal row and sends 0 CPA requests.
		restartedBeforeClear := restart()
		cpaBeforeRestartRead := cpaSrv.requests.Load()
		if cfgRes := gatewayRequest(t, restartedBeforeClear, http.MethodGet, "gateway/config", "", ""); cfgRes.Code != http.StatusConflict || errorCode(t, cfgRes) != freshconfig.ReasonManagementRejected {
			t.Errorf("restarted app gateway/config before durable clear = %d %s, want 409 %s", cfgRes.Code, cfgRes.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if modelsRes := gatewayRequest(t, restartedBeforeClear, http.MethodGet, "cpamp/v1/models", "", ""); modelsRes.Code != http.StatusConflict || errorCode(t, modelsRes) != freshconfig.ReasonManagementRejected {
			t.Errorf("restarted app cpamp/v1/models before durable clear = %d %s, want 409 %s", modelsRes.Code, modelsRes.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if got := cpaSrv.requests.Load() - cpaBeforeRestartRead; got != 0 {
			t.Errorf("restarted app contacted CPA %d times while refusal row remained, want 0", got)
		}

		// 4. Clear the DELETE fault and retry same-K1 re-verify: durable clear commits.
		if _, err := restartedBeforeClear.rejection.db.ExecContext(t.Context(), `DROP TRIGGER block_rejection_delete;`); err != nil {
			t.Fatalf("drop delete fault trigger: %v", err)
		}
		reverifyOK := gatewayRequest(t, restartedBeforeClear, http.MethodPost, "external/rebind", reverifyBody, "application/json")
		if reverifyOK.Code != http.StatusOK {
			t.Fatalf("retry same-key re-verify after fault cleared = %d %s, want 200", reverifyOK.Code, reverifyOK.Body.String())
		}
		var okBody struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(reverifyOK.Body.Bytes(), &okBody); err != nil || okBody.Status != "reverified" {
			t.Fatalf("retry same-key re-verify body = %s, want status=reverified", reverifyOK.Body.String())
		}

		// 5. Restart on the exact same SQLite file again: refusal row is gone and K1 reads succeed.
		restartedAfterClear := restart()
		var remaining int
		if err := restartedAfterClear.rejection.db.QueryRowContext(
			t.Context(),
			`SELECT count(*) FROM settings WHERE key = ?`,
			externalRejectionKey,
		).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("remaining refusal rows after retry clear = %d (err=%v), want 0", remaining, err)
		}
		if restartedAfterClear.externalReadGrant(t.Context()).Refused {
			t.Fatal("restarted app after durable clear still marked K1 refused")
		}
		if cfgOK := gatewayRequest(t, restartedAfterClear, http.MethodGet, "gateway/config", "", ""); cfgOK.Code != http.StatusOK {
			t.Fatalf("restarted app gateway/config after durable clear = %d %s, want 200", cfgOK.Code, cfgOK.Body.String())
		}
		if modelsOK := gatewayRequest(t, restartedAfterClear, http.MethodGet, "cpamp/v1/models", "", ""); modelsOK.Code != http.StatusOK {
			t.Fatalf("restarted app cpamp/v1/models after durable clear = %d %s, want 200", modelsOK.Code, modelsOK.Body.String())
		}
	})

	t.Run("k2_rotation_keeps_committed_credential_under_delete_fault_and_clears_row_on_same_process_retry", func(t *testing.T) {
		const rotatedKey = "rebind-k2-delete-fault-0000111122223333444455556666777788889999"
		cpaSrv := newStatefulExternalCPA(t, rotatedKey)
		app, restart := newRestartableExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)

		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict {
			t.Fatalf("initial refusal = %d %s, want 409", first.Code, first.Body.String())
		}
		if _, err := app.rejection.db.ExecContext(
			t.Context(),
			`CREATE TRIGGER block_rejection_delete_k2 BEFORE DELETE ON settings WHEN OLD.key = 'fresh_external_rejection_v1' BEGIN SELECT RAISE(ABORT, 'simulated k2 rejection delete fault'); END;`,
		); err != nil {
			t.Fatalf("install delete fault trigger: %v", err)
		}

		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, rotatedKey, strings.Repeat("2", 32))
		rebindFault := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		if rebindFault.Code != http.StatusServiceUnavailable || errorCode(t, rebindFault) != "external_rebind_unavailable" {
			t.Errorf("K2 rebind under DELETE fault = %d %s, want 503 external_rebind_unavailable", rebindFault.Code, rebindFault.Body.String())
		}

		// Retry in the same process once the transient DELETE fault clears (keeping
		// the exact same requestId and K2 managementKey, matching FreshRebindDialog).
		if _, err := app.rejection.db.ExecContext(t.Context(), `DROP TRIGGER block_rejection_delete_k2;`); err != nil {
			t.Fatalf("drop delete fault trigger: %v", err)
		}
		retryK2 := gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		if retryK2.Code != http.StatusOK {
			t.Fatalf("same-process retry K2 rebind after fault cleared = %d %s, want 200", retryK2.Code, retryK2.Body.String())
		}

		// Restart on the exact same SQLite file: K2 is committed and durable refusal row is gone.
		restartedK2 := restart()
		var remaining int
		if err := restartedK2.rejection.db.QueryRowContext(
			t.Context(),
			`SELECT count(*) FROM settings WHERE key = ?`,
			externalRejectionKey,
		).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("remaining refusal rows after K2 retry clear = %d (err=%v), want 0", remaining, err)
		}
		if cfgOK := gatewayRequest(t, restartedK2, http.MethodGet, "gateway/config", "", ""); cfgOK.Code != http.StatusOK {
			t.Fatalf("restarted app gateway/config on committed K2 = %d %s, want 200", cfgOK.Code, cfgOK.Body.String())
		}
	})
}

// TestExternalClearDeleteInFlightInterleavedWithNewRefusalKeepsLocal409 verifies
// that when a rebind's durable refusal DELETE is still in flight after exceeding
// its persistence deadline (returning 503 external_rebind_unavailable), and CPA
// subsequently refuses the newly rebound key before that earlier DELETE finishes:
//  1. Both when the new refusal write queues behind the in-flight DELETE and
//     persists right after it completes, and when the new refusal write's own
//     persistence deadline expires before the earlier DELETE completes, the new
//     key remains refused locally (409 external_management_key_rejected).
//  2. Zero additional CPA authentication attempts are sent after the single
//     refused probe on the newly rotated key.
func TestExternalClearDeleteInFlightInterleavedWithNewRefusalKeepsLocal409(t *testing.T) {
	t.Run("new_refusal_queued_behind_in_flight_delete_persists_and_stays_409", func(t *testing.T) {
		const (
			keyK2 = "interleave-queued-k2-0000111122223333444455556666777788889999aa"
			keyK3 = "interleave-queued-k3-111122223333444455556666777788889999aaaabb"
		)
		cpaSrv := newStatefulExternalCPA(t, keyK2)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)

		var (
			pauseDelete   atomic.Bool
			deleteEntered = make(chan struct{}, 1)
			releaseDelete = make(chan struct{})
			insertDone    = make(chan struct{}, 4)
		)
		ctrl := &rejectionSQLController{
			onDelete: func() error {
				if pauseDelete.Load() {
					select {
					case deleteEntered <- struct{}{}:
					default:
					}
					<-releaseDelete
				}
				return nil
			},
			afterInsert: func(error) {
				if !pauseDelete.Load() {
					select {
					case insertDone <- struct{}{}:
					default:
					}
				}
			},
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		persistClockCh := make(chan time.Time, 4)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			return persistClockCh, func() {}
		}

		// 1. Persist initial refusal for K1.
		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("initial K1 refusal = %d %s, want 409", first.Code, first.Body.String())
		}
		<-insertDone

		// 2. Rebind to K2 while pausing clear's DELETE in flight; fire persist deadline so rebind returns 503.
		pauseDelete.Store(true)
		rebindDone := make(chan *httptest.ResponseRecorder, 1)
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, keyK2, strings.Repeat("7", 32))
		go func() {
			rebindDone <- gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		}()
		select {
		case <-deleteEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("rebind clear did not enter SQLite DELETE")
		}
		persistClockCh <- time.Now()
		select {
		case rb := <-rebindDone:
			if rb.Code != http.StatusServiceUnavailable || errorCode(t, rb) != "external_rebind_unavailable" {
				t.Fatalf("rebind with DELETE in flight = %d %s, want 503 external_rebind_unavailable", rb.Code, rb.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("rebind did not return after clear deadline expired")
		}

		// 3. CPA rotates to K3 so K2 is now refused while earlier clear DELETE is still in flight.
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = keyK3
		cpaSrv.mu.Unlock()
		cpaBeforeK2Probe := cpaSrv.requests.Load()

		probeEnteredSlotWait := make(chan struct{}, 1)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			select {
			case probeEnteredSlotWait <- struct{}{}:
			default:
			}
			return make(chan time.Time), func() {}
		}
		k2ProbeDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			k2ProbeDone <- gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
		}()
		select {
		case <-probeEnteredSlotWait:
		case <-time.After(5 * time.Second):
			t.Fatal("K2 refusal record did not enter dbSlot wait while DELETE was in flight")
		}

		// 4. Let the earlier clear DELETE complete so the queued K2 refusal write persists.
		pauseDelete.Store(false)
		close(releaseDelete)
		select {
		case res := <-k2ProbeDone:
			if res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
				t.Fatalf("K2 refused probe = %d %s, want 409 %s", res.Code, res.Body.String(), freshconfig.ReasonManagementRejected)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("K2 refused probe did not finish after earlier DELETE completed")
		}

		// 5. Assert K2 stays locally 409 across surfaces and cold SQLite reload with 0 more CPA attempts.
		cpaAfterFirstRefusal := cpaSrv.requests.Load()
		if sent := cpaAfterFirstRefusal - cpaBeforeK2Probe; sent != 1 {
			t.Fatalf("K2 probe sent %d CPA requests, want 1", sent)
		}
		for _, route := range []string{"gateway/config", "cpamp/v1/models", "api-keys"} {
			if res := gatewayRequest(t, app, http.MethodGet, route, "", ""); res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
				t.Fatalf("follow-up %s after earlier clear completed = %d %s, want 409 %s", route, res.Code, res.Body.String(), freshconfig.ReasonManagementRejected)
			}
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, &rejectionSQLController{}))
		if res := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
			t.Fatalf("cold-start gateway/config after interleaved clear+refusal = %d %s, want 409 %s", res.Code, res.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if got := cpaSrv.requests.Load() - cpaAfterFirstRefusal; got != 0 {
			t.Fatalf("follow-up reads sent %d CPA requests after K2 refusal, want 0", got)
		}
	})

	t.Run("earlier_delete_completing_after_new_refusal_deadline_keeps_local_409", func(t *testing.T) {
		const (
			keyK2 = "interleave-timeout-k2-0000111122223333444455556666777788889999aa"
			keyK3 = "interleave-timeout-k3-111122223333444455556666777788889999aaaabb"
		)
		cpaSrv := newStatefulExternalCPA(t, keyK2)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		app.gatewayConfig = gatewayFor(app)

		var (
			pauseDelete   atomic.Bool
			deleteEntered = make(chan struct{}, 1)
			releaseDelete = make(chan struct{})
		)
		ctrl := &rejectionSQLController{
			afterDelete: func(error) {
				if pauseDelete.Load() {
					select {
					case deleteEntered <- struct{}{}:
					default:
					}
					<-releaseDelete
				}
			},
		}
		app.rejection = newExternalRejection(openControlledRejectionDB(t, app, ctrl))
		persistClockCh := make(chan time.Time, 4)
		app.rejection.newPersistTimer = func(time.Duration) (<-chan time.Time, func()) {
			return persistClockCh, func() {}
		}

		if first := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); first.Code != http.StatusConflict {
			t.Fatalf("initial K1 refusal = %d %s, want 409", first.Code, first.Body.String())
		}

		pauseDelete.Store(true)
		rebindDone := make(chan *httptest.ResponseRecorder, 1)
		rebindBody := fmt.Sprintf(`{"baseUrl":%q,"managementKey":%q,"requestId":%q}`,
			cpaSrv.server.URL, keyK2, strings.Repeat("8", 32))
		go func() {
			rebindDone <- gatewayRequest(t, app, http.MethodPost, "external/rebind", rebindBody, "application/json")
		}()
		<-deleteEntered
		persistClockCh <- time.Now()
		if rb := <-rebindDone; rb.Code != http.StatusServiceUnavailable {
			t.Fatalf("rebind = %d %s, want 503", rb.Code, rb.Body.String())
		}

		// K2 is refused while DELETE still holds dbSlot, and K2's record() also hits its persist deadline.
		cpaSrv.mu.Lock()
		cpaSrv.activeKey = keyK3
		cpaSrv.mu.Unlock()
		cpaBefore := cpaSrv.requests.Load()
		persistClockCh <- time.Now()
		if k2Res := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", ""); k2Res.Code != http.StatusConflict || errorCode(t, k2Res) != freshconfig.ReasonManagementRejected {
			t.Fatalf("K2 refusal = %d %s, want 409", k2Res.Code, k2Res.Body.String())
		}

		pauseDelete.Store(false)
		close(releaseDelete)
		slot := app.rejection.slot()
		slot <- struct{}{}
		<-slot
		cpaAfterRefusal := cpaSrv.requests.Load()
		if sent := cpaAfterRefusal - cpaBefore; sent != 1 {
			t.Fatalf("K2 probe sent %d CPA requests, want 1", sent)
		}
		for _, route := range []string{"gateway/config", "cpamp/v1/models", "api-keys"} {
			if res := gatewayRequest(t, app, http.MethodGet, route, "", ""); res.Code != http.StatusConflict || errorCode(t, res) != freshconfig.ReasonManagementRejected {
				t.Fatalf("follow-up %s after late DELETE completed = %d %s, want 409 %s", route, res.Code, res.Body.String(), freshconfig.ReasonManagementRejected)
			}
		}
		if got := cpaSrv.requests.Load() - cpaAfterRefusal; got != 0 {
			t.Fatalf("follow-up reads sent %d CPA requests after late DELETE completed, want 0", got)
		}
	})
}
