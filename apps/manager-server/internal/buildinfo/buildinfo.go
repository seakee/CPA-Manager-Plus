package buildinfo

import "strings"

// Version is injected by release builds with -ldflags. It deliberately
// remains useful in local builds so runtime APIs never need to infer the
// server version from the embedded web bundle.
var Version = "dev"

func RuntimeVersion() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}
