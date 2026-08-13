package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	updatecore "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/update"
)

type Service struct {
	manifestPath string
	dataDir      string
	dbPath       string
	dataKeyPath  string
	releases     updatecore.ReleaseClient
	mu           sync.Mutex
}

func New(manifestPath, dataDir, dbPath, dataKeyPath string, releases updatecore.ReleaseClient) *Service {
	return &Service{manifestPath: manifestPath, dataDir: dataDir, dbPath: dbPath, dataKeyPath: dataKeyPath, releases: releases}
}

func (s *Service) Capability() updatecore.Capability {
	capability := updatecore.DetectCapability(s.manifestPath)
	if !capability.Supported {
		return capability
	}
	if updatecore.NormalizeVersion(buildinfo.RuntimeVersion()) == "" {
		capability.Supported = false
		capability.BackupSupported = false
		capability.RollbackSupport = false
		capability.Reason = "runtime_version_unavailable"
		return capability
	}
	manifest, err := updatecore.LoadInstallManifest(s.manifestPath)
	if err != nil {
		capability.Supported = false
		capability.Reason = "invalid_install_manifest"
		return capability
	}
	root, _ := filepath.Abs(manifest.InstallRoot)
	dataRoot, dataErr := filepath.Abs(s.dataDir)
	if dataErr != nil || dataRoot == root || !updatecore.PathWithin(root, dataRoot) {
		capability.Supported = false
		capability.BackupSupported = false
		capability.RollbackSupport = false
		capability.Reason = "unsupported_data_layout"
		return capability
	}
	backupRoot, backupErr := filepath.Abs(manifest.BackupRoot)
	secretsRoot := filepath.Join(root, "secrets")
	if backupErr != nil || updatecore.PathWithin(dataRoot, backupRoot) || updatecore.PathWithin(backupRoot, dataRoot) ||
		updatecore.PathWithin(secretsRoot, backupRoot) || updatecore.PathWithin(backupRoot, secretsRoot) ||
		updatecore.PathWithin(secretsRoot, dataRoot) || updatecore.PathWithin(dataRoot, secretsRoot) {
		capability.Supported = false
		capability.BackupSupported = false
		capability.RollbackSupport = false
		capability.Reason = "unsupported_backup_layout"
		return capability
	}
	for _, candidate := range []string{s.dbPath, s.dataKeyPath} {
		absolute, err := filepath.Abs(candidate)
		if err != nil || !updatecore.PathWithin(dataRoot, absolute) {
			capability.Supported = false
			capability.BackupSupported = false
			capability.RollbackSupport = false
			capability.Reason = "unsupported_data_layout"
			return capability
		}
	}
	return capability
}

func (s *Service) Check(ctx context.Context) (updatecore.ReleaseCheck, error) {
	return s.releases.Check(ctx, buildinfo.RuntimeVersion())
}

func (s *Service) Status() (updatecore.TransactionStatus, bool, error) {
	capability := s.Capability()
	if !capability.Supported {
		return updatecore.TransactionStatus{}, false, nil
	}
	manifest, err := updatecore.LoadInstallManifest(s.manifestPath)
	if err != nil {
		return updatecore.TransactionStatus{}, false, err
	}
	status, err := updatecore.ReadTransactionStatus(filepath.Join(manifest.InstallRoot, ".update", "status.json"))
	if os.IsNotExist(err) {
		return updatecore.TransactionStatus{}, false, nil
	}
	if err == nil && status.InstallID != manifest.InstallID {
		return updatecore.TransactionStatus{}, false, errors.New("managed update status belongs to another installation")
	}
	return status, err == nil, err
}

