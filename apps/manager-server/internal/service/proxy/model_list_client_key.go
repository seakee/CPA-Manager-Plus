package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// modelListClientKeyBudget bounds one server-side model list read. The read is
// made on behalf of a console page that is waiting for it, so a CPA that accepts
// the connection and never answers has to end as an error rather than as a page
// that never renders.
const modelListClientKeyBudget = 15 * time.Second

// modelListClientKeyLimit bounds the read body. A model list is small; a body
// past this is not one, and buffering it would only cost the Manager memory.
const modelListClientKeyLimit = 8 << 20

// modelListClientKeyTransport is the transport of the server-side model list
// read. It is direct on purpose: the read carries a client key the saved CPA
// already holds, and an environment proxy must never be handed that credential.
// It keeps connections open like any other read and adds no retry: one console
// page is one request.
var modelListClientKeyTransport = sync.OnceValue(func() *http.Transport {
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	direct.MaxResponseHeaderBytes = 32 << 10
	return direct
})

// ProxyModelListWithClientKey serves the model list of the CPA this Manager is
// connected to, presenting a client key that CPA already holds.
//
// It exists for the authenticated console bridge. That caller authenticates with
// the Manager Admin credential, and a CPA only accepts its own gateway client
// keys, so a request that forwards the caller's header is answered 401 by the
// CPA. The request built here therefore starts from the CPA address and the
// client key alone: the caller's Authorization, cookies and every other header
// stay behind, and nothing about them reaches the CPA.
//
// A non-200 answer is reported as the typed upstream failure, never as this
// route's 401: the CPA's verdict on the client key says nothing about the
// console session that asked.
func ProxyModelListWithClientKey(
	w http.ResponseWriter,
	r *http.Request,
	cpaBaseURL, clientKey string,
	writeError func(http.ResponseWriter, int, error),
	methodNotAllowed func(http.ResponseWriter),
) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if clientKey == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("saved CPA holds no client key"))
		return
	}
	if cpaBaseURL == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("saved CPA address is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelListClientKeyBudget)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cpaBaseURL, "/")+"/v1/models", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	request.Header.Set("Authorization", "Bearer "+clientKey)
	request.Header.Set("Accept", "application/json")
	// The body is relayed as CPA wrote it, so it must not be decompressed
	// underneath the length the caller receives.
	request.Header.Set("Accept-Encoding", "identity")
	response, err := modelListClientKeyTransport().RoundTrip(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, modelListClientKeyLimit+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if len(body) > modelListClientKeyLimit {
		writeError(w, http.StatusBadGateway, errors.New("cpa model list is larger than the read budget"))
		return
	}
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, fmt.Errorf("cpa model list returned %d", response.StatusCode))
		return
	}
	if !json.Valid(body) {
		writeError(w, http.StatusBadGateway, errors.New("cpa model list is not JSON"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
