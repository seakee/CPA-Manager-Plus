package usageevent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestRecentAccountRequestsUseSnapshotIdentityLimitAndConservativeLegacyFallback(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()
	baseMS := int64(1_700_000_000_000)

	oldest := latestAccountRequestEvent("oldest", baseMS+500, "credential-a.json", "auth-a", "source-a")
	current := latestAccountRequestEvent("current", baseMS+1_000, "credential-a.json", "auth-a", "source-a")
	latest := latestAccountRequestEvent("latest", baseMS+2_000, "Credential-A.JSON", "AUTH-A", "source-a")
	latest.Failed = true
	latest.FailStatusCode = 429
	latest.FailBody = "Authorization: Bearer hidden-request-token"
	latest.HeaderErrorKind = "rate_limit"
	latest.HeaderErrorCode = "quota_exceeded"
	latest.HeaderTraceID = "trace-latest-a"
	wrongFile := latestAccountRequestEvent("wrong-file", baseMS+9_000, "credential-b.json", "auth-a", "source-b")
	wrongIndex := latestAccountRequestEvent("wrong-index", baseMS+10_000, "credential-a.json", "auth-b", "source-a")
	emailCollision := latestAccountRequestEvent("email-collision", baseMS+11_000, "", "auth-a", "alice@example.com")
	emailCollision.AccountSnapshot = "alice@example.com"
	legacy := latestAccountRequestEvent("legacy", baseMS+3_000, "", "legacy.json", "legacy.json")
	legacy.Failed = true
	legacy.FailStatusCode = 503
	legacy.FailSummary = "upstream unavailable"
	legacyWithSnapshot := latestAccountRequestEvent("legacy-with-snapshot", baseMS+12_000, "other.json", "legacy.json", "legacy.json")

	if _, err := repo.InsertBatch(ctx, []usage.Event{
		oldest,
		current,
		latest,
		wrongFile,
		wrongIndex,
		emailCollision,
		legacy,
		legacyWithSnapshot,
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	requests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "credential-a.json", AuthIndex: "auth-a"},
		{RequestIndex: 1, AuthFileSnapshot: "legacy.json", AuthIndex: "legacy.json"},
		{RequestIndex: 2, AuthFileSnapshot: "missing.json", AuthIndex: "auth-missing"},
	}, 2)
	if err != nil {
		t.Fatalf("recent account requests: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}

	byIndex := make(map[int][]LatestAccountRequest, len(requests))
	for _, request := range requests {
		byIndex[request.RequestIndex] = append(byIndex[request.RequestIndex], request)
	}

	primaryRequests := byIndex[0]
	if len(primaryRequests) != 2 {
		t.Fatalf("primary requests = %#v", primaryRequests)
	}
	primary := primaryRequests[0]
	if primary.TimestampMS != latest.TimestampMS || !primary.Failed || !primary.FailStatusCode.Valid || primary.FailStatusCode.Int64 != 429 {
		t.Fatalf("primary latest request = %#v", primary)
	}
	if primary.HeaderErrorKind != "rate_limit" || primary.HeaderErrorCode != "quota_exceeded" || primary.HeaderTraceID != "trace-latest-a" {
		t.Fatalf("primary diagnostics = %#v", primary)
	}
	if strings.Contains(primary.FailSummary, "hidden-request-token") || !strings.Contains(primary.FailSummary, "[redacted]") {
		t.Fatalf("primary failure summary was not safely reduced: %q", primary.FailSummary)
	}
	if primaryRequests[1].TimestampMS != current.TimestampMS {
		t.Fatalf("primary request order = %#v", primaryRequests)
	}
	for _, request := range primaryRequests {
		if request.TimestampMS == oldest.TimestampMS {
			t.Fatalf("per-credential limit was not applied: %#v", primaryRequests)
		}
	}

	legacyRequests := byIndex[1]
	if len(legacyRequests) != 1 {
		t.Fatalf("legacy requests = %#v", legacyRequests)
	}
	legacyResult := legacyRequests[0]
	if legacyResult.TimestampMS != legacy.TimestampMS || !legacyResult.Failed || !legacyResult.FailStatusCode.Valid || legacyResult.FailStatusCode.Int64 != 503 {
		t.Fatalf("legacy fallback = %#v", legacyResult)
	}
	if _, ok := byIndex[2]; ok {
		t.Fatalf("missing credential unexpectedly matched: %#v", byIndex[2])
	}
}

func TestRecentAccountRequestsFiltersCodexWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, withIndexes bool) []LatestAccountRequest {
		t.Helper()
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if withIndexes {
			if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
				t.Fatalf("prepare latest-request indexes: %v", err)
			}
		}
		repo := New(db)
		alice := latestAccountRequestEvent("codex-alice", 1_700_000_020_000, "shared.json", "auth-shared", "shared.json")
		alice.Provider = "codex"
		alice.AuthProviderSnapshot = "codex"
		alice.AuthAccountIDSnapshot = "workspace-1"
		alice.AccountSnapshot = "alice@example.com"
		bob := latestAccountRequestEvent("codex-bob", 1_700_000_030_000, "shared.json", "auth-shared", "shared.json")
		bob.Provider = "codex"
		bob.AuthProviderSnapshot = "codex"
		bob.AuthAccountIDSnapshot = "workspace-1"
		bob.AccountSnapshot = "bob@example.com"
		legacyAlice := latestAccountRequestEvent("codex-alice-legacy", 1_700_000_010_000, "", "auth-shared", "shared.json")
		legacyAlice.Provider = "codex"
		legacyAlice.AuthProviderSnapshot = "codex"
		legacyAlice.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-1")
		legacyAlice.AccountSnapshot = "alice@example.com"
		legacyOtherWorkspace := latestAccountRequestEvent("codex-alice-other-workspace", 1_700_000_040_000, "", "auth-shared", "shared.json")
		legacyOtherWorkspace.Provider = "codex"
		legacyOtherWorkspace.AuthProviderSnapshot = "codex"
		legacyOtherWorkspace.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-2")
		legacyOtherWorkspace.AccountSnapshot = "alice@example.com"
		if _, err := repo.InsertBatch(ctx, []usage.Event{alice, bob, legacyAlice, legacyOtherWorkspace}); err != nil {
			t.Fatalf("insert shared Codex requests: %v", err)
		}
		requests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
			{
				RequestIndex:          7,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AccountSnapshot:       "alice@example.com",
			},
			{
				RequestIndex:          8,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AccountSnapshot:       "bob@example.com",
			},
			{
				RequestIndex:          9,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
			},
			{
				RequestIndex:          10,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AuthProjectIDSnapshot: usageidentity.CodexAccountIDSnapshot("workspace-2"),
				AccountSnapshot:       "alice@example.com",
			},
		}, 10)
		if err != nil {
			t.Fatalf("recent shared Codex requests: %v", err)
		}
		return requests
	}

	indexed := seed(t, true)
	batched := seed(t, false)
	if !sameLatestAccountRequests(indexed, batched) {
		t.Fatalf("indexed = %#v, batched = %#v", indexed, batched)
	}
	byIndex := make(map[int][]LatestAccountRequest)
	for _, request := range indexed {
		byIndex[request.RequestIndex] = append(byIndex[request.RequestIndex], request)
	}
	if len(byIndex[7]) != 2 || byIndex[7][0].TimestampMS != 1_700_000_020_000 || byIndex[7][1].TimestampMS != 1_700_000_010_000 {
		t.Fatalf("Alice requests included Bob/another Workspace or lost legacy Alice request: %#v", byIndex[7])
	}
	if len(byIndex[8]) != 1 || byIndex[8][0].TimestampMS != 1_700_000_030_000 {
		t.Fatalf("Bob requests = %#v", byIndex[8])
	}
	if len(byIndex[9]) != 4 || byIndex[9][0].TimestampMS != 1_700_000_040_000 {
		t.Fatalf("workspace-only Codex target did not use credential fallback: %#v", byIndex[9])
	}
	if _, ok := byIndex[10]; ok {
		t.Fatalf("conflicting Codex target returned requests: %#v", byIndex[10])
	}
}

