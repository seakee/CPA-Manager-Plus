package main

import (
	"net/http"
	"os"
	"strings"

	upgradewizard "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
)

const (
	// legacyRootEnvironment names the 1.x data directory an operator mounted for
	// the Upgrade operation. The installation's compose override mounts it
	// read-only at defaultLegacyRoot; nothing here writes inside it.
	legacyRootEnvironment = "CPAMP_LEGACY_ROOT"
	defaultLegacyRoot     = "/legacy"
)

// newUpgradeWizard builds the read-only Upgrade wizard routes. Authorization is
// decided by the surface that mounts this handler: the installation session on
// the fixed Bootstrap API, the canonical continuation before the product is
// complete. Both routes only read.
func (a *freshApplication) newUpgradeWizard() upgradewizard.Handler {
	return upgradewizard.Handler{
		Authorize: func(*http.Request) int { return 0 },
		LegacyRoot: configuredLegacyRoot,
		Database:  func() string { return a.cfg.DBPath },
		Timeout:   upgradewizard.DefaultTimeout,
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
