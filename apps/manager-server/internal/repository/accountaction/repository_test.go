package accountaction_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestUpsertMergesPendingCandidateByAuthFileAndAction(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	repo := st.AccountActions

	first, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:          model.AccountActionTypeDelete,
		Provider:            "codex",
		AuthFileName:        "codex-auth.json",
		AuthIndex:           "3",
		AccountSnapshot:     "user@example.com",
		AuthLabel:           "User",
		ReasonCode:          "token_revoked",
		Reason:              "token revoked",
		AutoDisableEligible: true,
		EvidenceJSON:        `{"code":"token_revoked"}`,
		SeenAtMS:            1000,
	})
	if err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	if first.ID == 0 || first.HitCount != 1 || first.Status != model.AccountActionStatusPending || first.ReasonCode != "token_revoked" || !first.AutoDisableEligible {
		t.Fatalf("first candidate = %#v", first)
	}

	second, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:      model.AccountActionTypeDelete,
		Provider:        "codex",
		AuthFileName:    "codex-auth.json",
		AuthIndex:       "3",
		AccountSnapshot: "user@example.com",
		ReasonCode:      "token_revoked",
		Reason:          "token revoked again",
		EvidenceJSON:    `{"code":"token_revoked","hit":2}`,
		SeenAtMS:        2000,
	})
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second ID = %d, want %d", second.ID, first.ID)
	}
	if second.HitCount != 2 || second.LastSeenAtMS != 2000 || second.Reason != "token revoked again" {
		t.Fatalf("second candidate = %#v", second)
	}

	pending, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d", len(pending))
	}
	count, err := repo.Count(ctx, model.AccountActionStatusPending)
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	if err := repo.MarkAutoDisabled(ctx, first.ID, 2500); err != nil {
		t.Fatalf("mark auto disabled: %v", err)
	}
	marked, ok, err := repo.Get(ctx, first.ID)
	if err != nil || !ok || marked.AutoDisabledAtMS != 2500 {
		t.Fatalf("marked candidate = %#v ok=%v err=%v", marked, ok, err)
	}
	if err := repo.MarkAutoDisabled(ctx, first.ID+999, 2600); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark missing candidate error = %v", err)
	}

	ignored, err := repo.UpdateStatus(ctx, first.ID, model.AccountActionStatusIgnored)
	if err != nil {
		t.Fatalf("ignore: %v", err)
	}
	if ignored.Status != model.AccountActionStatusIgnored {
		t.Fatalf("ignored status = %q", ignored.Status)
	}
	if err := repo.MarkAutoDisabled(ctx, first.ID, 2700); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark ignored candidate auto disabled error = %v", err)
	}
	ignored, ok, err = repo.Get(ctx, first.ID)
	if err != nil || !ok || ignored.Status != model.AccountActionStatusIgnored || ignored.AutoDisabledAtMS != 2500 {
		t.Fatalf("ignored candidate changed = %#v ok=%v err=%v", ignored, ok, err)
	}

	third, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:   model.AccountActionTypeDelete,
		AuthFileName: "codex-auth.json",
		Reason:       "new pending after ignored",
		SeenAtMS:     3000,
	})
	if err != nil {
		t.Fatalf("upsert third: %v", err)
	}
	if third.ID == first.ID || third.HitCount != 1 || third.Status != model.AccountActionStatusPending {
		t.Fatalf("third candidate = %#v", third)
	}
}

func TestUpsertKeepsDifferentReasonCodesSeparate(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	repo := st.AccountActions

	credentialPermission, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:          model.AccountActionTypeReview,
		Provider:            "xai",
		AuthFileName:        "xai-auth.json",
		AuthIndex:           "1",
		ReasonCode:          "credential_permission_denied",
		Reason:              "credential permission denied",
		AutoDisableEligible: true,
		SeenAtMS:            1000,
	})
	if err != nil {
		t.Fatalf("upsert credential permission: %v", err)
	}
	regional, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:          model.AccountActionTypeReview,
		Provider:            "xai",
		AuthFileName:        "xai-auth.json",
		AuthIndex:           "1",
		ReasonCode:          "authentication_review",
		Reason:              "regional permission denied",
		AutoDisableEligible: false,
		SeenAtMS:            2000,
	})
	if err != nil {
		t.Fatalf("upsert regional review: %v", err)
	}
	if regional.ID == credentialPermission.ID {
		t.Fatalf("different reason codes merged into candidate %d", regional.ID)
	}
	items, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if !credentialPermission.AutoDisableEligible || regional.AutoDisableEligible {
		t.Fatalf("eligibility credential=%t regional=%t", credentialPermission.AutoDisableEligible, regional.AutoDisableEligible)
	}
}

func TestUpsertKeepsSharedFileFallbackIdentitiesSeparate(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	repo := st.AccountActions

	for _, account := range []string{"alice@example.com", "bob@example.com"} {
		if _, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
			ActionType:      model.AccountActionTypeReauth,
			Provider:        "codex",
			AuthFileName:    "shared.json",
			AccountSnapshot: account,
			ReasonCode:      "token_revoked",
			Reason:          "token revoked",
			SeenAtMS:        1000,
		}); err != nil {
			t.Fatalf("upsert %q: %v", account, err)
		}
	}

	items, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v, want two credential identities", items)
	}
}

