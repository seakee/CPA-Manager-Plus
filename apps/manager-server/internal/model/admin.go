package model

type AdminCredential struct {
	Version     int    `json:"version"`
	Salt        string `json:"salt"`
	KeyHash     string `json:"keyHash"`
	Iterations  int    `json:"iterations"`
	CreatedAtMS int64  `json:"createdAtMs"`
	RotatedAtMS int64  `json:"rotatedAtMs,omitempty"`
	Source      string `json:"source,omitempty"`
}

type BootstrapState struct {
	Version            int    `json:"version"`
	Status             string `json:"status"`
	AdminReady         bool   `json:"adminReady"`
	ProjectInitialized bool   `json:"projectInitialized"`
	DataKeyReady       bool   `json:"dataKeyReady"`
	MigratedLegacy     bool   `json:"migratedLegacy"`
	HasHistoricalData  bool   `json:"hasHistoricalData"`
	UpdatedAtMS        int64  `json:"updatedAtMs"`
	// ConnectionStorageMigrationVersion tracks the connection-storage
	// normalization migration independently of MigratedLegacy, because older
	// releases already set MigratedLegacy=true without performing this
	// normalization. States persisted before the field existed decode as 0.
	ConnectionStorageMigrationVersion int `json:"connectionStorageMigrationVersion,omitempty"`
}
