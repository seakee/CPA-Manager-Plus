// Package reconnect lets the owners of subscription logins held by CPA
// reconnect them themselves.
//
// A worker watches CPA auth files. When a login can only recover through a
// new OAuth login, its owner (matched by the credential email) is sent a
// one-time link through a notification webhook. The OAuth redirect only goes
// to localhost and CPA waits only minutes for it, so the authorize URL is
// created when the owner clicks Connect on the public reconnect page; they
// paste the localhost URL back (or, for device-code logins, just approve).
package reconnect

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	reconnectrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/reconnect"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	// grace: a credential must look broken this long before anyone is messaged.
	grace = 10 * time.Minute
	// quietAfterCompletion: CPA can keep a 401 cooldown on a replaced
	// credential for a while, so a fresh reconnect is not re-alerted.
	quietAfterCompletion = time.Hour
	// attemptInProgress: no reminder (which replaces the link) right after
	// the owner clicked Connect.
	attemptInProgress = 10 * time.Minute
	pollTimeout       = 45 * time.Second
	pollInterval      = time.Second
)

var (
	ErrNotFound       = errors.New("this reconnect link is not valid, or a newer one was sent to you; use the latest link you received")
	ErrClosed         = errors.New("this reconnect link was already used or is no longer needed")
	ErrExpired        = errors.New("this reconnect link has expired; a new one will be sent to you")
	ErrUnused         = errors.New("this link has expired; ask an administrator for a new one")
	ErrDisabled       = errors.New("self-service reconnect is not enabled")
	ErrNoSetup        = errors.New("the CPA connection is not configured")
	ErrNoPublicURL    = errors.New("set the public panel URL before sending reconnect links")
	ErrNoWebhook      = errors.New("set the notification webhook URL before sending reconnect links")
	ErrNotStarted     = errors.New("click Connect first, then paste the URL you were redirected to")
	ErrBadCallbackURL = errors.New("paste the full URL from the address bar after approving (it starts with http://localhost)")
	ErrStaleAttempt   = errors.New("this URL belongs to an older attempt; click Connect again and use the new page")
	ErrAttemptExpired = errors.New("the time to finish this attempt ran out; click Connect again (you are already signed in, so it is quick)")
	ErrDeviceFlow     = errors.New("nothing to paste for this login: approve the request in the page that opened, and this page detects it")
)

// WrongAccountError: the owner signed in to a different account.
type WrongAccountError struct {
	Site, Expected, Got string
	// Note says what the sign-in did for the other account, if anything.
	Note string
}

func (e *WrongAccountError) Error() string {
	if e.Got == "" {
		return fmt.Sprintf("you signed in with a different %s account; sign in as %s and try again", e.Site, e.Expected)
	}
	msg := fmt.Sprintf("you signed in as %s, but this link is for %s; sign out of %s, sign in as %s and click Connect again.", e.Got, e.Expected, e.Site, e.Expected)
	if e.Note != "" {
		msg += " " + e.Note
	}
	return msg
}

// SetupResolver yields the CPA base URL and management key.
type SetupResolver interface {
	ResolveSetup(ctx context.Context) (store.Setup, bool, error)
}

type Service struct {
	repo        reconnectrepo.Repository
	setup       SetupResolver
	authFiles   *cpaauthfiles.Client
	coordinator *cpaauthfiles.MutationCoordinator
	http        *http.Client
	now         func() time.Time

	running     sync.Mutex
	firstSeenMu sync.Mutex
	firstSeen   map[string]time.Time
	lastRunMSMu sync.Mutex
	lastRunMS   int64
}

func New(repo reconnectrepo.Repository, setup SetupResolver, coordinator *cpaauthfiles.MutationCoordinator) *Service {
	httpClient := &http.Client{Timeout: 20 * time.Second}
	return &Service{
		repo:        repo,
		setup:       setup,
		authFiles:   cpaauthfiles.New(httpClient),
		coordinator: coordinator,
		http:        httpClient,
		now:         time.Now,
		firstSeen:   map[string]time.Time{},
	}
}

