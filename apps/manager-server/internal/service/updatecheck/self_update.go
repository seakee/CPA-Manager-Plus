package updatecheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const DefaultSlotActivateTimeout = 3 * time.Minute

// PreSlotSnapshotFunc creates an online database snapshot before stopping
// processes for a slot switch and returns the snapshot directory and the
// active database schema generation.
type PreSlotSnapshotFunc func(ctx context.Context, targetVersion string) (snapshotDir string, currentSchemaGen int, err error)

// PreActivateFenceFunc engages the process-wide write/collector hold and drains
// in-flight mutations before taking the pre-slot SQLite snapshot. The returned
// release function is called only if snapshot creation or launcher activation
// fails; once launcher activation succeeds, the hold stays active until exit.
type PreActivateFenceFunc func(ctx context.Context) (release func(), err error)

type SelfUpdateStatus struct {
	Supported             bool                  `json:"supported"`
	State                 string                `json:"state"`
	ActiveSlot            string                `json:"active_slot"`
	BaseVersion           string                `json:"base_version"`
	BaseEpoch             int                   `json:"base_epoch,omitempty"`
	RunningVersion        string                `json:"running_version,omitempty"`
	ActiveAttemptID       string                `json:"active_attempt_id,omitempty"`
	LastAttemptID         string                `json:"last_attempt_id,omitempty"`
	RollbackAvailable     bool                  `json:"rollback_available"`
	RollbackTargetVersion string                `json:"rollback_target_version"`
	RollbackSameSchema    bool                  `json:"rollback_same_schema"`
	RollbackBlockedReason string                `json:"rollback_blocked_reason"`
	StagedSlots           []string              `json:"staged_slots"`
	PrepareOperation      *SlotPrepareOperation `json:"prepare_operation"`
	LastFailureCode       string                `json:"last_failure_code"`
	LastRolledBackSlot    string                `json:"last_rolled_back_slot"`
	CDNPresets            []string              `json:"cdn_presets"`
}

type PrepareSlotInput struct {
	Version   string `json:"version"`
	Source    string `json:"source"`
	CDNPrefix string `json:"cdn_prefix"`
}

func defaultSlotCDNPresets() []string {
	out := make([]string, len(DefaultSlotCDNPresets))
	copy(out, DefaultSlotCDNPresets)
	return out
}

// WithLauncherClient overrides the Launcher control client (intended for tests
// and custom wiring).
func (s *Service) WithLauncherClient(client *LauncherClient) *Service {
	s.SetLauncherClient(client)
	return s
}

// SetLauncherClient attaches or replaces the Launcher control client.
func (s *Service) SetLauncherClient(client *LauncherClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launcher = client
}

// WithPreSlotSnapshot configures the online SQLite snapshot callback invoked
// prior to slot activation.
func (s *Service) WithPreSlotSnapshot(fn PreSlotSnapshotFunc) *Service {
	s.SetPreSlotSnapshot(fn)
	return s
}

// SetPreSlotSnapshot configures the online SQLite snapshot callback invoked
// prior to slot activation.
func (s *Service) SetPreSlotSnapshot(fn PreSlotSnapshotFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preSlotSnapshot = fn
}

// WithPreActivateFence configures the pre-snapshot write and collector fence
// invoked before taking the pre-slot database snapshot.
func (s *Service) WithPreActivateFence(fn PreActivateFenceFunc) *Service {
	s.SetPreActivateFence(fn)
	return s
}

// SetPreActivateFence configures the pre-snapshot write and collector fence
// invoked before taking the pre-slot database snapshot.
func (s *Service) SetPreActivateFence(fn PreActivateFenceFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preActivateFence = fn
}

// WithCurrentSchemaGeneration configures the active SQLite schema generation
// reported to Launcher on activate and rollback.
func (s *Service) WithCurrentSchemaGeneration(gen int) *Service {
	s.SetCurrentSchemaGeneration(gen)
	return s
}

