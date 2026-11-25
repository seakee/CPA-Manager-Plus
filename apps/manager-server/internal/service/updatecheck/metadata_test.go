package updatecheck

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIndexValidateRequiresReleaseInfoSHA256OnEveryNonNullV2Target(t *testing.T) {
	clock := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	validSHA := "sha256:" + strings.Repeat("b", 64)

	valid := Index{
		SchemaVersion: 2,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": targetFor("v2.0.0", validSHA),
			"rc":     targetFor("v2.0.1-rc.1", validSHA),
			"beta":   targetFor("v2.0.1-beta.1", validSHA),
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid schema 2 index failed Validate(): %v", err)
	}

	for _, channel := range []string{"stable", "rc", "beta"} {
		versionByChannel := map[string]string{
			"stable": "v2.0.0",
			"rc":     "v2.0.1-rc.1",
			"beta":   "v2.0.1-beta.1",
		}
		for _, badDigest := range []struct {
			name   string
			digest string
		}{
			{"empty", ""},
			{"missing_prefix", strings.Repeat("b", 64)},
			{"uppercase", "sha256:" + strings.Repeat("B", 64)},
			{"short", "sha256:bbbb"},
			{"whitespace", " " + validSHA},
		} {
			t.Run(channel+"_"+badDigest.name, func(t *testing.T) {
				idx := Index{
					SchemaVersion: 2,
					Revision:      1,
					GeneratedAt:   clock,
					Channels: map[string]*Target{
						"stable": nil,
						"rc":     nil,
						"beta":   nil,
					},
				}
				idx.Channels[channel] = targetFor(versionByChannel[channel], badDigest.digest)
				if err := idx.Validate(); err == nil || err.Error() != "invalid channel target digest" {
					t.Fatalf("Validate() = %v, want invalid channel target digest", err)
				}
			})
		}
	}

	// Wire JSON without release_info_sha256 must also fail Validate().
	rawJSON := `{
		"schema_version": 2,
		"revision": 1,
		"generated_at": "2026-11-26T00:00:00Z",
		"channels": {
			"stable": null,
			"rc": null,
			"beta": {
				"version": "v2.0.0-beta.1",
				"schema_version": 2,
				"release_info_path": "releases/download/v2.0.0-beta.1/release-info.json"
			}
		}
	}`
	var decoded Index
	if err := json.Unmarshal([]byte(rawJSON), &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if err := decoded.Validate(); err == nil || err.Error() != "invalid channel target digest" {
		t.Fatalf("decoded Validate() = %v, want invalid channel target digest", err)
	}
}
