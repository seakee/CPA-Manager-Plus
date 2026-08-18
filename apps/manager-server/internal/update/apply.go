package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func ApplyTransaction(ctx context.Context, transactionPath string) error {
	transaction, err := ReadTransaction(transactionPath)
	if err != nil {
		return err
	}
	manifest, err := LoadInstallManifest(transaction.InstallManifest)
	if err != nil {
		return err
	}
	status, err := ReadTransactionStatus(transaction.StatusPath)
	if err != nil {
		return err
	}
	if ValidateTransactionStatus(manifest, transaction, status) != nil || (status.State != StateStaged && status.State != StateLaunching) {
		return errors.New("update transaction is not staged")
	}
	status.UpdaterPID = os.Getpid()
	updateStatus := func(state TransactionState, message string) error {
		status.State = state
		status.Message = message
		return WriteTransactionStatus(transaction.StatusPath, status)
	}
	if err := updateStatus(StateStopping, "waiting for Manager Server to stop"); err != nil {
		return err
	}
	if err := waitForProcessExit(ctx, transaction.ParentPID, 45*time.Second); err != nil {
		_ = updateStatus(StateFailed, err.Error())
		return err
	}
	if err := CheckBackupCapacity(manifest, transaction.DataPaths); err != nil {
		_ = updateStatus(StateFailed, "backup preflight failed after shutdown: "+err.Error())
		_ = startManagedProcess(manifest)
		return err
	}
	backupPath := filepath.Join(manifest.BackupRoot, fmt.Sprintf("update-%s-%s-to-%s-%s", time.Now().UTC().Format("20060102-150405"), safeVersion(transaction.CurrentVersion), safeVersion(transaction.TargetVersion), safeTransactionPrefix(transaction.TransactionID)))
	status.BackupPath = backupPath
	if err := updateStatus(StateBackingUp, "creating offline rollback snapshot"); err != nil {
		_ = startManagedProcess(manifest)
		return err
	}
	if err := createRollbackSnapshot(manifest, transaction, backupPath); err != nil {
		_ = updateStatus(StateFailed, "rollback snapshot failed: "+err.Error())
		_ = startManagedProcess(manifest)
		return err
	}
	if err := updateStatus(StateSwitching, "switching managed program files"); err != nil {
		_ = startManagedProcess(manifest)
		return err
	}
	if err := installManagedFiles(manifest, transaction.PackageRoot); err != nil {
		_ = updateStatus(StateRollingBack, "managed file switch failed; restoring rollback snapshot")
		if rollbackErr := restoreRollbackSnapshot(manifest, transaction, backupPath); rollbackErr != nil {
			_ = updateStatus(StateManualRecoveryRequired, "program switch and rollback failed: "+rollbackErr.Error())
			return errors.Join(err, rollbackErr)
		}
		_ = startManagedProcess(manifest)
		_ = updateStatus(StateRolledBack, "program switch failed and the previous version was restored")
		return err
	}
	if err := updateStatus(StateStarting, "starting the target version"); err != nil {
		return rollbackAfterFailure(manifest, transaction, backupPath, err, updateStatus)
	}
	if err := startManagedProcess(manifest); err == nil {
		if statusErr := updateStatus(StateValidating, "validating target version health"); statusErr != nil {
			err = statusErr
		} else {
			err = validateTargetVersion(ctx, manifest, transaction)
		}
	}
	if err == nil {
		message := "update completed successfully"
		if cleanupErr := CleanupTransactionStaging(transactionPath); cleanupErr != nil {
			message = "update completed successfully; staged files could not be removed: " + cleanupErr.Error()
		}
		if statusErr := updateStatus(StateSucceeded, message); statusErr != nil {
			return rollbackAfterFailure(manifest, transaction, backupPath, statusErr, updateStatus)
		}
		return nil
	}
	return rollbackAfterFailure(manifest, transaction, backupPath, err, updateStatus)
}

func rollbackAfterFailure(manifest InstallManifest, transaction Transaction, backupPath string, cause error, updateStatus func(TransactionState, string) error) error {
	_ = updateStatus(StateRollingBack, "target version validation failed; restoring rollback snapshot")
	_ = stopManagedProcess(manifest)
	if rollbackErr := restoreRollbackSnapshot(manifest, transaction, backupPath); rollbackErr != nil {
		_ = updateStatus(StateManualRecoveryRequired, "target version failed and rollback failed: "+rollbackErr.Error())
		return errors.Join(cause, rollbackErr)
	}
	if startErr := startManagedProcess(manifest); startErr != nil {
		_ = updateStatus(StateManualRecoveryRequired, "rollback restored files but the previous version did not start: "+startErr.Error())
		return errors.Join(cause, startErr)
	}
	_ = updateStatus(StateRolledBack, "target version failed and the previous version was restored")
	return cause
}

