package middleware_test

import (
	"context"
	"encoding/json"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrialHoldGateHoldsMutationsUntilLoopbackAuthenticatedRelease(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "launcher-token")
	const validToken = "tok_9f8e7d6c5b4a3210"
	if err := os.WriteFile(tokenFile, []byte(validToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CPAMP_TRIAL_HOLD", "1")
	t.Setenv("CPAMP_LAUNCHER_TOKEN_FILE", tokenFile)
	gate := middleware.NewTrialHoldGateFromEnv()
	if !gate.Active() {
		t.Fatal("expected TrialHoldGate to initialize active when CPAMP_TRIAL_HOLD=1")
	}

	upstreamHits := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-ok"))
	})
	handler := gate.Wrap(upstream)

	// 1. Safe read methods pass through during active trial hold.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/usage-service/updates", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s during trial hold returned %d, want 200", method, rec.Code)
		}
	}
	if upstreamHits != 3 {
		t.Fatalf("upstreamHits = %d, want 3", upstreamHits)
	}

	// 2. Write methods fail closed with 503 upgrade_trial_verifying + Retry-After: 5.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v2/api-keys", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s during trial hold returned %d, want 503", method, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("%s headers = %v", method, rec.Header())
		}
		var errBody struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("decode 503 JSON: %v", err)
		}
		if errBody.Code != middleware.TrialHoldErrorCode || errBody.Message != middleware.TrialHoldErrorMessage {
			t.Fatalf("unexpected 503 body: %+v", errBody)
		}
	}
	if upstreamHits != 3 {
		t.Fatalf("blocked mutations reached upstream: hits=%d", upstreamHits)
	}

	// 3. Non-loopback callers cannot release the trial hold even with the right token.
	for _, remote := range []string{"192.0.2.1:1234", "10.0.0.8:5000", "127.0.0.2:5000", "[2001:db8::1]:5000"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+validToken)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !gate.Active() {
			t.Fatalf("non-loopback %s returned %d (active=%v), want 403", remote, rec.Code, gate.Active())
		}
	}

	// 4. Loopback GET is rejected with 405.
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, middleware.TrialReleasePath, nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+validToken)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || !gate.Active() {
			t.Fatalf("GET release returned %d (active=%v), want 405", rec.Code, gate.Active())
		}
	}

	// 5. Missing or invalid token returns 401.
	for _, header := range []string{"", "Bearer wrong-token", validToken} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		req.RemoteAddr = "127.0.0.1:50000"
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !gate.Active() {
			t.Fatalf("auth %q returned %d (active=%v), want 401", header, rec.Code, gate.Active())
		}
	}

	// 6. Valid loopback IPv6 ([::1]) release clears the hold and returns 200 {"ok":true}.
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		req.RemoteAddr = "[::1]:54321"
		req.Header.Set("Authorization", "Bearer "+validToken)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gate.Active() {
			t.Fatalf("valid IPv6 release returned %d (active=%v): %s", rec.Code, gate.Active(), rec.Body.String())
		}
		var okBody map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &okBody); err != nil || !okBody["ok"] {
			t.Fatalf("unexpected 200 release body: %s", rec.Body.String())
		}
	}

	// 7. Subsequent mutations pass through to upstream, and IPv4 loopback release remains idempotent.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v2/api-keys", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("post-release %s returned %d, want 200", method, rec.Code)
		}
	}
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		req.RemoteAddr = "127.0.0.1:54322"
		req.Header.Set("Authorization", "Bearer "+validToken)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("idempotent IPv4 release returned %d", rec.Code)
		}
	}
}

func TestTrialHoldReleaseUnconfiguredAndSymlinkTokenRejected(t *testing.T) {
	unconfigured := middleware.NewTrialHoldGate(false, "")
	wrapped := unconfigured.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured token file status = %d, want 404", rec.Code)
	}

	dir := t.TempDir()
	realToken := filepath.Join(dir, "real-token")
	linkToken := filepath.Join(dir, "link-token")
	if err := os.WriteFile(realToken, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realToken, linkToken); err != nil {
		t.Fatal(err)
	}
	symlinkGate := middleware.NewTrialHoldGate(true, linkToken)
	symlinkWrapped := symlinkGate.Wrap(http.NotFoundHandler())
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer secret-token")
	symlinkWrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !symlinkGate.Active() {
		t.Fatalf("symlink token file status = %d (active=%v), want 401", rec.Code, symlinkGate.Active())
	}
}

