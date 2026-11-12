package ingress

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const proxyBufferSize = 32 << 10

// PreFreshPosture describes Manager's routing posture while Ingress holds no
// active Fresh routing projection.
type PreFreshPosture uint8

const (
	// PreFreshBootstrap indicates routing is not committed and the fixed
	// Bootstrap UI is the entry point for browser navigations.
	PreFreshBootstrap PreFreshPosture = iota
	// PreFreshLegacyGateway preserves the explicit 204 legacy Manager
	// passthrough to the model Gateway.
	PreFreshLegacyGateway
	// PreFreshPendingClosed keeps all non-Control traffic closed with 503
	// without redirecting browser navigations (for example when Manager already
	// has a committed Fresh route awaiting Restore, or Manager is unreachable).
	PreFreshPendingClosed
)

type Handler struct {
	classifier       Classifier
	manager          http.Handler
	gateway          http.Handler
	fresh            *FreshRoutes
	legacyGateway    func(context.Context) bool
	preFreshPosture  func(context.Context) PreFreshPosture
	gatewayAdmission func(context.Context) bool
	chat             *privateChat
}

func NewFreshHandler(fresh *FreshRoutes, managerURL, gatewayURL *url.URL, logger *log.Logger, legacyGateway func(context.Context) bool) *Handler {
	legacy := NewTransitionalClassifier()
	matcher := ControlMatcherFunc(func(p string) bool { return fresh.Matches(p) || legacy.Classify(p) == RouteManager })
	h := NewHandler(NewClassifier(matcher), managerURL, gatewayURL, logger)
	h.fresh = fresh
	h.legacyGateway = legacyGateway
	return h
}

func NewHandler(classifier Classifier, managerURL, gatewayURL *url.URL, logger *log.Logger) *Handler {
	return &Handler{
		classifier: classifier,
		manager:    newReverseProxy(managerURL, logger),
		gateway:    newReverseProxy(gatewayURL, logger),
	}
}

// EnableFreshGatewayAdmission admits Fresh client Gateway traffic only while
// the private Manager answer for the live routing projection is positive. It is
// the only way a Fresh installation opens the model Gateway; without it every
// Fresh Gateway request stays 503.
func (h *Handler) EnableFreshGatewayAdmission(admitted func(context.Context) bool) {
	h.gatewayAdmission = admitted
}

// EnablePreFreshPosture supplies the single private Manager authority posture
// used while no Fresh routing projection is active in Ingress.
func (h *Handler) EnablePreFreshPosture(posture func(context.Context) PreFreshPosture) {
	h.preFreshPosture = posture
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := h.classifier.ClassifyRequest(r)
	switch route {
	case RouteManager:
		w.Header().Set("X-CPAMP-Route-Owner", "manager")
		h.manager.ServeHTTP(w, r)
	case RouteDeny:
		// Retired/ambiguous Control ownership is denied at the transport edge;
		// do not forward Manager/Admin-looking credentials to either upstream.
		http.NotFound(w, r)
	default:
		w.Header().Set("X-CPAMP-Route-Owner", "gateway")
		if h.chat != nil {
			h.chat.ServeHTTP(w, r)
			return
		}
		if h.fresh != nil && h.fresh.Active() {
			// A Fresh routing projection establishes Control ownership only.
			// Gateway traffic is admitted solely by a current positive private
			// Manager answer bound to that same installation, generation and
			// committed projection. Absence, error, expiry-without-reconfirmation
			// and mismatch all stay closed.
			if h.gatewayAdmission == nil || !h.gatewayAdmission(r.Context()) {
				if !h.fresh.State().Committed && isPreCommitBrowserNavigation(r) {
					redirectToBootstrapUI(w, r)
					return
				}
				http.Error(w, "gateway pending configuration", http.StatusServiceUnavailable)
				return
			}
			h.gateway.ServeHTTP(w, r)
			return
		}
		// Only a positive legacy-mode reply from the private Manager authority
		// preserves the separate pre-Fresh passthrough contract. A handler
		// without Fresh routing keeps its plain classifier-to-Gateway behavior.
		if h.fresh != nil {
			switch h.resolvePreFreshPosture(r.Context()) {
			case PreFreshLegacyGateway:
				h.gateway.ServeHTTP(w, r)
				return
			case PreFreshBootstrap:
				if isPreCommitBrowserNavigation(r) {
					redirectToBootstrapUI(w, r)
					return
				}
			}
			http.Error(w, "gateway pending configuration", http.StatusServiceUnavailable)
			return
		}
		h.gateway.ServeHTTP(w, r)
	}
}

func (h *Handler) resolvePreFreshPosture(ctx context.Context) PreFreshPosture {
	if h.preFreshPosture != nil {
		return h.preFreshPosture(ctx)
	}
	if h.legacyGateway != nil && h.legacyGateway(ctx) {
		return PreFreshLegacyGateway
	}
	return PreFreshBootstrap
}

func redirectToBootstrapUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Location", FixedBootstrapUIPath)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusSeeOther)
	_, _ = io.WriteString(w, `<a href="`+FixedBootstrapUIPath+`">See Other</a>.`+"\n")
}

