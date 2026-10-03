package reconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

const testKey = "test-management-key"

// fakeCPA is an in-memory CPA management API.
type fakeCPA struct {
	mu         sync.Mutex
	files      []map[string]any
	state      string
	authStatus string
	onCallback func(f *fakeCPA)
	callbacks  []map[string]string
	deleted    []string
	authPaths  []string
}

func (f *fakeCPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	path := strings.TrimPrefix(r.URL.Path, "/v0/management")
	switch {
	case path == "/auth-files" && r.Method == http.MethodGet:
		write(map[string]any{"files": f.files})
	case path == "/auth-files" && r.Method == http.MethodDelete:
		name := r.URL.Query().Get("name")
		kept := f.files[:0]
		for _, file := range f.files {
			if file["name"] != name {
				kept = append(kept, file)
			}
		}
		f.files = kept
		f.deleted = append(f.deleted, name)
		write(map[string]any{"status": "ok"})
	case strings.HasSuffix(path, "-auth-url"):
		f.authPaths = append(f.authPaths, path)
		f.state = fmt.Sprintf("state-%d", time.Now().UnixNano())
		f.authStatus = "wait"
		resp := map[string]any{"status": "ok", "url": "https://login.example/authorize?state=" + f.state, "state": f.state}
		if path == "/xai-auth-url" || path == "/meta-auth-url" {
			resp["flow"], resp["user_code"], resp["expires_in"] = "device", "ABCD-1234", 900
		}
		write(resp)
	case path == "/oauth-callback":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.callbacks = append(f.callbacks, body)
		u, _ := url.Parse(body["redirect_url"])
		if u.Query().Get("state") != f.state {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"error": "unknown or expired state"})
			return
		}
		if f.onCallback != nil {
			f.onCallback(f)
		}
		f.authStatus = "ok"
		write(map[string]any{"status": "ok"})
	case path == "/get-auth-status":
		write(map[string]any{"status": f.authStatus})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeCPA) set(files ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = files
}

type fakeWebhook struct {
	mu       sync.Mutex
	payloads []webhookPayload
	fail     bool
}

func (h *fakeWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var p webhookPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	h.payloads = append(h.payloads, p)
	w.WriteHeader(http.StatusAccepted)
}

func (h *fakeWebhook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.payloads)
}

func (h *fakeWebhook) last(t *testing.T) webhookPayload {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.payloads) == 0 {
		t.Fatal("no webhook message sent")
	}
	return h.payloads[len(h.payloads)-1]
}

func (h *fakeWebhook) lastToken(t *testing.T) string {
	link := h.last(t).Link
	i := strings.LastIndex(link, "#/reconnect/")
	if i < 0 {
		t.Fatalf("unexpected link %q", link)
	}
	return link[i+len("#/reconnect/"):]
}

type setupResolver struct{ base string }

func (s setupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: s.base, ManagementKey: testKey}, true, nil
}

type fixture struct {
	svc   *Service
	cpa   *fakeCPA
	hook  *fakeWebhook
	clock time.Time
}

func (fx *fixture) advance(d time.Duration) { fx.clock = fx.clock.Add(d) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	cpaFake, hook := &fakeCPA{}, &fakeWebhook{}
	cpaSrv, hookSrv := httptest.NewServer(cpaFake), httptest.NewTLSServer(hook)
	t.Cleanup(cpaSrv.Close)
	t.Cleanup(hookSrv.Close)

	fx := &fixture{cpa: cpaFake, hook: hook, clock: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)}
	fx.svc = New(st.Reconnect, setupResolver{base: cpaSrv.URL}, cpaauthfiles.NewMutationCoordinator())
	fx.svc.http = hookSrv.Client() // trusts the TLS webhook, plain HTTP still works for CPA
	fx.svc.authFiles = cpaauthfiles.New(fx.svc.http)
	fx.svc.now = func() time.Time { return fx.clock }

	settings := model.DefaultReconnectSettings()
	settings.Enabled = true
	settings.PublicURL = "https://panel.example"
	settings.WebhookURL = hookSrv.URL
	settings.FollowupStartHour, settings.FollowupEndHour = 0, 24
	if _, err := st.Reconnect.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	return fx
}

