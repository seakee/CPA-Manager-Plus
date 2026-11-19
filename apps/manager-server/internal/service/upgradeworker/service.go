package upgradeworker

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgrade "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	SourceEnvironment     = "CPAMP_UPGRADE_SOURCE_ROOT"
	CPARootEnvironment    = "CPAMP_UPGRADE_CPA_ROOT"
	CPAAuthEnvironment    = "CPAMP_UPGRADE_CPA_AUTH_ROOT"
	CPAVersionEnvironment = "CPAMP_UPGRADE_CPA_VERSION"
	BundleDirectory       = "/upgrade/backup/bundle"
	ManagerUID            = 10002
	ManagerGID            = 10002
	defaultJobTimeout     = 30 * time.Minute
)

var errAdminStoreUnavailable = errors.New("upgrade_store_unavailable")
var readReconcilePostRunSnapshot = upgrade.ReadRecordFromSnapshot

type Options struct {
	Config          config.Config
	TargetRoot      string
	SourceRoot      string
	CPARoot         string
	CPAAuthRoot     string
	CPAVersion      string
	PublicOrigin    string
	RestartParent   func() error
	ValidateRestart func() error
	ImportRuntime   upgrade.CPAImportRuntime
	VerifyRuntime   upgrade.VerifyRuntime

	// Test seams (nil/empty in production; worker CLI never exposes these to callers).
	BundleDir                  string
	JobTimeout                 time.Duration
	RepairOwnership            func(string) error
	BeforeExternalSettingsSync func()
}

func (o Options) bundleDir() string {
	if trimmed := strings.TrimSpace(o.BundleDir); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	return BundleDirectory
}

func (o Options) jobTimeout() time.Duration {
	if o.JobTimeout > 0 {
		return o.JobTimeout
	}
	return defaultJobTimeout
}

func (o Options) repairTarget() error {
	if o.RepairOwnership != nil {
		return o.RepairOwnership(o.TargetRoot)
	}
	return RepairTargetOwnership(o.TargetRoot)
}

func (o Options) dataKeyPath() string {
	if trimmed := strings.TrimSpace(o.Config.DataKeyPath); trimmed != "" {
		return trimmed
	}
	return upgrade.SourceDataKeyPath(upgrade.TargetDatabasePath(o.TargetRoot))
}

type Service struct {
	options                   Options
	instanceHMAC              [32]byte
	recordedSourceFingerprint string
	operationRevision         string

	mu        sync.Mutex
	execution Execution
	// mutation serializes every write, including routing and artifact mutations.
	mutation                    sync.Mutex
	jobs                        sync.WaitGroup
	ctx                         context.Context
	inner                       http.Handler
	activationRequested         bool
	reconcileDays               []string
	reconcileHasParityGaps      bool
	reconcileDaysRevision       int64
	lastSourceRevision          string
	fallbackRevision            string
	verifiedFingerprintRevision string
}

// InspectRequired never opens the target in place or initializes an installation.
// It returns (true, nil) for a valid unfinished upgrade operation, (false, nil)
// only when Verify proves authoritative completion with committed routing, and
// a non-nil error for missing, Fresh, rolled-back, uncommitted, or invalid state.
func InspectRequired(ctx context.Context, targetRoot string) (bool, error) {
	cleaned, dbPath, err := validateTargetSnapshotPreconditions(targetRoot)
	if err != nil {
		return false, err
	}
	dataKeyPath := upgrade.SourceDataKeyPath(dbPath)
	if _, err := processlock.InspectRegularSingleLink(dataKeyPath); err != nil {
		return false, err
	}
	if _, err := security.LoadExistingDataKey("", dataKeyPath); err != nil {
		return false, err
	}
	snapshot, err := upgrade.OpenSnapshotWithRetry(ctx, upgrade.OpenSnapshot, dbPath, "")
	if err != nil {
		return false, err
	}
	op, found, readErr := upgrade.ReadRecordFrom(ctx, snapshot.DB())
	_ = snapshot.Close()
	if readErr != nil {
		return false, readErr
	}
	if !found {
		return false, errors.New("upgrade_operation_missing")
	}
	if !ValidPreparedOperation(op) {
		return false, errors.New("upgrade_operation_invalid")
	}
	return inspectPreparedOperationStage(ctx, cleaned, op)
}

func validateTargetSnapshotPreconditions(targetRoot string) (cleaned string, dbPath string, err error) {
	trimmed := strings.TrimSpace(targetRoot)
	if trimmed == "" {
		return "", "", errors.New("upgrade_worker_roots_required")
	}
	cleaned = filepath.Clean(trimmed)
	if err := upgrade.ValidateScratchDir("", "", cleaned); err != nil {
		return "", "", err
	}
	dbPath = upgrade.TargetDatabasePath(cleaned)
	if hasNonEmptyLockMarker(dbPath) {
		return "", "", errors.New("upgrade_fresh_marker_present")
	}
	if marker, present, markerErr := copyinmarker.Read(cleaned); markerErr != nil {
		return "", "", markerErr
	} else if present {
		if !copyinmarker.SameTarget(marker.TargetDatabase, dbPath) {
			return "", "", errors.New("upgrade_copy_in_marker_mismatch")
		}
		if marker.AwaitingCommit(dbPath) {
			return "", "", errors.New("upgrade_copy_in_incomplete")
		}
	}
	dbInfo, statErr := os.Lstat(dbPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			if sideErr := copyinmarker.DatabaseSidecarsAbsent(dbPath); sideErr != nil {
				return "", "", sideErr
			}
			return "", "", errors.New("upgrade_operation_missing")
		}
		return "", "", statErr
	}
	if !dbInfo.Mode().IsRegular() {
		return "", "", errors.New("upgrade_operation_invalid")
	}
	if dbInfo.Size() == 0 {
		if sideErr := copyinmarker.DatabaseSidecarsAbsent(dbPath); sideErr != nil {
			return "", "", sideErr
		}
		return "", "", errors.New("upgrade_operation_missing")
	}
	return cleaned, dbPath, nil
}

func inspectPreparedOperationStage(ctx context.Context, targetRoot string, op model.UpgradeOperation) (bool, error) {
	switch op.Stage {
	case model.UpgradeStageComplete:
		result, verifyErr := upgrade.Verify(ctx, upgrade.VerifyOptions{TargetRoot: targetRoot})
		if verifyErr != nil || !result.Complete || !result.RoutingCommitted {
			return false, errors.New("upgrade_completion_invalid")
		}
		return false, nil
	case model.UpgradeStageCopyIn,
		model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport,
		model.UpgradeStageExternalConnect,
		model.UpgradeStageVerify,
		model.UpgradeStageFailed:
		return true, nil
	default:
		return false, errors.New("upgrade_operation_not_runnable")
	}
}

func validUpgradedOperationRow(op model.UpgradeOperation) bool {
	return strings.TrimSpace(op.SourceKind) == string(upgrade.SourceKindUpgradedLegacy) &&
		strings.TrimSpace(op.SourceFingerprintBefore) != "" &&
		strings.TrimSpace(op.SourceFingerprintAfter) != "" &&
		strings.TrimSpace(op.BackupManifestSHA256) != "" &&
		len(op.DomainsDone) > 0
}

