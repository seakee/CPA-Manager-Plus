package model

// CredentialLabel is Manager-owned display metadata. It never contains a raw API key.
type CredentialLabel struct {
	KeyHash     string `json:"keyHash"`
	Label       string `json:"label"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}