// SetCurrentSchemaGeneration configures the active SQLite schema generation
// reported to Launcher on activate and rollback.
func (s *Service) SetCurrentSchemaGeneration(gen int) {
	if gen < 1 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentSchemaGen = gen
}

func (s *Service) slotActivateTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activateTimeout > 0 {
		return s.activateTimeout
	}
	return DefaultSlotActivateTimeout
}

func (s *Service) slotSettings() (launcher *LauncherClient, currentVer, baseVer, activeSlot string, schemaGen int, fenceFn PreActivateFenceFunc, snapFn PreSlotSnapshotFunc, slotArch, releaseBase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schemaGen = s.currentSchemaGen
	if schemaGen < 1 {
		schemaGen = 1
	}
	slotArch = strings.TrimSpace(s.slotArch)
	if slotArch == "" {
		slotArch = "amd64"
	}
	releaseBase = strings.TrimRight(s.releaseBaseURL, "/")
	if releaseBase == "" {
		releaseBase = Repository
	}
	return s.launcher, s.current, s.baseImageVersion, s.activeSlot, schemaGen, s.preActivateFence, s.preSlotSnapshot, slotArch, releaseBase
}

func (s *Service) attachSelfUpdateStatus(ctx context.Context, out Status) Status {
	launcher, _, baseVer, activeSlot, schemaGen, _, _, _, _ := s.slotSettings()
	if baseVer != "" {
		out.BaseImageVersion = baseVer
	}
	if activeSlot != "" {
		out.ActiveSlot = activeSlot
	}
	if launcher == nil {
		return out
	}
	statusCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		statusCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	ls, err := launcher.Status(statusCtx)
	if err != nil {
		out.SelfUpdate = &SelfUpdateStatus{
			Supported:       false,
			BaseVersion:     baseVer,
			ActiveSlot:      activeSlot,
			StagedSlots:     []string{},
			LastFailureCode: "launcher_unavailable",
			CDNPresets:      defaultSlotCDNPresets(),
		}
		return out
	}
	su := buildSelfUpdateStatus(ls, baseVer, schemaGen)
	if su.BaseVersion != "" && out.BaseImageVersion == "" {
		out.BaseImageVersion = su.BaseVersion
	}
	if su.ActiveSlot != "" {
		out.ActiveSlot = su.ActiveSlot
	} else if su.State == "none" {
		out.ActiveSlot = ""
	}
	out.SelfUpdate = &su
	return out
}

func buildSelfUpdateStatus(ls LauncherSlotStatus, fallbackBaseVer string, currentSchemaGen int) SelfUpdateStatus {
	staged := append([]string(nil), ls.StagedSlots...)
	if staged == nil {
		staged = []string{}
	}
	baseVer := strings.TrimSpace(ls.BaseVersion)
	if baseVer == "" {
		baseVer = fallbackBaseVer
	}
	rollbackAvailable := ls.RollbackAvailable
	rollbackSameSchema := ls.RollbackSameSchema
	rollbackBlockedReason := strings.TrimSpace(ls.RollbackBlockedReason)
	if ls.State == "recovery_required" {
		rollbackAvailable = false
	}
	if ls.State == "committed" && rollbackSameSchema && ls.PreviousSchemaGeneration > 0 && currentSchemaGen > 0 && ls.PreviousSchemaGeneration != currentSchemaGen {
		rollbackSameSchema = false
		rollbackAvailable = false
		rollbackBlockedReason = "schema_rollback_forbidden"
	}
	return SelfUpdateStatus{
		Supported:             ls.Supported,
		State:                 ls.State,
		ActiveSlot:            ls.ActiveSlot,
		BaseVersion:           baseVer,
		BaseEpoch:             ls.BaseEpoch,
		RunningVersion:        ls.RunningVersion,
		ActiveAttemptID:       strings.TrimSpace(ls.ActiveAttemptID),
		LastAttemptID:         strings.TrimSpace(ls.LastAttemptID),
		RollbackAvailable:     rollbackAvailable,
		RollbackTargetVersion: ls.RollbackTargetVersion,
		RollbackSameSchema:    rollbackSameSchema,
		RollbackBlockedReason: rollbackBlockedReason,
		StagedSlots:           staged,
		PrepareOperation:      ls.PrepareOperation,
		LastFailureCode:       ls.LastFailureCode,
		LastRolledBackSlot:    ls.LastRolledBackSlot,
		CDNPresets:            defaultSlotCDNPresets(),
	}
}