func New(ctx context.Context, options Options) (*Service, error) {
	if strings.TrimSpace(options.TargetRoot) == "" || strings.TrimSpace(options.SourceRoot) == "" {
		return nil, errors.New("upgrade_worker_roots_required")
	}
	options.TargetRoot = filepath.Clean(strings.TrimSpace(options.TargetRoot))
	options.SourceRoot = filepath.Clean(strings.TrimSpace(options.SourceRoot))
	if err := upgrade.RefuseOverlappingRoots(options.SourceRoot, options.TargetRoot, options.bundleDir()); err != nil {
		return nil, err
	}
	if err := upgrade.ValidateScratchDir("", options.SourceRoot, options.TargetRoot); err != nil {
		return nil, err
	}
	_, dbPath, err := validateTargetSnapshotPreconditions(options.TargetRoot)
	if err != nil {
		return nil, err
	}
	var instanceKey [32]byte
	rawDataKey, keyErr := loadTargetDataKeyForHMAC(options)
	if keyErr == nil && len(rawDataKey) > 0 {
		instanceKey = sha256.Sum256(append([]byte("cpamp-upgrade-source-revision-v1:"), rawDataKey...))
	} else if errors.Is(keyErr, os.ErrNotExist) {
		if _, err := rand.Read(instanceKey[:]); err != nil {
			return nil, err
		}
	} else {
		return nil, keyErr
	}

	// Prove both the prepared upgrade_operations row and admin credential from ONE
	// read-only target snapshot before touching ownership. A Fresh or partial
	// target must never be mutated by starting this service.
	snapshot, err := upgrade.OpenSnapshotWithRetry(ctx, upgrade.OpenSnapshot, dbPath, "")
	if err != nil {
		return nil, err
	}
	op, opFound, opErr := upgrade.ReadRecordFrom(ctx, snapshot.DB())
	var (
		credential model.AdminCredential
		credFound  bool
		credErr    error
	)
	if opErr == nil && opFound && ValidPreparedOperation(op) {
		credential, credFound, credErr = setting.New(snapshot.DB()).LoadAdminCredential(ctx)
	}
	_ = snapshot.Close()
	if opErr != nil {
		return nil, opErr
	}
	if !opFound {
		return nil, errors.New("upgrade_operation_missing")
	}
	if !ValidPreparedOperation(op) {
		return nil, errors.New("upgrade_operation_invalid")
	}
	required, err := inspectPreparedOperationStage(ctx, options.TargetRoot, op)
	if err != nil {
		return nil, err
	}
	if credErr != nil {
		return nil, credErr
	}
	if !credFound || !security.IsValidAdminCredential(credential) {
		return nil, errors.New("upgrade_admin_credential_unavailable")
	}

	startupLock, lockErr := processlock.Acquire(dbPath)
	if lockErr != nil {
		// If the upgrade already reached authoritative completion before container
		// restart and Manager has opened the target, allow worker child restart
		// recovery in read-only snapshot mode so /status and activate still work.
		if required || !errors.Is(lockErr, processlock.ErrLocked) {
			return nil, upgrade.ErrStoreLocked
		}
	} else {
		repairErr := options.repairTarget()
		_ = startupLock.Close()
		if repairErr != nil {
			return nil, repairErr
		}
	}

	recordedFP := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))
	fallbackRev := recordedFP
	if !isLowerHex64(fallbackRev) {
		fallbackMac := hmac.New(sha256.New, instanceKey[:])
		_, _ = fallbackMac.Write([]byte("recorded:" + strings.TrimSpace(op.SourceFingerprintAfter)))
		fallbackRev = hex.EncodeToString(fallbackMac.Sum(nil))
	}
	var initialDays []string
	initialRev := op.Revision
	var initialHasParityGaps bool
	if len(op.PendingReconcileDays) > 0 {
		initialDays = normalizeDays(op.PendingReconcileDays)
		if report, preErr := upgrade.Preflight(ctx, upgrade.Options{
			LegacyRoot:           options.SourceRoot,
			TargetDir:            options.TargetRoot,
			ReconcileTargetReady: true,
		}); preErr == nil && len(report.ParityGaps.AffectedDates) > 0 {
			initialHasParityGaps = true
		}
	} else if op.Stage == model.UpgradeStageCopyIn && op.HasDomain(model.UpgradeDomainSourceCopy) {
		if report, preErr := upgrade.Preflight(ctx, upgrade.Options{
			LegacyRoot:           options.SourceRoot,
			TargetDir:            options.TargetRoot,
			ReconcileTargetReady: true,
		}); preErr == nil && len(report.ParityGaps.AffectedDates) > 0 {
			initialDays = normalizeDays(report.ParityGaps.AffectedDates)
			initialHasParityGaps = true
		}
	}

	svc := &Service{
		options:                   options,
		instanceHMAC:              instanceKey,
		recordedSourceFingerprint: recordedFP,
		operationRevision:         OperationRevision(op),
		ctx:                       ctx,
		execution:                 Execution{Available: true, ManagedAvailable: managedAvailable(options)},
		reconcileDays:             initialDays,
		reconcileHasParityGaps:    initialHasParityGaps,
		reconcileDaysRevision:     initialRev,
		fallbackRevision:          fallbackRev,
	}
	if rev, _, state := svc.observeSourceRevision(ctx); state == sourceObservationOK {
		svc.lastSourceRevision = rev
	}
	return svc, nil
}

func loadTargetDataKeyForHMAC(options Options) ([]byte, error) {
	if strings.TrimSpace(options.Config.DataKey) != "" {
		raw, err := security.LoadExistingDataKey(options.Config.DataKey, "")
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), raw...), nil
	}
	keyPath := options.dataKeyPath()
	if _, err := processlock.InspectRegularSingleLink(keyPath); err != nil {
		return nil, err
	}
	raw, err := security.LoadExistingDataKey("", keyPath)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), raw...), nil
}

func cpaConfigPath(root string) (configFile string, cpaDir string) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", ""
	}
	cleaned := filepath.Clean(trimmed)
	lower := strings.ToLower(cleaned)
	if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
		return cleaned, filepath.Dir(cleaned)
	}
	return filepath.Join(cleaned, "config.yaml"), cleaned
}

