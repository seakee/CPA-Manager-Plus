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

// newUpgradeWizard builds the Upgrade wizard routes. The handler proves its own
// credential — the key the Upgrade entry verified, presented as a Bearer value —
// against the surface's one resolved source, so no surface-level session or
// "authorize everything" hook can stand in for it. The two reads only read; the
// verify action is the surface's one write, and it delegates to the same
// completion gate the command line runs.
func (a *freshApplication) newUpgradeWizard() *upgradewizard.Handler {
	var artifact upgradewizard.ArtifactRuntime
	if a.artifact != nil {
		artifact = a.artifact
	}
	return &upgradewizard.Handler{
		LegacyRoot:   configuredLegacyRoot,
		PublicOrigin: strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
		Database:     func() string { return a.cfg.DBPath },
		DataKey:      func() string { return a.cfg.DataKeyPath },
		Timeout:      upgradewizard.DefaultTimeout,
		Artifact:     artifact,
	}
}

// upgradeCredentialDatabase is the Upgrade surface's construction-time
// credential rule for the authentication entry leaf: the configured 1.x
// database when MountDatabase accepts it or when an existing, non-empty,
// single-link regular usage.sqlite failed solely with os.ErrPermission (such as
// a 0600 root-owned 1.x file before targeted chown), otherwise the Manager's
// own store. Absent, empty, symlinked, hard-linked, or non-regular entries
// still fall back to the Manager store so the surface stays reachable and the
// preflight can name the condition.
//
// Only the entry leaf resolves through this function, and it resolves once, when
// it is constructed. The wizard's two reads inspect the mount per request and
// apply upgradeservice.CredentialDatabaseFrom: while a mounted 1.x file denies
// read permission, the entry leaf reports the ownership remedy (when the mount
// carries a recognized 1.x layout), and once the operator runs targeted chown
// both the already-bound entry leaf and the per-request wizard reads agree on
// the mounted 1.x database without restarting the container. A mount that
// appears, disappears, or is repointed after startup is seen by the reads first,
// and the entry leaf keeps its startup resolution until it is rebuilt.
func upgradeCredentialDatabase(legacyRoot, managerDatabase string) string {
	return upgradeservice.EntryCredentialDatabase(legacyRoot, managerDatabase)
}

// upgradeCredentialPaths is the surface's one credential resolution expressed as
// the inspection paths the entry leaf reads. When the resolved credential file is
// the configured 1.x database, its sibling data key and archive tree are the
// layout the rest of the 1.x inspection already uses; otherwise the Manager's
// own paths are the credential source and are used unchanged — which includes the
// case where the mount holds only a database the shared file policy refuses.
//
// The second result reports which file it is, because the two are read
// differently: the configured source is preserved byte for byte and is never
// opened in place, while the Manager store is this process's own live database.
func upgradeCredentialPaths(legacyRoot string, manager sqliterepo.InspectionPaths) (sqliterepo.InspectionPaths, bool) {
	database := upgradeCredentialDatabase(legacyRoot, manager.Database)
	if strings.TrimSpace(database) == strings.TrimSpace(manager.Database) {
		return manager, false
	}
	return legacypreflight.DefaultPaths(database), true
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

// upgradeWizardReadNamespace is the namespace the Upgrade surface owns on the
// canonical continuation. Every route in it goes to the reader, which answers the
// credential before the path, so an unknown route cannot be mapped by a caller
// without one. The verb rule belongs to the reader as well: the two reads take
// GET and HEAD, the verify action takes POST.
const upgradeWizardReadNamespace = "installation/upgrade/"

// upgradeWizardResource reports whether a canonical continuation resource belongs
// to the Upgrade wizard's reads.
func upgradeWizardResource(resource string) bool {
	return strings.HasPrefix(resource, upgradeWizardReadNamespace)
}