func validateCanonicalTag(raw string) (string, error) {
	tag := strings.TrimSpace(raw)
	if tag == "" || !strings.HasPrefix(tag, "v") || strings.Contains(tag, "+") {
		return "", &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "version must be a canonical release tag starting with v",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	v, err := ParseVersion(tag)
	if err != nil || v.Stage == "" {
		return "", &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "version is not a valid release version",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return tag, nil
}

func findSlotAssetForPlatform(info ReleaseInfo, platform string) (SlotAsset, bool) {
	if info.Distribution.Slot == nil {
		return SlotAsset{}, false
	}
	for _, asset := range info.Distribution.Slot.Assets {
		if asset.Platform == platform {
			return asset, true
		}
	}
	return SlotAsset{}, false
}

func (s *Service) PrepareSlot(ctx context.Context, input PrepareSlotInput) (SelfUpdateStatus, error) {
	launcher, _, baseVer, _, schemaGen, _, _, slotArch, releaseBase := s.slotSettings()
	if launcher == nil {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "self_update_unsupported",
			Message:    "self-update launcher is not configured",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	version, err := validateCanonicalTag(input.Version)
	if err != nil {
		return SelfUpdateStatus{}, err
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "github"
	}
	if source != "github" && source != "cdn" {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "source must be github or cdn",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	cdnPrefix := strings.TrimSpace(input.CDNPrefix)
	if source == "github" && cdnPrefix != "" {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "cdn_prefix is only permitted when source is cdn",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if source == "cdn" && !validSlotCDNPrefix(cdnPrefix) {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "cdn_prefix must be a valid public https URL",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	s.mu.Lock()
	if err := s.load(ctx); err != nil {
		s.mu.Unlock()
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "failed to load update check state",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	st := s.snapshot()
	s.mu.Unlock()

	if st.Stale || st.LastError != "" || st.State != "update_available" || st.Target == nil {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "version_conflict",
			Message:    "no verified direct update candidate is currently available",
			HTTPStatus: http.StatusConflict,
		}
	}
	if st.Target.Release.Version != version {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "version_conflict",
			Message:    fmt.Sprintf("requested version %s does not match active target %s", version, st.Target.Release.Version),
			HTTPStatus: http.StatusConflict,
		}
	}
	if st.UpgradeAction != "direct" || st.Target.Update.Breaking || st.Target.Update.MigrationRequired {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "migration_required",
			Message:    "target release requires following the manual migration guide",
			HTTPStatus: http.StatusUnprocessableEntity,
		}
	}

	platform := "linux/" + slotArch
	asset, ok := findSlotAssetForPlatform(*st.Target, platform)
	if !ok {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "slot_asset_unavailable",
			Message:    fmt.Sprintf("target release %s does not provide a signed slot archive for %s", version, platform),
			HTTPStatus: http.StatusUnprocessableEntity,
		}
	}

	officialURL := releaseBase + "/releases/download/" + url.PathEscape(version) + "/" + url.PathEscape(asset.Name)
	archiveURL := officialURL
	if source == "cdn" {
		archiveURL = strings.TrimRight(cdnPrefix, "/") + "/" + officialURL
	}

	op, err := launcher.Prepare(ctx, LauncherPrepareRequest{
		TargetVersion:  version,
		ArchiveURL:     archiveURL,
		ExpectedSHA256: asset.SHA256,
		ExpectedSize:   asset.Size,
	})
	if err != nil {
		return SelfUpdateStatus{}, err
	}
	if ls, statusErr := launcher.Status(ctx); statusErr == nil {
		return buildSelfUpdateStatus(ls, baseVer, schemaGen), nil
	}
	return fallbackStatusFromOp(baseVer, op), nil
}

func (s *Service) UploadSlot(ctx context.Context, version string, body io.Reader, contentLength int64) (SelfUpdateStatus, error) {
	launcher, _, baseVer, _, schemaGen, _, _, slotArch, _ := s.slotSettings()
	if launcher == nil {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "self_update_unsupported",
			Message:    "self-update launcher is not configured",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	targetVersion, err := validateCanonicalTag(version)
	if err != nil {
		return SelfUpdateStatus{}, err
	}
	if body == nil || contentLength == 0 {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "slot archive body is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if contentLength > MaxSlotArchiveBytes {
		return SelfUpdateStatus{}, &SelfUpdateError{
			Code:       "archive_too_large",
			Message:    "slot archive exceeds 192 MiB limit",
			HTTPStatus: http.StatusRequestEntityTooLarge,
		}
	}

	var expectedSHA256 string
	s.mu.Lock()
	if loadErr := s.load(ctx); loadErr == nil {
		st := s.snapshot()
		var matched *ReleaseInfo
		if st.Target != nil && st.Target.Release.Version == targetVersion {
			matched = st.Target
		} else if info, ok := s.state.Releases[targetVersion]; ok {
			matched = &info
		}
		if matched != nil {
			if matched.Update.Breaking || matched.Update.MigrationRequired {
				s.mu.Unlock()
				return SelfUpdateStatus{}, &SelfUpdateError{
					Code:       "migration_required",
					Message:    "target release requires following the manual migration guide",
					HTTPStatus: http.StatusUnprocessableEntity,
				}
			}
			if st.Target != nil && st.Target.Release.Version == targetVersion && st.UpgradeAction == "migration_guide" {
				s.mu.Unlock()
				return SelfUpdateStatus{}, &SelfUpdateError{
					Code:       "migration_required",
					Message:    "target release requires following the manual migration guide",
					HTTPStatus: http.StatusUnprocessableEntity,
				}
			}
			if asset, ok := findSlotAssetForPlatform(*matched, "linux/"+slotArch); ok {
				expectedSHA256 = asset.SHA256
			}
		}
	}
	s.mu.Unlock()

	op, err := launcher.PrepareUpload(ctx, targetVersion, expectedSHA256, body, contentLength)
	if err != nil {
		return SelfUpdateStatus{}, err
	}
	if ls, statusErr := launcher.Status(ctx); statusErr == nil {
		return buildSelfUpdateStatus(ls, baseVer, schemaGen), nil
	}
	return fallbackStatusFromOp(baseVer, op), nil
}

func fallbackStatusFromOp(baseVer string, op SlotPrepareOperation) SelfUpdateStatus {
	staged := []string{}
	if op.Phase == "succeeded" && op.TargetVersion != "" {
		staged = []string{op.TargetVersion}
	}
	return SelfUpdateStatus{
		Supported:        true,
		BaseVersion:      baseVer,
		StagedSlots:      staged,
		PrepareOperation: &op,
		CDNPresets:       defaultSlotCDNPresets(),
	}
}

func generateSlotAttemptID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "attempt-" + hex.EncodeToString(buf[:]), nil
}

func (s *Service) ActivateSlot(ctx context.Context, version string) (SlotActionAccepted, error) {
	launcher, currentVer, _, _, schemaGen, fenceFn, preSnapFn, _, _ := s.slotSettings()
	if launcher == nil {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "self_update_unsupported",
			Message:    "self-update launcher is not configured",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	targetVersion, err := validateCanonicalTag(version)
	if err != nil {
		return SlotActionAccepted{}, err
	}
	if !s.activating.CompareAndSwap(false, true) {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "slot_busy",
			Message:    "slot activation is already in progress",
			HTTPStatus: http.StatusConflict,
		}
	}
	defer s.activating.Store(false)

	ls, err := launcher.Status(ctx)
	if err != nil {
		return SlotActionAccepted{}, err
	}
	if !slices.Contains(ls.StagedSlots, targetVersion) {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "slot_not_staged",
			Message:    fmt.Sprintf("slot %s is not staged", targetVersion),
			HTTPStatus: http.StatusConflict,
		}
	}
	runningVer := strings.TrimSpace(ls.RunningVersion)
	if runningVer == "" {
		runningVer = strings.TrimSpace(currentVer)
	}
	if CompareSemVer(targetVersion, runningVer) <= 0 {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "version_conflict",
			Message:    fmt.Sprintf("target slot %s must be newer than running version %s", targetVersion, runningVer),
			HTTPStatus: http.StatusConflict,
		}
	}

	attemptID, err := generateSlotAttemptID()
	if err != nil {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "internal_error",
			Message:    "failed to generate slot activation attempt id",
			HTTPStatus: http.StatusInternalServerError,
		}
	}

	var releaseFence func()
	if fenceFn != nil {
		var fenceErr error
		releaseFence, fenceErr = fenceFn(ctx)
		if fenceErr != nil {
			return SlotActionAccepted{}, &SelfUpdateError{
				Code:       "slot_busy",
				Message:    fmt.Sprintf("failed to quiesce in-flight writes before slot snapshot: %v", fenceErr),
				HTTPStatus: http.StatusConflict,
			}
		}
	}

	var snapshotDir string
	if preSnapFn != nil {
		var snapGen int
		var snapErr error
		snapshotDir, snapGen, snapErr = preSnapFn(ctx, targetVersion)
		if snapErr != nil || strings.TrimSpace(snapshotDir) == "" {
			if releaseFence != nil {
				releaseFence()
			}
			msg := "failed to create pre-slot database snapshot"
			if snapErr != nil {
				msg = snapErr.Error()
			}
			return SlotActionAccepted{}, &SelfUpdateError{
				Code:       "snapshot_failed",
				Message:    msg,
				HTTPStatus: http.StatusInternalServerError,
			}
		}
		if snapGen >= 1 {
			schemaGen = snapGen
		}
	}
	actCtx, cancelAct := context.WithTimeout(context.WithoutCancel(ctx), s.slotActivateTimeout())
	defer cancelAct()
	accepted, err := launcher.Activate(actCtx, LauncherActivateRequest{
		TargetVersion:            targetVersion,
		SnapshotDir:              strings.TrimSpace(snapshotDir),
		PreviousSchemaGeneration: schemaGen,
		AttemptID:                attemptID,
	})
	if err != nil {
		var suErr *SelfUpdateError
		if errors.As(err, &suErr) && suErr != nil && suErr.DefiniteRejection {
			cleanupOrphanSnapshotDir(snapshotDir)
			if releaseFence != nil {
				releaseFence()
			}
			return SlotActionAccepted{}, err
		}
		abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		abortRes, abortErr := launcher.AbortAttempt(abortCtx, attemptID)
		cancelAbort()
		if abortErr == nil && strings.TrimSpace(abortRes.AttemptID) == attemptID {
			switch strings.TrimSpace(abortRes.Outcome) {
			case LauncherAbortOutcomeAccepted:
				return SlotActionAccepted{
					Accepted:            true,
					TargetVersion:       targetVersion,
					TrialTimeoutSeconds: 60,
					AttemptID:           attemptID,
				}, nil
			case LauncherAbortOutcomeAborted:
				if releaseFence != nil {
					releaseFence()
				}
				return SlotActionAccepted{}, err
			}
		}
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:              "launcher_unavailable",
			Message:           "launcher activation outcome could not be confirmed after dropped response",
			HTTPStatus:        http.StatusBadGateway,
			DefiniteRejection: false,
		}
	}
	if accepted.TargetVersion == "" {
		accepted.TargetVersion = targetVersion
	}
	if accepted.AttemptID == "" {
		accepted.AttemptID = attemptID
	}
	if accepted.Accepted && accepted.TrialTimeoutSeconds <= 0 {
		accepted.TrialTimeoutSeconds = 60
	}
	return accepted, nil
}