func TestRecentAccountRequestsStopsAtLimitInsteadOfScanningOlderRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}
	repo := New(db)
	ready, err := repo.(*repository).latestRequestIndexesReady(ctx)
	if err != nil {
		t.Fatalf("inspect indexes: %v", err)
	}
	if !ready {
		t.Fatal("latest-request indexes were not created for the indexed Top-N path")
	}
	baseMS := int64(1_700_100_000_000)

	events := make([]usage.Event, 0, 40)
	for i := 0; i < 40; i++ {
		events = append(events, latestAccountRequestEvent(
			fmt.Sprintf("old-%d", i),
			baseMS+int64(i),
			"hot.json",
			"idx-1",
			"hot.json",
		))
	}
	latest := latestAccountRequestEvent("hot-latest", baseMS+1000, "hot.json", "idx-1", "hot.json")
	previous := latestAccountRequestEvent("hot-previous", baseMS+900, "hot.json", "idx-1", "hot.json")
	events = append(events, previous, latest)
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	requests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 7, AuthFileSnapshot: "hot.json", AuthIndex: "idx-1"},
	}, 2)
	if err != nil {
		t.Fatalf("recent account requests: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].TimestampMS != latest.TimestampMS || requests[1].TimestampMS != previous.TimestampMS {
		t.Fatalf("limit order = %#v", requests)
	}
	for _, request := range requests {
		if request.RequestIndex != 7 {
			t.Fatalf("request index = %#v", request)
		}
	}
}

