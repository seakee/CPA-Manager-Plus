// Package buildinfo contains release identity injected by official builds.
package buildinfo

import (
	"fmt"
	"os"
	"strings"
)

var Version = "dev"
var SourceCommit = "unknown"
var BuildDate = "unknown"

// ShortCommit returns the 8-character prefix for a 40-hex commit SHA,
// preserving shorter values and placeholders such as "unknown".
func ShortCommit() string {
	commit := strings.TrimSpace(SourceCommit)
	if commit == "" {
		return "unknown"
	}
	if len(commit) == 40 && isHex(commit) {
		return commit[:8]
	}
	return commit
}

// EffectiveVersion returns the normalized running binary version, falling back
// to ActiveSlot() when Version is unset/dev and "dev" otherwise.
func EffectiveVersion() string {
	v := strings.TrimSpace(Version)
	if (v == "" || v == "dev") && ActiveSlot() != "" {
		return ActiveSlot()
	}
	if v == "" {
		return "dev"
	}
	return v
}

// BaseImageVersion returns CPAMP_BASE_VERSION when set by cpamp-launcher,
// falling back to Version ("dev" when blank).
func BaseImageVersion() string {
	if base := strings.TrimSpace(os.Getenv("CPAMP_BASE_VERSION")); base != "" {
		return base
	}
	v := strings.TrimSpace(Version)
	if v == "" {
		return "dev"
	}
	return v
}

// ActiveSlot returns CPAMP_RUNNING_SLOT when running from a staged self-update
// slot, or empty string when running directly from the base image/install.
func ActiveSlot() string {
	return strings.TrimSpace(os.Getenv("CPAMP_RUNNING_SLOT"))
}

// StartupLine formats the startup build identity banner logged on server start.
func StartupLine() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "dev"
	}
	builtAt := strings.TrimSpace(BuildDate)
	if builtAt == "" {
		builtAt = "unknown"
	}
	base := fmt.Sprintf("CPA Manager Plus Version: %s, Commit: %s, BuiltAt: %s", version, ShortCommit(), builtAt)
	if slot := ActiveSlot(); slot != "" {
		return fmt.Sprintf("%s, BaseVersion: %s, ActiveSlot: %s", base, BaseImageVersion(), slot)
	}
	return base
}

func isHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		isUpperHex := c >= 'A' && c <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
}
