package upgradewizard

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/artifactproxy"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// ArtifactRuntime is the Manager-owned Supervisor artifact boundary used by the
// Upgrade wizard while an Upgrade operation is recorded and unfinished.
type ArtifactRuntime = artifactproxy.ArtifactRuntime

// CodeArtifactUnavailable is returned when the caller asks for the runtime
// artifact endpoints without a recorded, unfinished Upgrade operation bound to
// the authenticated source.
const CodeArtifactUnavailable = "upgrade_artifact_unavailable"

const (
	sourceKindUpgradedLegacy    = "upgraded-legacy"
	artifactProofCacheEntries   = 16
	artifactProofFlightLimit    = 4
	artifactProofBytesPerSecond = 64 << 20
	artifactProofMaxBudget      = 5 * time.Minute
)

var (
	errArtifactProofMismatch   = errors.New("upgrade artifact source provenance mismatch")
	errArtifactProofTimeout    = errors.New("upgrade artifact source provenance timed out")
	errArtifactSourceUnstamped = errors.New("upgrade artifact source has no change timestamp")
	// Hooks for deterministic testing of cold reads, cache hits, and timeouts.
	artifactIdentifySourceTree = upgradeservice.IdentifySourceTree
	artifactSourceFingerprint  = upgradeservice.SourceFingerprint

	defaultArtifactProofCache = newArtifactProofCache()
)

func matchArtifactSuffix(path string) (string, bool) {
	switch {
	case strings.HasSuffix(path, ArtifactStatusSuffix) && path != ArtifactStatusSuffix:
		return artifactproxy.ResourceStatus, true
	case strings.HasSuffix(path, ArtifactProbeSuffix) && path != ArtifactProbeSuffix:
		return artifactproxy.ResourceProbe, true
	case strings.HasSuffix(path, ArtifactAcquireSuffix) && path != ArtifactAcquireSuffix:
		return artifactproxy.ResourceAcquire, true
	case strings.HasSuffix(path, ArtifactUploadSuffix) && path != ArtifactUploadSuffix:
		return artifactproxy.ResourceUpload, true
	default:
		return "", false
	}
}

func artifactResourceMethod(resource string) string {
	switch resource {
	case artifactproxy.ResourceStatus:
		return http.MethodGet
	case artifactproxy.ResourceProbe, artifactproxy.ResourceAcquire:
		return http.MethodPost
	case artifactproxy.ResourceUpload:
		return http.MethodPut
	default:
		return ""
	}
}

func (h *Handler) serveArtifact(w http.ResponseWriter, r *http.Request, mount mountResolution, resource string) {
	expectedMethod := artifactResourceMethod(resource)
	if expectedMethod == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != expectedMethod {
		w.Header().Set("Allow", expectedMethod)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.requireUnfinishedOperation(w, r, mount) {
		return
	}
	artifactproxy.ServeResource(w, r, h.Artifact, resource)
}

type artifactOperationProvenance struct {
	recorded          bool
	evidence          string
	stage             string
	stageKnown        bool
	domainsDone       []string
	sourceKind        string
	sourceRoot        string
	fingerprintBefore string
	fingerprintAfter  string
	backupManifest    string
}

func (h *Handler) artifactTimeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return DefaultTimeout
}

func (h *Handler) requireUnfinishedOperation(w http.ResponseWriter, r *http.Request, mount mountResolution) bool {
	database := h.managerDatabase()
	if database == "" {
		artifactproxy.WriteError(w, http.StatusConflict, CodeArtifactUnavailable)
		return false
	}
	baseTimeout := h.artifactTimeout()
	sqlCtx, cancel := context.WithTimeout(r.Context(), baseTimeout)
	prov, err := readArtifactProvenance(sqlCtx, database)
	cancel()
	if err != nil {
		if errors.Is(err, errStatusRowInvalid) {
			writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeOperationInvalid)
			return false
		}
		writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return false
	}
	if !prov.recorded || !prov.stageKnown || prov.stage == "" ||
		prov.stage == StagePreflight || prov.stage == StageComplete || prov.stage == StageRolledBack {
		artifactproxy.WriteError(w, http.StatusConflict, CodeArtifactUnavailable)
		return false
	}
	if err := verifyProvenanceMatchesAuthenticatedSource(r.Context(), baseTimeout, prov, mount); err != nil {
		switch {
		case errors.Is(err, errArtifactProofMismatch):
			artifactproxy.WriteError(w, http.StatusConflict, CodeArtifactUnavailable)
		case errors.Is(err, errArtifactProofTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeCredentialTimeout)
		case errors.Is(err, upgradeservice.ErrSourceSymlinked):
			writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeSourceSymlinked)
		case errors.Is(err, errArtifactSourceUnstamped):
			writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeSourceUnstamped)
		default:
			writeArtifactServiceError(w, http.StatusServiceUnavailable, CodeSourceUnreadable)
		}
		return false
	}
	return true
}

