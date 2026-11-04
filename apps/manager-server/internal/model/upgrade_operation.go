package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// upgradeStageRanks is the single forward order of the operation. Failed and
// rolled_back are deliberately absent: failed can be entered from any stage and
// left for any stage, and never becomes a checkpoint; rolled_back is terminal
// and is recorded with its evidence instead of extending the order.
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
	if s == UpgradeStageFailed || s == UpgradeStageRolledBack {
		return true
	}
	_, ok := upgradeStageRanks[s]
	return ok
}

func (s UpgradeStage) rank() int { return upgradeStageRanks[s] }

// RollbackApplicable reports whether a rollback may abandon this position.
//
// Preflight is excluded: the copy-in has not run, so there is no v2 root to
// abandon and no backup to compare. Failed is excluded: it has no position, so
// it may have failed before the copy-in, and a verdict that the v2 root was
// abandoned cannot be justified from it. Every forward position from copy_in
// onward copied a root, so any of them can be abandoned.
func (s UpgradeStage) RollbackApplicable() bool {
	switch s {
	case UpgradeStageCopyIn, UpgradeStageReconcile, UpgradeStageCPAImport,
		UpgradeStageExternalConnect, UpgradeStageVerify, UpgradeStageComplete:
		return true
	default:
		return false
	}
}