func managedAvailable(options Options) bool {
	if options.ImportRuntime == nil && !options.Config.EmbeddedRuntimeConfigured() {
		return false
	}
	if _, err := processlock.InspectRegularSingleLink(upgrade.SourceDatabasePath(options.SourceRoot)); err != nil {
		return false
	}
	cfgPath, cpaDir := cpaConfigPath(options.CPARoot)
	if cfgPath == "" {
		return false
	}
	info, err := os.Lstat(cpaDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	if _, err := processlock.InspectRegularSingleLink(cfgPath); err != nil {
		return false
	}
	f, err := os.Open(cfgPath)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func (s *Service) SetHandler(handler http.Handler) {
	s.mu.Lock()
	s.inner = handler
	s.mu.Unlock()
}

func (s *Service) Wait() {
	s.jobs.Wait()
}

func (s *Service) Execution() Execution {
	managed := managedAvailable(s.options)
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.execution
	state.ManagedAvailable = managed
	return state
}

func (s *Service) ReconciliationState() ([]string, bool) {
	readCtx := s.ctx
	if readCtx == nil {
		readCtx = context.Background()
	}
	snapCtx, cancel := context.WithTimeout(readCtx, 2*time.Second)
	defer cancel()
	op, opFound, err := upgrade.ReadRecordFromSnapshot(snapCtx, upgrade.TargetDatabasePath(s.options.TargetRoot), "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && opFound {
		if len(op.PendingReconcileDays) > 0 && op.Revision >= s.reconcileDaysRevision {
			s.reconcileDays = normalizeDays(op.PendingReconcileDays)
			s.reconcileDaysRevision = op.Revision
		} else if len(op.PendingReconcileDays) == 0 && op.Revision > s.reconcileDaysRevision {
			s.reconcileDays = nil
			s.reconcileHasParityGaps = false
			s.reconcileDaysRevision = op.Revision
		}
	}
	return append([]string(nil), s.reconcileDays...), s.reconcileHasParityGaps
}

func (s *Service) ReconciliationDays() []string {
	days, _ := s.ReconciliationState()
	return days
}

func (s *Service) ReconciliationHasParityGaps() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileHasParityGaps
}

// Authenticate verifies either a full "Bearer <key>" Authorization header or a
// bounded pre-extracted inherited admin key against the current PBKDF2/HMAC
// admin credential loaded from a private snapshot of the target database.
func (s *Service) Authenticate(ctx context.Context, headerOrKey string) error {
	key := headerOrKey
	if strings.HasPrefix(headerOrKey, "Bearer ") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(headerOrKey)), "bearer ") {
		extracted, ok := parseInheritedBearerHeader(headerOrKey)
		if !ok {
			return upgrade.ErrAdminUnauthorized
		}
		key = extracted
	} else {
		if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key {
			return upgrade.ErrAdminUnauthorized
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	authCtx, cancel := context.WithTimeout(ctx, upgrade.CredentialVerifyTimeout)
	defer cancel()
	if err := upgrade.ValidateScratchDir("", s.options.SourceRoot, s.options.TargetRoot); err != nil {
		return fmt.Errorf("%w: %v", errAdminStoreUnavailable, err)
	}
	snapshot, err := upgrade.OpenSnapshotWithRetry(authCtx, upgrade.OpenSnapshot, upgrade.TargetDatabasePath(s.options.TargetRoot), "")
	if err != nil {
		return fmt.Errorf("%w: %v", errAdminStoreUnavailable, err)
	}
	defer snapshot.Close()
	credential, found, err := setting.New(snapshot.DB()).LoadAdminCredential(authCtx)
	if err != nil {
		return fmt.Errorf("%w: %v", errAdminStoreUnavailable, err)
	}
	if !found || !security.IsValidAdminCredential(credential) {
		return fmt.Errorf("%w: admin credential unavailable", errAdminStoreUnavailable)
	}
	if !security.VerifyAdminKey(credential, key) {
		return upgrade.ErrAdminUnauthorized
	}
	return nil
}

type sourceObservationState uint8

const (
	sourceObservationOK sourceObservationState = iota
	sourceObservationAbsent
	sourceObservationInvalid
)

func observeFileChangeStamp(path string) (stamp string, stampAvailable bool, err error) {
	stamp, err = copyinmarker.ChangeStamp(path)
	if err != nil {
		if errors.Is(err, copyinmarker.ErrChangeStampUnavailable) {
			return "unavailable", false, nil
		}
		return "", false, err
	}
	return stamp, true, nil
}

func hashRegularFileSHA256(ctx context.Context, path string, inspected os.FileInfo) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := processlock.OpenedDatabase(f, inspected); err != nil {
		return "", err
	}
	h := sha256.New()
	var buf [64 << 10]byte
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf[:])
		if n > 0 {
			total += int64(n)
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if total != inspected.Size() {
		return "", upgrade.ErrSourceChanged
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func inspectSourceRootEntries(sourceRoot string) error {
	listing, err := os.ReadDir(sourceRoot)
	if err != nil {
		return err
	}
	for _, entry := range listing {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			return processlock.ErrSymlinkedDatabase
		}
		name := entry.Name()
		switch name {
		case "usage.sqlite", "usage.sqlite-wal", "usage.sqlite-shm", "usage.sqlite-journal", "data.key", "usage.sqlite.manager.lock":
			if !mode.IsRegular() {
				return fmt.Errorf("source entry %s is not a regular file", name)
			}
		case "usage-archives":
			if !mode.IsDir() {
				return fmt.Errorf("source archive path %s is not a directory", name)
			}
		default:
			if !mode.IsDir() && !mode.IsRegular() {
				return fmt.Errorf("source entry %s is not a regular file or directory", name)
			}
		}
	}
	return nil
}

func (s *Service) observeSourceRevision(ctx context.Context) (string, bool, sourceObservationState) {
	if strings.TrimSpace(s.options.SourceRoot) == "" {
		return "", false, sourceObservationAbsent
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return "", false, sourceObservationInvalid
	}
	dbPath := upgrade.SourceDatabasePath(s.options.SourceRoot)
	info, err := processlock.InspectRegularSingleLink(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if copyinmarker.DatabaseSidecarsAbsent(dbPath) != nil {
				return "", false, sourceObservationInvalid
			}
			if _, keyErr := processlock.InspectRegularSingleLink(upgrade.SourceDataKeyPath(dbPath)); keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
				return "", false, sourceObservationInvalid
			}
			return "", false, sourceObservationAbsent
		}
		return "", false, sourceObservationInvalid
	}
	if err := inspectSourceRootEntries(s.options.SourceRoot); err != nil {
		return "", false, sourceObservationInvalid
	}
	f, err := os.Open(dbPath)
	if err != nil {
		return "", false, sourceObservationInvalid
	}
	if openErr := processlock.OpenedDatabase(f, info); openErr != nil {
		_ = f.Close()
		return "", false, sourceObservationInvalid
	}
	var header [100]byte
	n, readErr := io.ReadFull(f, header[:])
	_ = f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", false, sourceObservationInvalid
	}
	dbID, idErr := copyinmarker.Identity(dbPath)
	if idErr != nil {
		return "", false, sourceObservationInvalid
	}
	dbStamp, cacheable, stampErr := observeFileChangeStamp(dbPath)
	if stampErr != nil {
		return "", false, sourceObservationInvalid
	}

	mac := hmac.New(sha256.New, s.instanceHMAC[:])
	_, _ = fmt.Fprintf(mac, "size=%d;mtime=%d;dev=%d;ino=%d;ctime=%s;hdr=%x",
		info.Size(), info.ModTime().UnixNano(), dbID.Device, dbID.Inode, dbStamp, header[:n])

	walPath := dbPath + "-wal"
	if walInfo, walErr := processlock.InspectRegularSingleLink(walPath); walErr == nil {
		walSum, hashErr := hashRegularFileSHA256(ctx, walPath, walInfo)
		if hashErr != nil {
			return "", false, sourceObservationInvalid
		}
		walID, wIDErr := copyinmarker.Identity(walPath)
		if wIDErr != nil {
			return "", false, sourceObservationInvalid
		}
		walStamp, walStampOK, wStampErr := observeFileChangeStamp(walPath)
		if wStampErr != nil {
			return "", false, sourceObservationInvalid
		}
		if !walStampOK {
			cacheable = false
		}
		_, _ = fmt.Fprintf(mac, ";wal=%d:%d:%d:%d:%s:%s",
			walInfo.Size(), walInfo.ModTime().UnixNano(), walID.Device, walID.Inode, walStamp, walSum)
	} else if errors.Is(walErr, os.ErrNotExist) {
		_, _ = io.WriteString(mac, ";wal=absent")
	} else {
		return "", false, sourceObservationInvalid
	}

	shmPath := dbPath + "-shm"
	if shmInfo, shmErr := processlock.InspectRegularSingleLink(shmPath); shmErr == nil {
		sf, openErr := os.Open(shmPath)
		if openErr != nil {
			return "", false, sourceObservationInvalid
		}
		if openCheckErr := processlock.OpenedDatabase(sf, shmInfo); openCheckErr != nil {
			_ = sf.Close()
			return "", false, sourceObservationInvalid
		}
		var shmHdr [136]byte
		sn, sReadErr := io.ReadFull(sf, shmHdr[:])
		_ = sf.Close()
		if sReadErr != nil && !errors.Is(sReadErr, io.EOF) && !errors.Is(sReadErr, io.ErrUnexpectedEOF) {
			return "", false, sourceObservationInvalid
		}
		shmID, sIDErr := copyinmarker.Identity(shmPath)
		if sIDErr != nil {
			return "", false, sourceObservationInvalid
		}
		shmStamp, shmStampOK, sStampErr := observeFileChangeStamp(shmPath)
		if sStampErr != nil {
			return "", false, sourceObservationInvalid
		}
		if !shmStampOK {
			cacheable = false
		}
		_, _ = fmt.Fprintf(mac, ";shm=%d:%d:%d:%d:%s:%x",
			shmInfo.Size(), shmInfo.ModTime().UnixNano(), shmID.Device, shmID.Inode, shmStamp, shmHdr[:sn])
	} else if errors.Is(shmErr, os.ErrNotExist) {
		_, _ = io.WriteString(mac, ";shm=absent")
	} else {
		return "", false, sourceObservationInvalid
	}

	journalPath := dbPath + "-journal"
	if journalInfo, journalErr := processlock.InspectRegularSingleLink(journalPath); journalErr == nil {
		journalID, jIDErr := copyinmarker.Identity(journalPath)
		if jIDErr != nil {
			return "", false, sourceObservationInvalid
		}
		journalStamp, journalStampOK, jStampErr := observeFileChangeStamp(journalPath)
		if jStampErr != nil {
			return "", false, sourceObservationInvalid
		}
		if !journalStampOK {
			cacheable = false
		}
		_, _ = fmt.Fprintf(mac, ";journal=%d:%d:%d:%d:%s",
			journalInfo.Size(), journalInfo.ModTime().UnixNano(), journalID.Device, journalID.Inode, journalStamp)
	} else if errors.Is(journalErr, os.ErrNotExist) {
		_, _ = io.WriteString(mac, ";journal=absent")
	} else {
		return "", false, sourceObservationInvalid
	}

	keyPath := upgrade.SourceDataKeyPath(dbPath)
	if keyInfo, keyErr := processlock.InspectRegularSingleLink(keyPath); keyErr == nil {
		keyID, kIDErr := copyinmarker.Identity(keyPath)
		if kIDErr != nil {
			return "", false, sourceObservationInvalid
		}
		keyStamp, keyStampOK, kStampErr := observeFileChangeStamp(keyPath)
		if kStampErr != nil {
			return "", false, sourceObservationInvalid
		}
		if !keyStampOK {
			cacheable = false
		}
		_, _ = fmt.Fprintf(mac, ";key=%d:%d:%d:%d:%s",
			keyInfo.Size(), keyInfo.ModTime().UnixNano(), keyID.Device, keyID.Inode, keyStamp)
	} else if errors.Is(keyErr, os.ErrNotExist) {
		_, _ = io.WriteString(mac, ";key=absent")
	} else {
		return "", false, sourceObservationInvalid
	}

	treeMeta, treeErr := upgrade.IdentifySourceTree(ctx, s.options.SourceRoot)
	if treeErr != nil {
		return "", false, sourceObservationInvalid
	}
	if treeMeta.Uncacheable {
		cacheable = false
		_, _ = io.WriteString(mac, ";tree=uncacheable")
	} else {
		_, _ = fmt.Fprintf(mac, ";tree=%s", treeMeta.Key)
	}

	return hex.EncodeToString(mac.Sum(nil)), cacheable, sourceObservationOK
}