func writeArtifactServiceError(w http.ResponseWriter, status int, code string) {
	type errorCode struct {
		Code string `json:"code"`
	}
	writeJSON(w, status, struct {
		ContractVersion string    `json:"contractVersion"`
		Code            string    `json:"code"`
		Error           errorCode `json:"error"`
	}{
		ContractVersion: Contract,
		Code:            code,
		Error:           errorCode{Code: code},
	})
}
func verifyProvenanceMatchesAuthenticatedSource(
	ctx context.Context,
	baseTimeout time.Duration,
	prov artifactOperationProvenance,
	mount mountResolution,
) error {
	if prov.sourceKind != sourceKindUpgradedLegacy || !slices.Contains(prov.domainsDone, domainSourceCopy) {
		return errArtifactProofMismatch
	}
	if !isSHA256Hex(prov.fingerprintBefore) || prov.backupManifest == "" {
		return errArtifactProofMismatch
	}
	switch prov.evidence {
	case EvidenceCopyInMarker:
		if prov.fingerprintAfter != "" && !isSHA256Hex(prov.fingerprintAfter) {
			return errArtifactProofMismatch
		}
	case EvidenceOperationRow:
		if !isSHA256Hex(prov.fingerprintAfter) {
			return errArtifactProofMismatch
		}
	default:
		return errArtifactProofMismatch
	}

	if mount.database != "" {
		// The credential was proven against the mounted 1.x source at mount.root.
		// Bind this request to the operation's recorded 1.x source root and
		// 64-hex SHA-256 source fingerprint.
		mountedRoot := strings.TrimSpace(mount.root)
		if mountedRoot == "" {
			return errArtifactProofMismatch
		}
		canonicalMounted := canonicalSourcePath(mountedRoot)
		if prov.evidence == EvidenceOperationRow {
			recordedRoot := strings.TrimSpace(prov.sourceRoot)
			if recordedRoot == "" || canonicalSourcePath(recordedRoot) != canonicalMounted {
				return errArtifactProofMismatch
			}
		}
		return defaultArtifactProofCache.verify(
			ctx,
			baseTimeout,
			canonicalMounted,
			mountedRoot,
			prov.fingerprintBefore,
			prov.fingerprintAfter,
		)
	}

	// The credential was proven against the Manager store fallback. The store
	// itself must be the recorded copy-in target of this operation.
	if prov.evidence == EvidenceOperationRow &&
		mount.rootKnown && strings.TrimSpace(mount.root) != "" &&
		mount.code != CodeLegacyRootMissing && strings.TrimSpace(prov.sourceRoot) != "" {
		if canonicalSourcePath(prov.sourceRoot) != canonicalSourcePath(mount.root) {
			return errArtifactProofMismatch
		}
	}
	return nil
}

func artifactProofBudget(base time.Duration, totalBytes int64) time.Duration {
	budget := base
	if budget <= 0 {
		budget = DefaultTimeout
	}
	if totalBytes > 0 {
		seconds := totalBytes / artifactProofBytesPerSecond
		if seconds >= int64(artifactProofMaxBudget/time.Second) {
			return artifactProofMaxBudget
		}
		budget += time.Duration(seconds) * time.Second
	}
	if budget > artifactProofMaxBudget {
		budget = artifactProofMaxBudget
	}
	return budget
}

type artifactProofFlight struct {
	done chan struct{}
	err  error
}

func (f *artifactProofFlight) wait(ctx context.Context) error {
	select {
	case <-f.done:
		if f.err == nil {
			if err := ctx.Err(); err != nil {
				return errArtifactProofTimeout
			}
		}
		return f.err
	case <-ctx.Done():
		return errArtifactProofTimeout
	}
}