// CanAdvanceTo reports whether moving from s to next keeps the operation
// monotonic. A completed operation never moves again, not even to itself: the
// completed row is frozen, so a same-stage transition is refused here and the
// repository only lets an exactly identical write through as a no-op it does
// not persist. A failed stage has no position of its own, so this method cannot
// judge a resume: the row's high-water mark decides that (see
// UpgradeOperation.CanLeaveFailedTo).
func (s UpgradeStage) CanAdvanceTo(next UpgradeStage) bool {
	// A rolled-back operation is terminal: it is never resumed, so it can only
	// be re-entered from itself.
	if s == UpgradeStageRolledBack {
		return s == next
	}
	// A rollback abandons the forward order instead of extending it. It is the
	// one move a completed operation may still make, so the completion barrier
	// below does not apply to it; RollbackApplicable decides which positions
	// carry a copied root a rollback may abandon.
	if next == UpgradeStageRolledBack {
		return s.RollbackApplicable()
	}
	if s == UpgradeStageComplete {
		return false
	}
	if s == next {
		return true
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
	// Image is the exact 1.x reference the rollback restarted: the operator's
	// release tag or digest when one was named, otherwise the release derived
	// from the recorded source version. It is a description of the plan, not an
	// observation that this image was the process the operator started.
	Image string `json:"image"`
	// ImageSource says who chose the reference: "operator" when the operator
	// named it, "derived" when it was derived from the recorded source version.
	// ImageForm says which accepted form it is: "release-tag" or "digest".
	// Both are persisted so a reader can tell a reference the operator started
	// from one this build inferred; the plan carried them, the row did not.
	ImageSource string `json:"imageSource"`
	ImageForm   string `json:"imageForm"`
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
	return e.Image != "" && e.ImageSource != "" && e.ImageForm != "" &&
		e.StatusURL != "" && e.SourceFingerprint != "" && e.ObservedAtMS > 0
}

// Upgrade target modes. The mode is a fact about where the v2 installation's CPA
// lives, not a claim a caller may make: External means the CPA behind the
// recorded connection is administered by the owner, Managed means this Manager
// owns it. The verification step records which one it proved, so a reader can
// tell an External completion from a Managed one without re-deriving it.
const (
	UpgradeTargetExternal = "external"
	UpgradeTargetManaged  = "managed"
)

func validUpgradeTargetMode(mode string) bool {
	return mode == UpgradeTargetExternal || mode == UpgradeTargetManaged
}

// UpgradeVerification is the observation the complete verdict rests on. The
// operation's last two stages are the only ones a verification authorizes, and
// this value is what lets a later reader re-check the verdict instead of trusting
// the stage: it names the source volume that was observed unchanged around the
// write, the operation domains that were proven done, and — for an External
// target — the binding the connection was recorded under.
//
// It is written whole or not at all, in the same statement as the stage it
// authorizes, so a store can never hold `complete` without the proof.
type UpgradeVerification struct {
	// TargetMode is UpgradeTargetExternal or UpgradeTargetManaged.
	TargetMode string `json:"targetMode"`
	// SourceFingerprint is the manifest digest of the 1.x source observed while
	// the read-only source ownership fence was held. It is the same digest the
	// recorded copy-in and reconciliation name.
	SourceFingerprint string `json:"sourceFingerprint"`
	// Domains is the operation domain set this run proved done: the domains the
	// reconciliation established plus the ones the target step recorded. It never
	// names a domain the source could not provide.
	Domains []string `json:"domains"`
	// BindingIdentity, BindingGeneration and BindingObservedAtMS are the External
	// target binding this run confirmed. They are empty for a Managed target,
	// which has no External connection to bind.
	BindingIdentity     string `json:"bindingIdentity,omitempty"`
	BindingGeneration   string `json:"bindingGeneration,omitempty"`
	BindingObservedAtMS int64  `json:"bindingObservedAtMs,omitempty"`
	// ObservedAtMS is when the verification observed the source and the target.
	ObservedAtMS int64 `json:"observedAtMs"`
}

// Validate reports whether the verification names everything a verdict rests on.
func (v UpgradeVerification) Validate() error {
	if !validUpgradeTargetMode(v.TargetMode) {
		return fmt.Errorf("upgrade verification target mode %q is not supported", v.TargetMode)
	}
	if strings.TrimSpace(v.SourceFingerprint) == "" {
		return errors.New("upgrade verification names no source fingerprint")
	}
	if len(v.Domains) == 0 {
		return errors.New("upgrade verification names no completed domain")
	}
	for index, domain := range v.Domains {
		if !ValidUpgradeDomain(domain) {
			return fmt.Errorf("upgrade verification domain %q is not supported", domain)
		}
		for _, earlier := range v.Domains[:index] {
			if earlier == domain {
				return fmt.Errorf("upgrade verification domain %q is named twice", domain)
			}
		}
	}
	if v.ObservedAtMS <= 0 {
		return errors.New("upgrade verification names no observation time")
	}
	if v.TargetMode == UpgradeTargetExternal {
		if strings.TrimSpace(v.BindingIdentity) == "" || strings.TrimSpace(v.BindingGeneration) == "" {
			return errors.New("an External upgrade verification names no target binding")
		}
		if v.BindingObservedAtMS <= 0 {
			return errors.New("an External upgrade verification names no binding observation time")
		}
	}
	return nil
}

// Complete reports whether the verification holds everything a verdict rests on.
func (v UpgradeVerification) Complete() bool { return v.Validate() == nil }

// SameVerdict reports whether two verifications prove the same thing. The
// observation time is the only field left out: it says when this run looked,
// while every other field says what it found. Re-running the verification of an
// unchanged operation therefore re-proves the recorded verdict and stores
// nothing, and only a verdict that actually changed is a new record.
func (v UpgradeVerification) SameVerdict(other UpgradeVerification) bool {
	if v.TargetMode != other.TargetMode || v.SourceFingerprint != other.SourceFingerprint {
		return false
	}
	if v.BindingIdentity != other.BindingIdentity || v.BindingGeneration != other.BindingGeneration ||
		v.BindingObservedAtMS != other.BindingObservedAtMS {
		return false
	}
	if len(v.Domains) != len(other.Domains) {
		return false
	}
	for index := range v.Domains {
		if v.Domains[index] != other.Domains[index] {
			return false
		}
	}
	return true
}

// UpgradeOperation is the whole persistent state of the v2 Upgrade operation:
// one row, one operation, no history rows. It carries observations (source
// kind/version/fingerprints, backup manifest, manual items) and never an
// authorization flag.
type UpgradeOperation struct {
	Stage UpgradeStage `json:"stage"`
	// LastForwardStage is the highest forward stage the operation reached. It
	// equals Stage except while parked in failed or recorded as rolled_back,
	// where it keeps the highest forward position the row ever reached. Empty
	// means no forward stage was ever recorded: either a row migrated from a
	// schema without this column that was already failed, or a failure before
	// the first step. Such a row fails closed and cannot resume. The repository
	// owns this field on every write.
	LastForwardStage UpgradeStage `json:"lastForwardStage,omitempty"`
	DomainsDone      []string     `json:"domainsDone"`
	SourceKind       string       `json:"sourceKind,omitempty"`
	SourceVersion    string       `json:"sourceVersion,omitempty"`
	// SourceRoot is the 1.x data directory the copy-in read. A rollback binds
	// the volume it restarts to this directory, so "the original volume" is a
	// checkable claim rather than the operator's assertion.
	SourceRoot              string          `json:"sourceRoot,omitempty"`
	SourceFingerprintBefore string          `json:"sourceFingerprintBefore,omitempty"`
	SourceFingerprintAfter  string          `json:"sourceFingerprintAfter,omitempty"`
	BackupManifestSHA256    string          `json:"backupManifestSha256,omitempty"`
	Reconciliation          json.RawMessage `json:"reconciliation"`
	// Verification is the observation the verify/complete verdict rests on. It
	// is written only by the completion gate, in the same statement as the stage
	// it authorizes, and is `{}` for every step before it. The repository owns
	// this column on every other write, so no generic write can erase it.
	Verification json.RawMessage `json:"verification,omitempty"`
	ManualItems  []string        `json:"manualItems"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	ErrorDetail  string          `json:"errorDetail,omitempty"`
	// Revision is the row's compare-and-set token. Put only commits when the
	// caller's Revision equals the stored one, and every committed write stores
	// one more, so two writes in the same millisecond stay distinguishable.
	Revision int64 `json:"revision"`
	// Rollback is the evidence recorded with the rolled_back verdict, and nil
	// for every other stage.
	Rollback    *RollbackEvidence `json:"rollback,omitempty"`
	CreatedAtMS int64             `json:"createdAtMs"`
	UpdatedAtMS int64             `json:"updatedAtMs"`
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
	if len(o.Verification) == 0 {
		o.Verification = json.RawMessage("{}")
	}
}

// VerificationEvidence decodes the recorded verification block. A row that
// carries none is reported instead of decoded into the zero value, so a caller
// cannot mistake "never verified" for "verified nothing".
func (o UpgradeOperation) VerificationEvidence() (UpgradeVerification, error) {
	var evidence UpgradeVerification
	if len(bytes.TrimSpace(o.Verification)) == 0 {
		return evidence, errors.New("the upgrade operation carries no verification evidence")
	}
	if err := json.Unmarshal(o.Verification, &evidence); err != nil {
		return evidence, fmt.Errorf("decode the upgrade verification: %w", err)
	}
	return evidence, nil
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

// HighWater is the highest forward stage the operation has reached. A parked or
// rolled-back row keeps it in LastForwardStage; every other row's Stage is that
// position, so a row stored before the column existed still reports its stored
// stage.
func (o UpgradeOperation) HighWater() UpgradeStage {
	if o.Stage == UpgradeStageFailed || o.Stage == UpgradeStageRolledBack {
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
	if next == UpgradeStageRolledBack {
		// A resume returns to the forward order. The terminal verdict is
		// recorded with its evidence by RecordRollback, and a failed row has no
		// position to justify it, so it is never a resume target.
		return false
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
	if o.Stage == UpgradeStageFailed || o.Stage == UpgradeStageRolledBack {
		// A parked or rolled-back row keeps the highest forward stage it
		// reached, so its LastForwardStage names a forward position instead of
		// its own terminal stage.
		if o.LastForwardStage != "" && (!o.LastForwardStage.Valid() ||
			o.LastForwardStage == UpgradeStageFailed || o.LastForwardStage == UpgradeStageRolledBack) {
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
	if len(o.Verification) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(o.Verification, &object); err != nil || object == nil {
			return errors.New("upgrade operation verification must be a JSON object")
		}
	}
	// The complete verdict is the one stage whose whole justification is this
	// block, so it is refused without it. No generic writer can reach the stage
	// (the repository refuses it), and the gate writes both in one statement, so
	// a complete row without evidence is a store written by a build that predates
	// the gate and is reported as the outdated one it is by every reader.
	if o.Stage == UpgradeStageComplete {
		evidence, err := o.VerificationEvidence()
		if err != nil {
			return errors.New("a complete upgrade operation must carry its verification evidence")
		}
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("complete upgrade operation verification is incomplete: %w", err)
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
		return errors.New("upgrade operation rollback evidence must name the image, its source and form, the health endpoint, the observed fingerprint and when it was observed")
	}
	return nil
}