func (s *Service) sourceMatchesRecordedFingerprint(ctx context.Context) (bool, error) {
	if s.recordedSourceFingerprint == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	revBefore, cacheableBefore, stateBefore := s.observeSourceRevision(ctx)
	if stateBefore != sourceObservationOK {
		return false, errAdminStoreUnavailable
	}
	if cacheableBefore {
		s.mu.Lock()
		cached := s.verifiedFingerprintRevision != "" && s.verifiedFingerprintRevision == revBefore
		s.mu.Unlock()
		if cached {
			return true, nil
		}
	}
	report, err := upgrade.Preflight(ctx, upgrade.Options{
		LegacyRoot:           s.options.SourceRoot,
		TargetDir:            s.options.TargetRoot,
		ReconcileTargetReady: true,
	})
	if err != nil {
		return false, err
	}
	for _, code := range report.Blocked {
		switch code {
		case upgrade.CodeSourceBusy,
			upgrade.CodeSourceLockUnavailable,
			upgrade.CodeSourceUnreadable,
			upgrade.CodeSnapshotFailed,
			upgrade.CodeSourceSymlinked,
			upgrade.CodeSourceMissing:
			return false, errAdminStoreUnavailable
		}
	}
	var (
		fingerprintCheck  upgrade.Check
		hasFingerprintChk bool
	)
	for _, chk := range report.Checks {
		if chk.Name == upgrade.CheckFingerprint {
			fingerprintCheck = chk
			hasFingerprintChk = true
			break
		}
	}
	if !hasFingerprintChk {
		return false, errAdminStoreUnavailable
	}
	switch fingerprintCheck.Status {
	case upgrade.StatusPass:
		if !strings.EqualFold(strings.TrimSpace(report.Fingerprint), s.recordedSourceFingerprint) {
			return false, nil
		}
	case upgrade.StatusBlocked:
		if fingerprintCheck.Code == upgrade.CodeSourceChanged {
			return false, nil
		}
		return false, errAdminStoreUnavailable
	default:
		return false, errAdminStoreUnavailable
	}
	revAfter, cacheableAfter, stateAfter := s.observeSourceRevision(ctx)
	if stateAfter != sourceObservationOK {
		return false, errAdminStoreUnavailable
	}
	if revAfter != revBefore {
		return false, nil
	}
	s.mu.Lock()
	if cacheableAfter {
		s.verifiedFingerprintRevision = revAfter
	} else {
		s.verifiedFingerprintRevision = ""
	}
	s.lastSourceRevision = revAfter
	s.mu.Unlock()
	return true, nil
}

