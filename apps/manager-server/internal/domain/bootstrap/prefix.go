package bootstrap

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

type PrefixOperationResult string

const (
	PrefixPending  PrefixOperationResult = "pending"
	PrefixApplied  PrefixOperationResult = "applied"
	PrefixUnknown  PrefixOperationResult = "unknown"
	PrefixConflict PrefixOperationResult = "conflict"
)

type PrefixOperationStage string

const (
	PrefixIntentPersisted PrefixOperationStage = "intent_persisted"
	PrefixRouteActivated  PrefixOperationStage = "route_activated"
	PrefixHealthVerified  PrefixOperationStage = "health_verified"
	PrefixCommitted       PrefixOperationStage = "committed"
)

type PrefixImpact string

const (
	PrefixControlRouteOnly PrefixImpact = "control_route_only"
)

type PrefixRecoveryAction string

const (
	PrefixInspectFirst    PrefixRecoveryAction = "inspect_first"
	PrefixComplete        PrefixRecoveryAction = "complete"
	PrefixResolveConflict PrefixRecoveryAction = "resolve_conflict"
)

func hexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func containsPercentEncoding(value string) bool {
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '%' && hexDigit(value[i+1]) && hexDigit(value[i+2]) {
			return true
		}
	}
	return false
}

var canonicalControlBasePathCharacters = regexp.MustCompile(`^/[A-Za-z0-9._~$&+,/:;=@-]+$`)

