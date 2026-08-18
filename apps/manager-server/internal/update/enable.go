package update

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type EnableOptions struct {
	ManifestPath  string
	InstallRoot   string
	BinaryPath    string
	ControlScript string
	UpdaterPath   string
	BackupRoot    string
}

func EnableManagedUpdates(options EnableOptions) (InstallManifest, error) {
	manifestPath, err := filepath.Abs(strings.TrimSpace(options.ManifestPath))
	if err != nil || strings.TrimSpace(options.ManifestPath) == "" {
		return InstallManifest{}, errors.New("managed update manifest path is invalid")
	}
	installRoot, err := filepath.Abs(strings.TrimSpace(options.InstallRoot))
	if err != nil || strings.TrimSpace(options.InstallRoot) == "" {
		return InstallManifest{}, errors.New("managed update install root is invalid")
	}
	binaryPath, err := filepath.Abs(strings.TrimSpace(options.BinaryPath))
	if err != nil || strings.TrimSpace(options.BinaryPath) == "" {
		return InstallManifest{}, errors.New("managed update binary path is invalid")
	}
	controlScript, err := filepath.Abs(strings.TrimSpace(options.ControlScript))
	if err != nil || strings.TrimSpace(options.ControlScript) == "" {
		return InstallManifest{}, errors.New("managed update control script is invalid")
	}
	updaterPath, err := filepath.Abs(strings.TrimSpace(options.UpdaterPath))
	if err != nil || strings.TrimSpace(options.UpdaterPath) == "" {
		return InstallManifest{}, errors.New("managed update updater path is invalid")
	}
	backupRoot, err := filepath.Abs(strings.TrimSpace(options.BackupRoot))
	if err != nil || strings.TrimSpace(options.BackupRoot) == "" {
		return InstallManifest{}, errors.New("managed update backup root is invalid")
	}
	if !strings.EqualFold(filepath.Clean(manifestPath), filepath.Join(installRoot, ".update", "install.json")) {
		return InstallManifest{}, errors.New("managed update manifest must use .update/install.json")
	}
	for _, required := range []string{binaryPath, controlScript, updaterPath} {
		info, statErr := os.Lstat(required)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return InstallManifest{}, fmt.Errorf("managed update file is unavailable or unsafe: %s", required)
		}
	}
	if !pathWithin(installRoot, manifestPath) || !pathWithin(installRoot, binaryPath) || !pathWithin(installRoot, controlScript) ||
		!pathWithin(installRoot, updaterPath) || !pathWithin(installRoot, backupRoot) {
		return InstallManifest{}, errors.New("managed update paths must stay within install root")
	}
	installID, err := randomInstallID()
	if err != nil {
		return InstallManifest{}, err
	}
	manifest := InstallManifest{
		SchemaVersion: installManifestSchemaVersion,
		InstallID:     installID,
		Mode:          "native-control-script",
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		InstallRoot:   installRoot,
		BinaryPath:    binaryPath,
		ControlScript: controlScript,
		UpdaterPath:   updaterPath,
		BackupRoot:    backupRoot,
		LaunchMode:    "control-script-default",
		Enabled:       true,
	}
	if err := validateInstallManifest(manifest); err != nil {
		return InstallManifest{}, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return InstallManifest{}, err
	}
	data = append(data, '\n')
	if err := writePrivateAtomic(manifestPath, data); err != nil {
		return InstallManifest{}, fmt.Errorf("restrict managed update manifest: %w", err)
	}
	return manifest, nil
}

func randomInstallID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate managed update install id: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