func isMutatingWizardRoute(r *http.Request) (mutating bool, repairsTarget bool) {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" {
		return false, false
	}
	path := r.URL.Path
	switch r.Method {
	case http.MethodPost:
		if (strings.HasSuffix(path, "/installation/upgrade/routing") && path != "/installation/upgrade/routing") ||
			(strings.HasSuffix(path, "/installation/upgrade/verify") && path != "/installation/upgrade/verify") {
			return true, true
		}
		if (strings.HasSuffix(path, "/installation/upgrade/runtime/artifact/probe") && path != "/installation/upgrade/runtime/artifact/probe") ||
			(strings.HasSuffix(path, "/installation/upgrade/runtime/artifact/acquire") && path != "/installation/upgrade/runtime/artifact/acquire") {
			return true, true
		}
	case http.MethodPut:
		if strings.HasSuffix(path, "/installation/upgrade/runtime/artifact/upload") && path != "/installation/upgrade/runtime/artifact/upload" {
			return true, true
		}
	}
	return false, false
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !peerAllowed(r.Context()) {
		writeError(w, http.StatusForbidden, "upgrade_worker_peer_refused", nil)
		return
	}
	if r == nil || r.URL == nil {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("X-CPAMP-Worker-Scheme") == "https" {
		cloned := r.Clone(r.Context())
		cloned.TLS = &tls.ConnectionState{}
		cloned.Header = r.Header.Clone()
		cloned.Header.Del("X-CPAMP-Worker-Scheme")
		r = cloned
	}
	// Auth shape and same-origin run BEFORE path, query, method, or busy-lock.
	if !RequestAllowed(w, r, s.options.PublicOrigin) {
		return
	}
	entry := isEntryNamespace(r.URL.Path)
	if err := s.Authenticate(r.Context(), r.Header.Get("Authorization")); err != nil {
		if errors.Is(err, upgrade.ErrAdminUnauthorized) {
			if entry {
				writeEntryError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
			} else {
				writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized, nil)
			}
			return
		}
		if entry {
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		} else {
			state := Execution{Code: CodeUnavailable}
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable, &state)
		}
		return
	}
	if entry {
		s.serveEntry(w, r)
		return
	}
	if strings.HasSuffix(r.URL.Path, ExecuteSuffix) && r.URL.Path != ExecuteSuffix {
		if r.URL.RawQuery != "" {
			setSecurityHeaders(w)
			http.NotFound(w, r)
			return
		}
		s.serveExecute(w, r)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/installation/upgrade/verify") && r.URL.Path != "/installation/upgrade/verify" {
		if r.URL.RawQuery != "" {
			setSecurityHeaders(w)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", nil)
			return
		}
		writeError(w, http.StatusConflict, "upgrade_execution_required", nil)
		return
	}
	// Only the narrow wizard namespace is delegated.
	if !strings.Contains(r.URL.Path, "/installation/upgrade/") {
		setSecurityHeaders(w)
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	inner := s.inner
	s.mu.Unlock()
	if inner == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, nil)
		return
	}
	if mutating, repairsTarget := isMutatingWizardRoute(r); mutating {
		if !s.mutation.TryLock() {
			writeError(w, http.StatusConflict, CodeBusy, nil)
			return
		}
		defer s.mutation.Unlock()
		if repairsTarget {
			defer func() {
				if err := s.options.repairTarget(); err != nil {
					s.setCode("upgrade_target_ownership_failed")
				}
			}()
		}
	}
	inner.ServeHTTP(w, r)
}

