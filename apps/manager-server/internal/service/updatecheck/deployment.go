package updatecheck

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DeploymentVariantDockerFull = "docker-full"
	DeploymentVariantDockerSlim = "docker-slim"
	DeploymentVariantNative     = "native"
	DeploymentVariantUnknown    = "unknown"

	BaseInstallRootEnv = "CPAMP_BASE_INSTALL_ROOT"

	defaultDockerProvenancePath = "/usr/local/share/cpamp/provenance.json"
)

type provenanceRecord struct {
	Role       string `json:"role"`
	CPABundled *bool  `json:"cpaBundled"`
}

// DetectDeploymentVariant inspects local packaging provenance without side
// effects and returns docker-full, docker-slim, native, or unknown.
func DetectDeploymentVariant() string {
	return detectDeploymentVariantFrom(defaultDockerProvenancePath, os.Executable)
}

func detectDeploymentVariantFrom(dockerProvenancePath string, executableFn func() (string, error)) string {
	for _, candidate := range provenanceCandidatePaths(dockerProvenancePath, executableFn) {
		if variant, ok := readDeploymentVariantFile(candidate); ok {
			return variant
		}
	}
	return DeploymentVariantUnknown
}

func provenanceCandidatePaths(dockerProvenancePath string, executableFn func() (string, error)) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	if baseRoot := strings.TrimSpace(os.Getenv(BaseInstallRootEnv)); baseRoot != "" {
		add(filepath.Join(baseRoot, "share", "cpamp", "provenance.json"))
	}
	if executableFn != nil {
		if exe, err := executableFn(); err == nil && strings.TrimSpace(exe) != "" {
			add(filepath.Join(filepath.Dir(exe), "..", "share", "cpamp", "provenance.json"))
			if resolved, err := filepath.EvalSymlinks(exe); err == nil && strings.TrimSpace(resolved) != "" {
				add(filepath.Join(filepath.Dir(resolved), "..", "share", "cpamp", "provenance.json"))
			}
		}
	}
	add(dockerProvenancePath)
	return paths
}

func readDeploymentVariantFile(filePath string) (string, bool) {
	lstat, err := os.Lstat(filePath)
	if err != nil || !lstat.Mode().IsRegular() || lstat.Size() <= 0 || lstat.Size() > 1024*1024 {
		return DeploymentVariantUnknown, false
	}
	f, err := os.Open(filePath)
	if err != nil {
		return DeploymentVariantUnknown, false
	}
	defer f.Close()
	fstat, err := f.Stat()
	if err != nil || !fstat.Mode().IsRegular() || !os.SameFile(lstat, fstat) || fstat.Size() <= 0 || fstat.Size() > 1024*1024 {
		return DeploymentVariantUnknown, false
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) == 0 || len(data) > 1024*1024 {
		return DeploymentVariantUnknown, false
	}
	variant := parseDeploymentVariant(data)
	if variant == DeploymentVariantUnknown {
		return DeploymentVariantUnknown, false
	}
	return variant, true
}

func parseDeploymentVariant(data []byte) string {
	var rec provenanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return DeploymentVariantUnknown
	}
	switch strings.TrimSpace(rec.Role) {
	case "cpamp":
		// image-evidence.py omits cpaBundled when cpaBundled is true on full images.
		if rec.CPABundled == nil || *rec.CPABundled {
			return DeploymentVariantDockerFull
		}
		return DeploymentVariantDockerSlim
	case "native":
		return DeploymentVariantNative
	default:
		return DeploymentVariantUnknown
	}
}
