package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxManifestDownload  = int64(2 * 1024 * 1024)
	maxChecksumsDownload = int64(2 * 1024 * 1024)
	maxPackageDownload   = int64(1024 * 1024 * 1024)
)

type PrepareOptions struct {
	ManifestPath   string
	CurrentVersion string
	DataPaths      []string
	HTTPAddress    string
	Client         HTTPDoer
	ReleaseClient  ReleaseClient
}

type PreparedUpdate struct {
	TransactionPath string
	Status          TransactionStatus
	UpdaterPath     string
}

func PrepareUpdate(ctx context.Context, options PrepareOptions) (PreparedUpdate, error) {
	manifest, err := LoadInstallManifest(options.ManifestPath)
	if err != nil {
		return PreparedUpdate{}, err
	}
	releaseClient := options.ReleaseClient
	if releaseClient.HTTP == nil {
		releaseClient.HTTP = options.Client
	}
	check, err := releaseClient.Check(ctx, options.CurrentVersion)
	if err != nil {
		return PreparedUpdate{}, err
	}
	if !check.Comparable {
		return PreparedUpdate{}, errors.New("current runtime version is not a comparable release version")
	}
	if !check.UpdateAvailable {
		return PreparedUpdate{}, errors.New("no newer stable release is available")
	}
	if !check.Installable {
		return PreparedUpdate{}, fmt.Errorf("latest release is not ready for managed updates: %s", check.InstallReason)
	}
	if NormalizeVersion(options.CurrentVersion) == "" {
		return PreparedUpdate{}, errors.New("current runtime version is not a comparable release version")
	}
	if err := CheckBackupCapacity(manifest, options.DataPaths); err != nil {
		return PreparedUpdate{}, fmt.Errorf("backup preflight failed: %w", err)
	}
	transactionID, err := randomTransactionID()
	if err != nil {
		return PreparedUpdate{}, err
	}
	transactionRoot := filepath.Join(manifest.InstallRoot, ".update", "transactions", transactionID)
	stagingRoot := filepath.Join(transactionRoot, "staging")
	if err := ensurePrivateDirectory(stagingRoot); err != nil {
		return PreparedUpdate{}, err
	}
	statusPath := filepath.Join(manifest.InstallRoot, ".update", "status.json")
	status := TransactionStatus{
		TransactionID:  transactionID,
		InstallID:      manifest.InstallID,
		CurrentVersion: options.CurrentVersion,
		TargetVersion:  check.LatestVersion,
		State:          StateDownloading,
	}
	if err := WriteTransactionStatus(statusPath, status); err != nil {
		_ = os.RemoveAll(transactionRoot)
		return PreparedUpdate{}, err
	}
	fail := func(message string, cause error) (PreparedUpdate, error) {
		status.State = StateFailed
		status.Message = message
		_ = WriteTransactionStatus(statusPath, status)
		_ = os.RemoveAll(transactionRoot)
		return PreparedUpdate{}, cause
	}
	manifestBytes, err := DownloadBytes(ctx, options.Client, check.ManifestAsset, maxManifestDownload)
	if err != nil {
		return fail("failed to download or verify update manifest", err)
	}
	_, manifestAsset, err := ParseUpdateManifest(manifestBytes, check.LatestVersion)
	if err != nil {
		return fail("update manifest validation failed", err)
	}
	checksumsBytes, err := DownloadBytes(ctx, options.Client, check.ChecksumsAsset, maxChecksumsDownload)
	if err != nil {
		return fail("failed to download or verify checksums", err)
	}
	checksums := ParseChecksums(checksumsBytes)
	if checksums[check.Asset.Name] == "" || !strings.EqualFold(checksums[check.Asset.Name], manifestAsset.SHA256) {
		return fail("release checksum sources disagree", errors.New("checksums.txt and update-manifest.json do not match"))
	}
	status.State = StateVerifying
	_ = WriteTransactionStatus(statusPath, status)
	archivePath := filepath.Join(stagingRoot, check.Asset.Name)
	digest, err := DownloadFile(ctx, options.Client, check.Asset, archivePath, maxPackageDownload)
	if err != nil {
		return fail("failed to download or verify native package", err)
	}
	if !strings.EqualFold(digest, manifestAsset.SHA256) || !strings.EqualFold(digest, checksums[check.Asset.Name]) {
		return fail("native package checksum sources disagree", errors.New("native package digest mismatch"))
	}
	extractRoot := filepath.Join(stagingRoot, "extracted")
	if err := ExtractArchive(archivePath, extractRoot); err != nil {
		return fail("native package extraction failed", err)
	}
	packageRoot, err := FindPackageRoot(extractRoot, check.Asset.Name)
	if err != nil {
		return fail("native package layout is invalid", err)
	}
	managed := RuntimeManagedFiles()
	for _, name := range []string{managed.Binary, managed.Updater, managed.Control} {
		info, statErr := os.Lstat(filepath.Join(packageRoot, name))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fail("native package is missing a managed file", fmt.Errorf("unsafe or missing managed file: %s", name))
		}
	}
	runnerPath := filepath.Join(transactionRoot, managed.Updater)
	if err := copyFile(filepath.Join(packageRoot, managed.Updater), runnerPath, 0o700); err != nil {
		return fail("failed to stage updater process", err)
	}
	healthURL, infoURL, err := localStatusURLs(options.HTTPAddress)
	if err != nil {
		return fail("Manager Server listen address is not update-compatible", err)
	}
	transactionPath := filepath.Join(transactionRoot, "transaction.json")
	transaction := Transaction{
		TransactionID:   transactionID,
		InstallManifest: options.ManifestPath,
		StatusPath:      statusPath,
		PackageRoot:     packageRoot,
		DataPaths:       options.DataPaths,
		CurrentVersion:  options.CurrentVersion,
		TargetVersion:   check.LatestVersion,
		ParentPID:       os.Getpid(),
		HealthURL:       healthURL,
		InfoURL:         infoURL,
	}
	if err := WriteTransaction(transactionPath, transaction); err != nil {
		return fail("failed to persist update transaction", err)
	}
	status.State = StateStaged
	status.Message = "update package is verified and staged"
	if err := WriteTransactionStatus(statusPath, status); err != nil {
		_ = os.RemoveAll(transactionRoot)
		return PreparedUpdate{}, err
	}
	return PreparedUpdate{TransactionPath: transactionPath, Status: status, UpdaterPath: runnerPath}, nil
}

func randomTransactionID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func localStatusURLs(address string) (string, string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	parsed := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	return parsed.String() + "/health", parsed.String() + "/usage-service/info", nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if mode.Perm()&0o077 == 0 {
		if err := restrictPrivateFile(destination); err != nil {
			output.Close()
			return err
		}
	}
	if _, err := output.ReadFrom(input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