func TestUpsertKeepsAccountIDFallbacksSeparateAcrossProviders(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	repo := st.AccountActions

	codex, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AccountIDSnapshot: "shared-account",
		ReasonCode:        "token_revoked",
		Reason:            "codex token revoked",
		EvidenceJSON:      `{"provider":"codex"}`,
		SeenAtMS:          1000,
	})
	if err != nil {
		t.Fatalf("upsert codex: %v", err)
	}
	xai, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "xai",
		AuthFileName:      "shared.json",
		AccountIDSnapshot: "shared-account",
		ReasonCode:        "token_revoked",
		Reason:            "xai token revoked",
		EvidenceJSON:      `{"provider":"xai"}`,
		SeenAtMS:          2000,
	})
	if err != nil {
		t.Fatalf("upsert xai: %v", err)
	}
	if codex.ID == xai.ID {
		t.Fatalf("cross-provider account IDs merged into candidate %d", codex.ID)
	}

	upgraded, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "x_ai",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-xai",
		AccountIDSnapshot: "shared-account",
		ReasonCode:        "token_revoked",
		Reason:            "xai token revoked again",
		EvidenceJSON:      `{"provider":"xai","hit":2}`,
		SeenAtMS:          3000,
	})
	if err != nil {
		t.Fatalf("upgrade xai: %v", err)
	}
	if upgraded.ID != xai.ID || upgraded.ID == codex.ID || upgraded.AuthIndex != "auth-xai" {
		t.Fatalf("upgraded candidate = %#v, codex = %#v, xai = %#v", upgraded, codex, xai)
	}

	items, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v, want two provider-scoped identities", items)
	}
}

func TestUpsertUpgradesFallbackIdentityWhenStableLocatorAppears(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	repo := st.AccountActions

	first, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:      model.AccountActionTypeReauth,
		Provider:        "x_ai",
		AuthFileName:    "shared.json",
		AccountSnapshot: "user@example.com",
		ReasonCode:      "token_revoked",
		Reason:          "token revoked",
		SeenAtMS:        1000,
	})
	if err != nil {
		t.Fatalf("upsert fallback: %v", err)
	}
	upgraded, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "xai",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "account-1",
		ReasonCode:        "token_revoked",
		Reason:            "token revoked again",
		SeenAtMS:          2000,
	})
	if err != nil {
		t.Fatalf("upgrade fallback: %v", err)
	}
	if upgraded.ID != first.ID || upgraded.AuthIndex != "auth-1" || upgraded.AccountIDSnapshot != "account-1" || upgraded.Provider != "xai" || upgraded.HitCount != 2 {
		t.Fatalf("upgraded candidate = %#v, first = %#v", upgraded, first)
	}
}

func TestUpsertEnrichesWorkspaceOnlyCodexCandidateWithMember(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	repo := st.AccountActions
	if err := st.RunDerivedStartupMaintenance(ctx); err != nil {
		t.Fatalf("prepare production account action identity index: %v", err)
	}

	workspaceOnly, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountIDSnapshot: "workspace-1",
		ReasonCode:        "token_revoked",
		Reason:            "workspace-only pending review",
		SeenAtMS:          1000,
	})
	if err != nil {
		t.Fatalf("upsert workspace-only candidate: %v", err)
	}

	member, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountSnapshot:   "bob@example.com",
		AccountIDSnapshot: "workspace-1",
		ReasonCode:        "token_revoked",
		Reason:            "member-specific pending review",
		SeenAtMS:          2000,
	})
	if err != nil {
		t.Fatalf("upsert member candidate: %v", err)
	}
	if member.ID != workspaceOnly.ID {
		t.Fatalf("Codex member created a second candidate: workspace-only=%d member=%d", workspaceOnly.ID, member.ID)
	}
	if workspaceOnly.AccountSnapshot != "" || member.AccountSnapshot != "bob@example.com" || member.AccountIDSnapshot != "workspace-1" {
		t.Fatalf("candidate identities = workspace-only %#v, enriched %#v", workspaceOnly, member)
	}

	items, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending candidates: %v", err)
	}
	if len(items) != 1 || items[0].ID != member.ID || items[0].HitCount != 2 {
		t.Fatalf("pending candidates = %#v, want one enriched candidate", items)
	}
}

func TestUpsertKeepsConflictingCodexMemberOnExistingCandidate(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	repo := st.AccountActions

	first, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountIDSnapshot: "workspace-1",
		AccountSnapshot:   "alice@example.com",
		ReasonCode:        "token_revoked",
		Reason:            "Alice token revoked",
	})
	if err != nil {
		t.Fatalf("upsert first candidate: %v", err)
	}

	conflicting, err := repo.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountIDSnapshot: "workspace-1",
		AccountSnapshot:   "bob@example.com",
		ReasonCode:        "token_revoked",
		Reason:            "Bob token revoked",
	})
	if err == nil || !strings.Contains(err.Error(), "member changed") {
		t.Fatalf("conflicting upsert error = %v, want member conflict", err)
	}
	if conflicting.ID != first.ID || conflicting.AccountSnapshot != "alice@example.com" {
		t.Fatalf("conflicting candidate = %#v, want original identity preserved", conflicting)
	}

	items, err := repo.List(ctx, model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending candidates: %v", err)
	}
	if len(items) != 1 || items[0].ID != first.ID || !strings.Contains(items[0].LastError, "member changed") {
		t.Fatalf("pending candidates = %#v, want one conflict-marked candidate", items)
	}
}
