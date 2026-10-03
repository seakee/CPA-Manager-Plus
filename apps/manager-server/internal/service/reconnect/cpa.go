package reconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

// authFile is the part of a CPA auth file this feature needs.
type authFile struct {
	Name          string
	Provider      string
	Email         string
	Status        string
	StatusMessage string
	Disabled      bool
	UpdatedAt     time.Time
}

func rawString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func toAuthFile(f cpaauthfiles.File) authFile {
	providerID := strings.ToLower(rawString(f.Raw, "provider", "type"))
	if providerID == "" {
		providerID = strings.ToLower(f.Provider)
	}
	out := authFile{
		Name:          f.Name,
		Provider:      providerID,
		Email:         strings.ToLower(rawString(f.Raw, "email")),
		Status:        rawString(f.Raw, "status"),
		StatusMessage: rawString(f.Raw, "status_message"),
		Disabled:      f.Disabled,
	}
	if ts := rawString(f.Raw, "updated_at", "modtime"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			out.UpdatedAt = t
		}
	}
	return out
}

// needsReauth reports whether a credential can only recover through a new
// login. Rate limits, Cloudflare challenges and transient refresh errors
// recover on their own and are not matched.
func needsReauth(f authFile) (bool, string) {
	if !IsProvider(f.Provider) || f.Disabled || strings.EqualFold(f.Status, "disabled") {
		return false, ""
	}
	msg := strings.ToLower(f.StatusMessage)
	if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "invalid_grant") || strings.Contains(msg, "invalid grant") {
		return true, f.StatusMessage
	}
	return false, ""
}

// cpaClient wraps the CPA management calls the reconnect flow uses.
type cpaClient struct {
	base, key   string
	http        *http.Client
	authFiles   *cpaauthfiles.Client
	coordinator *cpaauthfiles.MutationCoordinator
}

type cpaError struct {
	StatusCode int
	Message    string
}

func (e *cpaError) Error() string { return fmt.Sprintf("CPA HTTP %d: %s", e.StatusCode, e.Message) }

func (c *cpaClient) listAuthFiles(ctx context.Context) ([]authFile, error) {
	files, err := c.authFiles.Fetch(ctx, c.base, c.key)
	if err != nil {
		return nil, err
	}
	out := make([]authFile, 0, len(files))
	for _, f := range files {
		out = append(out, toAuthFile(f))
	}
	return out, nil
}

func (c *cpaClient) deleteAuthFile(ctx context.Context, name string) error {
	if c.coordinator != nil {
		release, err := c.coordinator.Acquire(ctx, name)
		if err != nil {
			return err
		}
		defer release()
	}
	return c.authFiles.Delete(ctx, c.base, c.key, name)
}

type loginStart struct {
	URL       string `json:"url"`
	State     string `json:"state"`
	Flow      string `json:"flow"`
	UserCode  string `json:"user_code"`
	ExpiresIn int    `json:"expires_in"`
}

func (c *cpaClient) startLogin(ctx context.Context, path string) (*loginStart, error) {
	var out loginStart
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.URL == "" || out.State == "" {
		return nil, errors.New("CPA " + path + " returned no url/state")
	}
	return &out, nil
}

func (c *cpaClient) submitCallback(ctx context.Context, providerName, redirectURL string) error {
	return c.do(ctx, http.MethodPost, "/oauth-callback", map[string]string{"provider": providerName, "redirect_url": redirectURL}, nil)
}

type authStatus struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

func (c *cpaClient) authStatus(ctx context.Context, state string) (*authStatus, error) {
	var out authStatus
	if err := c.do(ctx, http.MethodGet, "/get-auth-status?state="+url.QueryEscape(state), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *cpaClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, cpa.NormalizeBaseURL(c.base)+"/v0/management"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &cpaError{StatusCode: resp.StatusCode, Message: e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