// artifactProofCache bounds repeated full-source SHA-256 proofs across browser
// status/probe/acquire/upload calls. Positive matches are cached in entries
// keyed by canonicalRoot + expected digests + change-stamped metadata Key;
// sources without change timestamps fail closed with errArtifactSourceUnstamped,
// and failures or mismatches are never stored.
type artifactProofCache struct {
	mu      sync.Mutex
	entries map[string]struct{}
	order   []string
	flights map[string]*artifactProofFlight
	slots   chan struct{}
}

func newArtifactProofCache() *artifactProofCache {
	return &artifactProofCache{
		entries: make(map[string]struct{}, artifactProofCacheEntries),
		flights: make(map[string]*artifactProofFlight),
		slots:   make(chan struct{}, artifactProofFlightLimit),
	}
}

func (c *artifactProofCache) verify(
	ctx context.Context,
	baseTimeout time.Duration,
	canonicalRoot string,
	mountedRoot string,
	expectedBefore string,
	expectedAfter string,
) error {
	if err := ctx.Err(); err != nil {
		return errArtifactProofTimeout
	}
	metaCtx, metaCancel := context.WithTimeout(ctx, baseTimeout)
	meta, err := artifactIdentifySourceTree(metaCtx, mountedRoot)
	metaTimedOut := metaCtx.Err() != nil
	metaCancel()
	if metaTimedOut {
		return errArtifactProofTimeout
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return errArtifactProofTimeout
		}
		return err
	}
	if meta.Uncacheable || meta.Key == "" {
		return errArtifactSourceUnstamped
	}
	budget := artifactProofBudget(baseTimeout, meta.TotalBytes)
	cacheKey := canonicalRoot + "@" + expectedBefore + ":" + expectedAfter + "@" + meta.Key

	for {
		if err := ctx.Err(); err != nil {
			return errArtifactProofTimeout
		}
		c.mu.Lock()
		if _, ok := c.entries[cacheKey]; ok {
			c.mu.Unlock()
			return nil
		}
		if flight, ok := c.flights[cacheKey]; ok {
			c.mu.Unlock()
			return flight.wait(ctx)
		}
		select {
		case c.slots <- struct{}{}:
			flight := &artifactProofFlight{done: make(chan struct{})}
			c.flights[cacheKey] = flight
			c.mu.Unlock()
			go c.runFlight(cacheKey, flight, budget, mountedRoot, expectedBefore, expectedAfter)
			return flight.wait(ctx)
		default:
			c.mu.Unlock()
		}

		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			return errArtifactProofTimeout
		}
		c.mu.Lock()
		if _, ok := c.entries[cacheKey]; ok {
			c.mu.Unlock()
			<-c.slots
			return nil
		}
		if flight, ok := c.flights[cacheKey]; ok {
			c.mu.Unlock()
			<-c.slots
			return flight.wait(ctx)
		}
		flight := &artifactProofFlight{done: make(chan struct{})}
		c.flights[cacheKey] = flight
		c.mu.Unlock()
		go c.runFlight(cacheKey, flight, budget, mountedRoot, expectedBefore, expectedAfter)
		return flight.wait(ctx)
	}
}

func (c *artifactProofCache) runFlight(
	cacheKey string,
	flight *artifactProofFlight,
	budget time.Duration,
	mountedRoot string,
	expectedBefore string,
	expectedAfter string,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.mu.Lock()
			delete(c.flights, cacheKey)
			flight.err = errors.New("source provenance proof panicked")
			close(flight.done)
			c.mu.Unlock()
			<-c.slots
			panic(recovered)
		}
	}()

	flightCtx, cancel := context.WithTimeout(context.Background(), budget)
	digest, err := artifactSourceFingerprint(flightCtx, mountedRoot)
	flightTimedOut := flightCtx.Err() != nil
	cancel()

	var resultErr error
	switch {
	case err != nil:
		if flightTimedOut || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			resultErr = errArtifactProofTimeout
		} else {
			resultErr = err
		}
	case digest == "":
		resultErr = errArtifactProofMismatch
	case (expectedAfter != "" && digest == expectedAfter) || digest == expectedBefore:
		resultErr = nil
	default:
		resultErr = errArtifactProofMismatch
	}

	c.mu.Lock()
	if resultErr == nil {
		c.rememberLocked(cacheKey)
	}
	delete(c.flights, cacheKey)
	flight.err = resultErr
	close(flight.done)
	c.mu.Unlock()
	<-c.slots
}

