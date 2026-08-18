package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

const UpdaterProtocolVersion = "v1.0.0"

type UpdateManifestAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type UpdateManifest struct {
	SchemaVersion         int                            `json:"schemaVersion"`
	Version               string                         `json:"version"`
	Channel               string                         `json:"channel"`
	MinimumUpdaterVersion string                         `json:"minimumUpdaterVersion"`
	Assets                map[string]UpdateManifestAsset `json:"assets"`
}

func ParseUpdateManifest(data []byte, expectedVersion string) (UpdateManifest, UpdateManifestAsset, error) {
	var manifest UpdateManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return UpdateManifest{}, UpdateManifestAsset{}, fmt.Errorf("parse update manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return UpdateManifest{}, UpdateManifestAsset{}, fmt.Errorf("parse update manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("unsupported update manifest schema")
	}
	if NormalizeVersion(manifest.Version) != NormalizeVersion(expectedVersion) {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("update manifest version does not match release")
	}
	if manifest.Channel != "stable" {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("stable update requires a stable manifest")
	}
	minimumUpdater := NormalizeVersion(manifest.MinimumUpdaterVersion)
	if !VersionAtLeast(UpdaterProtocolVersion, minimumUpdater) {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("update requires a newer updater protocol")
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	asset, ok := manifest.Assets[key]
	if !ok {
		return UpdateManifest{}, UpdateManifestAsset{}, fmt.Errorf("update manifest does not support %s", key)
	}
	if asset.Name != NativeAssetName(NormalizeVersion(expectedVersion), runtime.GOOS, runtime.GOARCH) {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("update manifest asset name does not match runtime")
	}
	if !validSHA256(asset.SHA256) {
		return UpdateManifest{}, UpdateManifestAsset{}, errors.New("update manifest asset checksum is invalid")
	}
	return manifest, asset, nil
}

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func ParseChecksums(data []byte) map[string]string {
	checksums := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !validSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "./")
		checksums[name] = strings.ToLower(fields[0])
	}
	return checksums
}