func RecoverInterruptedUpdate(manifestPath string) (TransactionStatus, bool, error) {
	manifest, err := LoadInstallManifest(manifestPath)
	if err != nil {
		return TransactionStatus{}, false, err
	}
	statusPath := filepath.Join(manifest.InstallRoot, ".update", "status.json")
	status, err := ReadTransactionStatus(statusPath)
	if os.IsNotExist(err) {
		return TransactionStatus{}, false, nil
	}
	if err != nil {
		return TransactionStatus{}, false, err
	}
	if status.InstallID != manifest.InstallID {
		status.State = StateManualRecoveryRequired
		status.Message = "interrupted update status belongs to another installation"
		_ = WriteTransactionStatus(statusPath, status)
		return status, false, errors.New(status.Message)
	}
	if status.Terminal() || status.State == StateStaged {
		return status, false, nil
	}
	if status.UpdaterPID > 0 {
		running, processErr := processExists(status.UpdaterPID)
		if processErr != nil {
			return status, false, fmt.Errorf("verify managed updater process: %w", processErr)
		}
		if running {
			return status, false, errors.New("managed update is still active")
		}
	}
	if status.State == StateDownloading || status.State == StateVerifying || status.State == StateLaunching ||
		status.State == StateStopping || status.State == StateBackingUp {
		status.State = StateFailed
		status.Message = "interrupted update stopped before managed files were switched"
		status.UpdaterPID = 0
		if err := WriteTransactionStatus(statusPath, status); err != nil {
			return status, false, err
		}
		return status, true, nil
	}
	transactionPath := filepath.Join(manifest.InstallRoot, ".update", "transactions", status.TransactionID, "transaction.json")
	transaction, err := ReadTransaction(transactionPath)
	if err != nil {
		status.State = StateManualRecoveryRequired
		status.Message = "interrupted update transaction is unavailable: " + err.Error()
		_ = WriteTransactionStatus(statusPath, status)
		return status, false, err
	}
	if err := ValidateTransactionStatus(manifest, transaction, status); err != nil {
		status.State = StateManualRecoveryRequired
		status.Message = "interrupted update transaction does not match its status"
		_ = WriteTransactionStatus(statusPath, status)
		return status, false, err
	}
	switch status.State {
	case StateSwitching, StateStarting, StateValidating, StateRollingBack:
		if status.BackupPath == "" || !pathWithin(manifest.BackupRoot, status.BackupPath) {
			status.State = StateManualRecoveryRequired
			status.Message = "interrupted update has no trusted rollback snapshot"
			_ = WriteTransactionStatus(statusPath, status)
			return status, false, errors.New(status.Message)
		}
		if err := restoreRollbackSnapshot(manifest, transaction, status.BackupPath); err != nil {
			status.State = StateManualRecoveryRequired
			status.Message = "interrupted update rollback failed: " + err.Error()
			_ = WriteTransactionStatus(statusPath, status)
			return status, false, err
		}
		status.State = StateRolledBack
		status.Message = "interrupted update was rolled back before startup"
		status.UpdaterPID = 0
		if err := WriteTransactionStatus(statusPath, status); err != nil {
			return status, false, err
		}
		return status, true, nil
	default:
		return status, false, fmt.Errorf("managed update is in an unsupported recovery state: %s", status.State)
	}
}

func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := processExists(pid)
		if err == nil && !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("Manager Server process %d did not stop before the update timeout", pid)
}

func processExists(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "parameter is incorrect") || strings.Contains(message, "cannot find") || strings.Contains(message, "not found") {
			return false, nil
		}
		return false, err
	}
	return platformProcessExists(process)
}

func createRollbackSnapshot(manifest InstallManifest, transaction Transaction, backupPath string) error {
	if err := ensurePrivateDirectory(backupPath); err != nil {
		return err
	}
	programRoot := filepath.Join(backupPath, "program")
	dataRoot := filepath.Join(backupPath, "data")
	if err := ensurePrivateDirectory(programRoot); err != nil {
		return err
	}
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control, "config.json"} {
		source := filepath.Join(manifest.InstallRoot, name)
		if _, err := os.Lstat(source); os.IsNotExist(err) && name == "config.json" {
			continue
		}
		if err := copyPath(source, filepath.Join(programRoot, name)); err != nil {
			return err
		}
	}
	for _, source := range transaction.DataPaths {
		relative, err := filepath.Rel(manifest.InstallRoot, source)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return errors.New("rollback data path is invalid")
		}
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		}
		if err := copyPath(source, filepath.Join(dataRoot, relative)); err != nil {
			return err
		}
	}
	backupManifest, err := buildBackupManifest(manifest, transaction, backupPath)
	if err != nil {
		return err
	}
	manifestBytes, err := json.MarshalIndent(backupManifest, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(backupPath, "backup-manifest.json"), append(manifestBytes, '\n'))
}

