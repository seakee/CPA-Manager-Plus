package fresh

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestContinuationRetainsOriginalExpiryGenerationAndRouteScope(t *testing.T) {
	opened, authority := openTest(t)
	_, session := sessionFor(t, authority)
	if _, err := authority.CommitAdmin(t.Context(), session.Token, AdminCommitRequest{Identity: session.Operation.Identity, CommitID: "original", Key: "Admin_0123456789-abcdefghijklmnopqrstuvwxyz!"}); err != nil {
		t.Fatal(err)
	}
	st := freshstore.New(opened.DB)
	before, _ := st.Load(t.Context())
	if authority.ValidateContinuation(before, session.Token) == nil {
		t.Fatal("canonical continuation accepted before route commit")
	}
	// A current install commits both prefixes: the immutable created suggestions
	// stay in place and the inference half is its own verified target.
	after, err := st.Update(t.Context(), before.Operation.Identity, before.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/control"
		r.Operation.Routing.InferenceTarget = "/access-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if authority.ValidateSession(after, session.Token) == nil {
		t.Fatal("fixed session survived route cutover")
	}
	if authority.ValidateContinuation(after, session.Token) != nil {
		t.Fatal("same installation continuation rejected")
	}
	if authority.ValidateContinuation(after, "wrong") == nil {
		t.Fatal("foreign token accepted")
	}
	stale := after
	stale.Authority.ClaimGeneration++
	if authority.ValidateContinuation(stale, session.Token) == nil {
		t.Fatal("stale generation accepted")
	}
	authority.now = func() time.Time { return time.UnixMilli(after.Authority.SessionExpiresAtMS) }
	if authority.ValidateContinuation(after, session.Token) == nil {
		t.Fatal("cutover extended installation expiry")
	}
}

func TestCompletedInstallationRestartDoesNotReinitializeOnOrdinaryUpdate(t *testing.T) {
	opened, authority := openTest(t)
	_, session := sessionFor(t, authority)
	const key = "Admin_0123456789-abcdefghijklmnopqrstuvwxyz!"
	if _, err := authority.CommitAdmin(t.Context(), session.Token, AdminCommitRequest{Identity: session.Operation.Identity, CommitID: "original", Key: key}); err != nil {
		t.Fatal(err)
	}
	st := freshstore.New(opened.DB)
	before, err := st.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	completed, err := st.Update(t.Context(), before.Operation.Identity, before.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/control"
		r.Operation.Routing.InferenceTarget = "/access-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := legacypaths(before.Operation.Identity.DatabasePath)
	dataKey, err := os.ReadFile(paths.DataKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	// Starting a replacement binary/image opens the same completed store; no
	// version string or missing Runtime can reset an installation decision.
	reopened, err := Open(t.Context(), paths, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := freshstore.New(reopened.DB).Load(t.Context())
	if err != nil || !reflect.DeepEqual(completed, after) || after.Admin == nil || !security.VerifyAdminKey(*after.Admin, key) {
		t.Fatal("restart replaced installation state or Admin", err)
	}
	keyAfter, err := os.ReadFile(paths.DataKey)
	if err != nil || !bytes.Equal(dataKey, keyAfter) {
		t.Fatal("restart replaced data key", err)
	}
	if _, err := New(freshstore.New(reopened.DB)).Issue(t.Context()); err == nil {
		t.Fatal("ordinary update reopened installation claim")
	}
}
