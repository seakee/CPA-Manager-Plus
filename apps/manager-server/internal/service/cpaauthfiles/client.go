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

const DefaultTimeout = 30 * time.Second

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

const authFilesPath = "/v0/management/auth-files"
const authFilesStatusPath = "/v0/management/auth-files/status"

func (c *Client) Fetch(ctx context.Context, baseURL string, managementKey string) ([]File, error) {
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+authFilesPath, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d %s", authFilesPath, res.StatusCode, strings.TrimSpace(string(body)))
	}
	files, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	return files, nil
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
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, base+authFilesStatusPath, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesStatusPath, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesStatusPath, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("PATCH %s: HTTP %d %s", authFilesStatusPath, res.StatusCode, strings.TrimSpace(string(body)))
}

func (c *Client) Delete(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	endpoint := base + authFilesPath + "?name=" + url.QueryEscape(fileName)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", authFilesPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", authFilesPath, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if actionFailed(body) {
			return fmt.Errorf("DELETE %s: CPA action failed", authFilesPath)
		}
		return nil
	}
	return fmt.Errorf("DELETE %s: HTTP %d %s", authFilesPath, res.StatusCode, strings.TrimSpace(string(body)))
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