// SettingsView is what the panel sees: secrets are reported, never returned.
type SettingsView struct {
	model.ReconnectSettings
	WebhookConfigured bool `json:"webhookConfigured"`
}

func (s *Service) Settings(ctx context.Context) (SettingsView, error) {
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	view := SettingsView{ReconnectSettings: settings, WebhookConfigured: settings.WebhookURL != ""}
	view.WebhookURL = ""
	return view, nil
}

// UpdateSettings saves settings; an empty webhook URL keeps the saved one.
func (s *Service) UpdateSettings(ctx context.Context, next model.ReconnectSettings) (SettingsView, error) {
	current, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	if strings.TrimSpace(next.WebhookURL) == "" {
		next.WebhookURL = current.WebhookURL
	}
	if next.PublicURL != "" {
		if u, err := url.Parse(strings.TrimSpace(next.PublicURL)); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return SettingsView{}, errors.New("public panel URL must start with http:// or https://")
		}
	}
	if next.WebhookURL != "" && !strings.HasPrefix(strings.TrimSpace(next.WebhookURL), "https://") {
		return SettingsView{}, errors.New("notification webhook URL must start with https://")
	}
	if next.Enabled && (strings.TrimSpace(next.PublicURL) == "" || strings.TrimSpace(next.WebhookURL) == "") {
		return SettingsView{}, errors.New("set the public panel URL and the notification webhook URL before enabling")
	}
	if _, err := s.repo.SaveSettings(ctx, next); err != nil {
		return SettingsView{}, err
	}
	return s.Settings(ctx)
}

// active returns the settings and a CPA client when the feature can run.
func (s *Service) active(ctx context.Context) (model.ReconnectSettings, *cpaClient, error) {
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return settings, nil, err
	}
	if !settings.Enabled {
		return settings, nil, ErrDisabled
	}
	setup, ok, err := s.setup.ResolveSetup(ctx)
	if err != nil {
		return settings, nil, err
	}
	if !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return settings, nil, ErrNoSetup
	}
	return settings, &cpaClient{base: setup.CPAUpstreamURL, key: setup.ManagementKey, http: s.http, authFiles: s.authFiles, coordinator: s.coordinator}, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func linkFor(settings model.ReconnectSettings, token string) string {
	return settings.PublicURL + "/management.html#/reconnect/" + token
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func messageType(purpose string) string {
	switch purpose {
	case model.ReconnectPurposeTest:
		return typeTest
	case model.ReconnectPurposeInvite:
		return typeInvite
	default:
		return typeReconnect
	}
}

// createAndNotify opens a request and sends its link. The plaintext token
// only exists in the message, so if sending fails the request is dropped and
// the next check retries.
func (s *Service) createAndNotify(ctx context.Context, settings model.ReconnectSettings, providerID, email, authFile, reason string, manual bool, purpose string) (*model.ReconnectRequest, error) {
	if settings.PublicURL == "" {
		return nil, ErrNoPublicURL
	}
	if settings.WebhookURL == "" {
		return nil, ErrNoWebhook
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	req := &model.ReconnectRequest{
		Provider:     providerFor(providerID).ID,
		Email:        strings.ToLower(strings.TrimSpace(email)),
		AuthFileName: authFile,
		TokenHash:    hashToken(token),
		Status:       model.ReconnectStatusPending,
		Purpose:      purpose,
		Manual:       manual,
		Reason:       truncate(reason, 255),
		CreatedAtMS:  now.UnixMilli(),
		ExpiresAtMS:  now.Add(time.Duration(settings.LinkTTLHours) * time.Hour).UnixMilli(),
	}
	if err := s.repo.Create(ctx, req); err != nil {
		return nil, err
	}
	if err := s.send(ctx, settings, webhookPayload{
		Type: messageType(purpose), Provider: req.Provider, Email: req.Email,
		Link: linkFor(settings, token), ExpiresAt: time.UnixMilli(req.ExpiresAtMS).UTC().Format(time.RFC3339), Reason: req.Reason,
	}); err != nil {
		_ = s.repo.Delete(ctx, req.ID)
		return nil, err
	}
	req.NotifiedAtMS = s.now().UnixMilli()
	_ = s.repo.Update(ctx, req.ID, map[string]any{"notified_at_ms": req.NotifiedAtMS})
	return req, nil
}

// followup re-sends an open request with a fresh link and lifetime. Only the
// token hash is stored, so the previous link stops working once the new one
// is delivered; if delivery fails the old link keeps working.
func (s *Service) followup(ctx context.Context, settings model.ReconnectSettings, req *model.ReconnectRequest) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	expires := s.now().Add(time.Duration(settings.LinkTTLHours) * time.Hour).UnixMilli()
	if err := s.send(ctx, settings, webhookPayload{
		Type: typeFollowup, Provider: req.Provider, Email: req.Email, Link: linkFor(settings, token),
		ExpiresAt: time.UnixMilli(expires).UTC().Format(time.RFC3339), Reason: req.Reason, Followup: req.FollowupCount + 1,
	}); err != nil {
		return err
	}
	return s.repo.Update(ctx, req.ID, map[string]any{
		"token_hash": hashToken(token), "notified_at_ms": s.now().UnixMilli(),
		"followup_count": req.FollowupCount + 1, "expires_at_ms": expires,
		// Only broken logins get reminders: a test/invite row that gets one
		// has become a real outage.
		"purpose": model.ReconnectPurposeReconnect,
	})
}

// Lookup resolves a link token to its open request.
func (s *Service) Lookup(ctx context.Context, token string) (*model.ReconnectRequest, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrNotFound
	}
	req, err := s.repo.GetByTokenHash(ctx, hashToken(token))
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != model.ReconnectStatusPending {
		if req.Status == model.ReconnectStatusExpired {
			return req, ErrUnused
		}
		return req, ErrClosed
	}
	if s.now().UnixMilli() >= req.ExpiresAtMS {
		return req, ErrExpired
	}
	return req, nil
}