func TestRecentAccountRequestsUsesSourceIndexForLegacyFallback(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}

	predicate := legacyLatestRequestPredicates("credential-a.json", "auth-a")[0]
	args := append(append([]any{}, predicate.args...), 10)
	rows, err := db.Query(`explain query plan select
		e.id,
		e.timestamp_ms,
		e.failed,
		e.fail_status_code,
		coalesce(e.fail_summary, ''),
		coalesce(e.header_error_kind, ''),
		coalesce(e.header_error_code, ''),
		coalesce(e.header_trace_id, '')
	from usage_events e
	where `+predicate.sql+`
	order by e.timestamp_ms desc, e.id desc
	limit ?`, args...)
	if err != nil {
		t.Fatalf("explain legacy latest-request query: %v", err)
	}
	defer rows.Close()

	usesSourceIndex := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		usesSourceIndex = usesSourceIndex || strings.Contains(detail, latestRequestSourceIndex)
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesSourceIndex || fullUsageScan {
		t.Fatalf("legacy latest-request query did not use the source index: sourceIndex=%t scan=%t", usesSourceIndex, fullUsageScan)
	}
}

func latestAccountRequestEvent(
	hash string,
	timestampMS int64,
	authFileSnapshot string,
	authIndex string,
	source string,
) usage.Event {
	return usage.Event{
		EventHash:        hash,
		TimestampMS:      timestampMS,
		Timestamp:        time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:            "gpt-test",
		Endpoint:         "POST /v1/responses",
		Method:           "POST",
		Path:             "/v1/responses",
		AuthIndex:        authIndex,
		Source:           source,
		AuthFileSnapshot: authFileSnapshot,
		InputTokens:      1,
		OutputTokens:     2,
		TotalTokens:      3,
		CreatedAtMS:      timestampMS,
	}
}

func TestRecentAccountRequestsIndexedMatchesBatchedFallback(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, withIndexes bool) Repository {
		t.Helper()
		db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if withIndexes {
			if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
				t.Fatalf("prepare latest-request indexes: %v", err)
			}
		}
		repo := New(db)
		legacy := latestAccountRequestEvent("legacy", 1_700_000_003_000, "", "legacy.json", "legacy.json")
		latest := latestAccountRequestEvent("latest", 1_700_000_002_000, "credential-a.json", "auth-a", "source-a")
		current := latestAccountRequestEvent("current", 1_700_000_001_000, "credential-a.json", "auth-a", "source-a")
		if _, err := repo.InsertBatch(ctx, []usage.Event{legacy, latest, current}); err != nil {
			t.Fatalf("insert events: %v", err)
		}
		ready, err := repo.(*repository).latestRequestIndexesReady(ctx)
		if err != nil {
			t.Fatalf("inspect indexes: %v", err)
		}
		if ready != withIndexes {
			t.Fatalf("latestRequestIndexesReady = %t, want %t", ready, withIndexes)
		}
		return repo
	}

	indexed, err := seed(t, true).RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "credential-a.json", AuthIndex: "auth-a"},
		{RequestIndex: 1, AuthFileSnapshot: "legacy.json", AuthIndex: "legacy.json"},
	}, 2)
	if err != nil {
		t.Fatalf("indexed recent account requests: %v", err)
	}
	batched, err := seed(t, false).RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "credential-a.json", AuthIndex: "auth-a"},
		{RequestIndex: 1, AuthFileSnapshot: "legacy.json", AuthIndex: "legacy.json"},
	}, 2)
	if err != nil {
		t.Fatalf("batched recent account requests: %v", err)
	}
	if !sameLatestAccountRequests(indexed, batched) {
		t.Fatalf("indexed = %#v batched = %#v", indexed, batched)
	}
}

