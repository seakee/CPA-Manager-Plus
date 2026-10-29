package freshkeyapply

import "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"

// ManagerOwnedStrategy reports whether one Fresh strategy yields an installation
// this Manager itself owns: it staged the destination, started it, and therefore
// holds its install-derived management authority. New Managed and Adopt are both
// Manager-owned; Connect External is the user's own CPA and is never answered by
// this authority.
//
// This is the single copy of that fact. Every site that used to spell out
// `strategy == new_managed` asks here instead, so a later strategy cannot be
// admitted on one surface and refused on the next.
func ManagerOwnedStrategy(strategy bootstrap.CPAStrategy) bool {
	_, ok := managerOwnedDestinationPrefix(strategy)
	return ok
}

// managerOwnedDestinationPrefix is the connection identity prefix the Manager
// derives for one Manager-owned strategy. The two identities are deliberately
// distinct values — an adopted destination is reached through the `external`
// digest the adoption recorded, a New Managed one through the `managed` digest —
// so a caller must pick by strategy and never by prefix alone.
func managerOwnedDestinationPrefix(strategy bootstrap.CPAStrategy) (string, bool) {
	switch strategy {
	case bootstrap.NewManaged:
		return "managed:sha256:", true
	case bootstrap.AdoptExisting:
		return "external:sha256:", true
	default:
		return "", false
	}
}
