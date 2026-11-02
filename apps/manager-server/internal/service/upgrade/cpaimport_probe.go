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
// answers at all, and whether it answers the way a CPA gateway answers an absent
// or unknown credential: a rejection. A silent endpoint is classified as
// unreachable and an endpoint that does not reject is classified as not this
// destination, and neither ever receives the management secret. What the probe
// cannot do is name the destination — only the staged secret does that, which is
// what the authenticated proof reads — so the probe gates the proof instead of
// replacing it, and the URL is recorded as proved only after the proof passes.

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
	// destinationListenerNotCPA means something answered, but not as the CPA
	// gateway this import stages: it either accepted an absent or unknown
	// credential or answered something other than a rejection, so it is not the
	// destination and must never be given this run's credential.
	destinationListenerNotCPA
	// destinationListenerCredentialGatingCPA means every unauthenticated and
	// unknown-credential request was rejected exactly as a CPA rejects them. That
	// is the precondition the authenticated proof may run under; it is not itself
	// proof that this is the staged destination.
	destinationListenerCredentialGatingCPA
)

// probeDestinationListener asks the caller's URL the two questions that need no
// credential: does anything answer, and does it reject an absent and an unknown
// credential.
//
// Both requests are read-only `GET /v1/models`, the same read the authenticated
// proof begins with, over a transport of its own: no proxy, no redirect, no retry
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
