package upgrade

import (
	"bufio"
	"errors"
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
	legacyCPADir := filepath.Dir(configPath)
	dirInfo, err := os.Lstat(legacyCPADir)
	if err != nil {
		return Check{Name: CheckCPAClassification, Status: StatusPass, Detail: "kind=" + CPAKindExternal}
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return Check{
			Name:   CheckCPAClassification,
			Status: StatusBlocked,
			Code:   CodeSourceSymlinked,
			Detail: "legacy-cpa: a symbolic link is not accepted for this directory",
		}
	}
	if !dirInfo.IsDir() {
		return Check{Name: CheckCPAClassification, Status: StatusPass, Detail: "kind=" + CPAKindExternal}
	}
	cfgInfo, err := os.Lstat(configPath)
	if err != nil {
		return Check{Name: CheckCPAClassification, Status: StatusPass, Detail: "kind=" + CPAKindExternal}
	}
	if cfgInfo.Mode()&os.ModeSymlink != 0 {
		return Check{
			Name:   CheckCPAClassification,
			Status: StatusBlocked,
			Code:   CodeSourceSymlinked,
			Detail: "legacy-cpa/config.yaml: a symbolic link is not accepted for this file",
		}
	}
	if !cfgInfo.Mode().IsRegular() {
		return Check{Name: CheckCPAClassification, Status: StatusPass, Detail: "kind=" + CPAKindExternal}
	}
	file, _, err := openRegularFileNoFollow(configPath)
	if err != nil {
		if errors.Is(err, ErrSourceSymlinked) {
			return Check{
				Name:   CheckCPAClassification,
				Status: StatusBlocked,
				Code:   CodeSourceSymlinked,
				Detail: "legacy-cpa/config.yaml: a symbolic link is not accepted for this file",
			}
		}
		return Check{
			Name:   CheckCPAClassification,
			Status: StatusPass,
			Detail: "kind=" + CPAKindManagedStack + " cpaVersion=" + CPAVersionUnknown,
		}
	}
	defer func() { _ = file.Close() }()
	return Check{
		Name:   CheckCPAClassification,
		Status: StatusPass,
		Detail: "kind=" + CPAKindManagedStack + " cpaVersion=" + readCPAConfigVersionFrom(file),
	}
}

// readCPAConfigVersionFrom returns the first top-level "version:" scalar from an
// already verified regular file, or unknown. Anything less trivial than that is
// left for the import step.
func readCPAConfigVersionFrom(file io.Reader) string {
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
