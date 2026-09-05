package usageidentity

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAccountKeySeparatesSharedAccountSnapshotsByCredential(t *testing.T) {
	first, ok := AccountKey(Fields{AuthFileSnapshot: "shared.json", AuthIndex: "auth-a", AuthProviderSnapshot: "codex", AccountSnapshot: "same@example.com"})
	if !ok {
		t.Fatal("first key is invalid")
	}
	second, ok := AccountKey(Fields{AuthFileSnapshot: "shared.json", AuthIndex: "auth-b", AuthProviderSnapshot: "codex", AccountSnapshot: "same@example.com"})
	if !ok {
		t.Fatal("second key is invalid")
	}
	if first == second {
		t.Fatalf("shared account snapshot merged distinct credentials: %q", first)
	}
}

func TestAccountKeyRejectsMissingIdentity(t *testing.T) {
	if key, ok := AccountKey(Fields{}); ok || key != "" {
		t.Fatalf("AccountKey() = %q, %v; want empty, false", key, ok)
	}
}

func TestAccountKeyKeepsNonCodexProviderMappings(t *testing.T) {
	tests := []struct {
		name   string
		fields Fields
		want   string
	}{
		{
			name: "Claude file and index",
			fields: Fields{
				AuthFileSnapshot:     "claude.json",
				AuthIndex:            "auth-1",
				AuthProviderSnapshot: "claude",
			},
			want: "usage-account-history:3:file-index:636C617564652E6A736F6E:617574682D31",
		},
		{
			name: "XAI file and project",
			fields: Fields{
				AuthFileSnapshot:      "xai.json",
				AuthProviderSnapshot:  "grok",
				AuthProjectIDSnapshot: "project-1",
			},
			want: "usage-account-history:3:file-project:7861692E6A736F6E:786169:70726F6A6563742D31",
		},
		{
			name: "Gemini account fallback",
			fields: Fields{
				AuthProviderSnapshot: "gemini",
				AccountSnapshot:      "alice@example.com",
			},
			want: "usage-account-history:3:account:67656D696E69:616C696365406578616D706C652E636F6D",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := AccountKey(test.fields)
			if !ok || got != test.want {
				t.Fatalf("AccountKey(%#v) = %q, %v; want %q, true", test.fields, got, ok, test.want)
			}
		})
	}
}

func TestAccountKeyUsesCodexWorkspaceAndMemberAcrossMutableCredentialIdentity(t *testing.T) {
	oldKey, ok := AccountKey(Fields{AuthFileSnapshot: "codex-a-free.json", AuthIndex: "auth-1", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AccountSnapshot: " Alice@Example.com "})
	if !ok {
		t.Fatal("old key is invalid")
	}
	newKey, ok := AccountKey(Fields{AuthFileSnapshot: "codex-a-pro.json", AuthIndex: "auth-2", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AccountSnapshot: "alice@example.com"})
	if !ok {
		t.Fatal("new key is invalid")
	}
	if oldKey != newKey {
		t.Fatalf("same Codex member split across reauth: old=%q new=%q", oldKey, newKey)
	}
	legacyKey, ok := LegacyAccountKey(Fields{AuthFileSnapshot: "codex-a-pro.json", AuthIndex: "auth-2", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AccountSnapshot: "alice@example.com"})
	if !ok || legacyKey == newKey {
		t.Fatalf("legacy exact credential key = %q, stable key = %q", legacyKey, newKey)
	}
	differentMemberKey, ok := AccountKey(Fields{AuthFileSnapshot: "codex-b.json", AuthIndex: "auth-3", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AccountSnapshot: "bob@example.com"})
	if !ok || differentMemberKey == oldKey {
		t.Fatalf("different Codex member merged: alice=%q bob=%q", oldKey, differentMemberKey)
	}
	differentWorkspaceKey, ok := AccountKey(Fields{AuthFileSnapshot: "codex-c.json", AuthIndex: "auth-4", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-2", AccountSnapshot: "alice@example.com"})
	if !ok || differentWorkspaceKey == oldKey {
		t.Fatalf("same email in different Codex workspace merged: workspace-1=%q workspace-2=%q", oldKey, differentWorkspaceKey)
	}
}

func TestLegacyCodexWorkspaceAccountKeyIsExplicitAndStable(t *testing.T) {
	key, ok := LegacyCodexWorkspaceAccountKey(" Codex ", " workspace-1 ")
	const want = "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31"
	if !ok || key != want {
		t.Fatalf("LegacyCodexWorkspaceAccountKey() = %q, %v; want %q, true", key, ok, want)
	}
	for _, test := range []struct {
		provider  string
		workspace string
	}{
		{provider: "openai", workspace: "workspace-1"},
		{provider: "codex", workspace: "   "},
	} {
		if got, valid := LegacyCodexWorkspaceAccountKey(test.provider, test.workspace); valid || got != "" {
			t.Fatalf("LegacyCodexWorkspaceAccountKey(%q, %q) = %q, %v; want empty, false", test.provider, test.workspace, got, valid)
		}
	}
}

func TestHasCodexAccountIDSnapshotMarkerIncludesMalformedEvidence(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: CodexAccountIDSnapshot("workspace-1"), want: true},
		{value: " codex-account-id:v1: ", want: true},
		{value: "workspace-1", want: false},
		{value: "", want: false},
	} {
		if got := HasCodexAccountIDSnapshotMarker(test.value); got != test.want {
			t.Fatalf("HasCodexAccountIDSnapshotMarker(%q) = %v; want %v", test.value, got, test.want)
		}
	}
}

