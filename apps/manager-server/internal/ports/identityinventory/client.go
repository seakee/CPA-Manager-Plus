package identityinventory

import "context"

// APIKeyObservation contains only the safe hashed representation of an observed API key.
// Raw API keys are strictly forbidden from crossing this port boundary.
type APIKeyObservation struct {
	KeyHash string // 64-char lowercase hex SHA-256(TrimSpace(raw))
}

// CredentialObservation contains observed metadata for a supported credential.
type CredentialObservation struct {
	SourceAuthID      string // CPA Auth.ID (guaranteed non-empty and trimmed)
	AuthIndex         string
	Provider          string
	PhysicalName      string
	AccountSnapshot   string
	AccountIDSnapshot string
	Disabled          bool
}

// Client defines the inventory observation boundary for CPA.
type Client interface {
	// FetchAPIKeys fetches top-level API keys from CPA and returns only normalized safe hashes.
	// Returns error if response is malformed, unavailable, or fails validation.
	FetchAPIKeys(ctx context.Context, baseURL string, managementKey string) ([]APIKeyObservation, error)

	// FetchCredentials fetches supported credentials from CPA.
	// Excludes runtime_only credentials.
	// Returns error if the inventory is incomplete (e.g. disk-only or missing Auth.ID) or unavailable.
	FetchCredentials(ctx context.Context, baseURL string, managementKey string) ([]CredentialObservation, error)
}