func installManagedFiles(manifest InstallManifest, packageRoot string) error {
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control} {
		if err := replaceFile(filepath.Join(packageRoot, name), filepath.Join(manifest.InstallRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func restoreRollbackSnapshot(manifest InstallManifest, transaction Transaction, backupPath string) error {
	backupManifest, err := readAndVerifyBackupManifest(manifest, transaction, backupPath)
	if err != nil {
		return err
	}
	programRoot := filepath.Join(backupPath, "program")
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control, "config.json"} {
		source := filepath.Join(programRoot, name)
		if !backupManifest.rootExisted("program", name) {
			if err := os.RemoveAll(filepath.Join(manifest.InstallRoot, name)); err != nil {
				return err
			}
			continue
		}
		if err := replacePath(source, filepath.Join(manifest.InstallRoot, name)); err != nil {
			return err
		}
	}
	dataRoot := filepath.Join(backupPath, "data")
	for _, destination := range transaction.DataPaths {
		relative, err := filepath.Rel(manifest.InstallRoot, destination)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return errors.New("rollback data path is invalid")
		}
		source := filepath.Join(dataRoot, relative)
		if !backupManifest.rootExisted("data", relative) {
			if err := os.RemoveAll(destination); err != nil {
				return err
			}
			continue
		}
		if err := replacePath(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func startManagedProcess(manifest InstallManifest) error {
	return runControlScript(manifest, "start")
}

func stopManagedProcess(manifest InstallManifest) error {
	return runControlScript(manifest, "stop")
}

func runControlScript(manifest InstallManifest, operation string) error {
	var command *exec.Cmd
	if strings.HasSuffix(strings.ToLower(manifest.ControlScript), ".ps1") {
		command = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", manifest.ControlScript, operation)
	} else {
		command = exec.Command(manifest.ControlScript, operation)
	}
	command.Dir = manifest.InstallRoot
	command.Env = append(os.Environ(), "CPA_MANAGER_PLUS_UPDATE_STARTING=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("control script %s failed: %w: %s", operation, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validateTargetVersion(ctx context.Context, manifest InstallManifest, transaction Transaction) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(45 * time.Second)
	const stableChecksRequired = 6
	stableChecks := 0
	for time.Now().Before(deadline) {
		healthy := targetVersionHealthy(ctx, client, transaction)
		if healthy {
			stableChecks++
			if stableChecks >= stableChecksRequired {
				if err := runControlScript(manifest, "status"); err == nil {
					return nil
				}
				stableChecks = 0
			}
		} else {
			stableChecks = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("target version did not remain healthy with the expected runtime version")
}

func targetVersionHealthy(ctx context.Context, client *http.Client, transaction Transaction) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, transaction.HealthURL, nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false
	}
	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, transaction.InfoURL, nil)
	if err != nil {
		return false
	}
	infoRes, err := client.Do(infoReq)
	if err != nil {
		return false
	}
	defer infoRes.Body.Close()
	if infoRes.StatusCode < 200 || infoRes.StatusCode >= 300 {
		return false
	}
	var info struct {
		RuntimeVersion string `json:"runtimeVersion"`
	}
	return json.NewDecoder(infoRes.Body).Decode(&info) == nil &&
		NormalizeVersion(info.RuntimeVersion) == NormalizeVersion(transaction.TargetVersion)
}

func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed backup refuses symbolic links")
	}
	if info.IsDir() {
		if err := ensurePrivateDirectory(destination); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed backup only supports regular files and directories")
	}
	mode := os.FileMode(0o600)
	if info.Mode().Perm()&0o111 != 0 {
		mode = 0o700
	}
	return copyFile(source, destination, mode)
}

func replaceFile(source, destination string) error {
	return replacePath(source, destination)
}

func replacePath(source, destination string) error {
	temporary := destination + ".restore-new-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := copyPathForRestore(source, temporary); err != nil {
		return err
	}
	temporaryInfo, err := os.Lstat(temporary)
	if err != nil {
		return err
	}
	if temporaryInfo.Mode().IsRegular() {
		if err := replaceRegularFile(temporary, destination); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return nil
	}
	old := destination + ".restore-old-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, old); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(old, destination)
		return err
	}
	return os.RemoveAll(old)
}

func copyPathForRestore(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed restore refuses symbolic links")
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPathForRestore(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed restore only supports regular files and directories")
	}
	return copyFileRaw(source, destination, info.Mode().Perm())
}

