package cpa

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ErrConfigChanged requires a fresh read before another explicit edit.
var ErrConfigChanged = errors.New("CPA configuration changed")

// ReadProviderNameDocument keeps the secret-bearing document inside Manager.
// The opaque ETag is CPA's persisted-file revision, never a name or credential ID.
func (t *Transport) ReadProviderNameDocument(ctx context.Context, management string) (map[string]any, string, error) {
	if !validManagementKey(management) {
		return nil, "", ErrProofUnavailable
	}
	code, body, headers, err := t.requestHeaders(ctx, http.MethodGet, "/v8/management/config", management, nil, nil)
	if err != nil || code != http.StatusOK {
		return nil, "", ErrProofUnavailable
	}
	etag := headers.Get("ETag")
	if !validConfigETag(etag) {
		return nil, "", ErrProofUnavailable
	}
	document, err := DecodeConfigDocument(body)
	return document, etag, err
}

// PutProviderNameGroups replaces a single provider array with CPA's atomic
// If-Match guard. No retry: a lost response is an unknown write, resolved by read.
func (t *Transport) PutProviderNameGroups(ctx context.Context, management, provider, etag string, payload []byte) error {
	if !validManagementKey(management) || !validConfigETag(etag) || !ProviderNameKind(provider) {
		return ErrProofUnavailable
	}
	code, body, _, err := t.requestHeaders(ctx, http.MethodPut, "/v8/management/config/api-keys/"+provider, management, payload, http.Header{"If-Match": {etag}})
	if err != nil {
		return ErrApplyUnknown
	}
	if code == http.StatusPreconditionFailed {
		return ErrConfigChanged
	}
	if code == http.StatusOK || code == http.StatusNoContent {
		return nil
	}
	rejection := parseConfigWriteError(code, body)
	if rejection.Definite() {
		return rejection
	}
	return ErrApplyUnknown
}

func validConfigETag(value string) bool {
	return len(value) >= 3 && len(value) <= 130 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && !strings.ContainsAny(value, "\r\n")
}

// ProviderNameKind is deliberately closed; paths never come from arbitrary input.
func ProviderNameKind(value string) bool {
	switch value {
	case "codex", "claude", "gemini", "interactions", "xai", "vertex", "openai-compatibility":
		return true
	}
	return false
}
