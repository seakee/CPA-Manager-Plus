package model

type APIKeyAlias struct {
	APIKeyHash    string `json:"apiKeyHash"`
	Alias         string `json:"alias"`
	UpdatedAtMS   int64  `json:"updatedAtMs"`
	APIKeyID      string `json:"apiKeyId,omitempty"`
	MaskedPreview string `json:"maskedPreview,omitempty"`
	Origin        string `json:"origin,omitempty"`
	Lifecycle     string `json:"lifecycle,omitempty"`
}