func credential(name, providerID, email, statusMessage string, updated time.Time) map[string]any {
	status := "active"
	if statusMessage != "" {
		status = "error"
	}
	return map[string]any{"name": name, "provider": providerID, "email": email, "status": status,
		"status_message": statusMessage, "updated_at": updated.UTC().Format(time.RFC3339Nano)}
}

func broken(name, providerID, email string) map[string]any {
	return credential(name, providerID, email, "unauthorized (refresh token invalid)", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

func healthy(name, providerID, email string) map[string]any {
	return credential(name, providerID, email, "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// detect runs two checks across the grace period so a broken login is acted on.
func (fx *fixture) detect(ctx context.Context) {
	fx.svc.RunCheck(ctx)
	fx.advance(grace + time.Minute)
	fx.svc.RunCheck(ctx)
}

func callbackURL(state string) string {
	return "http://localhost:54545/callback?code=abc&state=" + state
}

func TestNeedsReauth(t *testing.T) {
	cases := []struct {
		name string
		file authFile
		want bool
	}{
		{"refresh token invalid", authFile{Provider: "claude", StatusMessage: "unauthorized (refresh token invalid)"}, true},
		{"invalid_grant", authFile{Provider: "codex", StatusMessage: "invalid_grant"}, true},
		{"healthy", authFile{Provider: "claude", Status: "active"}, false},
		{"rate limited", authFile{Provider: "claude", StatusMessage: "quota exhausted"}, false},
		{"cloudflare", authFile{Provider: "claude", StatusMessage: "cloudflare challenge"}, false},
		{"transient", authFile{Provider: "claude", StatusMessage: "token expired"}, false},
		{"disabled", authFile{Provider: "claude", Disabled: true, StatusMessage: "unauthorized"}, false},
		{"unsupported provider", authFile{Provider: "gemini", StatusMessage: "unauthorized"}, false},
	}
	for _, tc := range cases {
		if got, _ := needsReauth(tc.file); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSettingsNormalizedLimits(t *testing.T) {
	s := model.ReconnectSettings{CheckIntervalMinutes: 0, LinkTTLHours: 500, FollowupHours: 0, FollowupStartHour: 22, FollowupEndHour: 6, FollowupTimeZone: "Nowhere/City"}.Normalized()
	if s.CheckIntervalMinutes != 5 || s.LinkTTLHours != 24 || s.FollowupHours != 1 {
		t.Fatalf("defaults not applied: %+v", s)
	}
	if s.FollowupStartHour != 8 || s.FollowupEndHour != 22 || s.FollowupTimeZone != "UTC" {
		t.Fatalf("invalid window/time zone not reset: %+v", s)
	}
	s = model.ReconnectSettings{CheckIntervalMinutes: 60, LinkTTLHours: 72, FollowupHours: 6}.Normalized()
	if s.CheckIntervalMinutes != 60 || s.LinkTTLHours != 72 || s.FollowupHours != 6 {
		t.Fatalf("upper limits rejected: %+v", s)
	}
}

func TestUpdateSettingsKeepsSecretAndValidates(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	view, err := fx.svc.Settings(ctx)
	if err != nil || !view.WebhookConfigured || view.WebhookURL != "" {
		t.Fatalf("webhook must be reported, never returned: %+v %v", view, err)
	}
	next := view.ReconnectSettings
	next.WebhookURL = "" // blank keeps the saved URL
	next.LinkTTLHours = 48
	if _, err := fx.svc.UpdateSettings(ctx, next); err != nil {
		t.Fatal(err)
	}
	saved, _ := fx.svc.repo.LoadSettings(ctx)
	if saved.WebhookURL == "" || saved.LinkTTLHours != 48 {
		t.Fatalf("settings not saved as expected: %+v", saved)
	}
	next.WebhookURL = "http://insecure.example"
	if _, err := fx.svc.UpdateSettings(ctx, next); err == nil {
		t.Fatal("non-https webhook must be rejected")
	}
	next.WebhookURL, next.PublicURL = "", ""
	if _, err := fx.svc.UpdateSettings(ctx, next); err == nil {
		t.Fatal("enabling without a public URL must be rejected")
	}
}

func TestMonitorNotifiesOnceAfterGraceThenAllClear(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "A@example.com"), healthy("claude-b.json", "claude", "b@example.com"))

	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 0 {
		t.Fatal("first sighting is inside the grace period")
	}
	fx.advance(grace + time.Minute)
	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 1 {
		t.Fatalf("expected one message, got %d", fx.hook.count())
	}
	p := fx.hook.last(t)
	if p.SendTo != "a@example.com" || p.Type != typeReconnect || !strings.HasPrefix(p.Link, "https://panel.example/management.html#/reconnect/") {
		t.Fatalf("unexpected payload %+v", p)
	}
	if !strings.Contains(p.Body, "Action required") || strings.Contains(p.Body, "a@example.com") {
		t.Fatalf("unexpected body %q", p.Body)
	}
	token := fx.hook.lastToken(t)
	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 1 {
		t.Fatal("an open link is not re-sent before a reminder is due")
	}

	fx.cpa.set(healthy("claude-a.json", "claude", "a@example.com"))
	fx.svc.RunCheck(ctx)
	if _, err := fx.svc.Lookup(ctx, token); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed link, got %v", err)
	}
	if fx.hook.count() != 2 || fx.hook.last(t).Type != typeResolved || fx.hook.last(t).Link != "" {
		t.Fatalf("expected one all-clear without a link, got %+v", fx.hook.last(t))
	}
}

func TestMonitorWebhookFailureRetries(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.hook.fail = true
	fx.detect(ctx)
	pending, _ := fx.svc.repo.ListPending(ctx)
	if len(pending) != 0 {
		t.Fatal("an undelivered link must not block a retry")
	}
	fx.hook.fail = false
	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 1 {
		t.Fatal("retry expected on the next check")
	}
}

func TestRemindersRotateLinkAndRespectWindow(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.detect(ctx)
	first := fx.hook.lastToken(t)

	// Due, but outside the window: wait.
	settings, _ := fx.svc.repo.LoadSettings(ctx)
	settings.FollowupStartHour, settings.FollowupEndHour = 1, 2 // clock is 10:11 UTC
	_, _ = fx.svc.repo.SaveSettings(ctx, settings)
	fx.advance(2 * time.Hour)
	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 1 {
		t.Fatal("no reminder outside the allowed hours")
	}

	settings.FollowupStartHour, settings.FollowupEndHour = 0, 24
	_, _ = fx.svc.repo.SaveSettings(ctx, settings)
	fx.svc.RunCheck(ctx)
	if fx.hook.count() != 2 || fx.hook.last(t).Type != typeFollowup || fx.hook.last(t).Followup != 1 {
		t.Fatalf("expected reminder #1, got %+v", fx.hook.last(t))
	}
	second := fx.hook.lastToken(t)
	if _, err := fx.svc.Lookup(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatal("the previous link must stop working")
	}
	if _, err := fx.svc.Lookup(ctx, second); err != nil {
		t.Fatal(err)
	}

	// An expired link is replaced on the same row.
	fx.advance(25 * time.Hour)
	fx.svc.RunCheck(ctx)
	pending, _ := fx.svc.repo.ListPending(ctx)
	if len(pending) != 1 || pending[0].FollowupCount != 2 || fx.hook.last(t).Followup != 2 {
		t.Fatalf("expected one row with reminder #2, got %+v", pending)
	}
}

func TestCallbackFlowAndSingleUse(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files[0] = credential("claude-a.json", "claude", "a@example.com", "unauthorized", fx.clock)
	}
	fx.detect(ctx)
	token := fx.hook.lastToken(t)

	attempt, err := fx.svc.Start(ctx, token)
	if err != nil || attempt.Device || !strings.Contains(attempt.AuthURL, "authorize") {
		t.Fatalf("start: %+v %v", attempt, err)
	}
	if err := fx.svc.Submit(ctx, token, callbackURL(fx.cpa.state)); err != nil {
		t.Fatal(err)
	}
	if fx.cpa.callbacks[0]["provider"] != "anthropic" {
		t.Fatalf("callback provider %q", fx.cpa.callbacks[0]["provider"])
	}
	if _, err := fx.svc.Lookup(ctx, token); !errors.Is(err, ErrClosed) {
		t.Fatal("links are single use")
	}
	// The lingering cooldown status must not re-alert right away.
	fx.detect(ctx)
	if fx.hook.count() != 1 {
		t.Fatal("no new outage inside the quiet period")
	}
}

func TestAttemptValidation(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.detect(ctx)
	token := fx.hook.lastToken(t)

	if err := fx.svc.Submit(ctx, token, callbackURL("x")); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("got %v", err)
	}
	if _, err := fx.svc.Start(ctx, token); err != nil {
		t.Fatal(err)
	}
	old := fx.cpa.state
	fx.advance(6 * time.Minute)
	if err := fx.svc.Submit(ctx, token, callbackURL(old)); !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("got %v", err)
	}
	if _, err := fx.svc.Start(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.Submit(ctx, token, callbackURL(old)); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("got %v", err)
	}
	if err := fx.svc.Submit(ctx, token, "not a url"); !errors.Is(err, ErrBadCallbackURL) {
		t.Fatalf("got %v", err)
	}
	if len(fx.cpa.callbacks) != 0 {
		t.Fatal("nothing invalid may reach CPA")
	}
}

