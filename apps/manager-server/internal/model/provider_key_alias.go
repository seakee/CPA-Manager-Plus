package model

// ProviderKeyAlias names an upstream provider credential without storing the
// credential itself. APIKeyHash is the SHA-256 hash of the upstream key.
type ProviderKeyAlias struct {
	Provider    string `json:"provider"`
	APIKeyHash  string `json:"apiKeyHash"`
	Alias       string `json:"alias"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}
