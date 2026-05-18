package model

type APIKeyAlias struct {
	APIKeyHash  string `json:"apiKeyHash"`
	Alias       string `json:"alias"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}
