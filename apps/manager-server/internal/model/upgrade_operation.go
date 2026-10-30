package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// UpgradeStage is the durable position of the single v2 Upgrade operation.
// It records the last domain the operation is known to have passed. It is
// storage vocabulary only: it never authorizes a step, never attests to
// completeness of a later step and never replaces a before/after source
// fingerprint or a backup manifest.
type UpgradeStage string

const (
	UpgradeStagePreflight       UpgradeStage = "preflight"
	UpgradeStageCopyIn          UpgradeStage = "copy_in"
	UpgradeStageReconcile       UpgradeStage = "reconcile"
	UpgradeStageCPAImport       UpgradeStage = "cpa_import"
	UpgradeStageExternalConnect UpgradeStage = "external_connect"
	UpgradeStageVerify          UpgradeStage = "verify"
	UpgradeStageComplete        UpgradeStage = "complete"
	// UpgradeStageRolledBack records that the v2 root was abandoned and the
	// v1 manager was put back in charge. It is a terminal position: a
	// rolled-back operation is never resumed, so it can only be re-entered
	// from itself.
	UpgradeStageRolledBack UpgradeStage = "rolled_back"
	// UpgradeStageFailed parks the operation without losing the domains that
	// already completed. It has no position in the forward order.
	UpgradeStageFailed UpgradeStage = "failed"
)

// upgradeStageRanks is the single forward order of the operation. Failed is
// deliberately absent: it can be entered from any stage and left for any
// stage, and never becomes a checkpoint.
var upgradeStageRanks = map[UpgradeStage]int{
	UpgradeStagePreflight:       1,
	UpgradeStageCopyIn:          2,
	UpgradeStageReconcile:       3,
	UpgradeStageCPAImport:       4,
	UpgradeStageExternalConnect: 5,
	UpgradeStageVerify:          6,
	UpgradeStageComplete:        7,
	UpgradeStageRolledBack:      8,
}

func (s UpgradeStage) Valid() bool {
	if s == UpgradeStageFailed {
		return true
	}
	_, ok := upgradeStageRanks[s]
	return ok
}

func (s UpgradeStage) rank() int { return upgradeStageRanks[s] }

// CanAdvanceTo reports whether moving from s to next keeps the operation
// monotonic. A completed operation never moves again; a failed one may resume
// any stage because it never claimed a position.
func (s UpgradeStage) CanAdvanceTo(next UpgradeStage) bool {
	if s == next {
		return true
	}
	if s == UpgradeStageRolledBack {
		return false
	}
	// A rollback abandons the forward order instead of extending it. It is the
	// one move a completed operation may still make, so the completion barrier
	// below does not apply to it; the caller decides which positions may be
	// abandoned.
	if next == UpgradeStageRolledBack {
		return s.Valid()
	}
	if s == UpgradeStageComplete {
		return false
	}
	if next == UpgradeStageFailed || s == UpgradeStageFailed {
		return true
	}
	if !s.Valid() || !next.Valid() {
		return false
	}
	return next.rank() > s.rank()
}

// Upgrade domain names. A domain is one bounded class of source facts an
// Upgrade step is responsible for; recording it means that class no longer
// needs to be redone, never that the whole operation is verified.
const (
	UpgradeDomainSourceCopy         = "source_copy"
	UpgradeDomainUsageEvents        = "usage_events"
	UpgradeDomainRetainedProjection = "retained_projection"
	UpgradeDomainPricing            = "pricing"
	UpgradeDomainCoverage           = "coverage"
	UpgradeDomainIdentity           = "identity"
	UpgradeDomainCPAConfig          = "cpa_config"
)

var upgradeDomains = map[string]bool{
	UpgradeDomainSourceCopy:         true,
	UpgradeDomainUsageEvents:        true,
	UpgradeDomainRetainedProjection: true,
	UpgradeDomainPricing:            true,
	UpgradeDomainCoverage:           true,
	UpgradeDomainIdentity:           true,
	UpgradeDomainCPAConfig:          true,
}

func ValidUpgradeDomain(domain string) bool { return upgradeDomains[domain] }

// UpgradeOperation is the whole persistent state of the v2 Upgrade operation:
// one row, one operation, no history rows. It carries observations (source
// kind/version/fingerprints, backup manifest, manual items) and never an
// authorization flag.
type UpgradeOperation struct {
	Stage                   UpgradeStage    `json:"stage"`
	DomainsDone             []string        `json:"domainsDone"`
	SourceKind              string          `json:"sourceKind,omitempty"`
	SourceVersion           string          `json:"sourceVersion,omitempty"`
	SourceFingerprintBefore string          `json:"sourceFingerprintBefore,omitempty"`
	SourceFingerprintAfter  string          `json:"sourceFingerprintAfter,omitempty"`
	BackupManifestSHA256    string          `json:"backupManifestSha256,omitempty"`
	Reconciliation          json.RawMessage `json:"reconciliation"`
	ManualItems             []string        `json:"manualItems"`
	ErrorCode               string          `json:"errorCode,omitempty"`
	ErrorDetail             string          `json:"errorDetail,omitempty"`
	CreatedAtMS             int64           `json:"createdAtMs"`
	UpdatedAtMS             int64           `json:"updatedAtMs"`
}

// Normalize fills the zero values that the schema requires and drops duplicate
// domains while keeping first-recorded order.
func (o *UpgradeOperation) Normalize() {
	if o.DomainsDone == nil {
		o.DomainsDone = []string{}
	}
	if o.ManualItems == nil {
		o.ManualItems = []string{}
	}
	if len(o.Reconciliation) == 0 {
		o.Reconciliation = json.RawMessage("{}")
	}
	seen := make(map[string]bool, len(o.DomainsDone))
	unique := o.DomainsDone[:0]
	for _, domain := range o.DomainsDone {
		if seen[domain] {
			continue
		}
		seen[domain] = true
		unique = append(unique, domain)
	}
	o.DomainsDone = unique
}

func (o *UpgradeOperation) HasDomain(domain string) bool {
	for _, done := range o.DomainsDone {
		if done == domain {
			return true
		}
	}
	return false
}

// MarkDomain records a completed domain exactly once. Domains are append-only:
// no path removes one, so a later stage can never make earlier work look
// uncompleted.
func (o *UpgradeOperation) MarkDomain(domain string) {
	if o.HasDomain(domain) {
		return
	}
	o.DomainsDone = append(o.DomainsDone, domain)
}

func (o UpgradeOperation) Validate() error {
	if !o.Stage.Valid() {
		return fmt.Errorf("upgrade operation stage %q is not supported", o.Stage)
	}
	if o.CreatedAtMS < 0 || o.UpdatedAtMS < 0 {
		return errors.New("upgrade operation timestamps must not be negative")
	}
	if o.CreatedAtMS > 0 && o.UpdatedAtMS > 0 && o.UpdatedAtMS < o.CreatedAtMS {
		return errors.New("upgrade operation was updated before it was created")
	}
	seen := make(map[string]bool, len(o.DomainsDone))
	for _, domain := range o.DomainsDone {
		if !ValidUpgradeDomain(domain) {
			return fmt.Errorf("upgrade operation domain %q is not supported", domain)
		}
		if seen[domain] {
			return fmt.Errorf("upgrade operation domain %q is recorded twice", domain)
		}
		seen[domain] = true
	}
	if len(o.Reconciliation) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(o.Reconciliation, &object); err != nil {
			return errors.New("upgrade operation reconciliation must be a JSON object")
		}
	}
	return nil
}
