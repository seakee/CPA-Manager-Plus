package updatecheck

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseDeploymentVariant(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "docker_full_explicit",
			raw:  `{"schemaVersion":1,"role":"cpamp","cpaBundled":true}`,
			want: DeploymentVariantDockerFull,
		},
		{
			name: "docker_full_omitted_cpaBundled",
			raw:  `{"schemaVersion":1,"role":"cpamp"}`,
			want: DeploymentVariantDockerFull,
		},
		{
			name: "docker_slim",
			raw:  `{"schemaVersion":1,"role":"cpamp","cpaBundled":false}`,
			want: DeploymentVariantDockerSlim,
		},
		{
			name: "native",
			raw:  `{"schemaVersion":1,"role":"native","status":"VERIFIED_LICENSE_COLLECTION"}`,
			want: DeploymentVariantNative,
		},
		{
			name: "other_role",
			raw:  `{"schemaVersion":1,"role":"manager"}`,
			want: DeploymentVariantUnknown,
		},
		{
			name: "invalid_json",
			raw:  `{broken`,
			want: DeploymentVariantUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDeploymentVariant([]byte(tc.raw)); got != tc.want {
				t.Fatalf("parseDeploymentVariant() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectDeploymentVariantFromPaths(t *testing.T) {
	tmp := t.TempDir()
	dockerProv := filepath.Join(tmp, "docker", "provenance.json")
	nativeBin := filepath.Join(tmp, "native", "bin", "cpa-manager-plus")
	nativeProv := filepath.Join(tmp, "native", "share", "cpamp", "provenance.json")

	if err := os.MkdirAll(filepath.Dir(dockerProv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nativeBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nativeProv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nativeBin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Missing both provenance files -> unknown.
	if got := detectDeploymentVariantFrom(filepath.Join(tmp, "missing.json"), func() (string, error) {
		return nativeBin, nil
	}); got != DeploymentVariantUnknown {
		t.Fatalf("missing provenance = %q, want unknown", got)
	}

	// Relative ../share/cpamp/provenance.json -> native.
	if err := os.WriteFile(nativeProv, []byte(`{"schemaVersion":1,"role":"native"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectDeploymentVariantFrom(filepath.Join(tmp, "missing.json"), func() (string, error) {
		return nativeBin, nil
	}); got != DeploymentVariantNative {
		t.Fatalf("native provenance = %q, want native", got)
	}

	// Executable-relative ../share/cpamp/provenance.json takes priority over a stray Docker provenance file.
	if err := os.WriteFile(dockerProv, []byte(`{"schemaVersion":1,"role":"cpamp","cpaBundled":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectDeploymentVariantFrom(dockerProv, func() (string, error) {
		return nativeBin, nil
	}); got != DeploymentVariantNative {
		t.Fatalf("executable-relative provenance priority = %q, want native", got)
	}

	// Docker provenance path is used when executable-relative provenance is absent.
	containerBin := filepath.Join(tmp, "app", "cpa-manager-plus")
	if err := os.MkdirAll(filepath.Dir(containerBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(containerBin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectDeploymentVariantFrom(dockerProv, func() (string, error) {
		return containerBin, nil
	}); got != DeploymentVariantDockerSlim {
		t.Fatalf("docker provenance fallback = %q, want docker-slim", got)
	}
}

func TestReadDeploymentVariantRejectsNonRegularAndInvalidFiles(t *testing.T) {
	tmp := t.TempDir()
	regularTarget := filepath.Join(tmp, "target-provenance.json")
	if err := os.WriteFile(regularTarget, []byte(`{"schemaVersion":1,"role":"native"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := readDeploymentVariantFile(regularTarget); !ok || got != DeploymentVariantNative {
		t.Fatalf("regular provenance = (%q, %v), want (native, true)", got, ok)
	}

	// Directory at provenance path must be rejected.
	dirPath := filepath.Join(tmp, "dir-provenance.json")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := readDeploymentVariantFile(dirPath); ok || got != DeploymentVariantUnknown {
		t.Fatalf("directory provenance = (%q, %v), want (unknown, false)", got, ok)
	}

	// Symlink to valid provenance file must be rejected by Lstat.IsRegular().
	symlinkPath := filepath.Join(tmp, "symlink-provenance.json")
	if err := os.Symlink(regularTarget, symlinkPath); err == nil {
		if got, ok := readDeploymentVariantFile(symlinkPath); ok || got != DeploymentVariantUnknown {
			t.Fatalf("symlink provenance = (%q, %v), want (unknown, false)", got, ok)
		}
	}

	// Character device (/dev/null on Unix) must be rejected.
	if runtime.GOOS != "windows" {
		if got, ok := readDeploymentVariantFile("/dev/null"); ok || got != DeploymentVariantUnknown {
			t.Fatalf("/dev/null provenance = (%q, %v), want (unknown, false)", got, ok)
		}
	}
}

func TestStatusJSONIncludesDeploymentVariant(t *testing.T) {
	for _, variant := range []string{
		DeploymentVariantDockerFull,
		DeploymentVariantDockerSlim,
		DeploymentVariantNative,
		DeploymentVariantUnknown,
	} {
		t.Run(variant, func(t *testing.T) {
			s := New(&memoryStore{}, "v2.0.0-beta.1", "test", false)
			s.detectDeployment = func() string { return variant }
			st, err := s.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.DeploymentVariant != variant {
				t.Fatalf("DeploymentVariant = %q, want %q", st.DeploymentVariant, variant)
			}
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["deployment_variant"] != variant {
				t.Fatalf("json deployment_variant = %v, want %q", decoded["deployment_variant"], variant)
			}
		})
	}
}
