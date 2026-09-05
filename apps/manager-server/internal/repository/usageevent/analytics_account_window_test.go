package usageevent

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestAccountWindowQueryKeysRequireExplicitCodexAccountIDForLegacyFallback(t *testing.T) {
	window := AccountWindowUsageQuery{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthProjectIDSnapshot: "workspace-project",
		AccountSnapshot:       "shared@example.com",
		AuthLabelSnapshot:     "Shared account",
		Source:                "codex-a.json",
	}

	accountKey, legacyAccountKey := accountWindowQueryKeys(window)
	if accountKey == "" || accountKey != legacyAccountKey {
		t.Fatalf("unmarked Codex project generated a compatibility key: account=%q legacy=%q", accountKey, legacyAccountKey)
	}

	window.AuthAccountIDSnapshot = "account-a"
	accountKey, legacyAccountKey = accountWindowQueryKeys(window)
	wantAccountKey, valid := usageidentity.AccountKey(accountWindowFields(window))
	if !valid {
		t.Fatal("explicit Codex account identity was rejected")
	}
	wantLegacyAccountKey, valid := usageidentity.LegacyAccountKey(accountWindowFields(window))
	if !valid {
		t.Fatal("explicit Codex legacy identity was rejected")
	}
	if accountKey != wantAccountKey || legacyAccountKey != accountKey {
		t.Fatalf("unresolved explicit Codex keys = account:%q legacy:%q, want account:%q and no legacy fallback", accountKey, legacyAccountKey, wantAccountKey)
	}
	window.LegacyAccountKeyChecked = true
	window.LegacyAccountKey = wantLegacyAccountKey
	accountKey, legacyAccountKey = accountWindowQueryKeys(window)
	if accountKey != wantAccountKey || legacyAccountKey != wantLegacyAccountKey || accountKey == legacyAccountKey {
		t.Fatalf("explicit Codex keys = account:%q legacy:%q, want account:%q legacy:%q", accountKey, legacyAccountKey, wantAccountKey, wantLegacyAccountKey)
	}

	otherAccount := window
	otherAccount.AuthAccountIDSnapshot = "account-b"
	otherAccountKey, _ := accountWindowQueryKeys(otherAccount)
	if accountKey == otherAccountKey {
		t.Fatalf("distinct Codex accounts sharing display metadata were merged: %q", accountKey)
	}

	otherCredential := window
	otherCredential.AuthAccountIDSnapshot = "account-b"
	otherCredential.AuthFileSnapshot = "codex-b.json"
	otherCredential.AuthIndex = "auth-b"
	otherCredential.Source = "codex-b.json"
	otherCredential.LegacyAccountKey = ""
	otherCredential.LegacyAccountKeyChecked = false
	otherCredentialKey, otherLegacyKey := accountWindowQueryKeys(otherCredential)
	if accountKey == otherCredentialKey || legacyAccountKey == otherLegacyKey {
		t.Fatalf("distinct credentials sharing display metadata were merged: account %q/%q legacy %q/%q", accountKey, otherCredentialKey, legacyAccountKey, otherLegacyKey)
	}
	otherCredential.LegacyAccountKeyChecked = true
	otherCredential.LegacyAccountKey, _ = usageidentity.LegacyAccountKey(accountWindowFields(otherCredential))
	otherCredentialKey, otherLegacyKey = accountWindowQueryKeys(otherCredential)
	if accountKey == otherCredentialKey || legacyAccountKey == otherLegacyKey {
		t.Fatalf("distinct credentials sharing display metadata were merged: account %q/%q legacy %q/%q", accountKey, otherCredentialKey, legacyAccountKey, otherLegacyKey)
	}
}

func TestAccountWindowQueryKeysDoNotTrustStaleCodexCallerKey(t *testing.T) {
	window := AccountWindowUsageQuery{
		AccountKey:            "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31",
		AuthFileSnapshot:      "alice.json",
		AuthIndex:             "auth-alice",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AccountSnapshot:       "alice@example.com",
		Source:                "alice.json",
	}

	got, legacy := accountWindowQueryKeys(window)
	want, valid := usageidentity.AccountKey(accountWindowFields(window))
	if !valid {
		t.Fatal("valid Codex member target was rejected")
	}
	if got != want || got == window.AccountKey || legacy != want {
		t.Fatalf("stale Codex caller key was trusted: account=%q legacy=%q want=%q", got, legacy, want)
	}

	bob := window
	bob.AccountSnapshot = "bob@example.com"
	bob.AccountKey = window.AccountKey
	bobKey, _ := accountWindowQueryKeys(bob)
	if bobKey == got {
		t.Fatalf("shared Workspace members still share a caller-derived key: Alice=%q Bob=%q", got, bobKey)
	}
}

func accountWindowFields(window AccountWindowUsageQuery) usageidentity.Fields {
	return usageidentity.Fields{
		AuthFileSnapshot:      window.AuthFileSnapshot,
		AuthIndex:             window.AuthIndex,
		AuthProviderSnapshot:  window.AuthProviderSnapshot,
		AuthAccountIDSnapshot: window.AuthAccountIDSnapshot,
		AuthProjectIDSnapshot: window.AuthProjectIDSnapshot,
		AccountSnapshot:       window.AccountSnapshot,
		AuthLabelSnapshot:     window.AuthLabelSnapshot,
		Source:                window.Source,
	}
}
