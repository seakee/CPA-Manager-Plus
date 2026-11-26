//go:build unix

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	// Version is the build-time launcher version injected via -ldflags "-X main.Version=...".
	Version = "dev"
	// SourceCommit is the build-time git commit SHA injected via -ldflags "-X main.SourceCommit=...".
	SourceCommit = "unknown"
	// ExtraTrustedSlotPublicKey is an optional "<key_id>:<64-lowercase-hex-ed25519-pubkey>"
	// entry injectable ONLY at link time via -ldflags for offline unit/E2E testing.
	// Public keys are NEVER read from environment variables or filesystem files.
	ExtraTrustedSlotPublicKey = ""
)

const (
	DefaultTrustedSlotKeyID        = "cpamp-root-2026-01"
	DefaultTrustedSlotPublicKeyHex = "e6eb42c6a92a4c78d1c2e6b050234824b17936788799036b4201f98e6291d41f"

	SlotManifestDomainPrefix = "cpamp-slot-manifest-v1\n"
	SlotManifestFileName     = "slot-manifest.json"
	SlotSignatureFileName    = "slot-manifest.json.sig"
	MaxSlotMetadataBytes     = 4 << 20 // 4 MiB

	SlotCodeInvalidRequest          = "invalid_request"
	SlotCodeUnauthorized            = "unauthorized"
	SlotCodeArchiveTooLarge         = "archive_too_large"
	SlotCodeSlotBusy                = "slot_busy"
	SlotCodeSlotNotStaged           = "slot_not_staged"
	SlotCodeVersionConflict         = "version_conflict"
	SlotCodeSchemaRollbackForbidden = "schema_rollback_forbidden"
	SlotCodeBaseEpochIncompatible   = "base_epoch_incompatible"
	SlotCodeSignatureInvalid        = "signature_invalid"
	SlotCodeArchiveInvalid          = "archive_invalid"
	SlotCodeSnapshotInvalid         = "snapshot_invalid"
	SlotCodeDownloadFailed          = "download_failed"
	SlotCodeInternalError           = "internal_error"
)

var requiredSlotBinaries = []string{
	"bin/cpamp-launcher",
	"bin/cpamp-runtime-supervisor",
	"bin/cpa-manager-plus",
	"bin/cpamp-ingress",
}

var requiredSlotShareFiles = []string{
	"share/cpamp/runtime/config.seed.yaml",
	"share/cpamp/runtime/seed-lineage.json",
	"share/cpamp/runtime/cpa-artifact.json",
	"share/cpamp/release/base-abi.json",
}

// SlotError carries a machine-readable contract error code and HTTP status.
type SlotError struct {
	Code       string
	HTTPStatus int
	Message    string
	Err        error
}

