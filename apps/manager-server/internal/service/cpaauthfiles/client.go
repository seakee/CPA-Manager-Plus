package cpaauthfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const DefaultTimeout = 15 * time.Second

var ErrAuthFileNotFound = errors.New("CPA auth file not found")

var ErrIdentityMismatch = errors.New("CPA auth file identity mismatch")

type Client struct {
	httpClient *http.Client
	timeout    time.Duration
}

type File struct {
	Name            string
	AuthIndex       string
	Provider        string
	AccountSnapshot string
	AccountID       string
	Disabled        bool
	Raw             map[string]any
}

type Identity struct {
	AuthFileName      string
	AuthIndex         string
	Provider          string
	AccountSnapshot   string
	AccountIDSnapshot string
}

func New(client *http.Client, timeout ...time.Duration) *Client {
	if client == nil {
		client = http.DefaultClient
	}
	d := DefaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		d = timeout[0]
	}
	return &Client{httpClient: client, timeout: d}
}

func (c *Client) Fetch(ctx context.Context, baseURL string, managementKey string) ([]File, error) {
	base := cpa.NormalizeBaseURL(baseURL)
	paths := []string{"/auth-files", "/v0/management/auth-files"}
	var endpointErrors []error
	for _, path := range paths {
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+path, nil)
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("GET %s: %w", path, err))
			continue
		}
		req.Header.Set("Authorization", "Bearer "+managementKey)
		res, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("GET %s: %w", path, err))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
		_ = res.Body.Close()
		cancel()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			endpointErrors = append(endpointErrors, fmt.Errorf("GET %s: HTTP %d %s", path, res.StatusCode, strings.TrimSpace(string(body))))
			continue
		}
		files, err := Parse(body)
		if err != nil {
			endpointErrors = append(endpointErrors, fmt.Errorf("GET %s: %w", path, err))
			continue
		}
		return files, nil
	}
	return nil, combineErrors(endpointErrors...)
}

func Parse(body []byte) ([]File, error) {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	files := filesFromJSON(decoded)
	if files == nil {
		return []File{}, nil
	}
	return files, nil
}

func Find(files []File, fileName string, authIndex string) (File, bool) {
	fileName = strings.TrimSpace(fileName)
	authIndex = strings.TrimSpace(authIndex)
	for _, file := range files {
		if file.Name != fileName {
			continue
		}
		if authIndex != "" && file.AuthIndex != authIndex {
			continue
		}
		return file, true
	}
	return File{}, false
}

func VerifyIdentity(files []File, identity Identity) (File, error) {
	file, ok := Find(files, identity.AuthFileName, identity.AuthIndex)
	if !ok {
		return File{}, ErrAuthFileNotFound
	}
	if identity.AccountIDSnapshot != "" && file.AccountID != strings.TrimSpace(identity.AccountIDSnapshot) {
		return File{}, fmt.Errorf("%w: account_id mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountIDSnapshot), file.AccountID)
	}
	if identity.Provider != "" && !strings.EqualFold(file.Provider, strings.TrimSpace(identity.Provider)) {
		return File{}, fmt.Errorf("%w: provider mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.Provider), file.Provider)
	}
	if identity.AccountSnapshot != "" && file.AccountSnapshot != strings.TrimSpace(identity.AccountSnapshot) {
		return File{}, fmt.Errorf("%w: account_snapshot mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountSnapshot), file.AccountSnapshot)
	}
	return file, nil
}

func (c *Client) PatchDisabled(ctx context.Context, baseURL string, managementKey string, fileName string, disabled bool) error {
	payload := map[string]any{"name": fileName, "disabled": disabled}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	base := cpa.NormalizeBaseURL(baseURL)
	paths := []string{"/auth-files", "/auth-files/status", "/v0/management/auth-files", "/v0/management/auth-files/status"}
	var endpointErrors []error
	for _, path := range paths {
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, base+path, bytes.NewReader(data))
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("PATCH %s: %w", path, err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+managementKey)
		res, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("PATCH %s: %w", path, err))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		cancel()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil
		}
		endpointErrors = append(endpointErrors, fmt.Errorf("PATCH %s: HTTP %d %s", path, res.StatusCode, strings.TrimSpace(string(body))))
	}
	return combineErrors(endpointErrors...)
}

func (c *Client) Delete(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	base := cpa.NormalizeBaseURL(baseURL)
	paths := []string{"/auth-files", "/v0/management/auth-files"}
	var endpointErrors []error
	for _, path := range paths {
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		endpoint := base + path + "?name=" + url.QueryEscape(fileName)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("DELETE %s: %w", path, err))
			continue
		}
		req.Header.Set("Authorization", "Bearer "+managementKey)
		res, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			endpointErrors = append(endpointErrors, fmt.Errorf("DELETE %s: %w", path, err))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		cancel()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			if actionFailed(body) {
				endpointErrors = append(endpointErrors, fmt.Errorf("DELETE %s: CPA action failed", path))
				continue
			}
			return nil
		}
		endpointErrors = append(endpointErrors, fmt.Errorf("DELETE %s: HTTP %d %s", path, res.StatusCode, strings.TrimSpace(string(body))))
	}
	return combineErrors(endpointErrors...)
}

func filesFromJSON(value any) []File {
	switch typed := value.(type) {
	case []any:
		files := make([]File, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				files = append(files, FromMap(m))
			}
		}
		return files
	case map[string]any:
		for _, key := range []string{"auth_files", "authFiles", "files", "items", "data"} {
			if child, ok := typed[key]; ok {
				if files := filesFromJSON(child); files != nil {
					return files
				}
			}
		}
		if name := stringField(typed, "name", "file_name", "fileName", "id"); name != "" {
			return []File{FromMap(typed)}
		}
	}
	return nil
}

func FromMap(file map[string]any) File {
	return File{
		Name:            stringField(file, "name", "file_name", "fileName", "id"),
		AuthIndex:       stringField(file, "auth_index", "authIndex", "auth-index"),
		Provider:        strings.ToLower(stringField(file, "provider", "type")),
		AccountSnapshot: stringField(file, "account", "email", "label", "display_account", "displayAccount"),
		AccountID: stringField(
			file,
			"account_id", "accountId", "chatgpt_account_id", "chatgptAccountId",
			"project_id", "projectId", "gemini_virtual_project", "geminiVirtualProject",
			"sub", "id",
		),
		Disabled: disabledField(file),
		Raw:      file,
	}
}

func disabledField(file map[string]any) bool {
	if raw, ok := file["disabled"]; ok {
		switch value := raw.(type) {
		case bool:
			return value
		case json.Number:
			parsed, _ := strconv.ParseFloat(value.String(), 64)
			return parsed != 0
		case float64:
			return value != 0
		case string:
			return strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1"
		}
	}
	status := strings.ToLower(stringField(file, "status", "state"))
	return status == "disabled" || status == "inactive"
}

func stringField(file map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := file[key]; ok && raw != nil {
			value := strings.TrimSpace(fmt.Sprint(raw))
			if value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func actionFailed(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	failed, ok := payload["failed"].([]any)
	return ok && len(failed) > 0
}

func combineErrors(errs ...error) error {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}