// LinkView is what the public page sees.
type LinkView struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	// Status: pending, expired (a reminder will replace it), unused, completed or resolved.
	Status      string `json:"status"`
	ExpiresAtMS int64  `json:"expiresAtMs"`
}

func (s *Service) Describe(ctx context.Context, token string) (*LinkView, error) {
	req, err := s.Lookup(ctx, token)
	if req == nil {
		return nil, err
	}
	view := &LinkView{Provider: req.Provider, Email: req.Email, Status: req.Status, ExpiresAtMS: req.ExpiresAtMS}
	switch {
	case errors.Is(err, ErrExpired):
		view.Status = "expired"
	case errors.Is(err, ErrUnused):
		view.Status = "unused"
	}
	return view, nil
}

// Attempt is what the public page needs after Connect.
type Attempt struct {
	AuthURL    string `json:"authUrl"`
	DeadlineMS int64  `json:"deadlineMs"`
	Device     bool   `json:"device"`
	UserCode   string `json:"userCode,omitempty"`
}

// Start begins a CPA login for the link's owner.
func (s *Service) Start(ctx context.Context, token string) (*Attempt, error) {
	req, err := s.Lookup(ctx, token)
	if err != nil {
		return nil, err
	}
	_, client, err := s.active(ctx)
	if err != nil {
		return nil, err
	}
	p := providerFor(req.Provider)
	files, err := client.listAuthFiles(ctx)
	if err != nil {
		return nil, err
	}
	known := []string{}
	for _, f := range files {
		if f.Provider == p.ID {
			known = append(known, f.Name)
		}
	}
	knownJSON, _ := json.Marshal(known)
	login, err := client.startLogin(ctx, p.AuthURLPath)
	if err != nil {
		return nil, err
	}
	started := s.now()
	window := p.Window
	if login.ExpiresIn > 0 {
		window = time.Duration(login.ExpiresIn) * time.Second
	}
	deadline := started.Add(window)
	if err := s.repo.Update(ctx, req.ID, map[string]any{
		"oauth_state": login.State, "oauth_started_at_ms": started.UnixMilli(),
		"oauth_deadline_ms": deadline.UnixMilli(), "known_auth_files": string(knownJSON),
	}); err != nil {
		return nil, err
	}
	return &Attempt{AuthURL: login.URL, DeadlineMS: deadline.UnixMilli(), Device: p.Device || login.Flow == "device", UserCode: login.UserCode}, nil
}