func (e *SlotError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil && e.Message != "" {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return e.Code
}

func (e *SlotError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newSlotError(code string, status int, msg string, err error) *SlotError {
	return &SlotError{
		Code:       code,
		HTTPStatus: status,
		Message:    msg,
		Err:        err,
	}
}

func slotErrorCode(err error, fallback string) string {
	var se *SlotError
	if errors.As(err, &se) && se.Code != "" {
		return se.Code
	}
	return fallback
}

func slotErrorStatus(err error, fallback int) int {
	var se *SlotError
	if errors.As(err, &se) && se.HTTPStatus != 0 {
		return se.HTTPStatus
	}
	return fallback
}

// SlotManifest represents slot-manifest.json inside a CPAMP v2 slot bundle.
type SlotManifest struct {
	SchemaVersion    int                          `json:"schema_version"`
	Version          string                       `json:"version"`
	SourceCommit     string                       `json:"source_commit"`
	Platform         string                       `json:"platform"`
	MinBaseEpoch     int                          `json:"min_base_epoch"`
	SchemaGeneration int                          `json:"schema_generation"`
	KeyID            string                       `json:"key_id"`
	Files            map[string]SlotManifestEntry `json:"files"`
}

// SlotManifestEntry describes one regular file inside a slot bundle.
type SlotManifestEntry struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

// SlotSignature represents slot-manifest.json.sig inside a CPAMP v2 slot bundle.
type SlotSignature struct {
	SchemaVersion int    `json:"schema_version"`
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"key_id"`
	Signature     string `json:"signature"`
}

// ExpectedSlotPlatform returns the canonical target slot platform string ("linux/" + GOARCH).
func ExpectedSlotPlatform() string {
	return "linux/" + runtime.GOARCH
}

// BuildTrustedSlotKeyring builds the immutable in-memory Ed25519 public keyring
// from compiled-in constants (plus link-time ExtraTrustedSlotPublicKey if set).
func BuildTrustedSlotKeyring() (map[string]ed25519.PublicKey, error) {
	ring := make(map[string]ed25519.PublicKey, 2)
	defaultPub, err := decodeStrictEd25519PublicKeyHex(DefaultTrustedSlotPublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid embedded default slot public key: %w", err)
	}
	ring[DefaultTrustedSlotKeyID] = defaultPub

	if ExtraTrustedSlotPublicKey != "" {
		parts := strings.Split(ExtraTrustedSlotPublicKey, ":")
		if len(parts) != 2 || !isValidKeyID(parts[0]) {
			return nil, fmt.Errorf("invalid ExtraTrustedSlotPublicKey format")
		}
		extraPub, err := decodeStrictEd25519PublicKeyHex(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid ExtraTrustedSlotPublicKey hex: %w", err)
		}
		ring[parts[0]] = extraPub
	}
	return ring, nil
}

func isValidKeyID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func decodeStrictEd25519PublicKeyHex(raw string) (ed25519.PublicKey, error) {
	if len(raw) != ed25519.PublicKeySize*2 {
		return nil, fmt.Errorf("expected %d hex chars, got %d", ed25519.PublicKeySize*2, len(raw))
	}
	for i := range len(raw) {
		c := raw[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return nil, fmt.Errorf("public key hex must be lowercase hexadecimal")
		}
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("failed to decode ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

// SemVerV2 holds parsed SemVer 2.0 components matching bin/release/update-contract.mjs.
type SemVerV2 struct {
	Major      string
	Minor      string
	Patch      string
	Prerelease []string
	Build      string
}

// ParseSemVerV2 parses a SemVer 2.0 string (with optional leading 'v'), rejecting
// leading zeroes in numeric core or prerelease identifiers.
func ParseSemVerV2(raw string) (SemVerV2, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 128 {
		return SemVerV2{}, false
	}
	if trimmed[0] == 'v' {
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return SemVerV2{}, false
	}

	var buildMeta string
	if plusIdx := strings.IndexByte(trimmed, '+'); plusIdx >= 0 {
		buildMeta = trimmed[plusIdx+1:]
		trimmed = trimmed[:plusIdx]
		if !isValidDotSeparatedIdentifiers(buildMeta, false) {
			return SemVerV2{}, false
		}
	}

	var prereleaseRaw string
	if dashIdx := strings.IndexByte(trimmed, '-'); dashIdx >= 0 {
		prereleaseRaw = trimmed[dashIdx+1:]
		trimmed = trimmed[:dashIdx]
		if !isValidDotSeparatedIdentifiers(prereleaseRaw, true) {
			return SemVerV2{}, false
		}
	}

	coreParts := strings.Split(trimmed, ".")
	if len(coreParts) != 3 {
		return SemVerV2{}, false
	}
	for _, p := range coreParts {
		if !isCanonicalNonNegativeInt(p) {
			return SemVerV2{}, false
		}
	}

	var pre []string
	if prereleaseRaw != "" {
		pre = strings.Split(prereleaseRaw, ".")
	}
	return SemVerV2{
		Major:      coreParts[0],
		Minor:      coreParts[1],
		Patch:      coreParts[2],
		Prerelease: pre,
		Build:      buildMeta,
	}, true
}

func isCanonicalNonNegativeInt(s string) bool {
	if s == "" {
		return false
	}
	if len(s) > 1 && s[0] == '0' {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isValidDotSeparatedIdentifiers(raw string, rejectNumericLeadingZero bool) bool {
	if raw == "" {
		return false
	}
	parts := strings.Split(raw, ".")
	for _, p := range parts {
		if p == "" {
			return false
		}
		for i := range len(p) {
			c := p[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-') {
				return false
			}
		}
		if rejectNumericLeadingZero && isAllDigits(p) && len(p) > 1 && p[0] == '0' {
			return false
		}
	}
	return true
}

func compareDecimalStrings(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// CompareSemVerV2 compares two SemVer 2.0 version strings (with or without leading 'v')
// using SemVer 2.0 precedence rules identical to update-contract.mjs.
func CompareSemVerV2(leftRaw, rightRaw string) (int, error) {
	left, ok := ParseSemVerV2(leftRaw)
	if !ok {
		return 0, fmt.Errorf("invalid semver version %q", leftRaw)
	}
	right, ok := ParseSemVerV2(rightRaw)
	if !ok {
		return 0, fmt.Errorf("invalid semver version %q", rightRaw)
	}

	if cmp := compareDecimalStrings(left.Major, right.Major); cmp != 0 {
		return cmp, nil
	}
	if cmp := compareDecimalStrings(left.Minor, right.Minor); cmp != 0 {
		return cmp, nil
	}
	if cmp := compareDecimalStrings(left.Patch, right.Patch); cmp != 0 {
		return cmp, nil
	}

	if len(left.Prerelease) == 0 && len(right.Prerelease) == 0 {
		return 0, nil
	}
	if len(left.Prerelease) == 0 {
		return 1, nil
	}
	if len(right.Prerelease) == 0 {
		return -1, nil
	}

	limit := len(left.Prerelease)
	if len(right.Prerelease) > limit {
		limit = len(right.Prerelease)
	}
	for i := range limit {
		if i >= len(left.Prerelease) {
			return -1, nil
		}
		if i >= len(right.Prerelease) {
			return 1, nil
		}
		li := left.Prerelease[i]
		ri := right.Prerelease[i]
		if li == ri {
			continue
		}
		lNum := isAllDigits(li)
		rNum := isAllDigits(ri)
		if lNum && rNum {
			return compareDecimalStrings(li, ri), nil
		}
		if lNum != rNum {
			if lNum {
				return -1, nil
			}
			return 1, nil
		}
		if li < ri {
			return -1, nil
		}
		return 1, nil
	}
	return 0, nil
}

// NormalizeVersionTag normalizes a valid SemVer string to carry a leading 'v'
// (e.g. "2.0.0-beta.2" -> "v2.0.0-beta.2"), stripping build metadata if absent.
func NormalizeVersionTag(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if _, ok := ParseSemVerV2(trimmed); !ok {
		return "", false
	}
	if strings.HasPrefix(trimmed, "v") {
		return trimmed, true
	}
	return "v" + trimmed, true
}

// NormalizeVersionOrRaw normalizes a SemVer version to have a leading 'v' when valid,
// or returns the trimmed raw version string (such as "dev").
func NormalizeVersionOrRaw(raw string) string {
	if norm, ok := NormalizeVersionTag(raw); ok {
		return norm
	}
	return strings.TrimSpace(raw)
}

// ValidateCanonicalSlotVersion enforces canonical CPAMP v2 slot tag rules:
// leading 'v', major == "2", valid SemVer 2.0, no '+' build metadata, safe path segment.
func ValidateCanonicalSlotVersion(ver string) error {
	if ver == "" || len(ver) > 128 || strings.TrimSpace(ver) != ver {
		return fmt.Errorf("slot version must be a non-empty canonical v2 tag")
	}
	if ver[0] != 'v' {
		return fmt.Errorf("slot version %q must start with 'v'", ver)
	}
	if strings.ContainsAny(ver, "/\\+") || strings.Contains(ver, "..") {
		return fmt.Errorf("slot version %q contains forbidden characters", ver)
	}
	parsed, ok := ParseSemVerV2(ver)
	if !ok {
		return fmt.Errorf("slot version %q is not valid SemVer 2.0", ver)
	}
	if parsed.Major != "2" {
		return fmt.Errorf("slot version %q must belong to major line v2", ver)
	}
	return nil
}

func isValidPrefixedSHA256(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return false
	}
	hexPart := digest[len("sha256:"):]
	for i := range len(hexPart) {
		c := hexPart[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isValidCommitSHA40(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	for i := range len(commit) {
		c := commit[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ValidateSlotRelativePath validates a clean relative file path inside a slot archive.
func ValidateSlotRelativePath(rel string) error {
	if rel == "" || len(rel) > 512 {
		return fmt.Errorf("empty or oversized slot path")
	}
	if strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") || strings.Contains(rel, "\\") {
		return fmt.Errorf("invalid slot relative path %q", rel)
	}
	for i := range len(rel) {
		if rel[i] < 0x20 || rel[i] == 0x7f {
			return fmt.Errorf("control character in slot path %q", rel)
		}
	}
	parts := strings.Split(rel, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("unsafe path segment in %q", rel)
		}
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("non-canonical slot path %q", rel)
	}
	if rel == SlotManifestFileName || rel == SlotSignatureFileName {
		return nil
	}
	if parts[0] == "bin" {
		if len(parts) != 2 {
			return fmt.Errorf("slot bin entry %q must be a direct child of bin/", rel)
		}
		return nil
	}
	if parts[0] == "share" {
		if len(parts) < 2 {
			return fmt.Errorf("slot share entry %q must be inside share/", rel)
		}
		return nil
	}
	return fmt.Errorf("slot path %q is outside permitted bin/ and share/ trees", rel)
}

func strictUnmarshalJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON content")
	}
	return nil
}

// VerifySlotManifestBytes verifies Ed25519 signature over "cpamp-slot-manifest-v1\n" + rawManifest,
// enforces all schema/platform/epoch invariants, and returns the parsed SlotManifest.
func VerifySlotManifestBytes(
	rawManifest []byte,
	rawSig []byte,
	expectedPlatform string,
	baseEpoch int,
	keyring map[string]ed25519.PublicKey,
) (*SlotManifest, error) {
	if len(rawManifest) == 0 || len(rawManifest) > MaxSlotMetadataBytes {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot-manifest.json missing or oversized", nil)
	}
	if len(rawSig) == 0 || len(rawSig) > 64*1024 {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "slot-manifest.json.sig missing or oversized", nil)
	}

	if keyring == nil {
		var err error
		keyring, err = BuildTrustedSlotKeyring()
		if err != nil {
			return nil, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to load trusted keyring", err)
		}
	}

	var sig SlotSignature
	if err := strictUnmarshalJSON(rawSig, &sig); err != nil {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "malformed slot-manifest.json.sig", err)
	}
	if sig.SchemaVersion != 1 || sig.Algorithm != "ed25519" || !isValidKeyID(sig.KeyID) || sig.Signature == "" {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "unsupported slot signature metadata", nil)
	}
	pubKey, trusted := keyring[sig.KeyID]
	if !trusted {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("untrusted slot signing key_id %q", sig.KeyID), nil)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil || len(sigBytes) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sigBytes) != sig.Signature {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "malformed base64 ed25519 signature", err)
	}

	msg := make([]byte, 0, len(SlotManifestDomainPrefix)+len(rawManifest))
	msg = append(msg, SlotManifestDomainPrefix...)
	msg = append(msg, rawManifest...)
	if !ed25519.Verify(pubKey, msg, sigBytes) {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "ed25519 signature verification failed", nil)
	}

	var manifest SlotManifest
	if err := strictUnmarshalJSON(rawManifest, &manifest); err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "malformed slot-manifest.json", err)
	}
	if manifest.KeyID != sig.KeyID {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "slot-manifest key_id does not match signature key_id", nil)
	}
	if manifest.SchemaVersion != 1 {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("unsupported slot-manifest schema_version %d", manifest.SchemaVersion), nil)
	}
	if err := ValidateCanonicalSlotVersion(manifest.Version); err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
	}
	if !isValidCommitSHA40(manifest.SourceCommit) {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot-manifest source_commit must be 40 lowercase hex chars", nil)
	}
	if expectedPlatform == "" {
		expectedPlatform = ExpectedSlotPlatform()
	}
	if manifest.Platform != expectedPlatform {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot platform %q does not match host %q", manifest.Platform, expectedPlatform), nil)
	}
	if manifest.MinBaseEpoch < 1 {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot min_base_epoch must be >= 1", nil)
	}
	if baseEpoch < 1 {
		return nil, newSlotError(SlotCodeBaseEpochIncompatible, http.StatusUnprocessableEntity, "host base_epoch is invalid", nil)
	}
	if manifest.MinBaseEpoch > baseEpoch {
		return nil, newSlotError(
			SlotCodeBaseEpochIncompatible,
			http.StatusUnprocessableEntity,
			fmt.Sprintf("slot requires min_base_epoch %d, host base_epoch is %d", manifest.MinBaseEpoch, baseEpoch),
			nil,
		)
	}
	if manifest.SchemaGeneration < 1 {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot schema_generation must be >= 1", nil)
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > MaxSlotMemberCount {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot manifest files map is empty or exceeds member limit", nil)
	}

	for relPath, entry := range manifest.Files {
		if relPath == SlotManifestFileName || relPath == SlotSignatureFileName {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot-manifest files must not self-reference manifest/signature", nil)
		}
		if err := ValidateSlotRelativePath(relPath); err != nil {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
		}
		if entry.Size < 0 || entry.Size > MaxSlotUncompressedBytes {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("invalid size for %s", relPath), nil)
		}
		if !isValidPrefixedSHA256(entry.SHA256) {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("invalid sha256 for %s", relPath), nil)
		}
		if strings.HasPrefix(relPath, "bin/") {
			if entry.Mode != "0555" || entry.Size == 0 {
				return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("binary %s must declare mode 0555 and non-zero size", relPath), nil)
			}
		} else {
			if entry.Mode != "0444" {
				return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("asset %s must declare mode 0444", relPath), nil)
			}
		}
	}

	for _, reqBin := range requiredSlotBinaries {
		if _, ok := manifest.Files[reqBin]; !ok {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot manifest missing required binary %s", reqBin), nil)
		}
	}
	for _, reqShare := range requiredSlotShareFiles {
		entry, ok := manifest.Files[reqShare]
		if !ok {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot manifest missing required metadata file %s", reqShare), nil)
		}
		if entry.Mode != "0444" {
			return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot metadata file %s must declare mode 0444", reqShare), nil)
		}
	}

	return &manifest, nil
}

// managerSnapshotManifest mirrors managerdatasnapshot's manifest.json schema (version 1).
type managerSnapshotManifest struct {
	Version           int                                    `json:"version"`
	TargetDBPath      string                                 `json:"target_db_path,omitempty"`
	TargetDataKeyPath string                                 `json:"target_data_key_path,omitempty"`
	Files             map[string]managerSnapshotManifestFile `json:"files"`
}

type managerSnapshotManifestFile struct {
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

var requiredSnapshotManifestKeys = []string{
	"database",
	"database-wal",
	"database-shm",
	"database-journal",
	"data-key",
}

// ValidateManagerDataSnapshotDir verifies that snapshotDir is a clean non-symlink directory
// within dataDir (walking every intermediate segment with O_NOFOLLOW) and contains a complete,
// hash-verified managerdatasnapshot v1 manifest.json with valid target paths.
func ValidateManagerDataSnapshotDir(snapshotDir, dataDir string, expectedManagerUID int) error {
	_, err := readValidatedManagerSnapshotManifest(snapshotDir, dataDir, expectedManagerUID)
	return err
}

func readValidatedManagerSnapshotManifest(snapshotDir, dataDir string, expectedManagerUID int) (*managerSnapshotManifest, error) {
	rawSnap := strings.TrimSpace(snapshotDir)
	rawData := strings.TrimSpace(dataDir)
	if rawSnap == "" || rawData == "" || !filepath.IsAbs(rawSnap) || !filepath.IsAbs(rawData) {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot_dir must be an absolute path", nil)
	}
	cleanSnap := filepath.Clean(rawSnap)
	cleanData := filepath.Clean(rawData)
	if cleanSnap == cleanData || !isPathWithin(cleanSnap, cleanData) {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot_dir must reside within data directory", nil)
	}

	dataFD, _, err := openNoFollowDirFD(cleanData)
	if err != nil {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "data directory is missing or symlinked", err)
	}
	defer func() { _ = unix.Close(dataFD) }()

	relSnap, err := filepath.Rel(cleanData, cleanSnap)
	if err != nil || relSnap == "" || relSnap == "." || strings.HasPrefix(relSnap, "..") {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot_dir must reside within data directory", err)
	}

	dirFD, err := openRelSubdirNoFollow(dataFD, filepath.ToSlash(relSnap))
	if err != nil {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot_dir or parent segment is missing or symlinked", err)
	}
	defer func() { _ = unix.Close(dirFD) }()

	var dirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &dirStat); err != nil {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "failed to stat snapshot_dir", err)
	}

	dirUID := int(dirStat.Uid)
	if dirUID != RootUID && dirUID != expectedManagerUID {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot_dir has untrusted owner uid %d", dirUID), nil)
	}
	if uint32(dirStat.Mode)&0o022 != 0 {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot_dir must not be group/world writable", nil)
	}

	rawManifest, manSt, err := readRegularFileAtNoFollow(dirFD, "manifest.json", MaxSlotMetadataBytes)
	if err != nil {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot manifest.json missing or unreadable", err)
	}
	if int(manSt.Uid) != RootUID && int(manSt.Uid) != expectedManagerUID {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot manifest.json has untrusted owner uid %d", manSt.Uid), nil)
	}
	if uint32(manSt.Mode)&0o022 != 0 {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot manifest.json must not be group/world writable", nil)
	}

	var sm managerSnapshotManifest
	if err := json.Unmarshal(rawManifest, &sm); err != nil {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot manifest.json is malformed", err)
	}
	if sm.Version != 1 || len(sm.Files) != len(requiredSnapshotManifestKeys) {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "unsupported snapshot manifest version or file map", nil)
	}

	dbEntry, hasDBEntry := sm.Files["database"]
	if !hasDBEntry || !dbEntry.Existed {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot manifest must include an existing database image", nil)
	}

	if err := validateSnapshotTargetFilePath(dataFD, cleanData, cleanSnap, sm.TargetDBPath, "target_db_path"); err != nil {
		return nil, err
	}
	if err := validateSnapshotTargetFilePath(dataFD, cleanData, cleanSnap, sm.TargetDataKeyPath, "target_data_key_path"); err != nil {
		return nil, err
	}
	if sm.TargetDBPath == sm.TargetDataKeyPath {
		return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "target_db_path and target_data_key_path must be distinct", nil)
	}

	allowedEntries := make(map[string]struct{}, len(requiredSnapshotManifestKeys)+1)
	allowedEntries["manifest.json"] = struct{}{}
	for _, key := range requiredSnapshotManifestKeys {
		entry, ok := sm.Files[key]
		if !ok {
			return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot manifest missing entry %q", key), nil)
		}
		if entry.Existed {
			allowedEntries[key] = struct{}{}
		}
	}

	if err := verifyNoExtraSnapshotEntries(dirFD, allowedEntries); err != nil {
		return nil, err
	}

	for _, key := range requiredSnapshotManifestKeys {
		entry := sm.Files[key]
		if !entry.Existed {
			continue
		}
		if entry.Size < 0 || entry.Mode&0o600 != 0o600 || entry.Mode&0o077 != 0 || !isValidCommitSHA64(entry.SHA256) {
			return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot manifest entry %q has invalid metadata", key), nil)
		}
		var (
			digest string
			size   int64
			hErr   error
		)
		if key == "data-key" {
			if entry.Size > MaxSlotMetadataBytes {
				return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "snapshot data-key exceeds metadata size bound", nil)
			}
			digest, size, hErr = hashSnapshotFileAtNoFollow(dirFD, key, entry.Size, expectedManagerUID)
		} else {
			digest, size, hErr = hashSnapshotFileAtNoFollow(dirFD, key, entry.Size, expectedManagerUID)
		}
		if hErr != nil {
			return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot file %q missing or unreadable", key), hErr)
		}
		if size != entry.Size || digest != entry.SHA256 {
			return nil, newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot file %q failed size/sha256 check", key), nil)
		}
	}
	return &sm, nil
}

func validateSnapshotTargetFilePath(dataFD int, cleanData, cleanSnap, rawTarget, fieldName string) error {
	if rawTarget == "" || strings.TrimSpace(rawTarget) != rawTarget || !filepath.IsAbs(rawTarget) || filepath.Clean(rawTarget) != rawTarget {
		return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot %s must be a clean non-empty absolute path", fieldName), nil)
	}
	if rawTarget == cleanData || !isPathWithin(rawTarget, cleanData) {
		return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot %s must reside strictly within data directory", fieldName), nil)
	}
	if isPathWithin(rawTarget, cleanSnap) {
		return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("snapshot %s must not point inside snapshot_dir", fieldName), nil)
	}

	parentDir := filepath.Dir(rawTarget)
	parentFD := dataFD
	if parentDir != cleanData {
		relParent, err := filepath.Rel(cleanData, parentDir)
		if err != nil || relParent == "" || relParent == "." || strings.HasPrefix(relParent, "..") {
			return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("invalid parent directory for %s", fieldName), err)
		}
		pfd, err := openRelSubdirNoFollow(dataFD, filepath.ToSlash(relParent))
		if err != nil {
			return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("parent directory for %s is missing or symlinked", fieldName), err)
		}
		parentFD = pfd
		defer func() { _ = unix.Close(parentFD) }()
	}

	var st unix.Stat_t
	if err := unix.Fstatat(parentFD, filepath.Base(rawTarget), &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
			return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("existing %s is not a regular file", fieldName), nil)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed to inspect %s", fieldName), err)
	}
	return nil
}

func verifyNoExtraSnapshotEntries(dirFD int, allowed map[string]struct{}) error {
	dupFD, err := unix.FcntlInt(uintptr(dirFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "dup snapshot dir fd", err)
	}
	f := os.NewFile(uintptr(dupFD), "snapshot_dir")
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, "failed reading snapshot_dir entries", err)
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return newSlotError(SlotCodeSnapshotInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("unexpected extra entry %q in snapshot_dir", entry.Name()), nil)
		}
	}
	return nil
}

func hashSnapshotFileAtNoFollow(dirFD int, name string, expectedSize int64, expectedManagerUID int) (string, int64, error) {
	if expectedSize < 0 {
		return "", 0, fmt.Errorf("%s expected size %d is negative", name, expectedSize)
	}
	var st unix.Stat_t
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", 0, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return "", 0, err
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s is not a regular file", name)
	}
	if uint64(st.Nlink) != 1 {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s has unexpected link count %d", name, st.Nlink)
	}
	if int(st.Uid) != RootUID && int(st.Uid) != expectedManagerUID {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s has untrusted owner uid %d", name, st.Uid)
	}
	if uint32(st.Mode)&0o077 != 0 {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s must not have group/other permissions", name)
	}
	if st.Size != expectedSize {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s size %d != expected %d", name, st.Size, expectedSize)
	}

	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()

	h := sha256.New()
	var src io.Reader = f
	if expectedSize < math.MaxInt64 {
		src = io.LimitReader(f, expectedSize+1)
	}
	n, err := io.Copy(h, src)
	if err != nil {
		return "", 0, err
	}
	if n != expectedSize {
		return "", 0, fmt.Errorf("%s size changed while hashing (%d != %d)", name, n, expectedSize)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func isValidCommitSHA64(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
