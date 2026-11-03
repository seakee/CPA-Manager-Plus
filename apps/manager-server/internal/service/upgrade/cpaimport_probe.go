package upgrade

// This file is the unauthenticated half of the destination binding: what may be
// learned about the caller's URL — and what must be true of it — before the
// secret this run minted is sent anywhere.
//
// # Why a separate probe exists
//
// cpa.VerifyV8Adoption is the authenticated proof: it reads the destination's
// configuration with the staged management secret and only then checks that an
// absent or unknown client key is rejected. Two problems follow from its order
// and its verdicts when it is the first thing a run does:
//
//   - the secret is presented before anything is known about the URL, and the
//     caller supplies that URL;
//   - a transport failure inside the read is collapsed into one refusal, so a CPA
//     that is simply not listening yet — the state a just-staged destination is in
//     for a moment, and the state of the stopped 1.x CPA the CLI's default
//     `CPA_UPSTREAM_URL` usually names — used to be reported as "not the
//     destination this operation staged".
//
// # What the probe can and cannot prove
//
// The probe presents no credential of this import. It asks whether something
// answers at all, and whether both an absent and an unknown credential are
// answered with 401. Any endpoint that answers 401 to both passes: an nginx basic
// auth front end, an OpenAI-compatible gateway with its own admission, or another
// CPA. The probe therefore proves "this endpoint enforces some credential", never
// "this endpoint is a CPA", and the authenticated readback — not the probe — is
// what names the destination.
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
// that body; the only typed `error.type` the server emits is on the realtime route,
// and it is "authentication_error" / "server_error"
// (internal/api/server_middleware.go:183-197). A body-type check would therefore
// refuse every real CPA, so the gate stays a status check.
//
// What follows from that: a silent endpoint is classified as unreachable and an
// endpoint that does not reject is classified as not this destination, and neither
// ever receives the management secret. The secret is what the probe cannot
// replace, which is why the proof runs only behind it and the URL is recorded as
// proved only after the proof passes.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

// destinationProbeUnknownCredentialPrefix marks the random value the probe
// presents to learn whether the endpoint rejects credentials it does not know. The
// value is generated per probe and reveals nothing.
const destinationProbeUnknownCredentialPrefix = "upgrade-cpa-import-probe-"

const (
	// destinationProbeBudget bounds one probe request. It is the budget
	// cpa.VerifyV8Adoption gives its own read, so a URL that is merely slow is
	// classified the same way by both.
	destinationProbeBudget = 5 * time.Second
	// destinationProbeBodyLimit bounds the answer the probe reads but does not
	// interpret, so one endpoint cannot stream an unbounded body at the import.
	destinationProbeBodyLimit = 64 << 10
)

// destinationListener is what the unauthenticated probe of the destination URL
// observed.
type destinationListener int

const (
	// destinationListenerSilent means nothing answered: the connection was
	// refused, the name did not resolve, or no answer arrived within the budget.
	// It is the only outcome that says nothing about whether the URL is right.
	destinationListenerSilent destinationListener = iota
	// destinationListenerNotCPA means something answered, but not with a rejection
	// of both an absent and an unknown credential: it either accepted one of them
	// or answered another status, so it is not a credential-gating endpoint at all
	// and must never be given this run's credential. The next step calls it a
	// foreign target because that is the refusal the import can act on; it does not
	// claim the endpoint is a CPA.
	destinationListenerNotCPA
	// destinationListenerCredentialGatingCPA means every unauthenticated and
	// unknown-credential request was answered with 401. Any endpoint that rejects
	// both credentials this way lands here — a CPA, but also a reverse proxy with
	// basic auth — so this is the precondition the authenticated proof may run
	// under and not proof that this is a CPA, let alone the staged destination.
	destinationListenerCredentialGatingCPA
)

// probeDestinationListener asks the caller's URL the two questions that need no
// credential: does anything answer, and does it answer both an absent and an
// unknown credential with 401.
//
// Both requests are read-only `GET /v1/models`, the same read the authenticated
// proof begins with, over the direct transport: no proxy, no redirect, no retry
// and one five-second budget per request. It never mutates a CPA.
func probeDestinationListener(ctx context.Context, baseURL string) (destinationListener, error) {
	status, err := probeModelsStatus(ctx, baseURL, "")
	if err != nil {
		return destinationListenerSilent, err
	}
	if status != http.StatusUnauthorized {
		return destinationListenerNotCPA, nil
	}
	nonce, err := newDestinationProbeNonce()
	if err != nil {
		return destinationListenerSilent, err
	}
	status, err = probeModelsStatus(ctx, baseURL, nonce)
	if err != nil {
		return destinationListenerSilent, err
	}
	if status != http.StatusUnauthorized {
		return destinationListenerNotCPA, nil
	}
	return destinationListenerCredentialGatingCPA, nil
}

// probeModelsStatus performs one `GET /v1/models` with the given credential and
// returns the status it answered. An empty credential sends no Authorization
// header at all.
func probeModelsStatus(ctx context.Context, baseURL, credential string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, destinationProbeBudget)
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
	response, err := destinationProbeClient().Do(request)
	if err != nil {
		return 0, err
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, destinationProbeBodyLimit))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return response.StatusCode, nil
}

// newDestinationProbeNonce returns a fresh, valueless credential for the
// unknown-credential question. It is never persisted and never derived from this
// import's secrets.
func newDestinationProbeNonce() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return destinationProbeUnknownCredentialPrefix + hex.EncodeToString(buffer), nil
}

// destinationProbeClient is the probe's transport: the same direct, bounded shape
// the management safe read uses, so a proxy or a redirect cannot turn a wrong
// answer into a right one.
func destinationProbeClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 32 << 10
	return &http.Client{
		Transport: transport,
		Timeout:   destinationProbeBudget,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