func TestWrongAccountKeptAndReplacedCredentialDropped(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files = append(f.files, credential("claude-other.json", "claude", "other@example.com", "", fx.clock))
	}
	fx.detect(ctx)
	token := fx.hook.lastToken(t)
	_, _ = fx.svc.Start(ctx, token)
	var wrong *WrongAccountError
	err := fx.svc.Submit(ctx, token, callbackURL(fx.cpa.state))
	if !errors.As(err, &wrong) || wrong.Got != "other@example.com" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "other@example.com's Claude login was added to the shared pool") {
		t.Fatalf("note missing: %v", err)
	}
	if len(fx.cpa.deleted) != 0 {
		t.Fatalf("a new working login joins the pool, deleted %v", fx.cpa.deleted)
	}
	if last := fx.hook.last(t); last.Type != typeWelcome || last.SendTo != "other@example.com" {
		t.Fatalf("welcome not sent: %+v", last)
	}

	// Second try saves the right account under a new name: the dead file goes.
	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files = append(f.files, credential("claude-1a2b-a.json", "claude", "a@example.com", "", fx.clock))
	}
	_, _ = fx.svc.Start(ctx, token)
	if err := fx.svc.Submit(ctx, token, callbackURL(fx.cpa.state)); err != nil {
		t.Fatal(err)
	}
	if fx.cpa.deleted[len(fx.cpa.deleted)-1] != "claude-a.json" {
		t.Fatalf("replaced credential not removed: %v", fx.cpa.deleted)
	}
}

