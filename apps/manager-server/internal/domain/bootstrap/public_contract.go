package bootstrap

import (
	"errors"
	"fmt"
)

// PublicStatus is the deliberately small status vocabulary available to a
// fixed unauthenticated discovery surface. It is not a replacement for Mode.
type PublicStatus string

const (
	PublicFreshClaimRequired          PublicStatus = "fresh_claim_required"
	PublicAdminAuthenticationRequired PublicStatus = "admin_authentication_required"
	PublicUnsupportedUpgrade          PublicStatus = "unsupported_upgrade"
	PublicRecoveryRequired            PublicStatus = "recovery_required"
	PublicUnavailable                 PublicStatus = "unavailable"
)

// PublicReason is a stable, non-sensitive explanation for a public status.
type PublicReason string

const (
	ReasonFreshClaimRequired       PublicReason = "fresh_claim_required"
	ReasonAdminAuthentication      PublicReason = "admin_authentication_required"
	ReasonQualificationUnavailable PublicReason = "qualification_unavailable"
	ReasonRecoveryHostIntervention PublicReason = "recovery_host_intervention"
	ReasonObservationUnavailable   PublicReason = "observation_unavailable"
)

type AuthEntry string

const (
	AuthEntryFreshClaim       AuthEntry = "fresh_claim"
	AuthEntryAdminSession     AuthEntry = "admin_session"
	AuthEntryHostIntervention AuthEntry = "host_intervention"
	AuthEntryUnavailable      AuthEntry = "unavailable"
)

func validPublicStatus(value PublicStatus) bool {
	switch value {
	case PublicFreshClaimRequired, PublicAdminAuthenticationRequired,
		PublicUnsupportedUpgrade, PublicRecoveryRequired, PublicUnavailable:
		return true
	default:
		return false
	}
}

func validPublicReason(value PublicReason) bool {
	switch value {
	case ReasonFreshClaimRequired, ReasonAdminAuthentication,
		ReasonQualificationUnavailable, ReasonRecoveryHostIntervention,
		ReasonObservationUnavailable:
		return true
	default:
		return false
	}
}

func validAuthEntry(value AuthEntry) bool {
	switch value {
	case AuthEntryFreshClaim, AuthEntryAdminSession, AuthEntryHostIntervention, AuthEntryUnavailable:
		return true
	default:
		return false
	}
}

// PublicDiscovery is the only domain shape that may be projected into fixed
// unauthenticated discovery. A nil Mode means that a stable classification was
// not safe to expose; it must never be replaced by a guessed mode.
type PublicDiscovery struct {
	ContractVersion        string
	Status                 PublicStatus
	Mode                   *Mode
	AuthenticationRequired bool
	AuthEntry              AuthEntry
	Reason                 PublicReason
}

const (
	PublicDiscoveryContractVersion = "cpamp.phase4-01.discovery.v1"
	FixedBootstrapDiscoveryPath    = "/.well-known/cpamp-bootstrap"
	// FixedLivenessPath preserves the existing Manager readiness carrier. It is
	// transport compatibility, not a Bootstrap lifecycle or route-ledger model.
	FixedLivenessPath = "/health"
)

func (d PublicDiscovery) Validate() error {
	if d.ContractVersion != PublicDiscoveryContractVersion {
		return fmt.Errorf("unsupported public discovery contract version %q", d.ContractVersion)
	}
	if !validPublicStatus(d.Status) || !validPublicReason(d.Reason) || !validAuthEntry(d.AuthEntry) {
		return errors.New("unknown public discovery status, reason or auth entry")
	}
	if d.Mode != nil && *d.Mode == Upgrade {
		return errors.New("upgrade is wire-contract-only and cannot be emitted by discovery")
	}
	if d.Mode != nil && *d.Mode == Completed {
		return errors.New("completed cannot be represented by unauthenticated discovery")
	}
	if d.Mode != nil {
		switch *d.Mode {
		case Fresh, UnsupportedUpgrade, Recovery:
		default:
			return fmt.Errorf("unknown or non-public discovery mode %q", *d.Mode)
		}
	}

	switch d.Status {
	case PublicFreshClaimRequired:
		if d.Mode == nil || *d.Mode != Fresh || !d.AuthenticationRequired ||
			d.AuthEntry != AuthEntryFreshClaim || d.Reason != ReasonFreshClaimRequired {
			return errors.New("fresh discovery requires a fresh claim entry")
		}
	case PublicAdminAuthenticationRequired:
		if d.Mode == nil || d.AuthEntry != AuthEntryAdminSession || !d.AuthenticationRequired ||
			d.Reason != ReasonAdminAuthentication {
			return errors.New("admin authentication discovery is inconsistent")
		}
	case PublicUnsupportedUpgrade:
		if d.Mode == nil || *d.Mode != UnsupportedUpgrade || d.AuthEntry != AuthEntryAdminSession ||
			!d.AuthenticationRequired || d.Reason != ReasonQualificationUnavailable {
			return errors.New("unsupported upgrade discovery is inconsistent")
		}
	case PublicRecoveryRequired:
		if d.Mode == nil || *d.Mode != Recovery || d.AuthEntry != AuthEntryHostIntervention ||
			d.Reason != ReasonRecoveryHostIntervention {
			return errors.New("recovery discovery is inconsistent")
		}
	case PublicUnavailable:
		if d.Mode != nil || d.AuthEntry != AuthEntryUnavailable ||
			d.Reason != ReasonObservationUnavailable || d.AuthenticationRequired {
			return errors.New("unavailable discovery must not manufacture a mode")
		}
	}
	return nil
}