func (s *Service) serveEntry(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != EntryPath || r.URL.RawQuery != "" {
		setSecurityHeaders(w)
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeEntryError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.ContentLength > 0 {
		writeEntryError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body != nil {
		var buf [1]byte
		if n, err := r.Body.Read(buf[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			writeEntryError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	prior, ok := parseSourceRevisionHeader(r.Header.Values("X-CPAMP-Upgrade-Source"))
	if !ok {
		writeEntryError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	status := "unsupported_upgrade"
	expected, _, obsState := s.observeSourceRevision(r.Context())
	if obsState == sourceObservationInvalid {
		writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	s.mu.Lock()
	hadLiveObservation := s.lastSourceRevision != ""
	if obsState == sourceObservationOK {
		s.lastSourceRevision = expected
	} else {
		if hadLiveObservation {
			s.mu.Unlock()
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		status = "observation_unavailable"
		expected = s.fallbackRevision
	}
	s.mu.Unlock()

	if obsState != sourceObservationOK && prior != "" && prior != expected {
		writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	if obsState == sourceObservationOK && prior != "" && prior != expected {
		if s.recordedSourceFingerprint != "" && strings.EqualFold(prior, s.recordedSourceFingerprint) {
			matched, matchErr := s.sourceMatchesRecordedFingerprint(r.Context())
			if matchErr != nil {
				writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
				return
			}
			if !matched {
				writeEntryError(w, http.StatusConflict, "source_changed")
				return
			}
		} else {
			writeEntryError(w, http.StatusConflict, "source_changed")
			return
		}
	}
	doc := map[string]any{
		"contract_version":    EntryContract,
		"authenticated":       true,
		"source_revision":     expected,
		"status":              status,
		"migration_available": false,
		"expires_in_seconds":  60,
	}
	if isLowerHex64(s.operationRevision) {
		doc["operation_revision"] = s.operationRevision
	}
	writeJSON(w, http.StatusOK, doc)
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (Request, error) {
	var request Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return request, errors.New(CodeRequestInvalid)
	}
	if len(request.AcknowledgeDays) > 36600 {
		return request, errors.New(CodeRequestInvalid)
	}
	for _, day := range request.AcknowledgeDays {
		if parsed, err := time.Parse("2006-01-02", day); err != nil || parsed.Format("2006-01-02") != day {
			return request, errors.New(CodeRequestInvalid)
		}
	}
	hasAck := len(request.AcknowledgeDays) > 0 || request.AcknowledgeAllMismatches || request.AcknowledgeParityGaps != nil
	hasExternal := request.BaseURL != nil || request.ManagementKey != nil
	switch request.Action {
	case "reconcile":
		if request.PluginsEnabled != nil || hasExternal {
			return request, errors.New(CodeRequestInvalid)
		}
	case "cpa-import":
		if request.PluginsEnabled == nil || hasAck || hasExternal {
			return request, errors.New(CodeRequestInvalid)
		}
	case "external-connect":
		if request.PluginsEnabled != nil || hasAck || (request.BaseURL == nil) != (request.ManagementKey == nil) {
			return request, errors.New(CodeRequestInvalid)
		}
		if request.BaseURL != nil && (len(*request.BaseURL) > 2048 || strings.TrimSpace(*request.BaseURL) == "" || len(*request.ManagementKey) > 4096 || strings.TrimSpace(*request.ManagementKey) == "") {
			return request, errors.New(CodeRequestInvalid)
		}
	case "verify", "activate":
		if request.PluginsEnabled != nil || hasAck || hasExternal {
			return request, errors.New(CodeRequestInvalid)
		}
	default:
		return request, errors.New(CodeRequestInvalid)
	}
	return request, nil
}

func (s *Service) serveExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", nil)
		return
	}
	request, err := decodeRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid, nil)
		return
	}
	if !s.mutation.TryLock() {
		writeError(w, http.StatusConflict, CodeBusy, nil)
		return
	}
	if request.Action == "activate" {
		defer s.mutation.Unlock()
		if len(s.ReconciliationDays()) > 0 {
			writeError(w, http.StatusConflict, string(upgrade.UpgradeErrorReconcileMismatch), nil)
			return
		}
		if !s.activationRequested {
			op, found, err := upgrade.ReadRecordFromSnapshot(r.Context(), upgrade.TargetDatabasePath(s.options.TargetRoot), "")
			if err != nil || !found || !validUpgradedOperationRow(op) || op.Stage != model.UpgradeStageComplete {
				writeError(w, http.StatusConflict, "upgrade_activation_not_ready", nil)
				return
			}
			// Activation reuses Verify rather than trusting stage=='complete' alone.
			// Verify's repeat path re-proves completion evidence, bindings and routing.
			result, err := upgrade.Verify(r.Context(), upgrade.VerifyOptions{
				LegacyRoot: s.options.SourceRoot,
				TargetRoot: s.options.TargetRoot,
				Runtime:    s.verifyRuntime(),
			})
			if err != nil || !result.Complete || !result.RoutingCommitted {
				code := "upgrade_activation_not_ready"
				if typed, ok := upgrade.UpgradeErrorCodeOf(err); ok {
					code = string(typed)
				}
				writeError(w, http.StatusConflict, code, nil)
				return
			}
			if err := s.options.repairTarget(); err != nil {
				writeError(w, http.StatusServiceUnavailable, "upgrade_target_ownership_failed", nil)
				return
			}
		}
		if s.options.RestartParent == nil {
			writeError(w, http.StatusConflict, "upgrade_restart_disabled", nil)
			return
		}
		if s.options.ValidateRestart != nil {
			if err := s.options.ValidateRestart(); err != nil {
				s.activationRequested = false
				s.setCode("upgrade_restart_failed")
				writeError(w, http.StatusServiceUnavailable, "upgrade_restart_failed", nil)
				return
			}
		}
		managed := managedAvailable(s.options)
		s.activationRequested = true
		s.mu.Lock()
		s.execution.Running = false
		s.execution.Action = "activate"
		s.execution.Code = ""
		s.execution.ManagedAvailable = managed
		state := s.execution
		s.mu.Unlock()

		writeJSON(w, http.StatusOK, executionDocument(state))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// The Content-Length response is flushed before triggering controlled
		// parent shutdown. An explicit operator retry re-invokes RestartParent,
		// which coalesces during an active timer window and re-delivers SIGTERM
		// to the same startup-validated launcher parent only after the prior
		// timer completed.
		if err := s.options.RestartParent(); err != nil {
			s.activationRequested = false
			s.mu.Lock()
			s.execution.Action = ""
			s.execution.Code = "upgrade_restart_failed"
			s.mu.Unlock()
		}
		return
	}
	if s.ctx.Err() != nil {
		s.mutation.Unlock()
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, nil)
		return
	}
	managed := managedAvailable(s.options)
	s.mu.Lock()
	s.execution.Running = true
	s.execution.Action = request.Action
	s.execution.Code = ""
	s.execution.ManagedAvailable = managed
	state := s.execution
	s.mu.Unlock()

	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer s.mutation.Unlock()
		jobCtx, cancel := context.WithTimeout(s.ctx, s.options.jobTimeout())
		defer cancel()
		err := s.run(jobCtx, request)
		code := ""
		if err != nil {
			code = "upgrade_execution_failed"
			if typed, ok := upgrade.UpgradeErrorCodeOf(err); ok {
				code = string(typed)
			} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = "upgrade_execution_canceled"
			}
		}
		if repairErr := s.options.repairTarget(); repairErr != nil {
			code = "upgrade_target_ownership_failed"
		}
		// Keep execution.Action populated with the last completed action so UI
		// polling observes completion without clearing action history.
		s.mu.Lock()
		s.execution.Running = false
		s.execution.Code = code
		s.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, executionDocument(state))
}

func executionDocument(state Execution) map[string]any {
	doc := map[string]any{
		"contractVersion":  Contract,
		"available":        state.Available,
		"running":          state.Running,
		"managedAvailable": state.ManagedAvailable,
	}
	if state.Action != "" {
		doc["action"] = state.Action
	}
	if state.Code != "" {
		doc["code"] = state.Code
	}
	return doc
}

func (s *Service) setCode(code string) {
	s.mu.Lock()
	s.execution.Code = code
	s.mu.Unlock()
}

func (s *Service) importRuntime() upgrade.CPAImportRuntime {
	if s.options.ImportRuntime != nil {
		return s.options.ImportRuntime
	}
	if !s.options.Config.EmbeddedRuntimeConfigured() {
		return nil
	}
	return runtimeservice.NewEmbeddedClientWithTokenSource(
		s.options.Config.RuntimeURL,
		runtimeservice.NewFileRuntimeTokenSource(s.options.Config.RuntimeTokenFile),
	)
}

func (s *Service) verifyRuntime() upgrade.VerifyRuntime {
	if s.options.VerifyRuntime != nil {
		return s.options.VerifyRuntime
	}
	if !s.options.Config.EmbeddedRuntimeConfigured() {
		return nil
	}
	return runtimeservice.NewEmbeddedClientWithTokenSource(
		s.options.Config.RuntimeURL,
		runtimeservice.NewFileRuntimeTokenSource(s.options.Config.RuntimeTokenFile),
	)
}

func normalizeDays(days ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range days {
		for _, raw := range list {
			day := strings.TrimSpace(raw)
			if day == "" {
				continue
			}
			if _, ok := seen[day]; ok {
				continue
			}
			seen[day] = struct{}{}
			out = append(out, day)
		}
	}
	slices.Sort(out)
	return out
}

func isTransientReconcileError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, upgrade.ErrReconcileTargetIncomplete) || errors.Is(err, upgrade.ErrReconcileNothingCompared) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, upgrade.ErrStoreLocked) || errors.Is(err, upgrade.ErrTargetLocked) ||
		errors.Is(err, upgrade.ErrSnapshotFailed) || errors.Is(err, upgrade.ErrSnapshotScratch) ||
		errors.Is(err, upgrade.ErrSourceChanged) {
		return true
	}
	if code, ok := upgrade.UpgradeErrorCodeOf(err); ok {
		switch code {
		case upgrade.UpgradeErrorCode(upgrade.CodeSourceBusy),
			upgrade.UpgradeErrorCode(upgrade.CodeSourceLockUnavailable),
			upgrade.UpgradeErrorTargetLocked,
			upgrade.UpgradeErrorStoreLocked,
			upgrade.UpgradeErrorSourceChanged,
			upgrade.UpgradeErrorSnapshotFailed:
			return true
		default:
			return false
		}
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "sqlite_busy") ||
		strings.Contains(lower, "sqlite_locked") ||
		strings.Contains(lower, "database is locked") ||
		strings.Contains(lower, "database table is locked")
}

