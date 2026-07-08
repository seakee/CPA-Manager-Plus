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

func authFilesEndpoint(baseURL string, fileName string, authIndex string) string {
	endpoint := baseURL + authFilesPath
	query := url.Values{}
	if fileName = strings.TrimSpace(fileName); fileName != "" {
		query.Set("name", fileName)
	}
	if authIndex = strings.TrimSpace(authIndex); authIndex != "" {
		query.Set("auth_index", authIndex)
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return endpoint
}

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
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("GET %s: HTTP %d %s", authFilesPath, res.StatusCode, strings.TrimSpace(string(body)))
	}
	files := make([]File, 0)
	if err := scanFiles(res.Body, func(file File) (bool, error) {
		files = append(files, file)
		return false, nil
	}); err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	return files, nil
}

func (c *Client) Find(ctx context.Context, baseURL string, managementKey string, fileName string, authIndex string) (File, bool, error) {
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, authFilesEndpoint(base, fileName, authIndex), nil)
	if err != nil {
		return File{}, false, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return File{}, false, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return File{}, false, fmt.Errorf("GET %s: HTTP %d %s", authFilesPath, res.StatusCode, strings.TrimSpace(string(body)))
	}

	var matched File
	found := false
	err = scanFiles(res.Body, func(file File) (bool, error) {
		if !matches(file, fileName, authIndex) {
			return false, nil
		}
		matched = file
		found = true
		return true, nil
	})
	if err != nil {
		return File{}, false, fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	return matched, found, nil
}

func (c *Client) Verify(ctx context.Context, baseURL string, managementKey string, identity Identity) (File, error) {
	file, ok, err := c.Find(ctx, baseURL, managementKey, identity.AuthFileName, identity.AuthIndex)
	if err != nil {
		return File{}, err
	}
	if !ok {
		return File{}, ErrAuthFileNotFound
	}
	if err := verifyFileIdentity(file, identity); err != nil {
		return File{}, err
	}
	return file, nil
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

func scanFiles(body io.Reader, visit func(File) (bool, error)) error {
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("expected JSON object or array")
	}
	switch delimiter {
	case '[':
		_, err := scanFileArray(decoder, visit)
		return err
	case '{':
		raw := make(map[string]any)
		scannedList := false
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected object key")
			}
			if !isAuthFilesListKey(key) {
				var value any
				if err := decoder.Decode(&value); err != nil {
					return err
				}
				raw[key] = value
				continue
			}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			valueDelimiter, ok := valueToken.(json.Delim)
			if !ok || valueDelimiter != '[' {
				value, err := decodeValueAfterToken(decoder, valueToken)
				if err != nil {
					return err
				}
				raw[key] = value
				continue
			}
			scannedList = true
			stopped, err := scanFileArray(decoder, visit)
			if err != nil || stopped {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if !scannedList && stringField(raw, "name", "file_name", "fileName", "id") != "" {
			_, err := visit(FromMap(raw))
			return err
		}
		return nil
	default:
		return fmt.Errorf("expected JSON object or array")
	}
}

func decodeValueAfterToken(decoder *json.Decoder, token json.Token) (any, error) {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		value := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("expected object key")
			}
			var child any
			if err := decoder.Decode(&child); err != nil {
				return nil, err
			}
			value[key] = child
		}
		_, err := decoder.Token()
		return value, err
	case '[':
		value := make([]any, 0)
		for decoder.More() {
			var child any
			if err := decoder.Decode(&child); err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		_, err := decoder.Token()
		return value, err
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func scanFileArray(decoder *json.Decoder, visit func(File) (bool, error)) (bool, error) {
	for decoder.More() {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			return false, err
		}
		if len(raw) == 0 {
			continue
		}
		stop, err := visit(FromMap(raw))
		if err != nil || stop {
			return stop, err
		}
	}
	_, err := decoder.Token()
	return false, err
}

func isAuthFilesListKey(key string) bool {
	switch key {
	case "auth_files", "authFiles", "files", "items", "data":
		return true
	default:
		return false
	}
}

func Find(files []File, fileName string, authIndex string) (File, bool) {
	fileName = strings.TrimSpace(fileName)
	authIndex = strings.TrimSpace(authIndex)
	for _, file := range files {
		if matches(file, fileName, authIndex) {
			return file, true
		}
	}
	return File{}, false
}

func matches(file File, fileName string, authIndex string) bool {
	fileName = strings.TrimSpace(fileName)
	authIndex = strings.TrimSpace(authIndex)
	if fileName != "" && file.Name != fileName {
		return false
	}
	if authIndex != "" && file.AuthIndex != authIndex {
		return false
	}
	return true
}

func VerifyIdentity(files []File, identity Identity) (File, error) {
	file, ok := Find(files, identity.AuthFileName, identity.AuthIndex)
	if !ok {
		return File{}, ErrAuthFileNotFound
	}
	if err := verifyFileIdentity(file, identity); err != nil {
		return File{}, err
	}
	return file, nil
}

func verifyFileIdentity(file File, identity Identity) error {
	if identity.AccountIDSnapshot != "" && file.AccountID != strings.TrimSpace(identity.AccountIDSnapshot) {
		return fmt.Errorf("%w: account_id mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountIDSnapshot), file.AccountID)
	}
	if identity.Provider != "" && !strings.EqualFold(file.Provider, strings.TrimSpace(identity.Provider)) {
		return fmt.Errorf("%w: provider mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.Provider), file.Provider)
	}
	if identity.AccountSnapshot != "" && file.AccountSnapshot != strings.TrimSpace(identity.AccountSnapshot) {
		return fmt.Errorf("%w: account_snapshot mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountSnapshot), file.AccountSnapshot)
	}
	return nil
}

func (c *Client) PatchDisabled(ctx context.Context, baseURL string, managementKey string, fileName string, disabled bool, authIndex ...string) error {
	payload := map[string]any{"name": fileName, "disabled": disabled}
	if len(authIndex) > 0 {
		if trimmed := strings.TrimSpace(authIndex[0]); trimmed != "" {
			payload["auth_index"] = trimmed
		}
	}
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
	if failed, ok := payload["failed"].([]any); ok && len(failed) > 0 {
		return true
	}
	if ok, _ := payload["success"].(bool); ok {
		return false
	}
	if ok, _ := payload["ok"].(bool); ok {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["status"])))
	return status == "error" || status == "failed"
}
