package upgrade

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CPA classification vocabulary. It is an observation about which mount the
// source stack used; it deliberately says nothing about CPA version support.
const (
	CPAKindManagedStack = "managed_stack"
	CPAKindExternal     = "external"
	CPAVersionUnknown   = "unknown"
)

// classifyCPA reads only the presence of the managed CPA config and, when it is
// a regular file, its top-level version line. No CPA connection, credential
// read or import is attempted here.
func classifyCPA(legacyRoot string) Check {
	configPath := filepath.Clean(filepath.Join(legacyRoot, managedCPAConfigRelativePath))
	info, err := os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() {
		return Check{Name: CheckCPAClassification, Status: StatusPass, Detail: "kind=" + CPAKindExternal}
	}
	return Check{
		Name:   CheckCPAClassification,
		Status: StatusPass,
		Detail: "kind=" + CPAKindManagedStack + " cpaVersion=" + readCPAConfigVersion(configPath),
	}
}

// readCPAConfigVersion returns the first top-level "version:" scalar, or
// unknown. Anything less trivial than that is left for the import step.
func readCPAConfigVersion(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return CPAVersionUnknown
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "version" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if value == "" {
			return CPAVersionUnknown
		}
		return value
	}
	return CPAVersionUnknown
}
