// Package reconnect serves the self-service reconnect API.
//
// Admin routes (panel/admin auth):
//
//	GET  /usage-service/reconnect/settings   PUT /usage-service/reconnect/settings
//	GET  /usage-service/reconnect/requests   GET /usage-service/reconnect/summary
//	POST /usage-service/reconnect/send
//
// Public routes, authorised only by the one-time link token and rate limited:
//
//	GET  /usage-service/reconnect/link/{token}
//	POST /usage-service/reconnect/link/{token}/start
//	POST /usage-service/reconnect/link/{token}/submit
//	GET  /usage-service/reconnect/link/{token}/poll
package reconnect

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	reconnectsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/reconnect"
)

const prefix = "/usage-service/reconnect/"

type Handler struct {
	App *app.Context
	// Public endpoints: link actions are cheap to guess-proof but still
	// rate limited per client IP; polling gets a larger budget.
	actions *limiter
	polls   *limiter
}

func New(appCtx *app.Context) *Handler {
	return &Handler{
		App:     appCtx,
		actions: newLimiter(30, 10*time.Minute),
		polls:   newLimiter(400, 10*time.Minute),
	}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if strings.HasPrefix(path, "link/") {
		h.handleLink(w, r, strings.TrimPrefix(path, "link/"))
		return
	}
	svc := h.App.ReconnectService
	switch path {
	case "settings":
		switch r.Method {
		case http.MethodGet:
			if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
				return
			}
			view, err := svc.Settings(r.Context())
			writeResult(w, view, err)
		case http.MethodPut:
			if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
				return
			}
			var body model.ReconnectSettings
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
				response.Error(w, http.StatusBadRequest, errors.New("invalid settings body"))
				return
			}
			view, err := svc.UpdateSettings(r.Context(), body)
			writeResult(w, view, err)
		default:
			response.MethodNotAllowed(w)
		}
	case "requests":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
			return
		}
		rows, err := svc.Outages(r.Context(), 30)
		writeResult(w, map[string]any{"items": rows}, err)
	case "summary":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
			return
		}
		rows, err := svc.Summary(r.Context())
		writeResult(w, map[string]any{"providers": rows}, err)
	case "send":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
			return
		}
		var body struct {
			Provider string `json:"provider"`
			Email    string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || !strings.Contains(body.Email, "@") {
			response.Error(w, http.StatusBadRequest, errors.New("a valid email is required"))
			return
		}
		result, err := svc.AdminSend(r.Context(), body.Provider, body.Email)
		writeResult(w, result, err)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleLink(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	token, action := parts[0], ""
	if len(parts) == 2 {
		action = parts[1]
	} else if len(parts) > 2 || token == "" {
		http.NotFound(w, r)
		return
	}
	lim := h.actions
	if action == "poll" {
		lim = h.polls
	}
	if !lim.allow(clientIP(r), time.Now()) {
		response.Error(w, http.StatusTooManyRequests, errors.New("too many requests; wait a few minutes and try again"))
		return
	}
	svc := h.App.ReconnectService
	switch {
	case action == "" && r.Method == http.MethodGet:
		view, err := svc.Describe(r.Context(), token)
		writePublic(w, view, err)
	case action == "start" && r.Method == http.MethodPost:
		attempt, err := svc.Start(r.Context(), token)
		writePublic(w, attempt, err)
	case action == "submit" && r.Method == http.MethodPost:
		var body struct {
			CallbackURL string `json:"callbackUrl"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			response.Error(w, http.StatusBadRequest, errors.New("invalid request body"))
			return
		}
		// CPA finishes the token exchange after the callback; do not let a
		// closed tab cancel the confirmation.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
		defer cancel()
		err := svc.Submit(ctx, token, body.CallbackURL)
		writePublic(w, map[string]string{"status": "ok"}, err)
	case action == "poll" && r.Method == http.MethodGet:
		status, err := svc.Poll(r.Context(), token)
		writePublic(w, map[string]string{"status": status}, err)
	default:
		response.MethodNotAllowed(w)
	}
}

func writeResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, reconnectsvc.ErrDisabled), errors.Is(err, reconnectsvc.ErrNoSetup),
			errors.Is(err, reconnectsvc.ErrNoPublicURL), errors.Is(err, reconnectsvc.ErrNoWebhook):
			status = http.StatusConflict
		case strings.Contains(err.Error(), "must start with") || strings.Contains(err.Error(), "before enabling") ||
			strings.Contains(err.Error(), "unsupported provider"):
			status = http.StatusBadRequest
		}
		response.Error(w, status, err)
		return
	}
	response.JSON(w, http.StatusOK, value)
}

// writePublic shows flow errors to the link owner as-is and hides internal
// details (CPA or database errors) behind a generic message.
func writePublic(w http.ResponseWriter, value any, err error) {
	if err == nil {
		response.JSON(w, http.StatusOK, value)
		return
	}
	var wrong *reconnectsvc.WrongAccountError
	known := []error{reconnectsvc.ErrNotFound, reconnectsvc.ErrClosed, reconnectsvc.ErrExpired, reconnectsvc.ErrUnused,
		reconnectsvc.ErrNotStarted, reconnectsvc.ErrBadCallbackURL, reconnectsvc.ErrStaleAttempt,
		reconnectsvc.ErrAttemptExpired, reconnectsvc.ErrDeviceFlow, reconnectsvc.ErrDisabled}
	for _, k := range known {
		if errors.Is(err, k) {
			status := http.StatusBadRequest
			if errors.Is(err, reconnectsvc.ErrNotFound) {
				status = http.StatusNotFound
			}
			response.Error(w, status, err)
			return
		}
	}
	if errors.As(err, &wrong) || strings.Contains(err.Error(), "click Connect") || strings.Contains(err.Error(), "taking too long") {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	response.Error(w, http.StatusBadGateway, errors.New("the login service is unavailable right now; try again in a minute or contact an administrator"))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter is a fixed-window request counter per key.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*windowCount
}

type windowCount struct {
	start time.Time
	count int
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string]*windowCount{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 { // bound memory under a flood
		for k, v := range l.hits {
			if now.Sub(v.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	c := l.hits[key]
	if c == nil || now.Sub(c.start) >= l.window {
		l.hits[key] = &windowCount{start: now, count: 1}
		return true
	}
	if c.count >= l.max {
		return false
	}
	c.count++
	return true
}