func (s *Service) Plan(ctx context.Context, httpAddress string) (updatecore.TransactionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capability := s.Capability()
	if !capability.Supported {
		return updatecore.TransactionStatus{}, errors.New("managed native updates are not supported by this installation")
	}
	existing, found, err := s.Status()
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	if found && existing.State == updatecore.StateManualRecoveryRequired {
		return updatecore.TransactionStatus{}, errors.New("manual update recovery is required before preparing another update")
	}
	if found && !existing.Terminal() {
		return updatecore.TransactionStatus{}, errors.New("an update transaction is already active")
	}
	manifest, err := updatecore.LoadInstallManifest(s.manifestPath)
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	protectedTransactionID := ""
	if found {
		protectedTransactionID = existing.TransactionID
	}
	if err := updatecore.CleanupTerminalTransactions(manifest.InstallRoot, protectedTransactionID); err != nil {
		return updatecore.TransactionStatus{}, err
	}
	dataPaths := uniquePaths([]string{s.dataDir, filepath.Join(manifest.InstallRoot, "secrets")})
	prepared, err := updatecore.PrepareUpdate(ctx, updatecore.PrepareOptions{
		ManifestPath:   s.manifestPath,
		CurrentVersion: buildinfo.RuntimeVersion(),
		DataPaths:      dataPaths,
		HTTPAddress:    httpAddress,
		ReleaseClient:  s.releases,
	})
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	return prepared.Status, nil
}

func (s *Service) Apply(shutdown func() error) (updatecore.TransactionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shutdown == nil {
		return updatecore.TransactionStatus{}, errors.New("managed shutdown is unavailable")
	}
	manifest, err := updatecore.LoadInstallManifest(s.manifestPath)
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	status, found, err := s.Status()
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	if !found || status.State != updatecore.StateStaged {
		return updatecore.TransactionStatus{}, errors.New("no staged update is ready to apply")
	}
	statusPath := filepath.Join(manifest.InstallRoot, ".update", "status.json")
	transactionRoot := filepath.Join(manifest.InstallRoot, ".update", "transactions", status.TransactionID)
	transactionPath := filepath.Join(transactionRoot, "transaction.json")
	transaction, err := updatecore.ReadTransaction(transactionPath)
	if err != nil {
		return updatecore.TransactionStatus{}, err
	}
	if updatecore.ValidateTransactionStatus(manifest, transaction, status) != nil {
		return updatecore.TransactionStatus{}, errors.New("staged update transaction does not match status")
	}
	runtimeVersion := buildinfo.RuntimeVersion()
	if updatecore.NormalizeVersion(runtimeVersion) != updatecore.NormalizeVersion(transaction.CurrentVersion) {
		return updatecore.TransactionStatus{}, errors.New("staged update no longer matches the running version; prepare the update again")
	}
	if comparison, ok := updatecore.CompareVersions(runtimeVersion, transaction.TargetVersion); !ok || comparison <= 0 {
		return updatecore.TransactionStatus{}, errors.New("staged update target is not newer than the running version")
	}
	transaction.ParentPID = os.Getpid()
	if err := updatecore.WriteTransaction(transactionPath, transaction); err != nil {
		return updatecore.TransactionStatus{}, err
	}
	status.State = updatecore.StateLaunching
	status.Message = "launching detached updater"
	if err := updatecore.WriteTransactionStatus(statusPath, status); err != nil {
		return updatecore.TransactionStatus{}, err
	}
	managed := updatecore.RuntimeManagedFiles()
	process, err := updatecore.StartDetachedUpdater(
		filepath.Join(transactionRoot, managed.Updater),
		transactionPath,
	)
	if err != nil {
		status.State = updatecore.StateStaged
		status.Message = "update is staged and ready to apply"
		_ = updatecore.WriteTransactionStatus(statusPath, status)
		return updatecore.TransactionStatus{}, err
	}
	if err := waitForUpdaterHandshake(statusPath, status.TransactionID, process.Pid, 5*time.Second); err != nil {
		_ = process.Kill()
		_ = process.Release()
		status.State = updatecore.StateStaged
		status.Message = "update is staged and ready to apply"
		status.UpdaterPID = 0
		_ = updatecore.WriteTransactionStatus(statusPath, status)
		return updatecore.TransactionStatus{}, err
	}
	_ = process.Release()
	go func() {
		time.Sleep(250 * time.Millisecond)
		_ = shutdown()
	}()
	return status, nil
}

func waitForUpdaterHandshake(statusPath, transactionID string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := updatecore.ReadTransactionStatus(statusPath)
		if err == nil && status.TransactionID == transactionID && status.UpdaterPID == pid && status.State == updatecore.StateStopping {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("detached updater did not become ready")
}

func uniquePaths(paths []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil || absolute == "" {
			continue
		}
		key := filepath.Clean(absolute)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}