func attemptDeadline(req *model.ReconnectRequest) time.Time {
	if req.OAuthDeadline > 0 {
		return time.UnixMilli(req.OAuthDeadline)
	}
	return time.UnixMilli(req.OAuthStartedMS).Add(providerFor(req.Provider).Window)
}

// Submit forwards the pasted localhost callback URL, waits for the token
// exchange and checks the new credential belongs to the link's owner.
func (s *Service) Submit(ctx context.Context, token, callbackURL string) error {
	req, err := s.Lookup(ctx, token)
	if err != nil {
		return err
	}
	if req.OAuthState == "" {
		return ErrNotStarted
	}
	p := providerFor(req.Provider)
	if p.Device {
		return ErrDeviceFlow
	}
	callbackURL = strings.TrimSpace(callbackURL)
	u, err := url.Parse(callbackURL)
	if err != nil || u.Query().Get("state") == "" || (u.Query().Get("code") == "" && u.Query().Get("error") == "") {
		return ErrBadCallbackURL
	}
	if u.Query().Get("state") != req.OAuthState {
		return ErrStaleAttempt
	}
	if e := u.Query().Get("error"); e != "" {
		return fmt.Errorf("%s was not authorized (%s); click Connect and approve it", p.Name, e)
	}
	if s.now().After(attemptDeadline(req)) {
		return ErrAttemptExpired
	}
	settings, client, err := s.active(ctx)
	if err != nil {
		return err
	}
	if err := client.submitCallback(ctx, p.CallbackProvider, callbackURL); err != nil {
		var ce *cpaError
		if errors.As(err, &ce) && (ce.StatusCode == http.StatusNotFound || ce.StatusCode == http.StatusConflict) {
			return ErrAttemptExpired
		}
		return err
	}
	if err := s.waitForLogin(ctx, client, req.OAuthState); err != nil {
		return err
	}
	return s.finish(ctx, settings, client, req)
}

