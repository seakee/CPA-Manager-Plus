package cpaidentityinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

var (
	// ErrMalformedAPIKeysResponse indicates that the CPA /v0/management/api-keys response does not match the expected schema.
	ErrMalformedAPIKeysResponse = errors.New("malformed CPA api-keys response")

	// ErrIncompleteCredentialInventory indicates that the CPA auth-files response contains items missing a trusted Auth.ID.
	ErrIncompleteCredentialInventory = errors.New("incomplete CPA credential inventory: missing trusted Auth.ID")
)

const (
	apiKeysPath     = "/v0/management/api-keys"
	defaultTimeout  = 30 * time.Second
	maxAPIKeysBytes = 4 * 1024 * 1024
)

type client struct {
	httpClient      *http.Client
	authFilesClient *cpaauthfiles.Client
	timeout         time.Duration
}

// New creates a new CPA identity inventory client.
func New(httpClient *http.Client, authFilesClient *cpaauthfiles.Client) identityinventory.Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if authFilesClient == nil {
		authFilesClient = cpaauthfiles.New(httpClient)
	}
	return &client{
		httpClient:      httpClient,
		authFilesClient: authFilesClient,
		timeout:         defaultTimeout,
	}
}

// FetchAPIKeys fetches top-level API keys from GET /v0/management/api-keys.
// Raw keys are trimmed, hashed with SHA-256, deduplicated, and returned as safe hashes only.
// Raw keys never cross this function's boundary.
func (c *client) FetchAPIKeys(ctx context.Context, baseURL string, managementKey string) ([]identityinventory.APIKeyObservation, error) {
	base := cpa.NormalizeBaseURL(baseURL)
	reqURL := base + apiKeysPath

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", apiKeysPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", apiKeysPath, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d", apiKeysPath, res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxAPIKeysBytes))
	if err != nil {
		return nil, fmt.Errorf("GET %s: read response: %w", apiKeysPath, err)
	}

	// Strictly decode raw map to verify presence of "api-keys" key and type
	var rawObj map[string]any
	if err := json.Unmarshal(body, &rawObj); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON object: %v", ErrMalformedAPIKeysResponse, err)
	}

	rawKeys, exists := rawObj["api-keys"]
	if !exists {
		return nil, fmt.Errorf("%w: missing 'api-keys' field", ErrMalformedAPIKeysResponse)
	}
	if rawKeys == nil {
		return nil, fmt.Errorf("%w: 'api-keys' is null", ErrMalformedAPIKeysResponse)
	}

	keysSlice, ok := rawKeys.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: 'api-keys' must be a JSON array, got %T", ErrMalformedAPIKeysResponse, rawKeys)
	}

	seen := make(map[string]struct{}, len(keysSlice))
	result := make([]identityinventory.APIKeyObservation, 0, len(keysSlice))

	for idx, item := range keysSlice {
		keyStr, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%w: item at index %d is not a string (type %T)", ErrMalformedAPIKeysResponse, idx, item)
		}
		normalized := strings.TrimSpace(keyStr)
		if normalized == "" {
			continue
		}

		sum := sha256.Sum256([]byte(normalized))
		hash := hex.EncodeToString(sum[:])

		if _, already := seen[hash]; already {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, identityinventory.APIKeyObservation{
			KeyHash: hash,
		})
	}

	return result, nil
}

// FetchCredentials fetches supported credentials from CPA using cpaauthfiles.Client.
// Filters out runtime_only credentials.
// If any non-runtime-only item lacks a trusted Auth.ID, fails closed with ErrIncompleteCredentialInventory.
func (c *client) FetchCredentials(ctx context.Context, baseURL string, managementKey string) ([]identityinventory.CredentialObservation, error) {
	files, err := c.authFilesClient.Fetch(ctx, baseURL, managementKey)
	if err != nil {
		return nil, fmt.Errorf("fetch auth-files: %w", err)
	}

	result := make([]identityinventory.CredentialObservation, 0, len(files))
	seenAuthIDs := make(map[string]struct{}, len(files))

	for _, file := range files {
		// Rule 7: runtime_only / plugin virtual must be excluded from 02A
		if file.RuntimeOnly {
			continue
		}

		authID := strings.TrimSpace(file.ID)
		// Rule 8: disk-only / no Auth.ID fail closed
		if authID == "" {
			return nil, fmt.Errorf("%w: credential %q has empty Auth.ID", ErrIncompleteCredentialInventory, file.Name)
		}

		// Deduplicate if identical Auth.ID appears multiple times
		if _, already := seenAuthIDs[authID]; already {
			continue
		}
		seenAuthIDs[authID] = struct{}{}

		result = append(result, identityinventory.CredentialObservation{
			SourceAuthID:      authID,
			AuthIndex:         file.AuthIndex,
			Provider:          file.Provider,
			PhysicalName:      file.Name,
			AccountSnapshot:   file.AccountSnapshot,
			AccountIDSnapshot: file.AccountID,
			Disabled:          file.Disabled,
		})
	}

	return result, nil
}
