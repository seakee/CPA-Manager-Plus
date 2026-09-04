package usageevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			name: "different account id blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("different-account", 3, "codex-a.json", "auth-a", "codex", "account-b"))
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
			name: "aliased non-codex provider blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events = append(events, identityTestEvent("grok-provider", 3, "codex-a.json", "auth-a", "x-ai", ""))
				return events
			},
			wantAllowed: false,
		},
		{
			name: "marked project snapshot mismatch blocks",
			mutate: func(events []usage.Event) []usage.Event {
				events[0].AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("account-b")
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
				identityTestEvent("legacy-event", 1, "codex-a.json", "auth-a", "codex", ""),
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
	event := identityTestEvent("legacy-source", 1, "", "auth-a", "codex", "")
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

func TestResolveCodexLegacyAccountKeySkipsNonCodexTargets(t *testing.T) {
	cases := []usageidentity.Fields{
		{
			AuthProviderSnapshot:  "openai",
			AuthAccountIDSnapshot: "account-a",
			AuthFileSnapshot:      "codex-a.json",
			AuthIndex:             "auth-a",
		},
		{
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "",
			AuthFileSnapshot:      "codex-a.json",
			AuthIndex:             "auth-a",
		},
		{
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthFileSnapshot:      "",
			AuthIndex:             "auth-a",
			AccountSnapshot:       "same@example.com",
			Source:                "same@example.com",
		},
		{
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthFileSnapshot:      "codex-a.json",
			AuthIndex:             "",
		},
		{
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthFileSnapshot:      "",
			AuthIndex:             "auth-a",
			Source:                "",
		},
		{
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthFileSnapshot:      "",
			AuthIndex:             "auth-a",
			AuthLabelSnapshot:     "ops",
			Source:                "ops",
		},
	}
	for _, fields := range cases {
		key, allowed, err := ResolveCodexLegacyAccountKey(context.Background(), panicLegacyIdentityQueryer{}, fields)
		if err != nil || allowed || key != "" {
			t.Fatalf("non-codex target = key:%q allowed:%v err:%v, want skip", key, allowed, err)
		}
	}
}

func TestResolveCodexLegacyAccountKeyReturnsQueryError(t *testing.T) {
	_, allowed, err := ResolveCodexLegacyAccountKey(
		context.Background(),
		stubLegacyIdentityQueryer{err: errors.New("query failed")},
		codexLegacyIdentityTestFields(),
	)
	if err == nil || allowed {
		t.Fatalf("query error = allowed:%v err:%v, want error", allowed, err)
	}
}

func TestResolveCodexLegacyAccountKeyAllowsMatchingSetAtProbeLimit(t *testing.T) {
	key, allowed := resolveLegacyIdentityWithMatchingEvents(t, maxCodexLegacyIdentityProbe)
	want, valid := usageidentity.LegacyAccountKey(codexLegacyIdentityTestFields())
	if !valid || !allowed || key != want {
		t.Fatalf("at-limit identity = key:%q allowed:%v, want key:%q allowed:true", key, allowed, want)
	}
}

func TestResolveCodexLegacyAccountKeyRefusesMatchingSetBeyondProbeLimit(t *testing.T) {
	key, allowed := resolveLegacyIdentityWithMatchingEvents(t, maxCodexLegacyIdentityProbe+1)
	if allowed || key != "" {
		t.Fatalf("over-limit identity = key:%q allowed:%v, want refused", key, allowed)
	}
}

func resolveLegacyIdentityWithMatchingEvents(t *testing.T, n int) (string, bool) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	events := make([]usage.Event, 0, n)
	for i := 0; i < n; i++ {
		accountID := ""
		if i%3 == 0 {
			accountID = "account-a"
		}
		events = append(events, identityTestEvent(
			fmt.Sprintf("probe-%d", i),
			int64(i+1),
			"codex-a.json",
			"auth-a",
			"codex",
			accountID,
		))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert identity events: %v", err)
	}
	key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), codexLegacyIdentityTestFields())
	if err != nil {
		t.Fatalf("resolve identity: %v", err)
	}
	return key, allowed
}

func codexLegacyIdentityTestFields() usageidentity.Fields {
	return usageidentity.Fields{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AccountSnapshot:       "same@example.com",
		Source:                "codex-a.json",
	}
}

type stubLegacyIdentityQueryer struct {
	err error
}

func (s stubLegacyIdentityQueryer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, s.err
}

type panicLegacyIdentityQueryer struct{}

func (panicLegacyIdentityQueryer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("legacy identity query should not run for skipped targets")
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
