package freshkeyapply

import (
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// ManagerOwnedStrategy reports whether one Fresh checkpoint describes an
// installation this Manager itself owns: it staged the destination, started it,
// and therefore holds its install-derived management authority. New Managed is
// one. An adoption is one only when its source class is `network_existing`.
//
// `legacy_managed` is deliberately excluded. That source class is Upgrade's
// input, not this lane's authority: an Upgrade destination is not staged by the
// Fresh adopt path, so nothing here proved its template and no key, admission or
// gateway write may be granted on it.
//
// This is the single copy of that fact. Every site that used to spell out
// `strategy == new_managed` asks here instead, so a later strategy cannot be
// admitted on one surface and refused on the next.
func ManagerOwnedStrategy(checkpoint bootstrap.FreshStrategyCheckpoint) bool {
	_, ok := managerOwnedDestinationPrefix(checkpoint)
	return ok
}

// ValidManagerOwnedDestinationCheckpoint reports whether one committed Fresh
// checkpoint carries a verified Manager-owned destination connection identity
// (`managed:sha256:<hex>` for New Managed, or `external:sha256:<hex>` for Adopt
// with `network_existing`). Unmigrated start-only checkpoints and historical
// operator-managed connections carry an empty DestinationConnectionIdentity and
// return false.
func ValidManagerOwnedDestinationCheckpoint(checkpoint bootstrap.FreshStrategyCheckpoint) bool {
	if checkpoint.Stage != "committed" {
		return false
	}
	prefix, owned := managerOwnedDestinationPrefix(checkpoint)
	if !owned || !strings.HasPrefix(checkpoint.DestinationConnectionIdentity, prefix) {
		return false
	}
	return validLowerHexDigest(strings.TrimPrefix(checkpoint.DestinationConnectionIdentity, prefix))
}

func validLowerHexDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for index := range len(digest) {
		ch := digest[index]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// managerOwnedDestinationPrefix is the connection identity prefix the Manager
// derives for one Manager-owned checkpoint. The two identities are deliberately
// distinct values — an adopted destination is reached through the `external`
// digest the adoption recorded, a New Managed one through the `managed` digest —
// so a caller must pick by strategy and never by prefix alone.
func managerOwnedDestinationPrefix(checkpoint bootstrap.FreshStrategyCheckpoint) (string, bool) {
	switch checkpoint.Strategy {
	case bootstrap.NewManaged:
		return "managed:sha256:", true
	case bootstrap.AdoptExisting:
		if checkpoint.SourceClass != bootstrap.AdoptNetworkExisting {
			return "", false
		}
		return "external:sha256:", true
	default:
		return "", false
	}
}