// StatusSnapshot keeps the three state axes explicit for authenticated
// consumers. SessionState never grants or changes BootstrapMode authority.
type StatusSnapshot struct {
	FreshOwnership  *FreshOwnership
	Mode            Mode
	AdminCredential AdminCredentialState
	Session         SessionState
}

func (s StatusSnapshot) Validate() error {
	if !validMode(s.Mode) || !validAdminCredentialState(s.AdminCredential) || !validSessionState(s.Session) {
		return errors.New("status snapshot contains an unknown mode, credential state or session state")
	}
	if s.Mode == Fresh {
		if s.FreshOwnership != nil && !s.FreshOwnership.Valid() {
			return errors.New("fresh status requires matching current ownership")
		}
		if s.AdminCredential == AdminCredentialUnverifiable || (s.AdminCredential == AdminCredentialInitialized && s.FreshOwnership == nil) {
			return errors.New("initialized fresh status requires matching current ownership")
		}
	}
	if s.Mode == Completed && s.AdminCredential != AdminCredentialInitialized {
		return errors.New("completed status requires an initialized administrator credential")
	}
	return nil
}

// LegacyBootstrapEvidence makes the legacy persisted status an evidence input,
// never a direct authority for the Phase4 mode contract.
type LegacyBootstrapEvidence struct {
	LegacyStatus   string
	Classification ClassificationEvidence
}

func ClassifyLegacyEvidence(e LegacyBootstrapEvidence) (Mode, error) {
	// LegacyStatus is intentionally not switched on. The accepted domain
	// evidence and its safety gates remain the only classification authority.
	return Classify(e.Classification)
}

type SourceAdmissionReason string

const (
	ReasonUnknownNewerSameSchema SourceAdmissionReason = "unknown_newer_same_schema"
	ReasonDowngradeForbidden     SourceAdmissionReason = "downgrade_forbidden"
)

type QualificationState string

const (
	QualificationPending QualificationState = "pending"
)

type SourceAdmissionCase string

const (
	AdmissionUnknownNewerSameSchema SourceAdmissionCase = "unknown_newer_same_schema"
	AdmissionDowngrade              SourceAdmissionCase = "downgrade"
)

type SourceAdmissionAttempt struct {
	Case                 SourceAdmissionCase
	Shape                SourceShape
	Provenance           SourceEvidenceStatus
	RequestedTarget      PlannedCandidate
	ObservedSourceCommit string
}

type SourceAdmissionDecision struct {
	Accepted                bool
	Mode                    Mode
	Reason                  SourceAdmissionReason
	Qualification           QualificationState
	SourceIdentityPreserved bool
}

func (a SourceAdmissionAttempt) Evaluate() (SourceAdmissionDecision, error) {
	decision := SourceAdmissionDecision{
		Accepted: false, Mode: UnsupportedUpgrade, Qualification: QualificationPending,
		SourceIdentityPreserved: true,
	}
	if !validSourceShape(a.Shape) || !validCandidate(a.RequestedTarget) || a.ObservedSourceCommit == "" {
		return SourceAdmissionDecision{}, errors.New("source admission evidence is incomplete")
	}
	switch a.Case {
	case AdmissionUnknownNewerSameSchema:
		if a.Shape != LegacySchemaC || a.Provenance != UnknownOrNewerSource {
			return SourceAdmissionDecision{}, errors.New("unknown/newer same-schema case requires SchemaC and unknown provenance")
		}
		decision.Reason = ReasonUnknownNewerSameSchema
	case AdmissionDowngrade:
		if a.Provenance != UnknownOrNewerSource {
			return SourceAdmissionDecision{}, errors.New("downgrade case requires newer or unknown provenance")
		}
		decision.Reason = ReasonDowngradeForbidden
	default:
		return SourceAdmissionDecision{}, fmt.Errorf("unknown source admission case %q", a.Case)
	}
	return decision, nil
}

func validCandidate(candidate PlannedCandidate) bool {
	return candidate == ExactV1144Candidate()
}

func (d SourceAdmissionDecision) Validate() error {
	if d.Accepted || d.Mode != UnsupportedUpgrade || d.Qualification != QualificationPending ||
		!d.SourceIdentityPreserved {
		return errors.New("deferred source admission must reject without rewriting identity")
	}
	switch d.Reason {
	case ReasonUnknownNewerSameSchema, ReasonDowngradeForbidden:
		return nil
	default:
		return fmt.Errorf("unknown source admission reason %q", d.Reason)
	}
}
