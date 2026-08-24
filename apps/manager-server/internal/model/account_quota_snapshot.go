package model

// AccountQuotaSnapshot is one allowlisted quota observation for a canonical
// credential and provider window. Provider response bodies are deliberately
// not persisted here.
type AccountQuotaSnapshot struct {
	ID                    int64
	ObservationID         int64
	LogicalWindowID       int64
	ActivationID          int64
	CycleID               int64
	AccountKey            string
	Provider              string
	ProviderWindowID      string
	ProviderWindowAliases []string
	WindowKind            string
	WindowMode            string
	ModelScopeKind        string
	ModelScopeKey         string
	ModelIDsJSON          string
	ScopeFingerprint      string
	ContentHash           string
	InventoryScopeKey     string
	RelationshipKind      string
	ContainerWindowID     string
	Source                string
	SourceObservationID   string
	ObservedAtMS          int64
	BoundaryAccuracy      string
	CycleStartMS          *int64
	CycleEndMS            *int64
	DurationSeconds       *int64
	UsedPercent           *float64
	RemainingPercent      *float64
	UsedValue             *float64
	LimitValue            *float64
	QuotaUnit             string
	ResetCreditsAvailable *int64
	ResetCreditsJSON      string
	PlanType              string
	CreatedAtMS           int64
}