func TestWrongAccountReconnectsThatOwner(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-b.json", "claude", "b@example.com"))
	fx.detect(ctx)
	tokenB := fx.hook.lastToken(t)
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"), broken("claude-b.json", "claude", "b@example.com"))
	fx.detect(ctx)
	tokenA := fx.hook.lastToken(t)
	sent := fx.hook.count()

	// a's link, but b signs in; CPA saves it under a new file name.
	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files = append(f.files, credential("claude-9f8e-b.json", "claude", "b@example.com", "", fx.clock))
	}
	_, _ = fx.svc.Start(ctx, tokenA)
	var wrong *WrongAccountError
	err := fx.svc.Submit(ctx, tokenA, callbackURL(fx.cpa.state))
	if !errors.As(err, &wrong) || !strings.Contains(err.Error(), "b@example.com's Claude login also needed reconnecting, so we reconnected it") {
		t.Fatalf("got %v", err)
	}
	if len(fx.cpa.deleted) != 1 || fx.cpa.deleted[0] != "claude-b.json" {
		t.Fatalf("b's broken file should be replaced, deleted %v", fx.cpa.deleted)
	}
	if _, err := fx.svc.Lookup(ctx, tokenB); !errors.Is(err, ErrClosed) {
		t.Fatalf("b's request should close, got %v", err)
	}
	if _, err := fx.svc.Lookup(ctx, tokenA); err != nil {
		t.Fatalf("a still needs to reconnect: %v", err)
	}
	if fx.hook.count() != sent+1 || fx.hook.last(t).Type != typeResolved || fx.hook.last(t).SendTo != "b@example.com" {
		t.Fatalf("b should get the all-clear: %d %+v", fx.hook.count()-sent, fx.hook.last(t))
	}
}