func (s *Service) run(ctx context.Context, request Request) error {
	o := s.options
	switch request.Action {
	case "reconcile":
		if err := upgrade.RequireActionStageWithScratch(ctx, upgrade.TargetDatabasePath(o.TargetRoot), "", o.SourceRoot, "reconcile", nil); err != nil {
			return err
		}
		report, err := upgrade.Preflight(ctx, upgrade.Options{
			LegacyRoot:           o.SourceRoot,
			TargetDir:            o.TargetRoot,
			ReconcileTargetReady: true,
		})
		if err != nil {
			return err
		}
		if !report.Ready {
			return upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_preflight_blocked"), "source preflight requires attention", nil)
		}
		sourceLock, err := processlock.AcquireExistingReadOnly(upgrade.SourceDatabasePath(o.SourceRoot))
		if err != nil {
			return upgrade.CodedError(upgrade.UpgradeErrorCode(upgrade.CodeSourceBusy), "source is not stopped", err)
		}
		defer sourceLock.Close()
		targetLock, err := processlock.Acquire(upgrade.TargetDatabasePath(o.TargetRoot))
		if err != nil {
			return upgrade.ErrStoreLocked
		}
		defer targetLock.Close()
		acknowledged := request.AcknowledgeDays
		if request.AcknowledgeAllMismatches {
			observedDays := s.ReconciliationDays()
			op, opFound, snapErr := upgrade.ReadRecordFromSnapshot(ctx, upgrade.TargetDatabasePath(o.TargetRoot), "")
			if snapErr != nil {
				return snapErr
			}
			if !opFound {
				return upgrade.CodedError(upgrade.UpgradeErrorReconcileNotCopiedIn, "target holds no recorded upgrade operation", nil)
			}
			acknowledged = normalizeDays(request.AcknowledgeDays, observedDays, op.PendingReconcileDays)
		}
		ackParity := request.AcknowledgeParityGaps != nil && *request.AcknowledgeParityGaps
		reconciled, err := upgrade.Reconcile(ctx, upgrade.Options{
			LegacyRoot:            o.SourceRoot,
			TargetRoot:            o.TargetRoot,
			BundleDir:             o.bundleDir(),
			Lock:                  sourceLock,
			TargetLock:            targetLock,
			Acknowledged:          acknowledged,
			AcknowledgeParityGaps: ackParity,
		})
		var (
			observedOp    model.UpgradeOperation
			observedFound bool
			observedRev   int64
		)
		snapBase := context.Background()
		if ctx != nil {
			snapBase = context.WithoutCancel(ctx)
		}
		snapCtx, snapCancel := context.WithTimeout(snapBase, 2*time.Second)
		op, opFound, snapErr := readReconcilePostRunSnapshot(snapCtx, upgrade.TargetDatabasePath(o.TargetRoot), "")
		snapCancel()
		if snapErr == nil && opFound {
			observedOp = op
			observedFound = true
			observedRev = op.Revision
		}
		s.mu.Lock()
		if err == nil {
			s.reconcileDays = nil
			s.reconcileHasParityGaps = false
			if observedFound {
				s.reconcileDaysRevision = observedRev
			} else {
				s.reconcileDaysRevision++
			}
		} else if errors.Is(err, upgrade.ErrReconcileMismatch) {
			days := normalizeDays(reconciled.MismatchDays, reconciled.ParityGapDays, reconciled.UnacknowledgedDays)
			s.reconcileDays = days
			s.reconcileHasParityGaps = len(days) > 0 && (len(reconciled.ParityGapDays) > 0 || len(report.ParityGaps.AffectedDates) > 0)
			if observedFound {
				s.reconcileDaysRevision = observedRev
			} else {
				s.reconcileDaysRevision++
			}
		} else {
			if !isTransientReconcileError(err) && (!observedFound || len(observedOp.PendingReconcileDays) == 0) {
				s.reconcileDays = nil
				s.reconcileHasParityGaps = false
				if observedFound {
					s.reconcileDaysRevision = observedRev
				}
			} else if !observedFound {
				s.reconcileHasParityGaps = s.reconcileHasParityGaps || len(report.ParityGaps.AffectedDates) > 0
			} else if len(observedOp.PendingReconcileDays) > 0 {
				s.reconcileDays = normalizeDays(observedOp.PendingReconcileDays)
				s.reconcileHasParityGaps = s.reconcileHasParityGaps || len(report.ParityGaps.AffectedDates) > 0
				s.reconcileDaysRevision = observedRev
			} else if observedOp.Stage == model.UpgradeStageCopyIn &&
				isTransientReconcileError(err) &&
				(s.reconcileHasParityGaps || len(report.ParityGaps.AffectedDates) > 0) {
				s.reconcileHasParityGaps = true
				if len(report.ParityGaps.AffectedDates) > 0 {
					s.reconcileDays = normalizeDays(s.reconcileDays, report.ParityGaps.AffectedDates)
				}
				s.reconcileDaysRevision = observedRev
			} else {
				s.reconcileDays = nil
				s.reconcileHasParityGaps = false
				s.reconcileDaysRevision = observedRev
			}
		}
		s.mu.Unlock()
		return err
	case "cpa-import":
		if err := upgrade.RequireActionStageWithScratch(ctx, upgrade.TargetDatabasePath(o.TargetRoot), "", o.SourceRoot, "cpa-import", s.ReconciliationDays()); err != nil {
			return err
		}
		if !managedAvailable(o) {
			return upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_cpa_source_unavailable"), "the configured CPA source is unavailable", nil)
		}
		targetLock, err := processlock.Acquire(upgrade.TargetDatabasePath(o.TargetRoot))
		if err != nil {
			return upgrade.ErrStoreLocked
		}
		defer targetLock.Close()
		sourceLock, err := processlock.AcquireExistingReadOnly(upgrade.SourceDatabasePath(o.SourceRoot))
		if err != nil {
			return upgrade.ErrCPASourceLocked
		}
		defer sourceLock.Close()
		_, cpaDir := cpaConfigPath(o.CPARoot)
		destURL := strings.TrimSpace(o.Config.CPAUpstreamURL)
		if destURL == "" {
			destURL = "http://127.0.0.1:8317"
		}
		_, err = upgrade.ImportCPA(ctx, upgrade.CPAImportOptions{
			LegacyRoot:       o.SourceRoot,
			SourceLock:       sourceLock,
			CPADirectory:     cpaDir,
			CPAAuthDirectory: strings.TrimSpace(o.CPAAuthRoot),
			CPAVersion:       strings.TrimSpace(o.CPAVersion),
			TargetRoot:       o.TargetRoot,
			Runtime:          s.importRuntime(),
			DestinationURL:   destURL,
			PluginsEnabled:   request.PluginsEnabled,
		})
		return err
	case "external-connect":
		if err := upgrade.RequireActionStageWithScratch(ctx, upgrade.TargetDatabasePath(o.TargetRoot), "", o.SourceRoot, "external-connect", s.ReconciliationDays()); err != nil {
			return err
		}
		baseURL, key, syncStore, reprove, err := s.externalConnection(ctx, request)
		if err != nil {
			return err
		}
		_, err = upgrade.ExternalConnect(ctx, upgrade.ExternalConnectOptions{
			TargetRoot:    o.TargetRoot,
			BaseURL:       baseURL,
			ManagementKey: key,
			CPAVersion:    strings.TrimSpace(o.CPAVersion),
			Reprove:       reprove,
		})
		if err != nil {
			return err
		}
		if syncStore {
			if o.BeforeExternalSettingsSync != nil {
				o.BeforeExternalSettingsSync()
			}
			return s.syncExternalManagerConnection(ctx, baseURL, key)
		}
		return nil
	case "verify":
		if err := upgrade.RequireActionStageWithScratch(ctx, upgrade.TargetDatabasePath(o.TargetRoot), "", o.SourceRoot, "verify", s.ReconciliationDays()); err != nil {
			return err
		}
		result, err := upgrade.Verify(ctx, upgrade.VerifyOptions{
			LegacyRoot: o.SourceRoot,
			TargetRoot: o.TargetRoot,
			Runtime:    s.verifyRuntime(),
		})
		if err != nil {
			return err
		}
		if !result.Complete {
			return upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_verification_failed"), "upgrade verification did not reach completion", nil)
		}
		return nil
	}
	return errors.New(CodeRequestInvalid)
}

func canonicalExternalBaseURL(raw string) (string, error) {
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return identity.CanonicalExternalURL(normalized)
}

func sameExternalBaseURL(a, b string) bool {
	ca, errA := canonicalExternalBaseURL(a)
	cb, errB := canonicalExternalBaseURL(b)
	return errA == nil && errB == nil && ca != "" && ca == cb
}

