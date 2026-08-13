package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const installManifestSchemaVersion = 2

type InstallManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstallID     string `json:"installId"`
	Mode          string `json:"mode"`
	Platform      string `json:"platform"`
	Architecture  string `json:"architecture"`
	InstallRoot   string `json:"installRoot"`
	BinaryPath    string `json:"binaryPath"`
	ControlScript string `json:"controlScript"`
	UpdaterPath   string `json:"updaterPath"`
	BackupRoot    string `json:"backupRoot"`
	LaunchMode    string `json:"launchMode"`
	Enabled       bool   `json:"enabled"`
}

type Capability struct {
	Supported       bool   `json:"supported"`
	Reason          string `json:"reason,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Platform        string `json:"platform"`
	Architecture    string `json:"architecture"`
	BackupSupported bool   `json:"backupSupported"`
	RollbackSupport bool   `json:"rollbackSupported"`
}

func DetectCapability(manifestPath string) Capability {
	capability := Capability{Platform: runtime.GOOS, Architecture: runtime.GOARCH}
	if strings.TrimSpace(manifestPath) == "" {
		capability.Reason = "managed_updates_not_enabled"
		return capability
	}
	manifest, err := LoadInstallManifest(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			capability.Reason = "managed_updates_not_enabled"
		} else {
			capability.Reason = "invalid_install_manifest"
		}
		return capability
	}
	if !manifest.Enabled {
		capability.Reason = "managed_updates_disabled"
		return capability
	}
	capability.Supported = true
	capability.Mode = manifest.Mode
	capability.BackupSupported = true
	capability.RollbackSupport = true
	return capability
}

func LoadInstallManifest(path string) (InstallManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return InstallManifest{}, errors.New("install manifest path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return InstallManifest{}, fmt.Errorf("stat install manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return InstallManifest{}, errors.New("install manifest must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallManifest{}, fmt.Errorf("read install manifest: %w", err)
	}
	var manifest InstallManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return InstallManifest{}, fmt.Errorf("parse install manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return InstallManifest{}, fmt.Errorf("parse install manifest: %w", err)
	}
	if err := validateInstallManifest(manifest); err != nil {
		return InstallManifest{}, err
	}
	expectedPath := filepath.Join(manifest.InstallRoot, ".update", "install.json")
	absolutePath, _ := filepath.Abs(path)
	if !strings.EqualFold(filepath.Clean(absolutePath), filepath.Clean(expectedPath)) {
		return InstallManifest{}, errors.New("install manifest must use the managed .update/install.json path")
	}
	return manifest, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validateInstallManifest(manifest InstallManifest) error {
	if manifest.SchemaVersion != installManifestSchemaVersion {
		return fmt.Errorf("unsupported install manifest schema version: %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.InstallID) == "" {
		return errors.New("install manifest installId is required")
	}
	if manifest.Mode != "native-control-script" {
		return errors.New("install manifest mode is unsupported")
	}
	if manifest.LaunchMode != "control-script-default" {
		return errors.New("install manifest launchMode is unsupported")
	}
	if manifest.Platform != runtime.GOOS || manifest.Architecture != runtime.GOARCH {
		return errors.New("install manifest platform does not match this runtime")
	}
	root, err := filepath.Abs(manifest.InstallRoot)
	if err != nil || strings.TrimSpace(manifest.InstallRoot) == "" {
		return errors.New("install manifest installRoot is invalid")
	}
	control, err := filepath.Abs(manifest.ControlScript)
	if err != nil || strings.TrimSpace(manifest.ControlScript) == "" {
		return errors.New("install manifest controlScript is invalid")
	}
	binary, err := filepath.Abs(manifest.BinaryPath)
	if err != nil || strings.TrimSpace(manifest.BinaryPath) == "" {
		return errors.New("install manifest binaryPath is invalid")
	}
	backup, err := filepath.Abs(manifest.BackupRoot)
	if err != nil || strings.TrimSpace(manifest.BackupRoot) == "" {
		return errors.New("install manifest backupRoot is invalid")
	}
	updater, err := filepath.Abs(manifest.UpdaterPath)
	if err != nil || strings.TrimSpace(manifest.UpdaterPath) == "" {
		return errors.New("install manifest updaterPath is invalid")
	}
	if !pathWithin(root, binary) || !pathWithin(root, control) || !pathWithin(root, backup) || !pathWithin(root, updater) {
		return errors.New("install manifest paths must stay within installRoot")
	}
	if filepath.Clean(backup) == filepath.Clean(root) || strings.EqualFold(filepath.Base(backup), ".update") {
		return errors.New("install manifest backupRoot must be a dedicated child directory")
	}
	managed := RuntimeManagedFiles()
	if !strings.EqualFold(filepath.Dir(binary), root) || !strings.EqualFold(filepath.Dir(control), root) ||
		!strings.EqualFold(filepath.Dir(updater), root) ||
		!strings.EqualFold(filepath.Base(binary), managed.Binary) ||
		!strings.EqualFold(filepath.Base(control), managed.Control) ||
		!strings.EqualFold(filepath.Base(updater), managed.Updater) {
		return errors.New("install manifest managed file names do not match this runtime")
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftClean := filepath.Clean(leftAbsolute)
	rightClean := filepath.Clean(rightAbsolute)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftClean, rightClean)
	}
	return leftClean == rightClean
}

func PathWithin(root, candidate string) bool {
	return pathWithin(root, candidate)
}
