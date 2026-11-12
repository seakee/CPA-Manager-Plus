package cpa

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// ExternalSavedCredentialTarget is the single-snapshot External CPA endpoint
// and management credential resolved under the caller's credential gate right
// before one outbound management request leaves this process.
type ExternalSavedCredentialTarget struct {
	BaseURL       string
	ManagementKey string
}

// ExternalSavedCredentialGate serializes one outbound CPA management request
// that presents the installation's stored External credential, binds the
// refusal check and outbound Authorization header to one store snapshot, and
// records any 401/403 authentication refusal before releasing the gate.
type ExternalSavedCredentialGate func(
	ctx context.Context,
	call func(requestCtx context.Context, target ExternalSavedCredentialTarget) error,
) error

type externalSavedCredentialGateKey struct{}

// WithExternalSavedCredentialGate binds outbound External stored-credential
// CPA management requests on ctx to gate.
func WithExternalSavedCredentialGate(ctx context.Context, gate ExternalSavedCredentialGate) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, externalSavedCredentialGateKey{}, gate)
}

// ExternalSavedCredentialGateFromContext returns the active External
// saved-credential gate bound to ctx, if any.
func ExternalSavedCredentialGateFromContext(ctx context.Context) ExternalSavedCredentialGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(externalSavedCredentialGateKey{}).(ExternalSavedCredentialGate)
	return gate
}

// HasExternalSavedCredentialGate reports whether ctx carries an External
// saved-credential wire gate.
func HasExternalSavedCredentialGate(ctx context.Context) bool {
	return ExternalSavedCredentialGateFromContext(ctx) != nil
}

type externalHeaderObserverKey struct{}
type externalKeepHoldKey struct{}

// ExternalHeaderObserver is invoked as soon as an upstream HTTP response's
// status headers arrive so 401/403 refusals can be recorded and the credential
// gate released before streaming or decoding the response body.
type ExternalHeaderObserver func(statusCode int, releaseGate bool)

// WithExternalHeaderObserver attaches an upstream status-header observer to ctx.
func WithExternalHeaderObserver(ctx context.Context, observer ExternalHeaderObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, externalHeaderObserverKey{}, observer)
}

// WithoutExternalHeaderRelease marks a multi-request snapshot step (such as the
// first GET of ExternalKeySnapshot) so 401/403 refusals are still recorded on
// header receipt while keeping the gate held across a 200 header into the
// companion GET.
func WithoutExternalHeaderRelease(ctx context.Context) context.Context {
	return context.WithValue(ctx, externalKeepHoldKey{}, true)
}

// ObserveExternalResponseStatus notifies any active External header observer
// that upstream HTTP response headers with statusCode have been received.
func ObserveExternalResponseStatus(ctx context.Context, statusCode int) {
	if ctx == nil {
		return
	}
	observer, _ := ctx.Value(externalHeaderObserverKey{}).(ExternalHeaderObserver)
	if observer == nil {
		return
	}
	keepHold, _ := ctx.Value(externalKeepHoldKey{}).(bool)
	observer(statusCode, !keepHold)
}

// TrustedCARoots holds reusable pooled and direct HTTP transports configured
// with a scoped X.509 root pool so self-signed TLS tests never mutate
// http.DefaultTransport or allocate unclosed transports per request.
type TrustedCARoots struct {
	pooled *http.Transport
	direct *http.Transport
}

// NewTrustedCARoots constructs reusable HTTP transports trusting pool.
func NewTrustedCARoots(pool *x509.CertPool) *TrustedCARoots {
	if pool == nil {
		return nil
	}
	build := func(direct bool) *http.Transport {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if direct {
			tr.Proxy = nil
			tr.DisableKeepAlives = true
			tr.MaxResponseHeaderBytes = 32 << 10
		}
		var tlsCfg *tls.Config
		if tr.TLSClientConfig != nil {
			tlsCfg = tr.TLSClientConfig.Clone()
		} else {
			tlsCfg = &tls.Config{}
		}
		tlsCfg.RootCAs = pool
		tr.TLSClientConfig = tlsCfg
		return tr
	}
	return &TrustedCARoots{
		pooled: build(false),
		direct: build(true),
	}
}

// CloseIdleConnections closes any idle connections held by the scoped
// transports.
func (r *TrustedCARoots) CloseIdleConnections() {
	if r == nil {
		return
	}
	if r.pooled != nil {
		r.pooled.CloseIdleConnections()
	}
	if r.direct != nil {
		r.direct.CloseIdleConnections()
	}
}

type trustedCARootsKey struct{}

// WithTrustedCARoots attaches reusable scoped TLS transports for CPA HTTP
// management requests executed on ctx.
func WithTrustedCARoots(ctx context.Context, roots *TrustedCARoots) context.Context {
	if roots == nil {
		return ctx
	}
	return context.WithValue(ctx, trustedCARootsKey{}, roots)
}

func trustedCARootsFromContext(ctx context.Context) *TrustedCARoots {
	if ctx == nil {
		return nil
	}
	roots, _ := ctx.Value(trustedCARootsKey{}).(*TrustedCARoots)
	return roots
}

func managementHTTPClient(ctx context.Context, timeout time.Duration, direct bool) *http.Client {
	roots := trustedCARootsFromContext(ctx)
	if roots != nil {
		tr := roots.pooled
		if direct {
			tr = roots.direct
		}
		client := &http.Client{Transport: tr, Timeout: timeout}
		if direct {
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		return client
	}
	if !direct {
		return &http.Client{Timeout: timeout}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	tr.MaxResponseHeaderBytes = 32 << 10
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (t *Transport) httpClient(ctx context.Context) *http.Client {
	if t == nil || t.client == nil {
		return nil
	}
	roots := trustedCARootsFromContext(ctx)
	if roots == nil {
		return t.client
	}
	clientCopy := *t.client
	clientCopy.Transport = roots.direct
	return &clientCopy
}
