package main

import (
	"os"
	"strings"

	upgradewizard "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
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
// proves its own credential — the key the Upgrade entry verified, presented as a
// Bearer value — against the surface's one resolved source, so no
// surface-level session or "authorize everything" hook can stand in for it.
// Both routes only read.
func (a *freshApplication) newUpgradeWizard() upgradewizard.Handler {
	return upgradewizard.Handler{
		LegacyRoot:         configuredLegacyRoot,
		CredentialDatabase: func() string { return upgradeCredentialDatabase(a.cfg.DBPath) },
		PublicOrigin:       strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
		Database:           func() string { return a.cfg.DBPath },
		Timeout:            upgradewizard.DefaultTimeout,
	}
}

// upgradeCredentialDatabase is the Upgrade surface's one credential resolution:
// the configured 1.x database when the mount holds it, otherwise the Manager's
// own store. The entry leaf and both wizard reads ask this question here and
// nowhere else, so a key accepted at the entry page is accepted by the reads.
func upgradeCredentialDatabase(managerDatabase string) string {
	return upgradeservice.CredentialDatabase(configuredLegacyRoot(), managerDatabase)
}

// upgradeCredentialPaths is the surface's one credential resolution expressed as
// the inspection paths the entry leaf reads. When the resolved credential file
// is the configured 1.x database, its sibling data key and archive tree are the
// layout the rest of the 1.x inspection already uses; otherwise the Manager's
// own paths are the credential source and are used unchanged.
func upgradeCredentialPaths(manager sqliterepo.InspectionPaths) sqliterepo.InspectionPaths {
	database := upgradeCredentialDatabase(manager.Database)
	if strings.TrimSpace(database) == strings.TrimSpace(manager.Database) {
		return manager
	}
	return legacypreflight.DefaultPaths(database)
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
