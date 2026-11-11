package proxy

import (
	"context"
	"sync/atomic"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// savedKeyMarkKey is the request-context key of the one fact a gate above this
// proxy cannot read from the path: whether the upstream request actually carried
// the saved management key. The path does not answer it. The same
// `/v0/management/...` prefix is proxied with the saved key for an Admin caller and
// with the caller's own header for an unauthenticated plugin-management caller,
// and the model list forwards the caller's header on a path that otherwise looks
// like a CPA read.
type savedKeyMarkKey struct{}

// SavedKeyMark is that fact. The gate creates it before the proxy call; the
// Director flips it at the moment the saved key is attached. The gate then reads
// what was sent rather than what the path suggests.
type SavedKeyMark struct {
	attached atomic.Bool
}

// Attached reports that the saved management key was set on the upstream request.
func (m *SavedKeyMark) Attached() bool {
	return m != nil && m.attached.Load()
}

// WithSavedKeyMark returns a context carrying a fresh mark together with the mark
// itself, so the caller can hand the context to the proxy and read the signal when
// the answer arrives.
func WithSavedKeyMark(ctx context.Context) (context.Context, *SavedKeyMark) {
	mark := &SavedKeyMark{}
	return context.WithValue(ctx, savedKeyMarkKey{}, mark), mark
}

// NoteSavedManagementKey reports that the request being sent to CPA carries the
// saved management key. It is a no-op for a request without a mark, so the entry
// points that were not gated are unaffected.
func NoteSavedManagementKey(ctx context.Context) {
	if mark, ok := ctx.Value(savedKeyMarkKey{}).(*SavedKeyMark); ok {
		mark.attached.Store(true)
	}
}

type savedSetupOverrideKey struct{}

// WithSavedSetupOverride binds proxy setup resolution on ctx to an explicit
// (upstreamURL, managementKey) snapshot already resolved under an upstream
// credential gate so the proxy Director presents the exact credential snapshot
// that the gate validated and tracks for refusal recording.
func WithSavedSetupOverride(ctx context.Context, upstreamURL, managementKey string) context.Context {
	if upstreamURL == "" || managementKey == "" {
		return ctx
	}
	return context.WithValue(ctx, savedSetupOverrideKey{}, store.Setup{
		CPAUpstreamURL: upstreamURL,
		ManagementKey:  managementKey,
	})
}

func savedSetupOverride(ctx context.Context) (store.Setup, bool) {
	if ctx == nil {
		return store.Setup{}, false
	}
	setup, ok := ctx.Value(savedSetupOverrideKey{}).(store.Setup)
	return setup, ok && setup.CPAUpstreamURL != "" && setup.ManagementKey != ""
}
