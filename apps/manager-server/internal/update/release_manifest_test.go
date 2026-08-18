package update

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestParseUpdateManifestEnforcesMinimumUpdaterVersion(t *testing.T) {
	assetName := NativeAssetName("v1.2.3", runtime.GOOS, runtime.GOARCH)
	manifest := UpdateManifest{
		SchemaVersion:         1,
		Version:               "v1.2.3",
		Channel:               "stable",
		MinimumUpdaterVersion: "v1.0.0",
		Assets: map[string]UpdateManifestAsset{
			runtime.GOOS + "-" + runtime.GOARCH: {Name: assetName, SHA256: strings.Repeat("a", 64)},
		},
	}
	data, _ := json.Marshal(manifest)
	if _, _, err := ParseUpdateManifest(data, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	manifest.MinimumUpdaterVersion = "v1.0.1"
	data, _ = json.Marshal(manifest)
	if _, _, err := ParseUpdateManifest(data, "v1.2.3"); err == nil {
		t.Fatal("ParseUpdateManifest() accepted a newer required updater protocol")
	}
}