func TestTrialHoldReleaseRejectsProxyForwardedHeaders(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "launcher-token")
	const validToken = "tok_forwarded_check_0123456789"
	if err := os.WriteFile(tokenFile, []byte(validToken), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := middleware.NewTrialHoldGate(true, tokenFile)
	handler := gate.Wrap(http.NotFoundHandler())

	for _, tc := range []struct {
		header string
		value  string
	}{
		{header: "X-Forwarded-For", value: "203.0.113.10"},
		{header: "X-Forwarded-For", value: ""},
		{header: "X-Forwarded-Host", value: "panel.example.com"},
		{header: "X-Forwarded-Proto", value: "https"},
		{header: "Forwarded", value: "for=203.0.113.10;proto=https"},
		{header: "X-Real-IP", value: "203.0.113.10"},
		{header: "X-Forwarded-Port", value: "443"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+validToken)
		req.Header[tc.header] = []string{tc.value}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !gate.Active() {
			t.Fatalf("header %s=%q returned %d (active=%v), want 403 and gate still active", tc.header, tc.value, rec.Code, gate.Active())
		}
	}

	// Direct loopback request without proxy headers succeeds.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+validToken)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gate.Active() {
		t.Fatalf("direct loopback release returned %d (active=%v), want 200 and inactive", rec.Code, gate.Active())
	}
}

func TestTrialHoldGateEngageDrainsOtherInflightMutations(t *testing.T) {
	gate := middleware.NewTrialHoldGate(false, "")
	middleware.SetProcessTrialHoldGate(gate)
	t.Cleanup(func() { middleware.SetProcessTrialHoldGate(nil) })

	writeEntered := make(chan struct{})
	allowWriteFinish := make(chan struct{})
	var writeFinished atomic.Bool
	var postFenceHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/api-keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Inflight-Test") == "1" {
			close(writeEntered)
			<-allowWriteFinish
			writeFinished.Store(true)
			w.WriteHeader(http.StatusCreated)
			return
		}
		postFenceHits.Add(1)
		w.WriteHeader(http.StatusCreated)
	})
	fenceBlockedUntilWriteDone := make(chan bool, 1)
	mux.HandleFunc("/usage-service/updates/slot/activate", func(w http.ResponseWriter, r *http.Request) {
		middleware.EngageProcessTrialHold()
		if err := middleware.WaitOtherInflightMutations(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		fenceBlockedUntilWriteDone <- writeFinished.Load()
		w.WriteHeader(http.StatusAccepted)
	})
	wrapped := gate.Wrap(mux)

	// 1. Start an in-flight POST mutation.
	inflightDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v2/api-keys", nil)
		req.Header.Set("X-Inflight-Test", "1")
		wrapped.ServeHTTP(rec, req)
		inflightDone <- rec.Code
	}()
	<-writeEntered

	// 2. Trigger POST .../slot/activate through the same TrialHoldGate.Wrap; it
	// engages the hold and blocks waiting for the other in-flight mutation.
	activateDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/usage-service/updates/slot/activate", nil)
		wrapped.ServeHTTP(rec, req)
		activateDone <- rec.Code
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !gate.Active() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !gate.Active() {
		t.Fatal("expected TrialHoldGate to become active immediately on EngageProcessTrialHold")
	}

	// 3. Any new mutation arriving after Engage receives 503 immediately while
	// the earlier in-flight write has still not finished.
	blockedRec := httptest.NewRecorder()
	wrapped.ServeHTTP(blockedRec, httptest.NewRequest(http.MethodPost, "/api/v2/api-keys", nil))
	if blockedRec.Code != http.StatusServiceUnavailable || postFenceHits.Load() != 0 {
		t.Fatalf("post-engage write status=%d upstreamHits=%d, want 503 and 0", blockedRec.Code, postFenceHits.Load())
	}
	select {
	case <-activateDone:
		t.Fatal("activate returned before in-flight write finished")
	case <-time.After(30 * time.Millisecond):
	}

	// Non-HTTP waiter with short deadline returns context.DeadlineExceeded while inflight > 0.
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancelShort()
	if err := gate.WaitOtherInflightMutations(shortCtx); err == nil {
		t.Fatal("expected short deadline WaitOtherInflightMutations to time out while write is held")
	}

	// 4. Allow the in-flight write to finish; activate unblocks without self-deadlock
	// and keeps TrialHoldGate active afterwards.
	close(allowWriteFinish)
	if got := <-inflightDone; got != http.StatusCreated {
		t.Fatalf("in-flight write returned %d, want 201", got)
	}
	if got := <-activateDone; got != http.StatusAccepted {
		t.Fatalf("activate returned %d, want 202", got)
	}
	if waited := <-fenceBlockedUntilWriteDone; !waited {
		t.Fatal("WaitOtherInflightMutations returned before in-flight write finished")
	}
	if !gate.Active() {
		t.Fatal("expected TrialHoldGate to stay active after successful slot activate")
	}
}