func TestAccountKeyFallsBackWhenCodexMemberEvidenceIsMissingOrWeak(t *testing.T) {
	for _, snapshot := range []string{"", "Alice", "alice@example.com@duplicate"} {
		fields := Fields{
			AuthFileSnapshot:      "codex.json",
			AuthIndex:             "auth-1",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-1",
			AccountSnapshot:       snapshot,
		}
		got, ok := AccountKey(fields)
		want, wantOK := LegacyAccountKey(fields)
		if !ok || !wantOK || got != want || strings.Contains(got, ":636F6465782D6D656D626572:") {
			t.Fatalf("snapshot %q: AccountKey() = %q, %v; want legacy %q, %v", snapshot, got, ok, want, wantOK)
		}
	}
}

func TestLegacyAccountKeyRejectsCodexDisplayFallbackWithoutCredentialIdentity(t *testing.T) {
	for _, fields := range []Fields{
		{AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AuthProjectIDSnapshot: "project-1"},
		{AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AccountSnapshot: "Alice"},
		{AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-1", AuthLabelSnapshot: "Alice"},
		{AuthProviderSnapshot: "codex", AccountSnapshot: "alice@example.com"},
	} {
		if key, ok := LegacyAccountKey(fields); ok || key != "" {
			t.Fatalf("LegacyAccountKey(%#v) = %q, %v; want empty, false", fields, key, ok)
		}
		if key, ok := AccountKey(fields); ok || key != "" {
			t.Fatalf("AccountKey(%#v) = %q, %v; want empty, false", fields, key, ok)
		}
	}
}

func TestNormalizeCodexMemberSnapshot(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
		ok    bool
	}{
		{value: " Alice@Example.com ", want: "alice@example.com", ok: true},
		{value: "Alice", ok: false},
		{value: "@example.com", ok: false},
		{value: "alice@", ok: false},
		{value: "alice@example.com@other", ok: false},
		{value: "alice @example.com", ok: false},
		{value: "alice\x7f@example.com", ok: false},
	} {
		got, ok := NormalizeCodexMemberSnapshot(test.value)
		if got != test.want || ok != test.ok {
			t.Fatalf("NormalizeCodexMemberSnapshot(%q) = %q, %v; want %q, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestNormalizeCodexWorkspaceSnapshotPreservesOpaqueIDs(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
		ok    bool
	}{
		{value: " workspace-1 ", want: "workspace-1", ok: true},
		{value: "workspace-1\t", want: "workspace-1\t", ok: true},
		{value: "workspace-1\u2003", want: "workspace-1\u2003", ok: true},
		{value: "workspace-1\x00", want: "workspace-1\x00", ok: true},
		{value: "workspace 1", want: "workspace 1", ok: true},
		{value: "工作区-1", want: "工作区-1", ok: true},
		{value: "   ", ok: false},
	} {
		got, ok := NormalizeCodexWorkspaceSnapshot(test.value)
		if got != test.want || ok != test.ok {
			t.Fatalf("NormalizeCodexWorkspaceSnapshot(%q) = %q, %v; want %q, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestLegacyAccountKeyDoesNotUseCodexWorkspaceSnapshotFallback(t *testing.T) {
	for _, fields := range []Fields{
		{
			AuthFileSnapshot:      "codex.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-1",
			AccountSnapshot:       "Alice",
		},
		{
			AuthFileSnapshot:      "codex.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-1",
			AccountSnapshot:       "Alice",
			AuthProjectIDSnapshot: "project-1",
		},
	} {
		key, ok := LegacyAccountKey(fields)
		if !ok || strings.Contains(key, "416C696365") || strings.Contains(key, "776F726B73706163652D31") {
			t.Fatalf("LegacyAccountKey(%#v) = %q, %v; must not use workspace/display fallback", fields, key, ok)
		}
	}

	key, ok := LegacyAccountKey(Fields{
		AuthFileSnapshot:      "codex.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AccountSnapshot:       "Alice",
		AuthIndex:             "auth-1",
	})
	if !ok || strings.Contains(key, "416C696365") {
		t.Fatalf("Codex auth-index fallback = %q, %v; must be credential-only", key, ok)
	}
}

func TestCodexMemberRevisionDoesNotChangeGlobalFormatVersion(t *testing.T) {
	if FormatVersion != "3" {
		t.Fatalf("FormatVersion = %q, want 3", FormatVersion)
	}
	if CodexIdentityRevision != "2" {
		t.Fatalf("CodexIdentityRevision = %q, want 2", CodexIdentityRevision)
	}
	if got := AccountHistoryStructureRevision(); got != "identity-3:codex-2:model-1" {
		t.Fatalf("account history revision = %q", got)
	}
	if got := MonitoringProjectionStructureRevision(); got != "identity-3:codex-2:model-1:project-v1" {
		t.Fatalf("monitoring projection revision = %q", got)
	}
}

func TestAccountKeyDoesNotPromoteHistoricalCodexProjectSnapshot(t *testing.T) {
	fields := Fields{AuthFileSnapshot: "legacy-codex.json", AuthIndex: "auth-old", AuthProviderSnapshot: "codex", AuthProjectIDSnapshot: "generic-project"}
	got, ok := AccountKey(fields)
	want, legacyOK := LegacyAccountKey(fields)
	if !ok || !legacyOK || got != want {
		t.Fatalf("historical Codex project snapshot promoted: got=%q want legacy=%q", got, want)
	}
}

func TestAccountKeyDoesNotReadLegacyCodexAccountMarkerFromProjectSnapshot(t *testing.T) {
	fields := Fields{AuthFileSnapshot: "legacy-codex.json", AuthIndex: "auth-old", AuthProviderSnapshot: "codex", AuthProjectIDSnapshot: CodexAccountIDSnapshot("account-a")}
	got, ok := AccountKey(fields)
	want, legacyOK := LegacyAccountKey(fields)
	if !ok || !legacyOK || got != want {
		t.Fatalf("legacy project marker promoted to stable account: got=%q want=%q", got, want)
	}
}

func TestCodexAccountIDSnapshotRequiresExplicitMarker(t *testing.T) {
	if got := CodexAccountIDFromSnapshot("account-a"); got != "" {
		t.Fatalf("unmarked snapshot returned account id %q", got)
	}
	if got := CodexAccountIDFromSnapshot(CodexAccountIDSnapshot(" account-a ")); got != "account-a" {
		t.Fatalf("marked snapshot returned %q", got)
	}
}

func TestSQLAccountKeyExpressionMatchesGo(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table usage_events (
		auth_file_snapshot text, auth_index text, auth_provider_snapshot text,
		auth_account_id_snapshot text, auth_project_id_snapshot text, account_snapshot text,
		auth_label_snapshot text, source text, provider text
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	testCases := []struct {
		name     string
		fields   Fields
		provider string
	}{
		{name: "file and auth index", fields: Fields{AuthFileSnapshot: "shared.json", AuthIndex: "auth-a", AuthProviderSnapshot: "x_ai", AuthProjectIDSnapshot: "project-a", AccountSnapshot: "same@example.com", AuthLabelSnapshot: "Same Account", Source: "legacy-source"}, provider: "xai"},
		{name: "codex stable account", fields: Fields{AuthFileSnapshot: "codex-new.json", AuthIndex: "auth-new", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "account-a", AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex second workspace member", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-team", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "bob@example.com"}, provider: "codex"},
		{name: "codex same member different workspace", fields: Fields{AuthFileSnapshot: "codex-other-workspace.json", AuthIndex: "auth-other-workspace", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-b", AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex direct and marked workspace agree", fields: Fields{AuthFileSnapshot: "codex-marked.json", AuthIndex: "auth-marked", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AuthProjectIDSnapshot: CodexAccountIDSnapshot("workspace-a"), AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex direct workspace ignores non-ASCII-space marker prefix", fields: Fields{AuthFileSnapshot: "codex-marked-leading-tab.json", AuthIndex: "auth-marked-leading-tab", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AuthProjectIDSnapshot: "\t" + CodexAccountIDSnapshot("workspace-a"), AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex marker-only workspace falls back", fields: Fields{AuthFileSnapshot: "codex-marker-only.json", AuthIndex: "auth-marker-only", AuthProviderSnapshot: "codex", AuthProjectIDSnapshot: CodexAccountIDSnapshot("workspace-a"), AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex conflicting workspace evidence falls back", fields: Fields{AuthFileSnapshot: "codex-conflict.json", AuthIndex: "auth-conflict", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AuthProjectIDSnapshot: CodexAccountIDSnapshot("workspace-b"), AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "codex missing member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-team", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a"}, provider: "codex"},
		{name: "codex weak member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-team", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "Alice"}, provider: "codex"},
		{name: "codex account with generic project fallback", fields: Fields{AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AuthProjectIDSnapshot: "generic-project", AccountSnapshot: "Alice"}, provider: "codex"},
		{name: "codex weak member with file and generic project", fields: Fields{AuthFileSnapshot: "codex-weak.json", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AuthProjectIDSnapshot: "generic-project", AccountSnapshot: "Alice"}, provider: "codex"},
		{name: "codex unicode member falls back", fields: Fields{AuthFileSnapshot: "codex-unicode.json", AuthIndex: "auth-unicode", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "álîce@example.com"}, provider: "codex"},
		{name: "codex control member falls back", fields: Fields{AuthFileSnapshot: "codex-control.json", AuthIndex: "auth-control", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "alice\x0e@example.com"}, provider: "codex"},
		{name: "codex tab member falls back", fields: Fields{AuthFileSnapshot: "codex-tab-member.json", AuthIndex: "auth-tab-member", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "\talice@example.com"}, provider: "codex"},
		{name: "codex unicode-space member falls back", fields: Fields{AuthFileSnapshot: "codex-unicode-space-member.json", AuthIndex: "auth-unicode-space-member", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "\u2003alice@example.com"}, provider: "codex"},
		{name: "codex tab workspace is preserved", fields: Fields{AuthFileSnapshot: "codex-tab.json", AuthIndex: "auth-tab", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a\t", AccountSnapshot: "alice@example.com"}, provider: "codex"},
		{name: "codex unicode workspace is preserved", fields: Fields{AuthFileSnapshot: "codex-unicode-workspace.json", AuthIndex: "auth-unicode-workspace", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-\u2003a", AccountSnapshot: "alice@example.com"}, provider: "codex"},
		{name: "codex nul workspace is preserved", fields: Fields{AuthFileSnapshot: "codex-nul-workspace.json", AuthIndex: "auth-nul-workspace", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a\x00", AccountSnapshot: "alice@example.com"}, provider: "codex"},
		{name: "explicit provider snapshot wins over raw provider", fields: Fields{AuthFileSnapshot: "claude.json", AuthIndex: "auth-claude", AuthProviderSnapshot: "claude", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "same@example.com"}, provider: "codex"},
		{name: "historical codex project", fields: Fields{AuthFileSnapshot: "codex-old.json", AuthIndex: "auth-old", AuthProviderSnapshot: "codex", AuthProjectIDSnapshot: "generic-project"}, provider: "codex"},
		{name: "legacy source file and project", fields: Fields{Source: "legacy.json", AuthProviderSnapshot: "vertex", AuthProjectIDSnapshot: "project-a"}, provider: "vertex"},
		{name: "auth index without file", fields: Fields{AuthIndex: "auth-only", AuthProviderSnapshot: "grok"}, provider: "x-ai"},
		{name: "account fallback ignores matching source", fields: Fields{AccountSnapshot: "legacy@example.com", AuthProviderSnapshot: "open_ai", Source: "legacy@example.com"}, provider: "open-ai"},
		{name: "label fallback", fields: Fields{AuthLabelSnapshot: "Legacy Label", AuthProviderSnapshot: "claude"}, provider: "claude"},
		{name: "missing identity", fields: Fields{}, provider: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`delete from usage_events`); err != nil {
				t.Fatalf("clear rows: %v", err)
			}
			f := tc.fields
			if _, err := db.Exec(`insert into usage_events values (?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.AuthFileSnapshot, f.AuthIndex, f.AuthProviderSnapshot, f.AuthAccountIDSnapshot, f.AuthProjectIDSnapshot, f.AccountSnapshot, f.AuthLabelSnapshot, f.Source, tc.provider); err != nil {
				t.Fatalf("insert row: %v", err)
			}
			want, valid := AccountKey(f)
			var got string
			if err := db.QueryRow(`select ` + SQLAccountKeyExpression("e") + ` from usage_events e`).Scan(&got); err != nil {
				t.Fatalf("query SQL key: %v", err)
			}
			if got != want {
				t.Fatalf("SQL key = %q, want %q", got, want)
			}
			if valid != (got != "") {
				t.Fatalf("valid = %v for SQL key %q", valid, got)
			}
		})
	}
}

func TestPricingStructureRevisionIncludesIdentityFormat(t *testing.T) {
	if got := PricingStructureRevision("price-revision"); got != "model-1:identity-3:codex-2:price-revision" {
		t.Fatalf("revision = %q", got)
	}
}

func TestSQLAccountKeyExpressionWithoutProjectMatchesGoForDailyIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table daily (
		auth_file_snapshot text, auth_index text, auth_provider_snapshot text,
		auth_account_id_snapshot text, account_snapshot text, auth_label_snapshot text,
		source text, provider text
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	testCases := []struct {
		name   string
		fields Fields
	}{
		{name: "codex stable member", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-alice", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "alice@example.com", Source: "codex-team.json"}},
		{name: "codex second member", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-bob", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "bob@example.com", Source: "codex-team.json"}},
		{name: "codex missing member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-missing", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", Source: "codex-team.json"}},
		{name: "codex weak member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-weak", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "Alice", Source: "codex-team.json"}},
		{name: "codex opaque workspace is preserved", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-opaque-workspace", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a\t", AccountSnapshot: "alice@example.com", Source: "codex-team.json"}},
		{name: "codex invalid member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-invalid-member", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "alice\x0e@example.com", Source: "codex-team.json"}},
		{name: "codex tab member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-tab-member", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "\talice@example.com", Source: "codex-team.json"}},
		{name: "codex unicode-space member falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-unicode-space-member", AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "workspace-a", AccountSnapshot: "\u2003alice@example.com", Source: "codex-team.json"}},
		{name: "codex marker-only workspace falls back", fields: Fields{AuthFileSnapshot: "codex-team.json", AuthIndex: "auth-marker", AuthProviderSnapshot: "codex", AuthProjectIDSnapshot: CodexAccountIDSnapshot("workspace-a"), AccountSnapshot: "alice@example.com", Source: "codex-team.json"}},
		{name: "non-codex file index", fields: Fields{AuthFileSnapshot: "claude.json", AuthIndex: "auth-claude", AuthProviderSnapshot: "claude", AccountSnapshot: "same@example.com", Source: "claude.json"}},
		{name: "non-codex account fallback", fields: Fields{AuthFileSnapshot: "claude.json", AuthProviderSnapshot: "claude", AccountSnapshot: "same@example.com", Source: "claude.json"}},
		{name: "missing identity", fields: Fields{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`delete from daily`); err != nil {
				t.Fatalf("clear rows: %v", err)
			}
			fields := tc.fields
			if _, err := db.Exec(`insert into daily values (?, ?, ?, ?, ?, ?, ?, ?)`, fields.AuthFileSnapshot, fields.AuthIndex, fields.AuthProviderSnapshot, fields.AuthAccountIDSnapshot, fields.AccountSnapshot, fields.AuthLabelSnapshot, fields.Source, fields.AuthProviderSnapshot); err != nil {
				t.Fatalf("insert daily row: %v", err)
			}
			want, valid := AccountKey(fields)
			var got string
			if err := db.QueryRow(`select ` + SQLAccountKeyExpressionWithoutProject("d") + ` from daily d`).Scan(&got); err != nil {
				t.Fatalf("query daily SQL key: %v", err)
			}
			if got != want {
				t.Fatalf("daily SQL key = %q, want %q", got, want)
			}
			if valid != (got != "") {
				t.Fatalf("valid = %v for daily SQL key %q", valid, got)
			}
		})
	}
}
