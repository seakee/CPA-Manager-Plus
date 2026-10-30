package main

import (
	"os"
	"strings"

	upgradewizard "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
)

const (
	// legacyRootEnvironment names the 1.x data directory an operator mounted for
	// the Upgrade operation. No shipped compose file mounts it and nothing here
	// writes inside it: the operator supplies the mount themselves, read-only,
	// for example `-v /srv/cpamp-1x:/legacy:ro` or a host path in
	// CPAMP_LEGACY_ROOT.
	legacyRootEnvironment = "CPAMP_LEGACY_ROOT"
	defaultLegacyRoot     = "/legacy"
)

// newUpgradeWizard builds the read-only Upgrade wizard routes. The handler
// proves its own credential — the source administrator key the Upgrade entry
// verified, presented as a Bearer value — against the configured 1.x source, so
// no surface-level session or "authorize everything" hook can stand in for it.
// Both routes only read.
func (a *freshApplication) newUpgradeWizard() upgradewizard.Handler {
	return upgradewizard.Handler{
		LegacyRoot: configuredLegacyRoot,
		Database:   func() string { return a.cfg.DBPath },
		Timeout:    upgradewizard.DefaultTimeout,
	}
}

// configuredLegacyRoot resolves the mounted 1.x source. An absent mount is not
// an error here: the route reports the typed "missing" code and the wizard
// names it, instead of inventing a source.
func configuredLegacyRoot() string {
	if root := strings.TrimSpace(os.Getenv(legacyRootEnvironment)); root != "" {
		return root
	}
	if info, err := os.Stat(defaultLegacyRoot); err == nil && info.IsDir() {
		return defaultLegacyRoot
	}
	return ""
}

// upgradeWizardResource reports whether a canonical continuation resource is one
// of the Upgrade wizard's reads.
func upgradeWizardResource(resource string) bool {
	return resource == "installation/upgrade/preflight" || resource == "installation/upgrade/status"
}
