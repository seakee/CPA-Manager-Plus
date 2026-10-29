package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// The usage statistics switch of an External CPA is the one setting this Manager
// may write on a CPA it does not own. It is read and written through the stable
// v0 management endpoints because that is the surface CPA has always exposed for
// exactly this switch; the v8 configuration tree is the user's own document and
// is never rewritten to reach it.
const externalUsageStatisticsPath = "/v0/management/usage-statistics-enabled"

// ErrManagementRejected stays the one typed refusal of a management credential:
// these helpers return it instead of a formatted status so a caller can stop
// collecting rather than report an unknown failure.

// FetchExternalUsageStatistics reports whether the user's CPA records usage. The
// authenticated read is also the first proof that the credential this Manager
// holds still authenticates.
func FetchExternalUsageStatistics(ctx context.Context, baseURL string, key string) (bool, error) {
	if !validManagementKey(key) {
		return false, ErrProofUnavailable
	}
	code, body, err := externalUsageRequest(ctx, http.MethodGet, baseURL, key, nil)
	if err != nil {
		return false, err
	}
	switch code {
	case http.StatusUnauthorized:
		return false, ErrManagementRejected
	case http.StatusForbidden:
		return false, ErrManagementClientForbidden
	case http.StatusOK:
	default:
		return false, ErrProofUnavailable
	}
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil {
		return false, ErrProofUnavailable
	}
	return readBoolField(raw, "usage-statistics-enabled", "usageStatisticsEnabled"), nil
}

// SetExternalUsageStatistics enables or disables usage recording on the user's
// CPA. It is only called after the administrator confirmed the change, and it is
// sent exactly once: a refusal is typed and never retried.
func SetExternalUsageStatistics(ctx context.Context, baseURL string, key string, enabled bool) error {
	payload, err := json.Marshal(map[string]bool{"value": enabled})
	if err != nil {
		return ErrProofUnavailable
	}
	code, _, err := externalUsageRequest(ctx, http.MethodPut, baseURL, key, payload)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusUnauthorized:
		return ErrManagementRejected
	case http.StatusForbidden:
		return ErrManagementClientForbidden
	}
	if code < 200 || code >= 300 {
		return ErrProofUnavailable
	}
	return nil
}

// externalUsageRequest bounds one usage-statistics exchange: no proxy, no
// redirect, no compressed body, and a timeout short enough that a stopped CPA
// cannot hold a startup attempt open.
func externalUsageRequest(ctx context.Context, method, baseURL, key string, payload []byte) (int, []byte, error) {
	endpoint, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return 0, nil, ErrProofUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+externalUsageStatisticsPath, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, ErrProofUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	direct.DisableKeepAlives = true
	direct.MaxResponseHeaderBytes = 32 << 10
	client := &http.Client{
		Transport: direct, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, err
		}
		return 0, nil, ErrProofUnavailable
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return 0, nil, ErrProofUnavailable
	}
	return res.StatusCode, body, nil
}