func TestSnapshotLatestRequestQueryUsesAuthFileAndAuthIndexIndex(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}

	rows, err := db.Query(`explain query plan `+snapshotLatestRequestByFileAndIndexSQL, "credential-a.json", "auth-a", 10)
	if err != nil {
		t.Fatalf("explain snapshot latest-request query: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 8)
	usesCompositeIndex := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
		usesCompositeIndex = usesCompositeIndex ||
			strings.Contains(detail, "INDEX "+latestRequestAuthFileIndex) &&
				strings.Contains(detail, "auth_file_snapshot=?") &&
				strings.Contains(detail, "auth_index=?")
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesCompositeIndex || fullUsageScan {
		t.Fatalf("snapshot latest-request query did not use the composite auth-file index: %v", details)
	}
}

func TestSnapshotLatestRequestEmptyAuthIndexUsesCompositeIndex(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}

	rows, err := db.Query(`explain query plan `+snapshotLatestRequestByFileAndEmptyIndexSQL, "file-a.json", 10)
	if err != nil {
		t.Fatalf("explain empty auth_index query: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 8)
	usesCompositeIndex := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
		usesCompositeIndex = usesCompositeIndex ||
			strings.Contains(detail, "INDEX "+latestRequestAuthFileIndex) &&
				strings.Contains(detail, "auth_file_snapshot=?") &&
				strings.Contains(detail, "auth_index=?")
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesCompositeIndex || fullUsageScan {
		t.Fatalf("empty auth_index query did not use the composite auth-file index: %v", details)
	}
}

func TestRecentAccountRequestsMatchesNullAndEmptyAuthIndex(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}
	repo := New(db)
	ctx := context.Background()
	baseMS := int64(1_700_200_000_000)

	nonEmpty := latestAccountRequestEvent("non-empty", baseMS+3, "file-a.json", "idx-1", "file-a.json")
	if _, err := repo.InsertBatch(ctx, []usage.Event{nonEmpty}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, auth_file_snapshot, auth_index, source, created_at_ms
	) values (?, ?, ?, ?, ?, null, ?, ?)`,
		"null-index",
		baseMS+1,
		time.UnixMilli(baseMS+1).UTC().Format(time.RFC3339Nano),
		"gpt-test",
		"file-a.json",
		"file-a.json",
		baseMS+1,
	); err != nil {
		t.Fatalf("insert null auth_index: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, auth_file_snapshot, auth_index, source, created_at_ms
	) values (?, ?, ?, ?, ?, '', ?, ?)`,
		"empty-index",
		baseMS+2,
		time.UnixMilli(baseMS+2).UTC().Format(time.RFC3339Nano),
		"gpt-test",
		"file-a.json",
		"file-a.json",
		baseMS+2,
	); err != nil {
		t.Fatalf("insert empty auth_index: %v", err)
	}
	var storedNull, storedEmpty int
	if err := db.QueryRow(`select
		sum(case when auth_index is null then 1 else 0 end),
		sum(case when auth_index = '' then 1 else 0 end)
	from usage_events where auth_file_snapshot = 'file-a.json'`).Scan(&storedNull, &storedEmpty); err != nil {
		t.Fatalf("inspect stored auth_index values: %v", err)
	}
	if storedNull != 1 || storedEmpty != 1 {
		t.Fatalf("stored auth_index null=%d empty=%d, want 1 and 1", storedNull, storedEmpty)
	}

	emptyRequests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "file-a.json", AuthIndex: ""},
	}, 10)
	if err != nil {
		t.Fatalf("empty auth_index query: %v", err)
	}
	if len(emptyRequests) != 2 {
		t.Fatalf("empty auth_index requests = %#v", emptyRequests)
	}
	if emptyRequests[0].TimestampMS != baseMS+2 || emptyRequests[1].TimestampMS != baseMS+1 {
		t.Fatalf("empty auth_index order = %#v", emptyRequests)
	}

	indexedRequests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 1, AuthFileSnapshot: "file-a.json", AuthIndex: "idx-1"},
	}, 10)
	if err != nil {
		t.Fatalf("non-empty auth_index query: %v", err)
	}
	if len(indexedRequests) != 1 || indexedRequests[0].TimestampMS != nonEmpty.TimestampMS {
		t.Fatalf("non-empty auth_index requests = %#v", indexedRequests)
	}
}

