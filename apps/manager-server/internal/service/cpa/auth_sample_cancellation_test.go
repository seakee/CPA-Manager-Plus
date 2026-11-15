package cpa

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cancelAfterInvalidBaselineTransport struct{ cancel context.CancelFunc }

type cancelOnProofBodyClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnProofBodyClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func (rt cancelAfterInvalidBaselineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := io.NopCloser(strings.NewReader("{}"))
	if r.Header.Get("Authorization") == "Bearer ku-invalid-client-key" {
		body = cancelOnProofBodyClose{ReadCloser: body, cancel: rt.cancel}
	}
	return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: body, Request: r}, nil
}

func TestCancelledClientAuthSweepNeverProvesUnprobedKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire := NewTransportForEndpoint("http://127.0.0.1")
	wire.client.Transport = cancelAfterInvalidBaselineTransport{cancel: cancel}
	// Both baseline responses have been consumed successfully when the parent
	// context is cancelled. No worker can certify the three queued client keys.
	proven, err := wire.SampleKeySet(ctx, []string{"candidate-one", "candidate-two", "candidate-three"}, nil)
	if proven || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sweep proved unobserved client credentials: proven=%t, err=%v", proven, err)
	}
}