var allowedOrphanSnapshotMembers = map[string]bool{
	"manifest.json":    true,
	"database":         true,
	"database-wal":     true,
	"database-shm":     true,
	"database-journal": true,
	"data-key":         true,
}

func cleanupOrphanSnapshotDir(snapshotDir string) {
	trimmed := strings.TrimSpace(snapshotDir)
	if trimmed == "" {
		return
	}
	absDir, err := filepath.Abs(trimmed)
	if err != nil {
		return
	}
	cleanDir := filepath.Clean(absDir)
	info, err := os.Lstat(cleanDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !allowedOrphanSnapshotMembers[entry.Name()] || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return
		}
	}
	for _, entry := range entries {
		_ = os.Remove(filepath.Join(cleanDir, entry.Name()))
	}
	_ = os.Remove(cleanDir)
}

func (s *Service) RollbackSlot(ctx context.Context, expectedActiveSlot string) (SlotActionAccepted, error) {
	launcher, _, _, _, schemaGen, _, _, _, _ := s.slotSettings()
	if launcher == nil {
		return SlotActionAccepted{}, &SelfUpdateError{
			Code:       "self_update_unsupported",
			Message:    "self-update launcher is not configured",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	activeSlot, err := validateCanonicalTag(expectedActiveSlot)
	if err != nil {
		return SlotActionAccepted{}, err
	}
	return launcher.Rollback(ctx, LauncherRollbackRequest{
		ExpectedActiveSlot:      activeSlot,
		CurrentSchemaGeneration: schemaGen,
	})
}

func validSlotCDNPrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) > 512 || !strings.HasPrefix(prefix, "https://") ||
		strings.ContainsAny(prefix, `\?#@`) || strings.IndexFunc(prefix, func(r rune) bool {
		return r < 0x20 || r == 0x7f || unicode.IsSpace(r)
	}) >= 0 {
		return false
	}
	parsed, err := url.Parse(prefix)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Hostname() == "" ||
		strings.HasSuffix(parsed.Host, ":") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return false
		}
	}
	if !validSlotCDNHost(parsed.Hostname()) {
		return false
	}
	lowerRawPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(lowerRawPath, "%2e") || strings.Contains(lowerRawPath, "%2f") {
		return false
	}
	cleanedPath := strings.TrimRight(parsed.Path, "/")
	if strings.Contains(cleanedPath, "//") {
		return false
	}
	for segment := range strings.SplitSeq(cleanedPath, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validSlotCDNHost(host string) bool {
	if addr, err := netip.ParseAddr(host); err == nil {
		return isPublicSlotCDNAddr(addr)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.ContainsRune(host, ':') {
		return false
	}
	last := host[strings.LastIndexByte(host, '.')+1:]
	return last != "" && !strings.HasPrefix(last, "0x") && strings.Trim(last, "0123456789") != ""
}

var (
	slotCDNNAT64           = netip.MustParsePrefix("64:ff9b::/96")
	slotCDNIPv6Global      = netip.MustParsePrefix("2000::/3")
	slotCDNIPv4AnycastPCP  = netip.MustParseAddr("192.0.0.9")
	slotCDNIPv4AnycastTURN = netip.MustParseAddr("192.0.0.10")
	slotCDNBlockedLiterals = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("5f00::/16"),
	}
)

func isPublicSlotCDNAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if slotCDNNAT64.Contains(addr) {
		raw := addr.As16()
		addr = netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]})
	}
	if !addr.Is4() && !slotCDNIPv6Global.Contains(addr) {
		return false
	}
	if addr == slotCDNIPv4AnycastPCP || addr == slotCDNIPv4AnycastTURN {
		return true
	}
	for _, blocked := range slotCDNBlockedLiterals {
		if blocked.Contains(addr) {
			return false
		}
	}
	return true
}