func TestTrialHoldGateBeginBackgroundMutationAdmissionAndDrain(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	})

	// 1. Nil process gate fallback obeys IsProcessTrialHoldActive().
	middleware.SetProcessTrialHoldGate(nil)
	middleware.ReleaseProcessTrialHold()
	noopDone, blocked := middleware.BeginBackgroundMutation()
	if blocked || noopDone == nil {
		t.Fatalf("expected inactive fallback to admit mutation, blocked=%v done=%v", blocked, noopDone != nil)
	}
	noopDone()
	noopDone()

	middleware.EngageProcessTrialHold()
	if done, isBlocked := middleware.BeginBackgroundMutation(); !isBlocked || done != nil {
		t.Fatalf("expected active fallback to block mutation, blocked=%v doneNil=%v", isBlocked, done == nil)
	}
	middleware.ReleaseProcessTrialHold()

	// 2. Installed TrialHoldGate tracks background inflight and blocks WaitOtherInflightMutations until idempotent done().
	gate := middleware.NewTrialHoldGate(false, "")
	middleware.SetProcessTrialHoldGate(gate)

	bgDone, blocked := middleware.BeginBackgroundMutation()
	if blocked || bgDone == nil {
		t.Fatalf("expected gate to admit background mutation, blocked=%v", blocked)
	}

	middleware.EngageProcessTrialHold()
	if lateDone, lateBlocked := middleware.BeginBackgroundMutation(); !lateBlocked || lateDone != nil {
		t.Fatalf("expected post-engage BeginBackgroundMutation to be blocked, blocked=%v", lateBlocked)
	}

	waitFinished := make(chan error, 1)
	go func() {
		waitFinished <- middleware.WaitOtherInflightMutations(context.Background())
	}()

	select {
	case err := <-waitFinished:
		t.Fatalf("WaitOtherInflightMutations returned before background done(): %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	bgDone()
	bgDone() // idempotent second call must not underflow inflight

	select {
	case err := <-waitFinished:
		if err != nil {
			t.Fatalf("WaitOtherInflightMutations returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WaitOtherInflightMutations after bgDone()")
	}
}

func TestTrialHoldActivatedChanTransitionsAcrossEngageAndRelease(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	})

	gate := middleware.NewTrialHoldGate(false, "")
	middleware.SetProcessTrialHoldGate(gate)

	ch1 := middleware.TrialHoldActivatedChan()
	select {
	case <-ch1:
		t.Fatal("expected ActivatedChan to remain open while inactive")
	default:
	}

	middleware.EngageProcessTrialHold()
	select {
	case <-ch1:
	default:
		t.Fatal("expected pre-existing ActivatedChan to close atomically on EngageProcessTrialHold")
	}
	select {
	case <-middleware.TrialHoldActivatedChan():
	default:
		t.Fatal("expected ActivatedChan while active to return an already-closed channel")
	}

	middleware.ReleaseProcessTrialHold()
	ch2 := middleware.TrialHoldActivatedChan()
	select {
	case <-ch2:
		t.Fatal("expected recreated ActivatedChan after ReleaseProcessTrialHold to be open")
	default:
	}

	gate.Engage()
	select {
	case <-ch2:
	default:
		t.Fatal("expected recreated ActivatedChan to close on second Engage")
	}
}