func externalSettingsCanonicalAndSynced(manager store.ManagerConfig, managerOK bool, setup store.Setup, setupOK bool, targetURL, targetKey string) bool {
	if !managerOK {
		return false
	}
	rawURL := strings.TrimSpace(manager.CPAConnection.CPABaseURL)
	rawKey := strings.TrimSpace(manager.CPAConnection.ManagementKey)
	if rawURL == "" || rawKey == "" {
		return false
	}
	canonicalTarget, err := canonicalExternalBaseURL(targetURL)
	if err != nil || canonicalTarget == "" {
		return false
	}
	if rawURL != canonicalTarget || !security.EqualHMAC(rawKey, targetKey) {
		return false
	}
	if setupOK {
		setupURL := strings.TrimSpace(setup.CPAUpstreamURL)
		if setupURL != "" && setupURL != canonicalTarget {
			return false
		}
		setupKey := strings.TrimSpace(setup.ManagementKey)
		if setupKey != "" && !security.EqualHMAC(setupKey, targetKey) {
			return false
		}
	}
	return true
}

func (s *Service) externalConnection(ctx context.Context, request Request) (baseURL, managementKey string, syncStore, reprove bool, err error) {
	keyBytes, err := security.LoadExistingDataKey("", s.options.dataKeyPath())
	if err != nil {
		return "", "", false, false, err
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		return "", "", false, false, err
	}
	snapshot, err := upgrade.OpenSnapshotWithRetry(ctx, upgrade.OpenSnapshot, upgrade.TargetDatabasePath(s.options.TargetRoot), "")
	if err != nil {
		return "", "", false, false, err
	}
	defer snapshot.Close()

	binding, hasBinding, bindErr := upgradeoperation.New(snapshot.DB()).ExternalBinding(ctx)
	if bindErr != nil {
		return "", "", false, false, bindErr
	}
	var recordedKey string
	var hasRecordedKey bool
	var keyDecryptErr error
	if hasBinding && security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
		plain, decErr := protector.UnprotectString(binding.ManagementKey)
		if decErr != nil {
			keyDecryptErr = decErr
		} else if strings.TrimSpace(plain) != "" {
			recordedKey = strings.TrimSpace(plain)
			hasRecordedKey = true
		}
	}
	if request.BaseURL != nil {
		canonicalURL, canonErr := canonicalExternalBaseURL(*request.BaseURL)
		if canonErr != nil || canonicalURL == "" {
			return "", "", false, false, upgrade.CodedError(
				upgrade.UpgradeErrorExternalAuthorityUnavailable,
				"the CPA address is not a usable http(s) endpoint; nothing was connected and the operation did not advance",
				canonErr,
			)
		}
		if request.ManagementKey != nil {
			managementKey = strings.TrimSpace(*request.ManagementKey)
		}
		reprove = false
		if hasBinding && sameExternalBaseURL(canonicalURL, binding.BaseURL) {
			if keyDecryptErr != nil || !hasRecordedKey || !security.EqualHMAC(recordedKey, managementKey) {
				reprove = true
			}
		}
		return canonicalURL, managementKey, true, reprove, nil
	}
	if keyDecryptErr != nil {
		return "", "", false, false, upgrade.CodedError(upgrade.UpgradeErrorExternalManagementRejected, "recorded External management key cannot be decrypted", keyDecryptErr)
	}

	st := store.New(snapshot.DB(), protector)
	manager, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return "", "", false, false, err
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return "", "", false, false, err
	}
	if hasBinding && strings.TrimSpace(binding.BaseURL) != "" && hasRecordedKey {
		resolved, resErr := managerconfig.ResolveLegacyConnectionAuthority(manager, managerOK, setup, setupOK)
		alreadySynced := resErr == nil &&
			resolved.Authority != managerconfig.LegacyConnectionAuthorityNone &&
			externalSettingsCanonicalAndSynced(manager, managerOK, setup, setupOK, binding.BaseURL, recordedKey)
		return strings.TrimSpace(binding.BaseURL), recordedKey, !alreadySynced, false, nil
	}
	resolved, err := managerconfig.ResolveLegacyConnectionAuthority(manager, managerOK, setup, setupOK)
	if err != nil {
		return "", "", false, false, err
	}
	if resolved.Authority == managerconfig.LegacyConnectionAuthorityNone {
		return "", "", false, false, upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_external_connection_required"), "an explicit CPA connection is required", nil)
	}
	targetURL := resolved.Connection.BaseURL
	if canonical, canonErr := canonicalExternalBaseURL(targetURL); canonErr == nil && canonical != "" {
		targetURL = canonical
	}
	alreadySynced := externalSettingsCanonicalAndSynced(manager, managerOK, setup, setupOK, targetURL, resolved.Connection.ManagementKey)
	return targetURL, resolved.Connection.ManagementKey, !alreadySynced, false, nil
}

func (s *Service) syncExternalManagerConnection(ctx context.Context, rawBaseURL, rawManagementKey string) error {
	canonicalURL, err := canonicalExternalBaseURL(rawBaseURL)
	if err != nil || canonicalURL == "" {
		return upgrade.CodedError(
			upgrade.UpgradeErrorExternalAuthorityUnavailable,
			"the CPA address is not a usable http(s) endpoint; nothing was written to Manager settings",
			err,
		)
	}
	secret := strings.TrimSpace(rawManagementKey)
	if secret == "" {
		return errors.New("external CPA connection is incomplete")
	}
	dbPath := upgrade.TargetDatabasePath(s.options.TargetRoot)
	targetLock, err := processlock.Acquire(dbPath)
	if err != nil {
		return upgrade.ErrStoreLocked
	}
	defer targetLock.Close()

	keyBytes, err := security.LoadExistingDataKey("", s.options.dataKeyPath())
	if err != nil {
		return err
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	op, opFound, err := upgrade.ReadRecordFrom(ctx, st.SQLDB())
	if err != nil {
		return err
	}
	if !opFound || op.Stage != model.UpgradeStageExternalConnect {
		return upgrade.CodedError(upgrade.UpgradeErrorExternalNotApplicable, "upgrade operation is no longer at external_connect", nil)
	}
	binding, hasBinding, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(ctx)
	if err != nil {
		return err
	}
	if !hasBinding || !sameExternalBaseURL(binding.BaseURL, canonicalURL) {
		return upgrade.CodedError(upgrade.UpgradeErrorExternalTargetMoved, "recorded External target no longer matches this execution", nil)
	}
	if !security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
		return upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_external_connect_conflict"), "recorded External management key is missing", nil)
	}
	recordedKey, err := protector.UnprotectString(binding.ManagementKey)
	if err != nil {
		return upgrade.CodedError(upgrade.UpgradeErrorExternalManagementRejected, "recorded External management key cannot be decrypted", err)
	}
	if !security.EqualHMAC(strings.TrimSpace(recordedKey), secret) {
		return upgrade.CodedError(upgrade.UpgradeErrorCode("upgrade_external_connect_conflict"), "recorded External management key changed concurrently", nil)
	}

	managerCfg, _, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return err
	}
	next := managerCfg
	managerconfig.MergeLegacyCollectorSettings(&next, setup, setupOK)
	next.CPAConnection.CPABaseURL = canonicalURL
	next.CPAConnection.ManagementKey = secret
	nextSetup := managerconfig.CanonicalSetupFromManagerConfig(next, setup, setupOK)
	if err := st.SaveManagerConfigAndSetup(ctx, next, nextSetup); err != nil {
		return err
	}
	verified, found, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	if !found || !sameExternalBaseURL(verified.CPAConnection.CPABaseURL, canonicalURL) ||
		!security.EqualHMAC(strings.TrimSpace(verified.CPAConnection.ManagementKey), secret) {
		return errors.New("verify stored External CPA connection failed")
	}
	return nil
}