func isPreCommitBrowserNavigation(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	if mode := strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")); mode != "" && !strings.EqualFold(mode, "navigate") {
		return false
	}
	if dest := strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")); dest != "" && !strings.EqualFold(dest, "document") {
		return false
	}
	if !acceptPrefersHTML(r.Header) {
		return false
	}
	return !gatewayDataPlaneCandidate(r)
}

func gatewayDataPlaneCandidate(r *http.Request) bool {
	pathValue := r.URL.Path
	if pathValue == "" {
		pathValue = "/"
	}
	for _, raw := range [4]string{pathValue, r.URL.RawPath, r.URL.EscapedPath(), r.RequestURI} {
		if raw == "" {
			continue
		}
		if index := strings.IndexByte(raw, '?'); index >= 0 {
			raw = raw[:index]
		}
		for _, candidate := range routeCandidates(raw) {
			for _, prefix := range [4]string{"/models", "/v0", "/v1", "/v1beta"} {
				if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
					return true
				}
			}
		}
	}
	return false
}

func acceptPrefersHTML(header http.Header) bool {
	values := header.Values("Accept")
	if len(values) == 0 {
		return false
	}
	pos := 0
	htmlQ := -1
	htmlPos := -1
	maxOtherQ := -1
	maxCompetingTextQ := -1
	maxSpecificOtherQ := -1
	maxSpecificOtherPos := -1

	for _, raw := range values {
		for rest := raw; rest != ""; {
			var item string
			item, rest, _ = strings.Cut(rest, ",")
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			mediaRange, params, _ := strings.Cut(item, ";")
			mediaRange = strings.TrimSpace(mediaRange)
			slash := strings.IndexByte(mediaRange, '/')
			if slash <= 0 || slash == len(mediaRange)-1 || strings.IndexAny(mediaRange, " \t") >= 0 {
				pos++
				continue
			}
			q, ok := parseAcceptQuality(params)
			if !ok {
				pos++
				continue
			}
			switch {
			case strings.EqualFold(mediaRange, "text/html"):
				if q > htmlQ {
					htmlQ = q
					htmlPos = pos
				}
			case strings.EqualFold(mediaRange, "application/xhtml+xml"):
			default:
				if q > maxOtherQ {
					maxOtherQ = q
				}
				if strings.EqualFold(mediaRange, "text/plain") ||
					strings.EqualFold(mediaRange, "application/json") ||
					strings.EqualFold(mediaRange, "text/event-stream") {
					if q > maxCompetingTextQ {
						maxCompetingTextQ = q
					}
				}
				if mediaRange != "*/*" && !strings.HasSuffix(mediaRange, "/*") {
					if q > maxSpecificOtherQ {
						maxSpecificOtherQ = q
						maxSpecificOtherPos = pos
					}
				}
			}
			pos++
		}
	}
	if htmlQ <= 0 {
		return false
	}
	if maxOtherQ > htmlQ || maxCompetingTextQ >= htmlQ {
		return false
	}
	if maxSpecificOtherQ == htmlQ && maxSpecificOtherPos < htmlPos {
		return false
	}
	return true
}

func parseAcceptQuality(params string) (int, bool) {
	for rest := params; rest != ""; {
		var param string
		param, rest, _ = strings.Cut(rest, ";")
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		return parseQValue(strings.TrimSpace(val))
	}
	return 1000, true
}

func parseQValue(val string) (int, bool) {
	if val == "" {
		return 0, false
	}
	switch val[0] {
	case '1':
		if len(val) == 1 {
			return 1000, true
		}
		if val[1] != '.' || len(val) > 5 {
			return 0, false
		}
		for i := 2; i < len(val); i++ {
			if val[i] != '0' {
				return 0, false
			}
		}
		return 1000, true
	case '0':
		if len(val) == 1 {
			return 0, true
		}
		if val[1] != '.' || len(val) > 5 {
			return 0, false
		}
		q := 0
		scale := 100
		for i := 2; i < len(val); i++ {
			c := val[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			q += int(c-'0') * scale
			scale /= 10
		}
		return q, true
	default:
		return 0, false
	}
}

func newReverseProxy(target *url.URL, logger *log.Logger) *httputil.ReverseProxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 100
	transport.IdleConnTimeout = 90 * time.Second
	transport.ExpectContinueTimeout = time.Second

	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			clearForwardingHeaders(request.Out.Header)
			request.SetURL(target)
			request.Out.Host = request.In.Host
			request.SetXForwarded()
		},
		Transport:     transport,
		FlushInterval: -1,
		BufferPool:    &bufferPool{},
		ErrorLog:      logger,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			logger.Printf("upstream request failed: %v", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
}

func clearForwardingHeaders(header http.Header) {
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			header.Del(name)
		}
	}
	header.Del("Forwarded")
	header.Del("X-Real-IP")
}

type bufferPool struct {
	pool sync.Pool
}

func (p *bufferPool) Get() []byte {
	if value := p.pool.Get(); value != nil {
		return value.([]byte)
	}
	return make([]byte, proxyBufferSize)
}

func (p *bufferPool) Put(buffer []byte) {
	if cap(buffer) < proxyBufferSize {
		return
	}
	p.pool.Put(buffer[:proxyBufferSize])
}