func TestRecentAccountRequestsMergesNewerLegacyWithoutSnapshot(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare latest-request indexes: %v", err)
	}
	repo := New(db)
	ctx := context.Background()
	baseMS := int64(1_700_300_000_000)

	snapshot := latestAccountRequestEvent("snap", baseMS+1_000, "cred.json", "idx-1", "other-source")
	legacyNewer := latestAccountRequestEvent("legacy-newer", baseMS+2_000, "", "idx-1", "cred.json")
	if _, err := repo.InsertBatch(ctx, []usage.Event{snapshot, legacyNewer}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	requests, err := repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 4, AuthFileSnapshot: "cred.json", AuthIndex: "idx-1"},
	}, 1)
	if err != nil {
		t.Fatalf("recent account requests: %v", err)
	}
	if len(requests) != 1 || requests[0].TimestampMS != legacyNewer.TimestampMS {
		t.Fatalf("global top-n = %#v", requests)
	}
}

func TestRecentAccountRequestsRejectsInvalidCodexWorkspaceTargets(t *testing.T) {
	for _, withIndexes := range []bool{true, false} {
		withIndexes := withIndexes
		t.Run(map[bool]string{true: "indexed", false: "batched"}[withIndexes], func(t *testing.T) {
			ctx := context.Background()
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if withIndexes {
				if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
					t.Fatalf("prepare latest-request indexes: %v", err)
				}
			}

			event := latestAccountRequestEvent("codex-workspace", 1_700_000_050_000, "shared.json", "auth-shared", "shared.json")
			event.Provider = "codex"
			event.AuthProviderSnapshot = "codex"
			event.AuthAccountIDSnapshot = "workspace-1"
			event.AccountSnapshot = "alice@example.com"
			if _, err := New(db).InsertBatch(ctx, []usage.Event{event}); err != nil {
				t.Fatalf("insert Codex request: %v", err)
			}

			for _, workspace := range []string{"workspace-1\t", "workspace-1\u00a0", "workspace-\x01-1"} {
				requests, err := New(db).RecentAccountRequests(ctx, []LatestAccountRequestQuery{{
					RequestIndex:          1,
					AuthFileSnapshot:      "shared.json",
					AuthIndex:             "auth-shared",
					Provider:              "codex",
					AuthAccountIDSnapshot: workspace,
					AccountSnapshot:       "alice@example.com",
				}}, 10)
				if err != nil {
					t.Fatalf("recent requests for invalid workspace %q: %v", workspace, err)
				}
				if len(requests) != 0 {
					t.Fatalf("invalid Codex workspace %q returned requests: %#v", workspace, requests)
				}
			}
		})
	}
}

func TestRecentAccountRequestsRejectsConflictingCodexWorkspaceMarker(t *testing.T) {
	for _, withIndexes := range []bool{true, false} {
		withIndexes := withIndexes
		t.Run(map[bool]string{true: "indexed", false: "batched"}[withIndexes], func(t *testing.T) {
			ctx := context.Background()
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if withIndexes {
				if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
					t.Fatalf("prepare latest-request indexes: %v", err)
				}
			}

			valid := latestAccountRequestEvent("codex-project", 1_700_000_050_000, "shared.json", "auth-shared", "shared.json")
			valid.Provider = "codex"
			valid.AuthProviderSnapshot = "codex"
			valid.AuthAccountIDSnapshot = "workspace-1"
			valid.AuthProjectIDSnapshot = "project-1"
			valid.AccountSnapshot = "alice@example.com"
			conflicting := latestAccountRequestEvent("codex-conflicting-marker", 1_700_000_060_000, "shared.json", "auth-shared", "shared.json")
			conflicting.Provider = "codex"
			conflicting.AuthProviderSnapshot = "codex"
			conflicting.AuthAccountIDSnapshot = "workspace-1"
			conflicting.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-2")
			conflicting.AccountSnapshot = "alice@example.com"
			if _, err := New(db).InsertBatch(ctx, []usage.Event{valid, conflicting}); err != nil {
				t.Fatalf("insert Codex requests: %v", err)
			}

			requests, err := New(db).RecentAccountRequests(ctx, []LatestAccountRequestQuery{{
				RequestIndex:          1,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AccountSnapshot:       "alice@example.com",
			}}, 10)
			if err != nil {
				t.Fatalf("recent requests: %v", err)
			}
			if len(requests) != 1 || requests[0].TimestampMS != valid.TimestampMS {
				t.Fatalf("requests = %#v, want only ordinary-project event at %d", requests, valid.TimestampMS)
			}
		})
	}
}