func (s *Service) waitForLogin(ctx context.Context, client *cpaClient, state string) error {
	deadline := s.now().Add(pollTimeout)
	for {
		st, err := client.authStatus(ctx, state)
		if err != nil {
			return err
		}
		switch st.Status {
		case "ok":
			return nil
		case "error":
			if msg := strings.ToLower(st.Error); strings.Contains(msg, "timeout") || strings.Contains(msg, "expired") {
				return ErrAttemptExpired
			}
			return fmt.Errorf("login failed: %s; click Connect to try again", st.Error)
		}
		if s.now().After(deadline) {
			return errors.New("the login is taking too long to confirm; wait a minute and reload this page")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Poll reports a device-code login: "wait" until the owner approves, then
// verifies the new credential like a submitted callback and returns "ok".
func (s *Service) Poll(ctx context.Context, token string) (string, error) {
	req, err := s.Lookup(ctx, token)
	if errors.Is(err, ErrClosed) && req != nil && req.Status == model.ReconnectStatusCompleted {
		return "ok", nil // another poll already finished it
	}
	if err != nil {
		return "", err
	}
	if req.OAuthState == "" {
		return "", ErrNotStarted
	}
	if s.now().After(attemptDeadline(req)) {
		return "", ErrAttemptExpired
	}
	settings, client, err := s.active(ctx)
	if err != nil {
		return "", err
	}
	st, err := client.authStatus(ctx, req.OAuthState)
	if err != nil {
		return "", err
	}
	switch st.Status {
	case "wait":
		return "wait", nil
	case "error":
		if msg := strings.ToLower(st.Error); strings.Contains(msg, "timeout") || strings.Contains(msg, "expired") {
			return "", ErrAttemptExpired
		}
		return "", fmt.Errorf("login failed: %s; click Connect to try again", st.Error)
	}
	if err := s.finish(ctx, settings, client, req); err != nil {
		return "", err
	}
	return "ok", nil
}

func (s *Service) finish(ctx context.Context, settings model.ReconnectSettings, client *cpaClient, req *model.ReconnectRequest) error {
	files, err := client.listAuthFiles(ctx)
	if err != nil {
		return err
	}
	var known []string
	_ = json.Unmarshal([]byte(req.KnownAuthFiles), &known)
	knownSet := map[string]bool{}
	for _, n := range known {
		knownSet[n] = true
	}
	p := providerFor(req.Provider)
	// Saved during this attempt; allow clock skew between hosts.
	since := time.UnixMilli(req.OAuthStartedMS).Add(-time.Minute)

	// Logins saved during this attempt, by owner email.
	refreshed := map[string]map[string]bool{}
	other := ""
	for _, f := range files {
		if f.Provider != p.ID || f.UpdatedAt.Before(since) || f.Email == "" {
			continue
		}
		key := strings.ToLower(f.Email)
		if refreshed[key] == nil {
			refreshed[key] = map[string]bool{}
		}
		refreshed[key][f.Name] = true
		if key != strings.ToLower(req.Email) && (other == "" || !knownSet[f.Name]) {
			other = f.Email
		}
	}

	nowMS := s.now().UnixMilli()
	// Signing in with another account still gives the pool a working
	// subscription: keep it for that account's owner.
	outcomes := map[string]string{}
	for email, names := range refreshed {
		if email == strings.ToLower(req.Email) {
			continue
		}
		outcomes[email] = s.settleOther(ctx, settings, client, files, knownSet, p, email, names, nowMS)
		log.Printf("reconnect: %s login for %s %s from %s's link", p.Name, email, outcomes[email], req.Email)
	}

	own := refreshed[strings.ToLower(req.Email)]
	if len(own) == 0 {
		return &WrongAccountError{Site: p.Name, Expected: req.Email, Got: other,
			Note: otherLoginNote(outcomes[strings.ToLower(other)], p.Name, other)}
	}
	return s.settle(ctx, client, files, p, req.Email, own, nowMS)
}

// What signing in with another account did for that account.
const (
	otherLoginRefreshed   = "refreshed"
	otherLoginReconnected = "reconnected"
	otherLoginAdded       = "added"
)

// settleOther keeps a login saved from someone else's link. A brand-new
// login gets a welcome message; one that was waiting to be reconnected
// closes like a normal reconnect, and its owner gets the all-clear if they
// had been messaged about it.
func (s *Service) settleOther(ctx context.Context, settings model.ReconnectSettings, client *cpaClient, files []authFile, knownSet map[string]bool, p provider, email string, names map[string]bool, nowMS int64) string {
	isNew, wasBroken := true, false
	for _, f := range files {
		if f.Provider != p.ID || !strings.EqualFold(f.Email, email) {
			continue
		}
		if knownSet[f.Name] || !names[f.Name] {
			isNew = false
		}
		if !names[f.Name] {
			if broken, _ := needsReauth(f); broken {
				wasBroken = true
			}
		}
	}
	notified := false
	if open, err := s.repo.GetOpen(ctx, p.ID, email); err == nil && open != nil &&
		open.Purpose != model.ReconnectPurposeTest && open.Purpose != model.ReconnectPurposeInvite {
		notified = true
	}
	// Closes any open request, an invitation included, as reconnected.
	if err := s.settle(ctx, client, files, p, email, names, nowMS); err != nil {
		log.Printf("reconnect: settling %s (%s): %v", email, p.Name, err)
	}
	switch {
	case isNew:
		if err := s.send(ctx, settings, webhookPayload{Type: typeWelcome, Provider: p.ID, Email: email}); err != nil {
			log.Printf("reconnect: welcome to %s (%s): %v", email, p.Name, err)
		}
		return otherLoginAdded
	case notified || wasBroken:
		if notified {
			if err := s.sendResolved(ctx, settings, p.ID, email); err != nil {
				log.Printf("reconnect: all-clear to %s (%s): %v", email, p.Name, err)
			}
		}
		return otherLoginReconnected
	default:
		return otherLoginRefreshed
	}
}

// otherLoginNote tells the link owner what their sign-in did for the
// account they used by mistake.
func otherLoginNote(outcome, providerName, email string) string {
	switch outcome {
	case otherLoginReconnected:
		return fmt.Sprintf("That sign-in was not wasted: %s's %s login also needed reconnecting, so we reconnected it.", email, providerName)
	case otherLoginAdded:
		return fmt.Sprintf("That sign-in was not wasted: %s's %s login was added to the shared pool.", email, providerName)
	case otherLoginRefreshed:
		return fmt.Sprintf("%s's %s login was already connected and stays as it is.", email, providerName)
	}
	return ""
}

// settle finishes a reconnect for one owner: a new login can be saved under a
// different file name than the broken one, and the old one can never
// recover, so it is dropped; then the owner's open requests close as
// reconnected.
func (s *Service) settle(ctx context.Context, client *cpaClient, files []authFile, p provider, email string, refreshed map[string]bool, nowMS int64) error {
	for _, f := range files {
		if f.Provider != p.ID || !strings.EqualFold(f.Email, email) || refreshed[f.Name] {
			continue
		}
		if broken, _ := needsReauth(f); broken {
			if err := client.deleteAuthFile(ctx, f.Name); err != nil {
				log.Printf("reconnect: could not remove replaced credential %s: %v", f.Name, err)
			}
		}
	}
	return s.repo.ClosePending(ctx, p.ID, email, model.ReconnectStatusCompleted, nowMS)
}

// AdminSendResult is the outcome of an admin "send test link".
type AdminSendResult struct {
	Sent        bool   `json:"sent"`
	Purpose     string `json:"purpose,omitempty"`
	Notice      string `json:"notice,omitempty"`
	ExpiresAtMS int64  `json:"expiresAtMs,omitempty"`
}

// AdminSend sends the right message for the person's state in CPA: nothing
// when they are already being chased, a reconnect request for a broken
// login, a test message for a working one, an invitation when there is none.
func (s *Service) AdminSend(ctx context.Context, providerID, email string) (*AdminSendResult, error) {
	if !IsProvider(providerID) {
		return nil, errors.New("unsupported provider")
	}
	p := providerFor(providerID)
	email = strings.ToLower(strings.TrimSpace(email))
	settings, client, err := s.active(ctx)
	if err != nil {
		return nil, err
	}
	open, err := s.repo.GetOpen(ctx, p.ID, email)
	if err != nil {
		return nil, err
	}
	if open != nil {
		loc := location(settings)
		return &AdminSendResult{Notice: fmt.Sprintf(
			"%s was already notified about their %s login on %s and has had %d reminder(s). They keep getting reminders until they reconnect; nothing new was sent. Current link expires %s.",
			email, p.Name, time.UnixMilli(open.CreatedAtMS).In(loc).Format("2006-01-02 15:04"), open.FollowupCount,
			time.UnixMilli(open.ExpiresAtMS).In(loc).Format("2006-01-02 15:04 MST"))}, nil
	}
	files, err := client.listAuthFiles(ctx)
	if err != nil {
		return nil, err
	}
	purpose, reason, authFile := model.ReconnectPurposeInvite, "invitation sent by an admin", ""
	for _, f := range files {
		if f.Provider != p.ID || !strings.EqualFold(f.Email, email) {
			continue
		}
		authFile = f.Name
		if broken, why := needsReauth(f); broken {
			purpose, reason = model.ReconnectPurposeReconnect, why
			break
		}
		purpose, reason = model.ReconnectPurposeTest, "test sent by an admin"
	}
	req, err := s.createAndNotify(ctx, settings, p.ID, email, authFile, reason, true, purpose)
	if err != nil {
		return nil, err
	}
	return &AdminSendResult{Sent: true, Purpose: purpose, ExpiresAtMS: req.ExpiresAtMS}, nil
}
