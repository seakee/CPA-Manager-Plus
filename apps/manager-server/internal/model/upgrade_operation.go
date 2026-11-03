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
// monotonic. A completed operation never moves again. A failed stage has no
// position of its own, so this method cannot judge a resume: the row's
// high-water mark decides that (see UpgradeOperation.CanLeaveFailedTo).
func (s UpgradeStage) CanAdvanceTo(next UpgradeStage) bool {
	if s == next {
		return true
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
	Stage UpgradeStage `json:"stage"`
	// LastForwardStage is the highest forward stage the operation reached. It
	// equals Stage except while parked in failed, where it keeps the position a
	// resume must not go below. Empty means no forward stage was ever recorded:
	// either a row migrated from a schema without this column that was already
	// failed, or a failure before the first step. Such a row fails closed and
	// cannot resume. The repository owns this field on every write.
	LastForwardStage        UpgradeStage    `json:"lastForwardStage,omitempty"`
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
	// Revision is the row's compare-and-set token. Put only commits when the
	// caller's Revision equals the stored one, and every committed write stores
	// one more, so two writes in the same millisecond stay distinguishable.
	Revision    int64 `json:"revision"`
	CreatedAtMS int64 `json:"createdAtMs"`
	UpdatedAtMS int64 `json:"updatedAtMs"`
}

// Normalize fills the zero values that the schema requires. It never removes,
// reorders or replaces recorded values: unknown or duplicate domains and a
// non-object reconciliation are rejected by Validate, so invalid input cannot
// become acceptable by being normalized.
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

// HighWater is the highest forward stage the operation has reached. A parked
// row keeps it in LastForwardStage; every other row's Stage is that position,
// so a row stored before the column existed still reports its stored stage.
func (o UpgradeOperation) HighWater() UpgradeStage {
	if o.Stage == UpgradeStageFailed {
		return o.LastForwardStage
	}
	return o.Stage
}

// CanLeaveFailedTo reports whether a failed operation may resume at next. It may
// stay failed and it may resume at or above the highest forward stage it
// reached, never below it. A row with no recorded forward stage cannot resume
// at all: its position is unknowable, so leaving failed fails closed instead of
// claiming work that may already have been done.
func (o UpgradeOperation) CanLeaveFailedTo(next UpgradeStage) bool {
	if next == UpgradeStageFailed {
		return true
	}
	if !next.Valid() {
		return false
	}
	high := o.HighWater()
	if high == "" {
		return false
	}
	return next.rank() >= high.rank()
}

func (o UpgradeOperation) Validate() error {
	if !o.Stage.Valid() {
		return fmt.Errorf("upgrade operation stage %q is not supported", o.Stage)
	}
	if o.Stage == UpgradeStageFailed {
		if o.LastForwardStage != "" && (!o.LastForwardStage.Valid() || o.LastForwardStage == UpgradeStageFailed) {
			return fmt.Errorf("upgrade operation last forward stage %q is not a forward stage", o.LastForwardStage)
		}
	} else if o.LastForwardStage != o.Stage {
		return fmt.Errorf("upgrade operation last forward stage %q does not match stage %q", o.LastForwardStage, o.Stage)
	}
	if o.Revision < 0 {
		return errors.New("upgrade operation revision must not be negative")
	}
	if o.CreatedAtMS < 0 || o.UpdatedAtMS < 0 {
		return errors.New("upgrade operation timestamps must not be negative")
	}
	if o.CreatedAtMS > 0 && o.UpdatedAtMS > 0 && o.UpdatedAtMS < o.CreatedAtMS {
		return errors.New("upgrade operation was updated before it was created")
	}
	for index, domain := range o.DomainsDone {
		if !ValidUpgradeDomain(domain) {
			return fmt.Errorf("upgrade operation domain %q is not supported", domain)
		}
		// The domain set is tiny, so a nested scan avoids allocating a set on
		// every read and validation.
		for _, earlier := range o.DomainsDone[:index] {
			if earlier == domain {
				return fmt.Errorf("upgrade operation domain %q is recorded twice", domain)
			}
		}
	}
	if len(o.Reconciliation) > 0 {
		// A comparison into a map accepts JSON null with a nil map and no error,
		// so both the decode error and a nil object are rejected: the field is a
		// JSON object, never null, an array or a scalar.
		var object map[string]json.RawMessage
		if err := json.Unmarshal(o.Reconciliation, &object); err != nil || object == nil {
			return errors.New("upgrade operation reconciliation must be a JSON object")
		}
	}
	return nil
}
