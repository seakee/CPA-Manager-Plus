package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestCPASourceAdmissionDoesNotInventAStackRelease(t *testing.T) {
	for _, config := range []struct{ name, body string }{
		{"canonical", v8FixtureConfig}, {"legacy-grammar-on-v8", v7LayoutFixtureConfig},
	} {
		for _, tc := range []struct {
			name, image, env, secondary, declared string
			compose, envDirectory                 bool
			want                                  UpgradeErrorCode
		}{
			{name: "unread-variable-default", image: "eceasy/cli-proxy-api:${CPA_TAG:-v8.0.55}", env: "CPA_TAG=v7.3.4\n", want: UpgradeErrorCPAVersionRequired},
			{name: "unread-variable-suffix", image: "eceasy/cli-proxy-api:v8.0.0$CPA_SUFFIX", env: "CPA_SUFFIX=-rc.1\n", want: UpgradeErrorCPAVersionRequired},
			{name: "oversized-environment", image: "${CPA_IMAGE:-eceasy/cli-proxy-api:v8.0.55}", env: "CPA_IMAGE=eceasy/cli-proxy-api:v7.3.4\n" + strings.Repeat("#", cpaStackFileBytes+1), want: UpgradeErrorCPAVersionRequired},
			{name: "nonregular-environment", image: "${CPA_IMAGE:-eceasy/cli-proxy-api:v8.0.55}", envDirectory: true, want: UpgradeErrorCPAVersionRequired},
			{name: "build-only-service", compose: true, image: "services:\n  cli-proxy-api:\n    build: .\n", want: UpgradeErrorCPAVersionRequired},
			{name: "unparseable-compose", compose: true, image: "services: [\n", want: UpgradeErrorCPAVersionRequired},
			{name: "primary-compose-wins", image: "eceasy/cli-proxy-api:latest", secondary: "eceasy/cli-proxy-api:v8.0.55", want: UpgradeErrorCPAVersionRequired},
			{name: "declared-v8-cannot-override-v7", image: "eceasy/cli-proxy-api:v7.3.4", declared: "8.0.55", want: UpgradeErrorCPAVersionUnsupported},
			{name: "commented-old-release", image: "${CPA_IMAGE}", env: "CPA_IMAGE=eceasy/cli-proxy-api:v7.3.4 # old/legacy\n", want: UpgradeErrorCPAVersionUnsupported},
			{name: "moving-major", image: "eceasy/cli-proxy-api:v8", want: UpgradeErrorCPAVersionRequired},
			{name: "moving-minor", image: "eceasy/cli-proxy-api:8.0", want: UpgradeErrorCPAVersionRequired},
			{name: "moving-branch", image: "eceasy/cli-proxy-api:main", want: UpgradeErrorCPAVersionRequired},
		} {
			t.Run(config.name+"/"+tc.name, func(t *testing.T) {
				legacy, directory := writeCPAFixture(t, config.body, nil)
				root := filepath.Dir(directory)
				put := func(name, body string) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				compose := tc.image
				if !tc.compose {
					compose = "services:\n  cli-proxy-api:\n    image: " + tc.image + "\n"
				}
				put("compose.yaml", compose)
				if tc.secondary != "" {
					put("docker-compose.yml", "services:\n  cli-proxy-api:\n    image: "+tc.secondary+"\n")
				}
				if tc.env != "" {
					put(".env", tc.env)
				}
				if tc.envDirectory {
					if err := os.Mkdir(filepath.Join(root, ".env"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacy, CPAVersion: tc.declared})
				if code, ok := UpgradeErrorCodeOf(err); !ok || code != tc.want {
					t.Fatalf("admission = %v (%q), want %q", err, code, tc.want)
				}
			})
		}
	}
}

func TestCPASourceConfiguredAuthDirectorySurvivesSymlinkedSource(t *testing.T) {
	config := "config-version: 8\noauth:\n  auth-dir: chosen\nauth-dir: ignored\naccess:\n  api-keys: [fixture-client]\n"
	legacy, directory := writeCPAFixture(t, config, map[string]string{"fallback.json": `{"type":"codex"}`})
	for _, name := range []string{"chosen", "ignored"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name, name+".json"), []byte(`{"type":"codex"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	parentAlias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(filepath.Dir(directory), parentAlias); err != nil {
		t.Fatal(err)
	}
	aliasedDir := filepath.Join(parentAlias, filepath.Base(directory))
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacy, CPADirectory: aliasedDir, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := source.credentialData["chosen.json"]; !ok {
		t.Fatalf("configured canonical auth directory lost: credentials=%v", source.credentials)
	}
	if _, ok := source.credentialData["fallback.json"]; ok {
		t.Fatal("fallback auth directory replaced configured canonical directory")
	}
	if _, ok := source.credentialData["ignored.json"]; ok {
		t.Fatal("legacy auth-dir overrode canonical oauth.auth-dir")
	}
}

func TestRecordedImportRejectsExplicitOldReleaseBeforeRuntimeAvailability(t *testing.T) {
	err := replayRecordedManagedImport(context.Background(), targetPaths{}, CPAImportOptions{CPAVersion: "7.3.4"}, model.UpgradeOperation{}, &CPAImportResult{})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("replay = %v (%q), want %q", err, code, UpgradeErrorCPAVersionUnsupported)
	}
}
