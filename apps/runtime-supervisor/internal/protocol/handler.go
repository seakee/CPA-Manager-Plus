// Package protocol implements the Runtime Protocol v1 surface.
package protocol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const (
	Version         = "v1"
	handshakePath   = "/v1/runtime/handshake"
	statusPath      = "/v1/runtime/status"
	startPath       = "/v1/runtime/operations/start"
	startCapability = "start"
)

// StartExecutor is the Supervisor-private application boundary used by the
// protocol adapter. A nil executor keeps the Runtime read-only.
type StartExecutor func(context.Context, lifecycle.StartRequest) (journal.Operation, error)

type Config struct {
	RuntimeIdentity   string
	RuntimeGeneration uint64
	Token             string
	Start             StartExecutor
}

type handler struct {
	runtimeIdentity   string
	runtimeGeneration uint64
	tokenDigest       [sha256.Size]byte
	start             StartExecutor
}

type handshakeResponse struct {
	ProtocolVersion   string   `json:"protocolVersion"`
	RuntimeIdentity   string   `json:"runtimeIdentity"`
	RuntimeGeneration uint64   `json:"runtimeGeneration"`
	Capabilities      []string `json:"capabilities"`
}

type statusResponse struct {
	ProtocolVersion    string   `json:"protocolVersion"`
	RuntimeIdentity    string   `json:"runtimeIdentity"`
	RuntimeGeneration  uint64   `json:"runtimeGeneration"`
	State              string   `json:"state"`
	CPAObservedVersion string   `json:"cpaObservedVersion"`
	Capabilities       []string `json:"capabilities"`
}

type errorResponse struct {
	Error protocolError `json:"error"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(config Config) (http.Handler, error) {
	identity := strings.TrimSpace(config.RuntimeIdentity)
	if identity == "" {
		return nil, errors.New("runtime identity is required")
	}
	if config.RuntimeGeneration == 0 {
		return nil, errors.New("runtime generation must be greater than zero")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("runtime token is required")
	}
	if strings.IndexFunc(config.Token, unicode.IsSpace) >= 0 {
		return nil, errors.New("runtime token must not contain whitespace")
	}
	return &handler{
		runtimeIdentity:   identity,
		runtimeGeneration: config.RuntimeGeneration,
		tokenDigest:       sha256.Sum256([]byte(config.Token)),
		start:             config.Start,
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var allowedMethod string
	switch r.URL.Path {
	case handshakePath, statusPath:
		allowedMethod = http.MethodGet
	case startPath:
		allowedMethod = http.MethodPost
	default:
		writeError(w, http.StatusNotFound, "not_found", "runtime endpoint not found")
		return
	}
	if r.Method != allowedMethod {
		w.Header().Set("Allow", allowedMethod)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !h.authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "request is not authorized")
		return
	}

	switch r.URL.Path {
	case handshakePath:
		writeJSON(w, http.StatusOK, handshakeResponse{
			ProtocolVersion:   Version,
			RuntimeIdentity:   h.runtimeIdentity,
			RuntimeGeneration: h.runtimeGeneration,
			Capabilities:      h.capabilities(),
		})
	case statusPath:
		writeJSON(w, http.StatusOK, statusResponse{
			ProtocolVersion:    Version,
			RuntimeIdentity:    h.runtimeIdentity,
			RuntimeGeneration:  h.runtimeGeneration,
			State:              "unknown",
			CPAObservedVersion: "",
			Capabilities:       h.capabilities(),
		})
	case startPath:
		h.handleStart(w, r)
	}
}

func (h *handler) capabilities() []string {
	if h.start != nil {
		return []string{startCapability}
	}
	return []string{}
}

func (h *handler) authorized(authorization string) bool {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	providedDigest := sha256.Sum256([]byte(fields[1]))
	return subtle.ConstantTimeCompare(providedDigest[:], h.tokenDigest[:]) == 1
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, errorResponse{Error: protocolError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