func (c *artifactProofCache) rememberLocked(key string) {
	if _, exists := c.entries[key]; exists {
		return
	}
	c.entries[key] = struct{}{}
	c.order = append(c.order, key)
	if len(c.order) > artifactProofCacheEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func readArtifactProvenance(ctx context.Context, databasePath string) (artifactOperationProvenance, error) {
	marker, inFlight, err := readCopyInMarker(databasePath)
	if err != nil {
		return artifactOperationProvenance{}, err
	}
	if inFlight && marker.AwaitingCommit(databasePath) {
		return provenanceFromMarker(marker), nil
	}
	recorded, found, err := readRecordedArtifactProvenance(ctx, databasePath)
	if err != nil {
		return artifactOperationProvenance{}, err
	}
	if found {
		return recorded, nil
	}
	if inFlight {
		return provenanceFromMarker(marker), nil
	}
	return artifactOperationProvenance{stageKnown: true}, nil
}

func provenanceFromMarker(marker copyinmarker.Marker) artifactOperationProvenance {
	return artifactOperationProvenance{
		recorded:          true,
		evidence:          EvidenceCopyInMarker,
		stage:             StageCopyIn,
		stageKnown:        true,
		domainsDone:       []string{domainSourceCopy},
		sourceKind:        marker.SourceKind,
		fingerprintBefore: strings.TrimSpace(marker.SourceFingerprintBefore),
		fingerprintAfter:  strings.TrimSpace(marker.SourceFingerprintAfter),
		backupManifest:    strings.TrimSpace(marker.BackupManifestSHA256),
	}
}

func readRecordedArtifactProvenance(ctx context.Context, databasePath string) (artifactOperationProvenance, bool, error) {
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return artifactOperationProvenance{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return artifactOperationProvenance{}, false, nil
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath))
	if err != nil {
		return artifactOperationProvenance{}, false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return artifactOperationProvenance{}, false, err
	}
	if present == 0 {
		return artifactOperationProvenance{}, false, nil
	}
	var (
		row               statusRow
		sourceRoot        sql.NullString
		fingerprintBefore sql.NullString
		fingerprintAfter  sql.NullString
		backupManifest    sql.NullString
	)
	err = db.QueryRowContext(ctx, `select stage, domains_done, source_kind, source_version,
		source_root, source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms
		from upgrade_operations where id = 1`).Scan(
		&row.Stage, &row.Domains, &row.SourceKind, &row.SourceVersion,
		&sourceRoot, &fingerprintBefore, &fingerprintAfter, &backupManifest,
		&row.Reconciled, &row.ManualItems, &row.ErrorCode, &row.ErrorDetail, &row.CreatedAtMS, &row.UpdatedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return artifactOperationProvenance{}, false, nil
	}
	if err != nil {
		return artifactOperationProvenance{}, false, err
	}
	if err := validateStatusRow(row); err != nil {
		return artifactOperationProvenance{}, false, err
	}
	domains, err := decodeStringColumn(row.Domains, "domains_done")
	if err != nil {
		return artifactOperationProvenance{}, false, err
	}
	if _, err := decodeStringColumn(row.ManualItems, "manual_items"); err != nil {
		return artifactOperationProvenance{}, false, err
	}
	if _, err := reconciliationSummary(row.Reconciled); err != nil {
		return artifactOperationProvenance{}, false, err
	}
	return artifactOperationProvenance{
		recorded:          true,
		evidence:          EvidenceOperationRow,
		stage:             row.Stage,
		stageKnown:        knownStage(row.Stage),
		domainsDone:       domains,
		sourceKind:        strings.TrimSpace(row.SourceKind.String),
		sourceRoot:        strings.TrimSpace(sourceRoot.String),
		fingerprintBefore: strings.TrimSpace(fingerprintBefore.String),
		fingerprintAfter:  strings.TrimSpace(fingerprintAfter.String),
		backupManifest:    strings.TrimSpace(backupManifest.String),
	}, true, nil
}

// canonicalSourcePath resolves a source directory path the same way
// upgradeservice.canonicalPath does (resolving symlinks of the deepest existing
// ancestor) so relative or symlinked spellings of the same root match.
func canonicalSourcePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	current := absolute
	missing := []string{}
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absolute
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}
