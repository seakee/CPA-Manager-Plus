// Package buildinfo contains release identity injected by official builds.
package buildinfo

import (
	"fmt"
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
	return fmt.Sprintf("CPA Manager Plus Version: %s, Commit: %s, BuiltAt: %s", version, ShortCommit(), builtAt)
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
