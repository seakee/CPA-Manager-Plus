package freshkeyapply

import "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"

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