func TestRecentAccountRequestsRejectsConflictingCodexTargetMarker(t *testing.T) {
	for _, withIndexes := range []bool{true, false} {
		withIndexes := withIndexes
		t.Run(map[bool]string{true: "indexed", false: "batched"}[withIndexes], func(t *testing.T) {
			ctx := context.Background()
			db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if withIndexes {
				if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
					t.Fatalf("prepare latest-request indexes: %v", err)
				}
			}

			event := latestAccountRequestEvent("codex-target-marker", 1_700_000_070_000, "shared.json", "auth-shared", "shared.json")
			event.Provider = "codex"
			event.AuthProviderSnapshot = "codex"
			event.AuthAccountIDSnapshot = "workspace-1"
			event.AccountSnapshot = "alice@example.com"
			if _, err := New(db).InsertBatch(ctx, []usage.Event{event}); err != nil {
				t.Fatalf("insert Codex request: %v", err)
			}

			requests, err := New(db).RecentAccountRequests(ctx, []LatestAccountRequestQuery{{
				RequestIndex:          1,
				AuthFileSnapshot:      "shared.json",
				AuthIndex:             "auth-shared",
				Provider:              "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AuthProjectIDSnapshot: usageidentity.CodexAccountIDSnapshot("workspace-2"),
				AccountSnapshot:       "alice@example.com",
			}}, 10)
			if err != nil {
				t.Fatalf("recent requests for conflicting target: %v", err)
			}
			if len(requests) != 0 {
				t.Fatalf("conflicting Codex target marker returned requests: %#v", requests)
			}
		})
	}
}

func sameLatestAccountRequests(left, right []LatestAccountRequest) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.RequestIndex != b.RequestIndex || a.TimestampMS != b.TimestampMS || a.Failed != b.Failed ||
			a.FailSummary != b.FailSummary || a.HeaderErrorKind != b.HeaderErrorKind ||
			a.HeaderErrorCode != b.HeaderErrorCode || a.HeaderTraceID != b.HeaderTraceID {
			return false
		}
		if a.FailStatusCode.Valid != b.FailStatusCode.Valid || a.FailStatusCode.Int64 != b.FailStatusCode.Int64 {
			return false
		}
	}
	return true
}

func BenchmarkRecentAccountRequests200Targets(b *testing.B) {
	db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open database: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
		b.Fatalf("prepare latest-request indexes: %v", err)
	}
	repo := New(db)
	events := make([]usage.Event, 0, 200*5)
	targets := make([]LatestAccountRequestQuery, 0, 200)
	baseMS := int64(1_700_400_000_000)
	for i := 0; i < 200; i++ {
		file := fmt.Sprintf("cred-%03d.json", i)
		index := fmt.Sprintf("idx-%03d", i)
		for n := 0; n < 5; n++ {
			events = append(events, latestAccountRequestEvent(
				fmt.Sprintf("e-%d-%d", i, n),
				baseMS+int64(i*10+n),
				file,
				index,
				file,
			))
		}
		targets = append(targets, LatestAccountRequestQuery{RequestIndex: i, AuthFileSnapshot: file, AuthIndex: index})
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		b.Fatalf("insert events: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.RecentAccountRequests(ctx, targets, 10); err != nil {
			b.Fatalf("recent account requests: %v", err)
		}
	}
}

func BenchmarkRecentAccountRequestsDenseCredential(b *testing.B) {
	db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open database: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		b.Fatalf("prepare latest-request indexes: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1
		union all
		select id + 1 from ids where id < 100000
	) insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		auth_index, source, auth_file_snapshot, created_at_ms
	) select
		printf('dense-account-%06d', id),
		1800000000000 + id,
		'2027-01-15T08:00:00Z',
		'gpt-test',
		'auth-a',
		'credential-a.json',
		'credential-a.json',
		1800000000000 + id
	from ids`); err != nil {
		b.Fatalf("seed dense credential history: %v", err)
	}
	repo := New(db)
	targets := []LatestAccountRequestQuery{{
		RequestIndex:     0,
		AuthFileSnapshot: "credential-a.json",
		AuthIndex:        "auth-a",
	}}

	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		requests, err := repo.RecentAccountRequests(context.Background(), targets, 10)
		if err != nil {
			b.Fatalf("recent account requests: %v", err)
		}
		if len(requests) != 10 {
			b.Fatalf("recent account requests = %d, want 10", len(requests))
		}
	}
}
