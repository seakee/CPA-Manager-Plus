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

// RollbackEvidence is the observation a rolled_back verdict rests on. It is
// written together with the stage, so a later reader can see what was observed
// instead of only learning that someone advanced the row. The checks that
// produced it are not recorded here: they are re-derived when the verdict is
// written, and this value is the report of that run.
type RollbackEvidence struct {
	// Image is the exact 1.x image the operator started.
	Image string `json:"image"`
	// StatusURL is the 1.x health endpoint that answered for this install.
	StatusURL string `json:"statusUrl"`
	// SourceFingerprint is the fingerprint observed on the live 1.x volume. It
	// is informational: a v1 that kept serving writes new rows, so equality with
	// the recorded before-fingerprint is not required and never claimed.
	SourceFingerprint string `json:"sourceFingerprint"`
	// ObservedAtMS is when the observation was taken.
	ObservedAtMS int64 `json:"observedAtMs"`
}

// Complete reports whether the evidence names everything a verdict rests on.
// The fields are recorded whole or not at all, so an incomplete value describes
// only the shape of an observation: a reader can neither re-check it nor name
// the moment it belonged to.
func (e RollbackEvidence) Complete() bool {
	return e.Image != "" && e.StatusURL != "" && e.SourceFingerprint != "" && e.ObservedAtMS > 0
}

// UpgradeOperation is the whole persistent state of the v2 Upgrade operation:
// one row, one operation, no history rows. It carries observations (source
// kind/version/fingerprints, backup manifest, manual items) and never an
// authorization flag.
type UpgradeOperation struct {
	Stage         UpgradeStage `json:"stage"`
	DomainsDone   []string     `json:"domainsDone"`
	SourceKind    string       `json:"sourceKind,omitempty"`
	SourceVersion string       `json:"sourceVersion,omitempty"`
	// SourceRoot is the 1.x data directory the copy-in read. A rollback binds
	// the volume it restarts to this directory, so "the original volume" is a
	// checkable claim rather than the operator's assertion.
	SourceRoot              string          `json:"sourceRoot,omitempty"`
	SourceFingerprintBefore string          `json:"sourceFingerprintBefore,omitempty"`
	SourceFingerprintAfter  string          `json:"sourceFingerprintAfter,omitempty"`
	BackupManifestSHA256    string          `json:"backupManifestSha256,omitempty"`
	Reconciliation          json.RawMessage `json:"reconciliation"`
	ManualItems             []string        `json:"manualItems"`
	ErrorCode               string          `json:"errorCode,omitempty"`
	ErrorDetail             string          `json:"errorDetail,omitempty"`
	// Rollback is the evidence recorded with the rolled_back verdict, and nil
	// for every other stage.
	Rollback    *RollbackEvidence `json:"rollback,omitempty"`
	CreatedAtMS int64             `json:"createdAtMs"`
	UpdatedAtMS int64             `json:"updatedAtMs"`
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
	// A rolled_back operation is terminal, so it can never be re-proven. It must
	// carry the observation that justified it: a terminal verdict without
	// evidence is a claim nobody can check, and no path may write one.
	if o.Stage == UpgradeStageRolledBack && o.Rollback == nil {
		return errors.New("a rolled_back upgrade operation must carry the evidence that justified it")
	}
	// Evidence is written whole or not at all. A partial record would let a
	// reader believe a verdict was observed when only its shape survived.
	if o.Rollback != nil && !o.Rollback.Complete() {
		return errors.New("upgrade operation rollback evidence must name the image, the health endpoint, the observed fingerprint and when it was observed")
	}
	return nil
}
