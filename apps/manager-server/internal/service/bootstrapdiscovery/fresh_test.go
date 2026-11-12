package bootstrapdiscovery

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

func TestOwnedFreshDiscoveryDoesNotRequestForbiddenAdminAuthentication(t *testing.T) {
	op := bootstrap.FreshOperation{Version: 1, Identity: bootstrap.FreshIdentity{
		AdmissionID: "admission-a", InstallationID: "installation-a", Generation: "generation-a",
		DatabasePath: "/data/manager.sqlite", OperationID: "fresh-operation-a",
	}, Revision: 1, GatewayPending: true}
	if got := Project(Input{FreshOperation: &op}); got.Status != bootstrap.PublicFreshClaimRequired {
		t.Fatal(got)
	}
	op.AdminInitialized = true
	if got := Project(Input{FreshOperation: &op}); got.Status != bootstrap.PublicFreshClaimRequired || string(got.Reason) != "fresh_resume_required" || got.AuthEntry != bootstrap.AuthEntryFreshClaim {
		t.Fatal(got)
	}
	op.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: op.Identity.OperationID, Target: "/control", Revision: 1, Committed: true, Stage: bootstrap.PrefixCommitted}
	if got := Project(Input{FreshOperation: &op}); got.Status != bootstrap.PublicUnavailable {
		t.Fatal(got)
	}
}