func copyFileRaw(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

type BackupManifest struct {
	SchemaVersion  int          `json:"schemaVersion"`
	TransactionID  string       `json:"transactionId"`
	CurrentVersion string       `json:"currentVersion"`
	TargetVersion  string       `json:"targetVersion"`
	CreatedAt      string       `json:"createdAt"`
	Roots          []BackupRoot `json:"roots"`
	Files          []BackupFile `json:"files"`
}

type BackupRoot struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
}

type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (m BackupManifest) rootExisted(kind, path string) bool {
	clean := filepath.Clean(path)
	for _, root := range m.Roots {
		if root.Kind == kind && filepath.Clean(root.Path) == clean {
			return root.Existed
		}
	}
	return false
}

func buildBackupManifest(manifest InstallManifest, transaction Transaction, backupPath string) (BackupManifest, error) {
	result := BackupManifest{
		SchemaVersion:  1,
		TransactionID:  transaction.TransactionID,
		CurrentVersion: transaction.CurrentVersion,
		TargetVersion:  transaction.TargetVersion,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control, "config.json"} {
		_, err := os.Lstat(filepath.Join(manifest.InstallRoot, name))
		result.Roots = append(result.Roots, BackupRoot{Kind: "program", Path: name, Existed: err == nil})
	}
	for _, source := range transaction.DataPaths {
		relative, err := filepath.Rel(manifest.InstallRoot, source)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return BackupManifest{}, errors.New("rollback data path is invalid")
		}
		_, statErr := os.Lstat(source)
		result.Roots = append(result.Roots, BackupRoot{Kind: "data", Path: relative, Existed: statErr == nil})
	}
	files, err := backupFiles(backupPath)
	if err != nil {
		return BackupManifest{}, err
	}
	result.Files = files
	return result, nil
}

func readAndVerifyBackupManifest(manifest InstallManifest, transaction Transaction, backupPath string) (BackupManifest, error) {
	data, err := os.ReadFile(filepath.Join(backupPath, "backup-manifest.json"))
	if err != nil {
		return BackupManifest{}, fmt.Errorf("read rollback manifest: %w", err)
	}
	var result BackupManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return BackupManifest{}, fmt.Errorf("parse rollback manifest: %w", err)
	}
	if result.SchemaVersion != 1 || result.TransactionID != transaction.TransactionID ||
		NormalizeVersion(result.CurrentVersion) != NormalizeVersion(transaction.CurrentVersion) ||
		NormalizeVersion(result.TargetVersion) != NormalizeVersion(transaction.TargetVersion) {
		return BackupManifest{}, errors.New("rollback manifest does not match update transaction")
	}
	expectedFiles := make(map[string]BackupFile, len(result.Files))
	for _, file := range result.Files {
		if file.Path == "" || filepath.IsAbs(file.Path) || strings.HasPrefix(filepath.Clean(file.Path), "..") || !validSHA256(file.SHA256) {
			return BackupManifest{}, errors.New("rollback manifest contains an invalid file entry")
		}
		key := strings.ToLower(filepath.Clean(file.Path))
		if _, exists := expectedFiles[key]; exists {
			return BackupManifest{}, errors.New("rollback manifest contains duplicate file entries")
		}
		expectedFiles[key] = file
	}
	actualFiles, err := backupFiles(backupPath)
	if err != nil {
		return BackupManifest{}, err
	}
	if len(actualFiles) != len(expectedFiles) {
		return BackupManifest{}, errors.New("rollback backup file set does not match its manifest")
	}
	for _, actual := range actualFiles {
		expected, ok := expectedFiles[strings.ToLower(filepath.Clean(actual.Path))]
		if !ok || actual.Size != expected.Size || !strings.EqualFold(actual.SHA256, expected.SHA256) {
			return BackupManifest{}, fmt.Errorf("rollback backup integrity check failed for %s", actual.Path)
		}
	}
	return result, nil
}

func backupFiles(backupPath string) ([]BackupFile, error) {
	files := []BackupFile{}
	for _, rootName := range []string{"program", "data"} {
		root := filepath.Join(backupPath, rootName)
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("rollback backup contains a symbolic link")
			}
			if entry.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return errors.New("rollback backup contains an unsupported file")
			}
			digest, size, err := hashFile(path)
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(backupPath, path)
			files = append(files, BackupFile{Path: relative, Size: size, SHA256: digest})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func safeVersion(version string) string {
	version = strings.TrimPrefix(NormalizeVersion(version), "v")
	if version == "" {
		return "unknown"
	}
	return strings.ReplaceAll(version, "/", "-")
}

func safeTransactionPrefix(transactionID string) string {
	if len(transactionID) > 8 {
		return transactionID[:8]
	}
	if transactionID == "" {
		return "unknown"
	}
	return transactionID
}
