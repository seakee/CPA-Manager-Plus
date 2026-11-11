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

func externalSavedCredentialGateFromContext(ctx context.Context) ExternalSavedCredentialGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(externalSavedCredentialGateKey{}).(ExternalSavedCredentialGate)
	return gate
}

type trustedCARootsKey struct{}

// WithTrustedCARoots attaches a scoped X.509 root certificate pool for CPA
// HTTP management requests executed on ctx, avoiding process-global mutations
// of http.DefaultTransport in self-signed TLS tests.
func WithTrustedCARoots(ctx context.Context, roots *x509.CertPool) context.Context {
	if roots == nil {
		return ctx
	}
	return context.WithValue(ctx, trustedCARootsKey{}, roots)
}

func trustedCARootsFromContext(ctx context.Context) *x509.CertPool {
	if ctx == nil {
		return nil
	}
	roots, _ := ctx.Value(trustedCARootsKey{}).(*x509.CertPool)
	return roots
}

func managementHTTPClient(ctx context.Context, timeout time.Duration, direct bool) *http.Client {
	roots := trustedCARootsFromContext(ctx)
	if !direct && roots == nil {
		return &http.Client{Timeout: timeout}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if direct {
		tr.Proxy = nil
		tr.DisableKeepAlives = true
		tr.MaxResponseHeaderBytes = 32 << 10
	}
	if roots != nil {
		var tlsCfg *tls.Config
		if tr.TLSClientConfig != nil {
			tlsCfg = tr.TLSClientConfig.Clone()
		} else {
			tlsCfg = &tls.Config{}
		}
		tlsCfg.RootCAs = roots
		tr.TLSClientConfig = tlsCfg
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	if direct {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return client
}

func (t *Transport) httpClient(ctx context.Context) *http.Client {
	if t == nil || t.client == nil {
		return nil
	}
	roots := trustedCARootsFromContext(ctx)
	if roots == nil {
		return t.client
	}
	base, ok := t.client.Transport.(*http.Transport)
	if !ok || base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	cloned := base.Clone()
	var tlsCfg *tls.Config
	if cloned.TLSClientConfig != nil {
		tlsCfg = cloned.TLSClientConfig.Clone()
	} else {
		tlsCfg = &tls.Config{}
	}
	tlsCfg.RootCAs = roots
	cloned.TLSClientConfig = tlsCfg
	clientCopy := *t.client
	clientCopy.Transport = cloned
	return &clientCopy
}
