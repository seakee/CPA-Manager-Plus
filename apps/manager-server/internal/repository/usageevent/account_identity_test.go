package usageevent

import (
	"bytes"
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

func TestResolveCodexLegacyAccountKeyAcceptsOnlyLeadingWeakIdentityPrefix(t *testing.T) {
	for _, test := range []struct {
		name        string
		buildEvents func() []usage.Event
		wantAllowed bool
	}{
		{
			name: "weak prefix followed by direct workspace",
			buildEvents: func() []usage.Event {
				weak := identityChronologyEvent("weak-prefix", 1000, "codex-a.json", "auth-a", "codex", "", "")
				strong := identityChronologyEvent("strong-anchor", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{weak, strong}
			},
			wantAllowed: true,
		},
		{
			name: "weak source prefix followed by direct file workspace",
			buildEvents: func() []usage.Event {
				weak := identityChronologyEvent("weak-source-prefix", 1000, "", "auth-a", "codex", "", "")
				weak.Source = "codex-a.json"
				strong := identityChronologyEvent("strong-file-anchor", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{weak, strong}
			},
			wantAllowed: true,
		},
		{
			name: "valid marked prefix followed by direct workspace",
			buildEvents: func() []usage.Event {
				marked := identityChronologyEvent("marked-prefix", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				strong := identityChronologyEvent("strong-anchor", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{marked, strong}
			},
			wantAllowed: true,
		},
		{
			name: "weak identity without direct anchor",
			buildEvents: func() []usage.Event {
				return []usage.Event{identityChronologyEvent("weak-only", 1000, "codex-a.json", "auth-a", "codex", "", "")}
			},
			wantAllowed: false,
		},
		{
			name: "weak identity after direct anchor",
			buildEvents: func() []usage.Event {
				strong := identityChronologyEvent("strong-anchor", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
				weak := identityChronologyEvent("weak-tail", 2000, "codex-a.json", "auth-a", "codex", "", "")
				return []usage.Event{strong, weak}
			},
			wantAllowed: false,
		},
		{
			name: "malformed provenance prefix",
			buildEvents: func() []usage.Event {
				invalid := identityChronologyEvent("invalid-prefix", 1000, "codex-a.json", "auth-a", "codex", "", "codex-account-id:v1:")
				strong := identityChronologyEvent("strong-anchor", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{invalid, strong}
			},
			wantAllowed: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repo := New(db)
			if _, err := repo.InsertBatch(context.Background(), test.buildEvents()); err != nil {
				t.Fatalf("insert identity events: %v", err)
			}

			key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			})
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllowed {
				t.Fatalf("allowed = %v, want %v (key=%q)", allowed, test.wantAllowed, key)
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

func identityChronologyEvent(hash string, createdAtMS int64, file, authIndex, provider, accountID, projectID string) usage.Event {
	event := identityTestEvent(hash, 0, file, authIndex, provider, accountID)
	event.AuthProjectIDSnapshot = projectID
	event.CreatedAtMS = createdAtMS
	event.TimestampMS = createdAtMS
	return event
}

func TestResolveCodexLegacyAccountKeyAcceptsValidMarkerOnlyHistory(t *testing.T) {
	for _, test := range []struct {
		name        string
		buildEvents func() []usage.Event
		wantAllowed bool
	}{
		{
			name: "Case 1: matching marker only",
			buildEvents: func() []usage.Event {
				event := identityChronologyEvent("marker-1", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{event}
			},
			wantAllowed: true,
		},
		{
			name: "Case 2: multiple matching markers only",
			buildEvents: func() []usage.Event {
				e1 := identityChronologyEvent("marker-1", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				e2 := identityChronologyEvent("marker-2", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				e3 := identityChronologyEvent("marker-3", 3000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{e1, e2, e3}
			},
			wantAllowed: true,
		},
		{
			name: "Case 3: marker-only conflicting workspace",
			buildEvents: func() []usage.Event {
				event := identityChronologyEvent("marker-b", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-b"))
				return []usage.Event{event}
			},
			wantAllowed: false,
		},
		{
			name: "Case 4: malformed marker-only",
			buildEvents: func() []usage.Event {
				event := identityChronologyEvent("marker-invalid", 1000, "codex-a.json", "auth-a", "codex", "", "codex-account-id:v1:")
				return []usage.Event{event}
			},
			wantAllowed: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repo := New(db)
			if _, err := repo.InsertBatch(context.Background(), test.buildEvents()); err != nil {
				t.Fatalf("insert identity events: %v", err)
			}

			key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			})
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllowed {
				t.Fatalf("allowed = %v, want %v (key=%q)", allowed, test.wantAllowed, key)
			}
		})
	}
}

func TestResolveCodexLegacyAccountKeyStateMachine(t *testing.T) {
	tests := []struct {
		name        string
		events      func() []usage.Event
		wantAllowed bool
	}{
		{
			name: "marker-only",
			events: func() []usage.Event {
				m1 := identityChronologyEvent("m1", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				m2 := identityChronologyEvent("m2", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{m1, m2}
			},
			wantAllowed: true,
		},
		{
			name: "direct-only",
			events: func() []usage.Event {
				d1 := identityChronologyEvent("d1", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
				d2 := identityChronologyEvent("d2", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{d1, d2}
			},
			wantAllowed: true,
		},
		{
			name: "marker then direct",
			events: func() []usage.Event {
				m := identityChronologyEvent("m", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				d := identityChronologyEvent("d", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{m, d}
			},
			wantAllowed: true,
		},
		{
			name: "direct then marker",
			events: func() []usage.Event {
				d := identityChronologyEvent("d", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
				m := identityChronologyEvent("m", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{d, m}
			},
			wantAllowed: true,
		},
		{
			name: "weak-only",
			events: func() []usage.Event {
				w1 := identityChronologyEvent("w1", 1000, "codex-a.json", "auth-a", "codex", "", "")
				w2 := identityChronologyEvent("w2", 2000, "codex-a.json", "auth-a", "codex", "", "")
				return []usage.Event{w1, w2}
			},
			wantAllowed: false,
		},
		{
			name: "weak then direct",
			events: func() []usage.Event {
				w := identityChronologyEvent("w", 1000, "codex-a.json", "auth-a", "codex", "", "")
				d := identityChronologyEvent("d", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{w, d}
			},
			wantAllowed: true,
		},
		{
			name: "weak then weak then direct",
			events: func() []usage.Event {
				w1 := identityChronologyEvent("w1", 1000, "codex-a.json", "auth-a", "codex", "", "")
				w2 := identityChronologyEvent("w2", 1500, "codex-a.json", "auth-a", "codex", "", "")
				d := identityChronologyEvent("d", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{w1, w2, d}
			},
			wantAllowed: true,
		},
		{
			name: "weak then marker then direct",
			events: func() []usage.Event {
				w := identityChronologyEvent("w", 1000, "codex-a.json", "auth-a", "codex", "", "")
				m := identityChronologyEvent("m", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				d := identityChronologyEvent("d", 3000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{w, m, d}
			},
			wantAllowed: true,
		},
		{
			name: "weak then marker",
			events: func() []usage.Event {
				w := identityChronologyEvent("w", 1000, "codex-a.json", "auth-a", "codex", "", "")
				m := identityChronologyEvent("m", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{w, m}
			},
			wantAllowed: false,
		},
		{
			name: "weak then marker then marker",
			events: func() []usage.Event {
				w := identityChronologyEvent("w", 1000, "codex-a.json", "auth-a", "codex", "", "")
				m1 := identityChronologyEvent("m1", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				m2 := identityChronologyEvent("m2", 3000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				return []usage.Event{w, m1, m2}
			},
			wantAllowed: false,
		},
		{
			name: "direct then weak",
			events: func() []usage.Event {
				d := identityChronologyEvent("d", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
				w := identityChronologyEvent("w", 2000, "codex-a.json", "auth-a", "codex", "", "")
				return []usage.Event{d, w}
			},
			wantAllowed: false,
		},
		{
			name: "marker then weak",
			events: func() []usage.Event {
				m := identityChronologyEvent("m", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				w := identityChronologyEvent("w", 2000, "codex-a.json", "auth-a", "codex", "", "")
				return []usage.Event{m, w}
			},
			wantAllowed: false,
		},
		{
			name: "marker then weak then direct",
			events: func() []usage.Event {
				m := identityChronologyEvent("m", 1000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				w := identityChronologyEvent("w", 2000, "codex-a.json", "auth-a", "codex", "", "")
				d := identityChronologyEvent("d", 3000, "codex-a.json", "auth-a", "codex", "account-a", "")
				return []usage.Event{m, w, d}
			},
			wantAllowed: false,
		},
		{
			name: "direct then marker then weak",
			events: func() []usage.Event {
				d := identityChronologyEvent("d", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
				m := identityChronologyEvent("m", 2000, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
				w := identityChronologyEvent("w", 3000, "codex-a.json", "auth-a", "codex", "", "")
				return []usage.Event{d, m, w}
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
			evs := test.events()
			if _, err := repo.InsertBatch(context.Background(), evs); err != nil {
				t.Fatalf("insert identity events: %v", err)
			}

			key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			})
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllowed {
				t.Fatalf("allowed = %v, want %v (key=%q)", allowed, test.wantAllowed, key)
			}
		})

		t.Run(test.name+"_reverse_insert", func(t *testing.T) {
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repo := New(db)
			evs := test.events()
			reversed := make([]usage.Event, len(evs))
			for i, ev := range evs {
				reversed[len(evs)-1-i] = ev
			}
			if _, err := repo.InsertBatch(context.Background(), reversed); err != nil {
				t.Fatalf("insert identity events reversed: %v", err)
			}

			key, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), usageidentity.Fields{
				AuthFileSnapshot:      "codex-a.json",
				AuthIndex:             "auth-a",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "account-a",
				AccountSnapshot:       "same@example.com",
				Source:                "codex-a.json",
			})
			if err != nil {
				t.Fatalf("resolve legacy identity: %v", err)
			}
			if allowed != test.wantAllowed {
				t.Fatalf("reverse insert allowed = %v, want %v (key=%q)", allowed, test.wantAllowed, key)
			}
		})
	}
}

func TestResolveCodexLegacyAccountKeyTimeFallback(t *testing.T) {
	fields := usageidentity.Fields{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AccountSnapshot:       "same@example.com",
		Source:                "codex-a.json",
	}

	t.Run("Case A: auth_snapshot_at_ms takes precedence over created_at_ms", func(t *testing.T) {
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		repo := New(db)

		// If CreatedAtMS were used: weak (3000) > direct (1500) => DENY
		// But AuthSnapshotAtMS: weak (1000) < direct (2000) => ALLOW
		weak := identityChronologyEvent("weak-precedence", 3000, "codex-a.json", "auth-a", "codex", "", "")
		weak.AuthSnapshotAtMS = 1000
		direct := identityChronologyEvent("direct-precedence", 1500, "codex-a.json", "auth-a", "codex", "account-a", "")
		direct.AuthSnapshotAtMS = 2000

		if _, err := repo.InsertBatch(context.Background(), []usage.Event{weak, direct}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !allowed {
			t.Fatal("expected allowed when auth_snapshot_at_ms establishes weak < direct")
		}
	})

	t.Run("Case B: auth_snapshot_at_ms is 0 falls back to created_at_ms", func(t *testing.T) {
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		repo := New(db)

		weak := identityChronologyEvent("weak-fallback", 1000, "codex-a.json", "auth-a", "codex", "", "")
		weak.AuthSnapshotAtMS = 0
		direct := identityChronologyEvent("direct-fallback", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")
		direct.AuthSnapshotAtMS = 0

		if _, err := repo.InsertBatch(context.Background(), []usage.Event{weak, direct}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !allowed {
			t.Fatal("expected allowed when created_at_ms establishes weak < direct")
		}
	})

	t.Run("Case C: weak history with both timestamps 0 fails closed", func(t *testing.T) {
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		repo := New(db)

		weak := identityChronologyEvent("weak-zero-time", 0, "codex-a.json", "auth-a", "codex", "", "")
		weak.AuthSnapshotAtMS = 0
		direct := identityChronologyEvent("direct-anchor", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")

		if _, err := repo.InsertBatch(context.Background(), []usage.Event{weak, direct}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if allowed {
			t.Fatal("expected denied when weak event lacks evidence time")
		}
	})

	t.Run("Case D: marker-only history with both timestamps 0 succeeds", func(t *testing.T) {
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		repo := New(db)

		marker := identityChronologyEvent("marker-zero-time", 0, "codex-a.json", "auth-a", "codex", "", usageidentity.CodexAccountIDSnapshot("account-a"))
		marker.AuthSnapshotAtMS = 0

		if _, err := repo.InsertBatch(context.Background(), []usage.Event{marker}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, allowed, err := repo.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !allowed {
			t.Fatal("expected allowed for marker-only history even when timestamp is 0")
		}
	})
}

func TestResolveCodexLegacyAccountKeyJSONLRoundTrip(t *testing.T) {
	fields := usageidentity.Fields{
		AuthFileSnapshot:      "codex-a.json",
		AuthIndex:             "auth-a",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AccountSnapshot:       "same@example.com",
		Source:                "codex-a.json",
	}

	t.Run("Round-trip Case A: weak to direct preserves ALLOW across export/import", func(t *testing.T) {
		dbA, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-a.sqlite"))
		if err != nil {
			t.Fatalf("open dbA: %v", err)
		}
		t.Cleanup(func() { _ = dbA.Close() })
		repoA := New(dbA)

		weak := identityChronologyEvent("rt-weak", 1000, "codex-a.json", "auth-a", "codex", "", "")
		direct := identityChronologyEvent("rt-direct", 2000, "codex-a.json", "auth-a", "codex", "account-a", "")

		if _, err := repoA.InsertBatch(context.Background(), []usage.Event{weak, direct}); err != nil {
			t.Fatalf("insert dbA: %v", err)
		}

		keyA, allowedA, err := repoA.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbA: %v", err)
		}
		if !allowedA {
			t.Fatalf("expected allowed in dbA, key=%q", keyA)
		}

		var exportBuf bytes.Buffer
		if err := repoA.WriteExportJSONL(context.Background(), &exportBuf, 100); err != nil {
			t.Fatalf("export dbA: %v", err)
		}

		dbB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-b.sqlite"))
		if err != nil {
			t.Fatalf("open dbB: %v", err)
		}
		t.Cleanup(func() { _ = dbB.Close() })
		repoB := New(dbB)

		streamResult, err := usage.StreamImportPayload(&exportBuf, 256, func(events []usage.Event) error {
			_, err := repoB.InsertBatch(context.Background(), events)
			return err
		})
		if err != nil {
			t.Fatalf("import into dbB: %v", err)
		}
		if streamResult.Failed != 0 || streamResult.Total != 2 {
			t.Fatalf("stream import result = %#v, want 2 total 0 failed", streamResult)
		}

		keyB, allowedB, err := repoB.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbB: %v", err)
		}
		if !allowedB || keyB != keyA {
			t.Fatalf("dbB result: allowed=%v, key=%q; want allowed=true, key=%q", allowedB, keyB, keyA)
		}

		// Verify inverted insertion order in DB C (direct inserted before weak, so direct gets lower row ID)
		dbC, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-c.sqlite"))
		if err != nil {
			t.Fatalf("open dbC: %v", err)
		}
		t.Cleanup(func() { _ = dbC.Close() })
		repoC := New(dbC)

		if _, err := repoC.InsertBatch(context.Background(), []usage.Event{direct, weak}); err != nil {
			t.Fatalf("insert dbC reversed: %v", err)
		}
		keyC, allowedC, err := repoC.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbC: %v", err)
		}
		if !allowedC || keyC != keyA {
			t.Fatalf("dbC (inverted SQLite ID) result: allowed=%v, key=%q; want allowed=true, key=%q", allowedC, keyC, keyA)
		}
	})

	t.Run("Round-trip Case B: direct to weak preserves DENY across export/import", func(t *testing.T) {
		dbA, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-a.sqlite"))
		if err != nil {
			t.Fatalf("open dbA: %v", err)
		}
		t.Cleanup(func() { _ = dbA.Close() })
		repoA := New(dbA)

		direct := identityChronologyEvent("rt-b-direct", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")
		weak := identityChronologyEvent("rt-b-weak", 2000, "codex-a.json", "auth-a", "codex", "", "")

		if _, err := repoA.InsertBatch(context.Background(), []usage.Event{direct, weak}); err != nil {
			t.Fatalf("insert dbA: %v", err)
		}

		_, allowedA, err := repoA.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbA: %v", err)
		}
		if allowedA {
			t.Fatal("expected denied in dbA for direct -> weak")
		}

		var exportBuf bytes.Buffer
		if err := repoA.WriteExportJSONL(context.Background(), &exportBuf, 100); err != nil {
			t.Fatalf("export dbA: %v", err)
		}

		dbB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-b.sqlite"))
		if err != nil {
			t.Fatalf("open dbB: %v", err)
		}
		t.Cleanup(func() { _ = dbB.Close() })
		repoB := New(dbB)

		_, err = usage.StreamImportPayload(&exportBuf, 256, func(events []usage.Event) error {
			_, err := repoB.InsertBatch(context.Background(), events)
			return err
		})
		if err != nil {
			t.Fatalf("import into dbB: %v", err)
		}

		_, allowedB, err := repoB.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbB: %v", err)
		}
		if allowedB {
			t.Fatal("expected denied in dbB for direct -> weak after export/import")
		}
	})

	t.Run("Round-trip Case C: same evidence time fails closed across export/import", func(t *testing.T) {
		dbA, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-a.sqlite"))
		if err != nil {
			t.Fatalf("open dbA: %v", err)
		}
		t.Cleanup(func() { _ = dbA.Close() })
		repoA := New(dbA)

		weak := identityChronologyEvent("rt-c-weak", 1000, "codex-a.json", "auth-a", "codex", "", "")
		direct := identityChronologyEvent("rt-c-direct", 1000, "codex-a.json", "auth-a", "codex", "account-a", "")

		if _, err := repoA.InsertBatch(context.Background(), []usage.Event{weak, direct}); err != nil {
			t.Fatalf("insert dbA: %v", err)
		}

		_, allowedA, err := repoA.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbA: %v", err)
		}
		if allowedA {
			t.Fatal("expected denied in dbA for same evidence timestamp")
		}

		var exportBuf bytes.Buffer
		if err := repoA.WriteExportJSONL(context.Background(), &exportBuf, 100); err != nil {
			t.Fatalf("export dbA: %v", err)
		}

		dbB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage-b.sqlite"))
		if err != nil {
			t.Fatalf("open dbB: %v", err)
		}
		t.Cleanup(func() { _ = dbB.Close() })
		repoB := New(dbB)

		_, err = usage.StreamImportPayload(&exportBuf, 256, func(events []usage.Event) error {
			_, err := repoB.InsertBatch(context.Background(), events)
			return err
		})
		if err != nil {
			t.Fatalf("import into dbB: %v", err)
		}

		_, allowedB, err := repoB.ResolveCodexLegacyAccountKey(context.Background(), fields)
		if err != nil {
			t.Fatalf("resolve dbB: %v", err)
		}
		if allowedB {
			t.Fatal("expected denied in dbB for same evidence timestamp after export/import")
		}
	})
}
