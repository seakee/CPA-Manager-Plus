package httpqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var ErrUnsupported = errors.New("http usage queue is unsupported")

// ErrUncertainPop marks a Pop failure where the request may already have
// reached the upstream handler and removed events from the queue.
var ErrUncertainPop = errors.New("usage queue pop outcome is uncertain")

// IsUncertainPop reports whether err came from a Pop attempt that may have
// removed items from the upstream queue before failing.
func IsUncertainPop(err error) bool {
	return errors.Is(err, ErrUncertainPop)
}

// ErrRejected reports a usage queue request the CPA refused as unauthenticated.
// It is the transport-level form of a refused management credential: the server
// answered, so retrying the same request is another failed authentication.
var ErrRejected = errors.New("usage queue rejected the management credential")

type StatusError struct {
	StatusCode int
	Status     string
	Body       string
	Uncertain  bool
}
func (e *StatusError) Error() string {
	if e.Body == "" {
		return "usage queue request failed: " + e.Status
	}
	return "usage queue request failed: " + e.Status + ": " + e.Body
}

// Unwrap makes a refused credential recognizable without matching on the status
// text, so a caller stops collecting instead of retrying.
func (e *StatusError) Unwrap() error {
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return ErrRejected
	}
	if e.Uncertain {
		return ErrUncertainPop
	}
	return nil
}

type Client struct {
	BaseURL       string
	ManagementKey string
	HTTPClient    *http.Client
}

func New(baseURL string, managementKey string) *Client {
	return &Client{
		BaseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		ManagementKey: strings.TrimSpace(managementKey),
		HTTPClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Pop(ctx context.Context, count int) ([]string, error) {
	if count <= 0 {
		count = 1
	}
	endpoint, err := c.endpoint(count)
	if err != nil {
		return nil, err
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var sentOnWire atomic.Bool
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx = httptrace.WithClientTrace(reqCtx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			sentOnWire.Store(true)
		},
		WroteHeaders: func() {
			sentOnWire.Store(true)
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			sentOnWire.Store(true)
		},
	})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.ManagementKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.ManagementKey)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, classifyDoError(ctx != nil && ctx.Err() != nil && !sentOnWire.Load(), sentOnWire.Load(), err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		if isUnsupportedStatus(res.StatusCode) {
			return nil, fmt.Errorf("%w: %s", ErrUnsupported, res.Status)
		}
		uncertain := res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden
		return nil, &StatusError{
			StatusCode: res.StatusCode,
			Status:     res.Status,
			Body:       strings.TrimSpace(string(body)),
			Uncertain:  uncertain,
		}
	}

	var entries []json.RawMessage
	decoder := json.NewDecoder(res.Body)
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUncertainPop, err)
	}

	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrUncertainPop, err)
			}
			if strings.TrimSpace(text) != "" {
				items = append(items, text)
			}
			continue
		}
		if trimmed[0] != '{' {
			return nil, fmt.Errorf("%w: unexpected usage queue item %s", ErrUncertainPop, string(trimmed))
		}
		items = append(items, string(trimmed))
	}
	return items, nil
}

func classifyDoError(ctxCanceledBeforeDo bool, sentOnWire bool, err error) error {
	if err == nil || ctxCanceledBeforeDo || !sentOnWire {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUncertainPop, err)
}

func (c *Client) endpoint(count int) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", errors.New("upstream URL is empty")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	parsed, err := url.Parse(base + "/v0/management/usage-queue")
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("count", strconv.Itoa(count))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func isUnsupportedStatus(status int) bool {
	return status == http.StatusNotFound ||
		status == http.StatusMethodNotAllowed ||
		status == http.StatusNotImplemented
}