// ValidControlBasePath is the shared canonical grammar for Bootstrap prefix,
// preview and routing snapshot contracts. Paths must serialize unchanged in
// both Go and browser URLs; this validates shape, not routing activation.
func ValidControlBasePath(value string) bool {
	if !canonicalControlBasePathCharacters.MatchString(value) ||
		value == "" || !strings.HasPrefix(value, "/") || value == "/" ||
		strings.ContainsAny(value, "?#\\") || strings.Contains(value, "//") ||
		containsPercentEncoding(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return path.Clean(value) == value
}

// ValidInferenceBasePath enforces the exact same canonical prefix grammar as
// ValidControlBasePath for public inference ingress prefixes.
func ValidInferenceBasePath(value string) bool {
	return ValidControlBasePath(value)
}

// SameOrNestedBasePath reports whether two canonical prefixes are equal or one
// is a strict segment ancestor of the other.
func SameOrNestedBasePath(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

// AccessPrefixCode names a deterministic validation or state refusal for
// control/inference prefix configuration.
type AccessPrefixCode string

const (
	CodeInvalidAccessPrefix              AccessPrefixCode = "invalid_access_prefix"
	CodeInferencePrefixRequired          AccessPrefixCode = "inference_prefix_required"
	CodeExternalInferencePrefixForbidden AccessPrefixCode = "external_inference_prefix_forbidden"
	CodeSharedPrefixMismatch             AccessPrefixCode = "shared_prefix_mismatch"
	CodeSharedPrefixConfirmationRequired AccessPrefixCode = "shared_prefix_confirmation_required"
	CodePrefixOverlapForbidden           AccessPrefixCode = "prefix_overlap_forbidden"
	CodeReservedPrefixCollision          AccessPrefixCode = "reserved_prefix_collision"
	CodeRoutingConflict                  AccessPrefixCode = "routing_conflict"
	CodeInferencePrefixAlreadyCommitted  AccessPrefixCode = "inference_prefix_already_committed"
)

// AccessPrefixError carries a typed AccessPrefixCode for HTTP controllers.
type AccessPrefixError struct {
	Code   AccessPrefixCode
	Detail string
}

func (e *AccessPrefixError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return string(e.Code) + ": " + e.Detail
	}
	return string(e.Code)
}

// AccessPrefixCodeOf extracts a typed AccessPrefixCode when present.
func AccessPrefixCodeOf(err error) (AccessPrefixCode, bool) {
	var target *AccessPrefixError
	if errors.As(err, &target) && target != nil && target.Code != "" {
		return target.Code, true
	}
	return "", false
}

const (
	suggestedFirstAlphabet = "abcdefghijklmnopqrstuvwxyz"
	suggestedRestAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
	suggestedPrefixLength  = 16
)

// GenerateSuggestedPrefix generates one 16-character CSPRNG canonical prefix
// (/[a-z][a-z0-9]{15}) suitable as an explicit UI input suggestion. It is never
// implicitly applied without operator submission.
func GenerateSuggestedPrefix() (string, error) {
	var raw [suggestedPrefixLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(1 + suggestedPrefixLength)
	b.WriteByte('/')
	b.WriteByte(suggestedFirstAlphabet[int(raw[0])%len(suggestedFirstAlphabet)])
	for i := 1; i < suggestedPrefixLength; i++ {
		b.WriteByte(suggestedRestAlphabet[int(raw[i])%len(suggestedRestAlphabet)])
	}
	candidate := b.String()
	if !ValidControlBasePath(candidate) {
		return "", errors.New("generated prefix failed canonical validation")
	}
	return candidate, nil
}

// GenerateSuggestedPrefixPair returns two distinct CSPRNG canonical prefix
// suggestions avoiding any caller-supplied collision check.
func GenerateSuggestedPrefixPair(collides func(string) bool) (string, string, error) {
	next := func(exclude string) (string, error) {
		for range 32 {
			candidate, err := GenerateSuggestedPrefix()
			if err != nil {
				return "", err
			}
			if candidate == exclude {
				continue
			}
			if collides != nil && collides(candidate) {
				continue
			}
			return candidate, nil
		}
		return "", errors.New("unable to generate collision-free suggested prefix")
	}
	control, err := next("")
	if err != nil {
		return "", "", err
	}
	inference, err := next(control)
	if err != nil {
		return "", "", err
	}
	return control, inference, nil
}

func validPrefixStage(stage PrefixOperationStage) bool {
	switch stage {
	case PrefixIntentPersisted, PrefixRouteActivated, PrefixHealthVerified, PrefixCommitted:
		return true
	default:
		return false
	}
}

type PrefixOperation struct {
	OperationID                   OperationID
	InstallationID                string
	CurrentInstallationID         string
	ExpectedRoutingRevision       uint64
	CurrentRoutingRevision        uint64
	PreviousVerifiedEffectivePath string
	DesiredControlBasePath        string
	VerifiedEffectivePath         string
	Impact                        PrefixImpact
	Stage                         PrefixOperationStage
	AuthorityQualified            bool
	CollisionFree                 bool
	PublicIdentityHealthy         bool
	StaticAssetsHealthy           bool
	APIAuthenticationHealthy      bool
	Result                        PrefixOperationResult
}

func (p PrefixOperation) Validate() error {
	if !validOperationID(p.OperationID) {
		return fmt.Errorf("invalid prefix operation id %q", p.OperationID)
	}
	if p.InstallationID == "" || p.CurrentInstallationID == "" || p.InstallationID != p.CurrentInstallationID {
		return errors.New("prefix operation requires matching installation identity")
	}
	if p.ExpectedRoutingRevision == 0 || p.CurrentRoutingRevision == 0 {
		return errors.New("prefix operation requires routing revision authority")
	}
	if !ValidControlBasePath(p.DesiredControlBasePath) {
		return errors.New("desired controlBasePath is not canonical")
	}
	if p.PreviousVerifiedEffectivePath != "" && !ValidControlBasePath(p.PreviousVerifiedEffectivePath) {
		return errors.New("previous verified effective controlBasePath is not canonical")
	}
	if p.VerifiedEffectivePath != "" && !ValidControlBasePath(p.VerifiedEffectivePath) {
		return errors.New("verified effective controlBasePath is not canonical")
	}
	if p.Impact != PrefixControlRouteOnly {
		return fmt.Errorf("unknown prefix impact %q", p.Impact)
	}
	if !validPrefixStage(p.Stage) {
		return fmt.Errorf("unknown prefix operation stage %q", p.Stage)
	}
	switch p.Result {
	case PrefixPending, PrefixApplied, PrefixUnknown, PrefixConflict:
	default:
		return fmt.Errorf("unknown prefix operation result %q", p.Result)
	}
	return nil
}

func CanCommitPrefix(p PrefixOperation) bool {
	return p.Validate() == nil && p.ExpectedRoutingRevision == p.CurrentRoutingRevision &&
		p.AuthorityQualified && p.CollisionFree && p.PublicIdentityHealthy &&
		p.StaticAssetsHealthy && p.APIAuthenticationHealthy &&
		p.Stage == PrefixHealthVerified && p.Result == PrefixPending
}

func PrefixActionAfterOperation(p PrefixOperation) (PrefixRecoveryAction, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	switch p.Result {
	case PrefixUnknown, PrefixPending:
		return PrefixInspectFirst, nil
	case PrefixApplied:
		if p.VerifiedEffectivePath != p.DesiredControlBasePath {
			return PrefixResolveConflict, nil
		}
		if p.Stage == PrefixCommitted {
			if p.ExpectedRoutingRevision != p.CurrentRoutingRevision ||
				!p.AuthorityQualified || !p.CollisionFree ||
				!p.PublicIdentityHealthy || !p.StaticAssetsHealthy || !p.APIAuthenticationHealthy {
				return PrefixInspectFirst, nil
			}
			return PrefixComplete, nil
		}
		return PrefixInspectFirst, nil
	case PrefixConflict:
		return PrefixResolveConflict, nil
	default:
		return "", fmt.Errorf("unknown prefix result %q", p.Result)
	}
}

type PrefixRecoveryObservation struct {
	CurrentInstallationID          string
	CurrentRoutingRevision         uint64
	CurrentEffectivePath           string
	CurrentOperationOwnerQualified bool
	CurrentCollisionFree           bool
	CurrentSourceQualified         bool
	PreviousSnapshotQualified      bool
	PreviousSnapshotEffectivePath  string
	PublicIdentityHealthy          bool
	StaticAssetsHealthy            bool
	APIAuthenticationHealthy       bool
}

func (o PrefixRecoveryObservation) valid() bool {
	if o.CurrentInstallationID == "" || o.CurrentRoutingRevision == 0 ||
		!ValidControlBasePath(o.CurrentEffectivePath) {
		return false
	}
	if o.PreviousSnapshotQualified {
		return ValidControlBasePath(o.PreviousSnapshotEffectivePath)
	}
	return o.PreviousSnapshotEffectivePath == ""
}

func prefixRecoveryFenceQualified(p PrefixOperation, o PrefixRecoveryObservation) bool {
	if p.Validate() != nil || !o.valid() {
		return false
	}
	return p.AuthorityQualified && p.CollisionFree &&
		p.ExpectedRoutingRevision == p.CurrentRoutingRevision &&
		p.CurrentRoutingRevision == o.CurrentRoutingRevision &&
		o.CurrentInstallationID == p.InstallationID &&
		o.CurrentOperationOwnerQualified &&
		o.CurrentCollisionFree &&
		o.CurrentSourceQualified
}

func CanCompletePrefixAfterObservation(p PrefixOperation, o PrefixRecoveryObservation) bool {
	if !prefixRecoveryFenceQualified(p, o) {
		return false
	}
	if p.Stage != PrefixRouteActivated && p.Stage != PrefixHealthVerified {
		return false
	}
	return o.CurrentEffectivePath == p.DesiredControlBasePath &&
		o.PublicIdentityHealthy && o.StaticAssetsHealthy && o.APIAuthenticationHealthy
}

func CanConditionallyRestorePrefix(p PrefixOperation, o PrefixRecoveryObservation) bool {
	if !prefixRecoveryFenceQualified(p, o) || p.PreviousVerifiedEffectivePath == "" {
		return false
	}
	if p.Stage != PrefixRouteActivated && p.Stage != PrefixHealthVerified {
		return false
	}
	return o.PreviousSnapshotQualified &&
		o.PreviousSnapshotEffectivePath == p.PreviousVerifiedEffectivePath &&
		o.CurrentEffectivePath == p.DesiredControlBasePath &&
		(!o.PublicIdentityHealthy || !o.StaticAssetsHealthy || !o.APIAuthenticationHealthy)
}
