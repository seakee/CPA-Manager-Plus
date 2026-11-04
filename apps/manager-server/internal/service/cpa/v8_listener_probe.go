package cpa

// This file is the credential-free half of "may this URL be addressed with a
// secret": what can be learned about a base URL before any credential is sent.
//
// # Why it shares the safe-read transport
//
// A gate, a proof and a classification that resolve the same name differently are
// worse than no gate: a dual-stack name whose first address is dead is answered by
// the gate — the default dialer falls back to the second address — refused by the
// proof, which pins the first, and then answered again by the classification, so a
// correct destination is reported as a foreign one. VerifyV8Adoption dials
// `addresses[0]` of one resolution through directReadClient, so this probe builds
// its client with NewDirectClient, the same constructor: one resolution per client,
// a direct connection, no proxy, no redirect, no retry, a bounded header/body
// budget and five seconds per call. A name is therefore either answered on the
// address both use or answered nothing, and "nothing" is reported as nothing.
//
// # What the probe can and cannot prove
//
// The probe presents no credential of the caller. It asks whether something answers
// at all, and whether both an absent and an unknown credential are answered with
// 401. Any endpoint that answers 401 to both passes: an nginx basic auth front end,
// an OpenAI-compatible gateway with its own admission, or another CPA. The probe
// therefore proves "this endpoint enforces some credential", never "this endpoint is
// a CPA", and the authenticated read is what names a destination.
//
// The CPA v8 rejection body was checked before deciding whether the probe could
// narrow that, and it cannot. In v8.0.13 (tag d7914af, the release
// internal/service/cpa/config_schema.go pins) the standard `v1` group — which
// carries GET /v1/models (internal/api/server_routes.go:64-66) — is guarded by
// AuthMiddleware, i.e. accessAuthMiddleware with realtimeError=false
// (internal/api/server_middleware.go:151-159), and that branch writes the message
// as a plain string: `c.AbortWithStatusJSON(statusCode, gin.H{"error": err.Message})`
// (internal/api/server_middleware.go:198). The typed codes
// AuthErrorCodeNoCredentials ("no_credentials") and AuthErrorCodeInvalidCredential
// ("invalid_credential") exist in sdk/access/errors.go:13-14,65-71 but never reach
// that body; the only typed `error.type` the authentication middleware emits is on
// the realtime route, and it is "authentication_error" / "server_error"
// (internal/api/server_middleware.go:183-197). A body-type check would therefore
// refuse every real CPA, so the probe stays a status check.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

const (
	// v8ProbeBudget bounds one probe request, matching the budget the
	// authenticated read gives its own requests so a URL that is merely slow is
	// classified the same way by both.
	v8ProbeBudget = 5 * time.Second
	// v8ProbeBodyLimit bounds the answer the probe reads but does not interpret,
	// so one endpoint cannot stream an unbounded body at the caller.
	v8ProbeBodyLimit = 64 << 10
	// v8ProbeUnknownCredentialPrefix marks the random value the probe presents to
	// learn whether the endpoint rejects credentials it does not know. The value is
	// generated per probe and reveals nothing.
	v8ProbeUnknownCredentialPrefix = "upgrade-cpa-import-probe-"
)

// V8Listener is what the credential-free probe of a base URL observed.
type V8Listener int

const (
	// V8ListenerSilent means nothing answered: the connection was refused, the name
	// did not resolve, or no answer arrived within the budget. It is the only
	// outcome that says nothing about whether the URL is right.
	V8ListenerSilent V8Listener = iota
	// V8ListenerNotCredentialGated means something answered, but not with a
	// rejection of both an absent and an unknown credential: it accepted one of
	// them or answered another status, so it does not gate credentials at all and
	// must never be given a secret. The caller's refusal treats it as not the
	// destination it stages; that is the outcome it can act on, and it does not
	// claim the endpoint is a CPA.
	V8ListenerNotCredentialGated
	// V8ListenerCredentialGated means every unauthenticated and unknown-credential
	// request was answered with 401. Any endpoint that rejects both credentials this
	// way lands here — a CPA, but also a reverse proxy with basic auth — so this is
	// the precondition an authenticated read may run under, not proof that this is a
	// CPA, let alone the destination in question.
	V8ListenerCredentialGated
)

// ProbeV8Listener asks baseURL the two questions that need no credential: does
// anything answer, and is both an absent and an unknown credential answered with
// 401.
//
// Both requests are read-only `GET /v1/models`, the same read an adoption proof
// begins with, over one client built by NewDirectClient and therefore one fixed
// resolution for the whole probe. It never mutates its target.
func ProbeV8Listener(ctx context.Context, baseURL string) (V8Listener, error) {
	baseURL, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return V8ListenerSilent, err
	}
	client, closeClient, err := NewDirectClient(ctx, baseURL)
	if err != nil {
		return V8ListenerSilent, err
	}
	defer closeClient()
	status, err := probeV8ModelsStatus(ctx, client, baseURL, "")
	if err != nil {
		return V8ListenerSilent, err
	}
	if status != http.StatusUnauthorized {
		return V8ListenerNotCredentialGated, nil
	}
	nonce, err := newV8ProbeNonce()
	if err != nil {
		return V8ListenerSilent, err
	}
	status, err = probeV8ModelsStatus(ctx, client, baseURL, nonce)
	if err != nil {
		return V8ListenerSilent, err
	}
	if status != http.StatusUnauthorized {
		return V8ListenerNotCredentialGated, nil
	}
	return V8ListenerCredentialGated, nil
}

// probeV8ModelsStatus performs one `GET /v1/models` with the given credential and
// returns the status it answered. An empty credential sends no Authorization
// header at all.
func probeV8ModelsStatus(ctx context.Context, client *http.Client, baseURL, credential string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, v8ProbeBudget)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, v8ProbeBodyLimit))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return response.StatusCode, nil
}

// newV8ProbeNonce returns a fresh, valueless credential for the
// unknown-credential question. It is never persisted and never derived from a
// caller's secrets.
func newV8ProbeNonce() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return v8ProbeUnknownCredentialPrefix + hex.EncodeToString(buffer), nil
}
