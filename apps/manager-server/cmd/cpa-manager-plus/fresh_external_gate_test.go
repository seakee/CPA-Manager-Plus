package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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

// TestExternalProviderWaitSet names the paths whose status belongs to an upstream
// provider: they take no header budget, and a trailing slash does not hide them.
func TestExternalProviderWaitSet(t *testing.T) {
	for _, path := range []string{"/v0/management/api-call", "/v0/management/api-call/", "/v0/management/oauth-callback"} {
		if !externalLegacyWaitsOnProvider(path) {
			t.Fatalf("%s waits on a provider but takes the console-read budget", path)
		}
	}
	for _, path := range []string{"/v0/management/api-keys", "/v0/management/config.yaml", "/v1/models"} {
		if externalLegacyWaitsOnProvider(path) {
			t.Fatalf("%s is a CPA read but is exempt from the budget", path)
		}
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
		baseStore := app.store
		rebinder := baseStore.(freshstore.ConnectionRebinder)
		var loads atomic.Int32
		hooked := &hookedLoadStore{
			Store:              baseStore,
			ConnectionRebinder: rebinder,
		}
		hooked.onLoad = func() {
			// Load 1 is pre-gate legacy auth check; Load 2 is inside freshRuntimeClientAdapter.Status.
			if loads.Add(1) == 2 {
				protected, err := app.protector.ProtectString(reboundKey)
				if err != nil {
					t.Errorf("protect reboundKey: %v", err)
					return
				}
				rec, err := baseStore.Load(context.Background())
				if err != nil {
					t.Errorf("load baseStore: %v", err)
					return
				}
				if _, err := rebinder.RebindConnection(context.Background(), rec.Operation.Identity, rec.Operation.Revision,
					model.ManagerCPAConnectionConfig{CPABaseURL: rec.Connection.CPABaseURL, ManagementKey: protected}); err != nil {
					t.Errorf("rebind in hook: %v", err)
				}
			}
		}
		app.store = hooked
		app.externalSource = externalInstallSource(app.store, app.protector)

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
			t.Fatal("refusal of pre-rebind snapshot poisoned reboundKey")
		}
	})
}

type hookedLoadStore struct {
	freshstore.Store
	freshstore.ConnectionRebinder
	onLoad func()
}

func (s *hookedLoadStore) Load(ctx context.Context) (freshstore.Record, error) {
	rec, err := s.Store.Load(ctx)
	if s.onLoad != nil {
		s.onLoad()
	}
	return rec, err
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

	t.Run("rebind between in-gate store load and proxy setup resolution", func(t *testing.T) {
		const reboundKey = "split-rebound-0000111122223333444455556666777788889999aaaabbbbcc"
		const rotatedAgain = "split-rotated-9999888877776666555544443333222211110000ffffeeeedd"

		cpaSrv := newStatefulExternalCPA(t, rotatedAgain)
		app := newExternalApp(t, cpaSrv.server.URL, externalAuthorityManagement)
		baseStore := app.store
		rebinder := baseStore.(freshstore.ConnectionRebinder)
		var loads atomic.Int32
		hooked := &hookedLoadStore{
			Store:              baseStore,
			ConnectionRebinder: rebinder,
		}
		hooked.onLoad = func() {
			// Load 1 is pre-gate externalLegacyDecision; Load 2 is inside legacyCredentialGate.
			// Trigger rebind immediately after the in-gate Load so proxy.Service.resolveSetup
			// sees reboundKey in SQLite unless bound to the in-gate snapshot.
			if loads.Add(1) == 2 {
				protected, err := app.protector.ProtectString(reboundKey)
				if err != nil {
					t.Errorf("protect reboundKey: %v", err)
					return
				}
				rec, err := baseStore.Load(context.Background())
				if err != nil {
					t.Errorf("load baseStore: %v", err)
					return
				}
				if _, err := rebinder.RebindConnection(context.Background(), rec.Operation.Identity, rec.Operation.Revision,
					model.ManagerCPAConnectionConfig{CPABaseURL: rec.Connection.CPABaseURL, ManagementKey: protected}); err != nil {
					t.Errorf("rebind in hook: %v", err)
				}
			}
		}
		app.store = hooked
		app.externalSource = externalInstallSource(app.store, app.protector)

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
			t.Fatal("refusal of pre-rebind snapshot poisoned reboundKey")
		}
	})
}
