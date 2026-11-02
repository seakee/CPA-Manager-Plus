package upgrade

// This file is the unauthenticated half of the destination binding: what may be
// learned about the caller's URL before any credential of this import is sent to
// it.
//
// # Why a separate probe exists
//
// cpa.VerifyV8Adoption is the authenticated proof: it reads the destination's
// configuration with the staged management secret and only then checks that an
// absent or unknown client key is rejected. A transport failure inside that read
// is indistinguishable from a wrong key set when it is collapsed into one
// refusal, so a CPA that is simply not listening yet — the state a just-staged
// destination is in for a moment, and the state of the stopped 1.x CPA the CLI's
// default `CPA_UPSTREAM_URL` usually names — used to be reported as "not the
// destination this operation staged". An operator following that message edits a
// correct URL.
//
// This probe asks only whether anything answers at the URL. It presents no
// credential, so the classification it enables cannot leak one, and it is the
// order the authenticated proof runs in: a silent URL is refused as unreachable
// and a URL that answered is refused as foreign.

import (
	"context"
	"io"
	"net/http"
	"time"
)

const (
	// destinationProbeBudget bounds one unauthenticated probe. It is the budget
	// cpa.VerifyV8Adoption gives its own read, so a URL that is merely slow is
	// classified the same way by both.
	destinationProbeBudget = 5 * time.Second
	// destinationProbeBodyLimit bounds the answer the probe reads but does not
	// interpret, so one endpoint cannot stream an unbounded body at the import.
	destinationProbeBodyLimit = 64 << 10
)

// destinationListener is what one unauthenticated probe of the destination URL
// observed.
type destinationListener int

const (
	// destinationListenerSilent means nothing answered: the connection was
	// refused, the name did not resolve, or no answer arrived within the budget.
	// It is the only outcome that says nothing about whether the URL is right.
	destinationListenerSilent destinationListener = iota
	// destinationListenerAnswered means the endpoint returned an HTTP answer. It
	// may still be a foreign CPA or not a CPA at all; that is settled by the
	// authenticated proof or by the shape probe, never by reachability alone.
	destinationListenerAnswered
)

// probeDestinationListener reports whether anything answers the destination URL
// without presenting a credential.
//
// The request is the same read-only `GET /v1/models` the authenticated proof
// begins with, sent over a transport of its own: no proxy, no redirect, no
// retry, keep-alives off and one five-second budget. It never mutates a CPA.
func probeDestinationListener(ctx context.Context, baseURL string) (destinationListener, error) {
	ctx, cancel := context.WithTimeout(ctx, destinationProbeBudget)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return destinationListenerSilent, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	response, err := destinationProbeClient().Do(request)
	if err != nil {
		return destinationListenerSilent, err
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, destinationProbeBodyLimit))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return destinationListenerSilent, copyErr
	}
	if closeErr != nil {
		return destinationListenerSilent, closeErr
	}
	return destinationListenerAnswered, nil
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