func TestWrongAccountClosesThatOwnersInvitation(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	if res, err := fx.svc.AdminSend(ctx, ProviderClaude, "c@example.com"); err != nil || res.Purpose != model.ReconnectPurposeInvite {
		t.Fatalf("%+v %v", res, err)
	}
	tokenC := fx.hook.lastToken(t)
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.detect(ctx)
	tokenA := fx.hook.lastToken(t)

	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files = append(f.files, credential("claude-c.json", "claude", "c@example.com", "", fx.clock))
	}
	_, _ = fx.svc.Start(ctx, tokenA)
	var wrong *WrongAccountError
	if err := fx.svc.Submit(ctx, tokenA, callbackURL(fx.cpa.state)); !errors.As(err, &wrong) {
		t.Fatalf("got %v", err)
	}
	if _, err := fx.svc.Lookup(ctx, tokenC); !errors.Is(err, ErrClosed) {
		t.Fatalf("c's invitation should close, got %v", err)
	}
	if last := fx.hook.last(t); last.Type != typeWelcome || last.SendTo != "c@example.com" {
		t.Fatalf("welcome not sent: %+v", last)
	}
}

func TestDeviceFlowPollsUntilApproved(t *testing.T) {
	for _, providerID := range []string{ProviderXAI, ProviderMeta} {
		t.Run(providerID, func(t *testing.T) {
			fx := newFixture(t)
			ctx := context.Background()
			fx.cpa.set(broken(providerID+"-a.json", providerID, "a@example.com"))
			res, err := fx.svc.AdminSend(ctx, providerID, "a@example.com")
			if err != nil || res.Purpose != model.ReconnectPurposeReconnect {
				t.Fatalf("%+v %v", res, err)
			}
			token := fx.hook.lastToken(t)
			attempt, err := fx.svc.Start(ctx, token)
			if err != nil || !attempt.Device || attempt.UserCode != "ABCD-1234" {
				t.Fatalf("%+v %v", attempt, err)
			}
			if err := fx.svc.Submit(ctx, token, callbackURL(fx.cpa.state)); !errors.Is(err, ErrDeviceFlow) {
				t.Fatalf("got %v", err)
			}
			if status, err := fx.svc.Poll(ctx, token); err != nil || status != "wait" {
				t.Fatalf("%s %v", status, err)
			}
			fx.cpa.mu.Lock()
			fx.cpa.files[0] = credential(providerID+"-a.json", providerID, "a@example.com", "", fx.clock)
			fx.cpa.authStatus = "ok"
			fx.cpa.mu.Unlock()
			for i := 0; i < 2; i++ { // a late poll still reports ok
				if status, err := fx.svc.Poll(ctx, token); err != nil || status != "ok" {
					t.Fatalf("poll %d: %s %v", i, status, err)
				}
			}
		})
	}
}

func TestAntigravityUsesItsCallbackProvider(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("antigravity-a.json", "antigravity", "a@example.com"))
	fx.cpa.onCallback = func(f *fakeCPA) {
		f.files[0] = credential("antigravity-a.json", "antigravity", "a@example.com", "", fx.clock)
	}
	if _, err := fx.svc.AdminSend(ctx, ProviderAntigravity, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	token := fx.hook.lastToken(t)
	if _, err := fx.svc.Start(ctx, token); err != nil {
		t.Fatal(err)
	}
	if fx.cpa.authPaths[0] != "/antigravity-auth-url" {
		t.Fatalf("auth path %v", fx.cpa.authPaths)
	}
	if err := fx.svc.Submit(ctx, token, "http://localhost:51121/oauth-callback?code=c&state="+fx.cpa.state); err != nil {
		t.Fatal(err)
	}
	if fx.cpa.callbacks[0]["provider"] != "antigravity" {
		t.Fatalf("callback provider %q", fx.cpa.callbacks[0]["provider"])
	}
}

