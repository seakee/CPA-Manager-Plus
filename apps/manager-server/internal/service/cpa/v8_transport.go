package cpa

import (
	"context"
	"net/http"
)

// NewDirectClient exposes the management safe-read transport to the packages
// that have to talk to a CPA the operator pointed at.
//
// It is the same construction `directReadClient` builds for every v8 probe and
// adoption read: direct connectivity, no proxy, one fixed DNS resolution, no
// redirect following, no retry, a bounded header/body budget and a five-second
// timeout per call. The caller must invoke the returned function when it is done
// with the client, which closes the idle connections of that resolution.
//
// It lives in this file rather than in v8.go so that publishing this transport
// changes no bytes of the safe-read implementation digest: the transport itself
// is already inside that digest because v8.go defines it, and a second exported
// name for it is not a second implementation.
func NewDirectClient(ctx context.Context, baseURL string) (*http.Client, func(), error) {
	return directReadClient(ctx, baseURL)
}
