package usageevent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestResolveCodexLegacyAccountKeyRequiresProviderAndMatchingAccountIDs(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func([]usage.Event) []usage.Event
		wantAllowed   bool
		wantLegacyKey bool
	}{
		{
			name: "matching stable and legacy events",
			mutate: func(events []usage.Event) []usage.Event {
				return events
			},
			wantAllowed:   true,
			wantLegacyKey: true,
		},
		{
			name: "provenance marked project snapshot agrees",
			mutate: func(events []usage.Event) []usage.Event {
				events[0].AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("account-a")
				return events
			},
			wantAllowed:   true,
			wantLegacyKey: true,
		},
		{
			name: "invalid provenance marked project snapshot blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events[0].AuthProjectIDSnapshot = "codex-account-id:v1:"
				return events
			},
			wantAllowed: false,
		},
		{
			name: "conflicting provenance marked project snapshot blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events[0].AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("account-b")
				return events
			},
			wantAllowed: false,
		},
		{
			name: "different account id blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("different-account", 3, "codex-a.json", "auth-a", "codex", "account-b"))
				return events
			},
			wantAllowed: false,
		},
		{
			name: "different member blocks",
			mutate: func(events []usage.Event) []usage.Event {
				event := identityTestEvent("different-member", 3, "codex-a.json", "auth-a", "codex", "account-a")
				event.AccountSnapshot = "bob@example.com"
				return append(events, event)
			},
			wantAllowed: false,
		},
		{
			name: "missing member blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events[0].AccountSnapshot = ""
				return events
			},
			wantAllowed: false,
		},
		{
			name: "foreign provider blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("foreign-provider", 3, "codex-a.json", "auth-a", "openai", ""))
				return events
			},
			wantAllowed: false,
		},
		{
			name: "different file and index do not participate",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("different-credential", 3, "codex-b.json", "auth-b", "codex", "account-b"))
				return events
			},
			wantAllowed:   true,
			wantLegacyKey: true,
		},
		{
			name: "missing provider blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("missing-provider", 3, "codex-a.json", "auth-a", "", ""))
				return events
			},
			wantAllowed: false,
		},
		{
			name: "missing workspace blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("missing-workspace", 3, "codex-a.json", "auth-a", "codex", ""))
				return events
			},
			wantAllowed: false,
		},
		{
			name: "conflicting target workspace evidence blocks",
			mutate: func(events []usage.Event) []usage.Event {
				return events
			},
			wantAllowed: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repo := New(db)
			events := []usage.Event{
				identityTestEvent("legacy-event", 1, "codex-a.json", "auth-a", "codex", "account-a"),
				identityTestEvent("stable-event", 2, "codex-a.json", "auth-a", "codex", "account-a"),
			}
			events = test.mutate(events)
			if _, err := repo.InsertBatch(context.Background(), events); err != nil {
				t.Fatalf("insert identity events: %v", err)
			}

			fields := usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			}
			if test.name == "conflicting target workspace evidence blocks" {
				fields.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("account-b")
			}
			gotKey, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllowed {
				t.Fatalf("allowed = %v, want %v (key=%q)", allowed, test.wantAllowed, gotKey)
			}
			wantKey, valid := usageidentity.LegacyAccountKey(fields)
			if !valid {
				t.Fatal("target legacy key is invalid")
			}
			if (gotKey == wantKey) != test.wantLegacyKey {
				t.Fatalf("legacy key = %q, want key present=%v (%q)", gotKey, test.wantLegacyKey, wantKey)
			}
		})
	}
}

func TestResolveCodexLegacyAccountKeyAcceptsLegacySourceFile(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	event := identityTestEvent("legacy-source", 1, "", "auth-a", "codex", "account-a")
	event.Source = "codex-a.json"
	event.AccountSnapshot = "same@example.com"
	stable := identityTestEvent("stable-source", 2, "codex-a.json", "auth-a", "codex", "account-a")
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event, stable}); err != nil {
		t.Fatalf("insert source identity events: %v", err)
	}

	fields := usageidentity.Fields{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AccountSnapshot:       "same@example.com",
		Source:                "codex-a.json",
	}
	key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
	if err != nil {
		t.Fatalf("resolve source identity: %v", err)
	}
	want, valid := usageidentity.LegacyAccountKey(fields)
	if !allowed || !valid || key != want {
		t.Fatalf("source legacy identity = key:%q allowed:%v, want key:%q allowed:true", key, allowed, want)
	}
}

func TestResolveCodexLegacyAccountKeyAcceptsSourceOnlyHistory(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	event := identityTestEvent("source-only", 1, "", "auth-a", "codex", "account-a")
	event.Source = "codex-a.json"
	event.AccountSnapshot = "Alice@Example.com"
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert source-only identity event: %v", err)
	}

	fields := usageidentity.Fields{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AccountSnapshot:       "alice@example.com",
		Source:                "codex-a.json",
	}
	key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
	if err != nil {
		t.Fatalf("resolve source-only identity: %v", err)
	}
	want, valid := usageidentity.LegacyAccountKey(fields)
	if !allowed || !valid || key != want {
		t.Fatalf("source-only legacy identity = key:%q allowed:%v, want key:%q allowed:true", key, allowed, want)
	}
}

func TestResolveCodexLegacyAccountKeySharesEvidenceAcrossPhysicalPredicates(t *testing.T) {
	for _, test := range []struct {
		name      string
		member    string
		accountID string
		provider  string
		wantAllow bool
	}{
		{name: "source predicate has different member", member: "bob@example.com", accountID: "account-a", provider: "codex"},
		{name: "source predicate has different workspace", member: "same@example.com", accountID: "account-b", provider: "codex"},
		{name: "source predicate has different provider", member: "same@example.com", accountID: "account-a", provider: "openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repo := New(db)
			exact := identityTestEvent("exact-predicate", 1, "codex-a.json", "auth-a", "codex", "account-a")
			source := identityTestEvent("source-predicate", 2, "", "auth-a", test.provider, test.accountID)
			source.Source = "codex-a.json"
			source.AccountSnapshot = test.member
			if _, err := repo.InsertBatch(context.Background(), []usage.Event{exact, source}); err != nil {
				t.Fatalf("insert identity events: %v", err)
			}

			fields := usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			}
			_, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllow {
				t.Fatalf("allowed = %v, want %v", allowed, test.wantAllow)
			}
		})
	}
}

func identityTestEvent(hash string, offset int64, file, authIndex, provider, accountID string) usage.Event {
	timestampMS := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli() + offset*1000
	return usage.Event{
		EventHash:             hash,
		TimestampMS:           timestampMS,
		Timestamp:             time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:              provider,
		Model:                 "gpt-test",
		AuthFileSnapshot:      file,
		AuthProviderSnapshot:  provider,
		AuthAccountIDSnapshot: accountID,
		AuthIndex:             authIndex,
		Source:                file,
		AccountSnapshot:       "same@example.com",
		InputTokens:           1,
		OutputTokens:          1,
		TotalTokens:           2,
		CreatedAtMS:           timestampMS,
	}
}