func TestAdminSendCases(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(healthy("claude-ok.json", "claude", "ok@example.com"), broken("claude-broken.json", "claude", "broken@example.com"))

	if res, err := fx.svc.AdminSend(ctx, ProviderClaude, "OK@example.com"); err != nil || res.Purpose != model.ReconnectPurposeTest {
		t.Fatalf("working login: %+v %v", res, err)
	}
	if !strings.Contains(fx.hook.last(t).Body, "Test:") {
		t.Fatal("test wording expected")
	}
	if res, err := fx.svc.AdminSend(ctx, ProviderClaude, "new@example.com"); err != nil || res.Purpose != model.ReconnectPurposeInvite {
		t.Fatalf("no login: %+v %v", res, err)
	}
	if !strings.Contains(fx.hook.last(t).Body, "Invitation:") {
		t.Fatal("invitation wording expected")
	}
	if res, err := fx.svc.AdminSend(ctx, ProviderClaude, "broken@example.com"); err != nil || res.Purpose != model.ReconnectPurposeReconnect {
		t.Fatalf("broken login: %+v %v", res, err)
	}
	res, err := fx.svc.AdminSend(ctx, ProviderClaude, "broken@example.com")
	if err != nil || res.Sent || !strings.Contains(res.Notice, "already notified") || fx.hook.count() != 3 {
		t.Fatalf("already notified: %+v %v", res, err)
	}
	if _, err := fx.svc.AdminSend(ctx, "gemini", "a@example.com"); err == nil {
		t.Fatal("unsupported provider must be rejected")
	}
}

func TestUnusedAdminLinksClose(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(healthy("claude-ok.json", "claude", "ok@example.com"))
	_, _ = fx.svc.AdminSend(ctx, ProviderClaude, "ok@example.com")
	_, _ = fx.svc.AdminSend(ctx, ProviderCodex, "ok@example.com") // no Codex login: invite
	fx.advance(25 * time.Hour)
	fx.svc.RunCheck(ctx)
	rows, err := fx.svc.Outages(ctx, 30)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Provider] = r.Status
	}
	if got[ProviderClaude] != model.ReconnectStatusResolved || got[ProviderCodex] != model.ReconnectStatusExpired {
		t.Fatalf("unexpected statuses %v", got)
	}
	if fx.hook.count() != 2 {
		t.Fatal("no reminders or all-clear for admin links")
	}
}

func TestClaudeAndCodexForSamePersonAreSeparate(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"), broken("codex-a.json", "codex", "a@example.com"))
	fx.detect(ctx)
	if fx.hook.count() != 2 {
		t.Fatalf("one message per provider, got %d", fx.hook.count())
	}
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"), healthy("codex-a.json", "codex", "a@example.com"))
	fx.svc.RunCheck(ctx)
	if last := fx.hook.last(t); last.Type != typeResolved || last.Provider != ProviderCodex {
		t.Fatalf("expected Codex all-clear, got %+v", last)
	}
	pending, _ := fx.svc.repo.ListPending(ctx)
	if len(pending) != 1 || pending[0].Provider != ProviderClaude {
		t.Fatalf("Claude must stay open: %+v", pending)
	}
}

func TestIdleWhenDisabled(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	settings, _ := fx.svc.repo.LoadSettings(ctx)
	settings.Enabled = false
	_, _ = fx.svc.repo.SaveSettings(ctx, settings)
	fx.cpa.set(broken("claude-a.json", "claude", "a@example.com"))
	fx.detect(ctx)
	if fx.hook.count() != 0 {
		t.Fatal("nothing runs while disabled")
	}
	if _, err := fx.svc.AdminSend(ctx, ProviderClaude, "a@example.com"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v", err)
	}
}
