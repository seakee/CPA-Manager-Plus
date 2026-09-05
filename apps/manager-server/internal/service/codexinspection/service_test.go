package codexinspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	codexinspectionrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

const xaiCompletedInferenceAPICallResponse = `{"status_code":200,"body":{"object":"response","status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}}`

type synchronizedStringList struct {
	mu     sync.Mutex
	values []string
}

func (s *synchronizedStringList) append(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, value)
}

func (s *synchronizedStringList) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.values...)
}

type synchronizedCounter struct {
	mu    sync.Mutex
	value int
}

func (c *synchronizedCounter) increment() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value++
}

func (c *synchronizedCounter) load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

type failAfterInsertCodexInspectionRepository struct {
	codexinspectionrepo.Repository
	failAfter         int
	successfulInserts int
}

func (r *failAfterInsertCodexInspectionRepository) InsertResult(
	ctx context.Context,
	result model.CodexInspectionResult,
) (model.CodexInspectionResult, error) {
	if r.successfulInserts >= r.failAfter {
		return model.CodexInspectionResult{}, errors.New("forced result write failure")
	}
	inserted, err := r.Repository.InsertResult(ctx, result)
	if err == nil {
		r.successfulInserts++
	}
	return inserted, err
}

type failFirstInsertCodexInspectionRepository struct {
	codexinspectionrepo.Repository
	failed bool
}

func (r *failFirstInsertCodexInspectionRepository) InsertResult(
	ctx context.Context,
	result model.CodexInspectionResult,
) (model.CodexInspectionResult, error) {
	if !r.failed {
		r.failed = true
		return model.CodexInspectionResult{}, errors.New("forced live result write failure")
	}
	return r.Repository.InsertResult(ctx, result)
}

type failDisableOwnershipUpsertRepository struct {
	codexinspectionrepo.Repository
	cancel context.CancelFunc
}

func (r *failDisableOwnershipUpsertRepository) UpsertDisableOwnership(
	context.Context,
	model.CodexInspectionDisableOwnership,
) error {
	if r.cancel != nil {
		r.cancel()
	}
	return errors.New("forced ownership write failure")
}

type failDisableOwnershipBatchRepository struct {
	codexinspectionrepo.Repository
	cancel context.CancelFunc
}

type failFirstQuotaSnapshotWriter struct {
	delegate quotaSnapshotWriter
	failed   bool
	calls    int
}

func (w *failFirstQuotaSnapshotWriter) WriteCodexInspectionResult(
	ctx context.Context,
	result model.CodexInspectionResult,
) error {
	w.calls++
	if !w.failed {
		w.failed = true
		return errors.New("forced quota snapshot write failure")
	}
	return w.delegate.WriteCodexInspectionResult(ctx, result)
}

func (r *failDisableOwnershipBatchRepository) UpsertDisableOwnerships(
	context.Context,
	[]model.CodexInspectionDisableOwnership,
) error {
	if r.cancel != nil {
		r.cancel()
	}
	return errors.New("forced grouped ownership write failure")
}

func requireInspectionLog(t *testing.T, logs []model.CodexInspectionLog, message string) model.CodexInspectionLog {
	t.Helper()
	for _, entry := range logs {
		if entry.Message == message {
			return entry
		}
	}
	t.Fatalf("inspection log %q not found in %#v", message, logs)
	return model.CodexInspectionLog{}
}

func TestPersistInspectionResultsKeepsOneQuotaObservationStableAcrossLifecyclePhases(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	svc := New(db, nil)
	quotaService := quotasnapshotsvc.New(db)
	statusOK := http.StatusOK
	duration := float64(5 * 60 * 60)
	usedPercent := 20.0
	observedAtMS := time.Now().Add(-time.Minute).UnixMilli()
	initial := model.CodexInspectionResult{
		ID:              1,
		RunID:           1,
		AccountKey:      "codex.json::-::auth-1",
		FileName:        "codex.json",
		DisplayAccount:  "alice@example.com",
		AccountSnapshot: "alice@example.com",
		AuthIndex:       "auth-1",
		Provider:        "codex",
		Action:          "keep",
		StatusCode:      &statusOK,
		CreatedAtMS:     observedAtMS,
		QuotaWindows: []model.CodexInspectionQuotaWindow{{
			ID:                 "five-hour",
			UsedPercent:        &usedPercent,
			ResetAtMS:          observedAtMS + int64(duration)*1000,
			ResetAccuracy:      "exact",
			LimitWindowSeconds: &duration,
		}},
		QuotaInventoryObserved: true,
	}
	if err := quotaService.WriteCodexInspectionResult(context.Background(), initial); err != nil {
		t.Fatalf("seed active quota window: %v", err)
	}
	run, err := db.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType: "manual",
		Status:      model.CodexInspectionStatusCompleted,
		StartedAtMS: observedAtMS + 1,
		Settings:    model.DefaultCodexInspectionConfig(),
	})
	if err != nil {
		t.Fatalf("create inspection run: %v", err)
	}
	results := []model.CodexInspectionResult{{
		AccountKey:             initial.AccountKey,
		FileName:               initial.FileName,
		DisplayAccount:         initial.DisplayAccount,
		AccountSnapshot:        initial.AccountSnapshot,
		AuthIndex:              initial.AuthIndex,
		Provider:               "codex",
		Action:                 "keep",
		StatusCode:             &statusOK,
		QuotaInventoryObserved: true,
	}}

	if failures := svc.persistInspectionResults(context.Background(), run.ID, results, runLogger{}); failures != 0 {
		t.Fatalf("first persistence failures = %d", failures)
	}
	firstID, firstCreatedAtMS := results[0].ID, results[0].CreatedAtMS
	if firstID <= 0 || firstCreatedAtMS <= 0 {
		t.Fatalf("stored identity was not copied back: %#v", results[0])
	}
	time.Sleep(2 * time.Millisecond)
	if failures := svc.persistInspectionResults(context.Background(), run.ID, results, runLogger{}); failures != 0 {
		t.Fatalf("second persistence failures = %d", failures)
	}
	if results[0].ID != firstID || results[0].CreatedAtMS != firstCreatedAtMS {
		t.Fatalf("stored observation identity changed: first=(%d,%d) second=(%d,%d)", firstID, firstCreatedAtMS, results[0].ID, results[0].CreatedAtMS)
	}

	query, err := quotaService.Query(context.Background(), quotasnapshotsvc.QueryRequest{
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey:   "row-1",
			Provider: "codex",
			Account: quotasnapshotsvc.AccountTarget{
				AuthFileSnapshot:     initial.FileName,
				AuthProviderSnapshot: "codex",
				AuthIndex:            initial.AuthIndex,
				AccountSnapshot:      initial.AccountSnapshot,
			},
		}},
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("query quota lifecycle: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 1 || query.Items[0].Windows[0].Availability != "pending_absent" {
		t.Fatalf("one empty inspection observation retired quota window: %#v", query)
	}
}

func TestPersistInspectionResultsCountsQuotaSnapshotFailuresForRetry(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	svc := New(db, nil)
	delegate := svc.quotaSnapshots
	failing := &failFirstQuotaSnapshotWriter{delegate: delegate}
	svc.quotaSnapshots = failing
	statusOK := http.StatusOK
	duration := float64(5 * 60 * 60)
	usedPercent := 35.0
	run, err := db.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType: "manual",
		Status:      model.CodexInspectionStatusCompleted,
		StartedAtMS: time.Now().UnixMilli(),
		Settings:    model.DefaultCodexInspectionConfig(),
	})
	if err != nil {
		t.Fatalf("create inspection run: %v", err)
	}
	results := []model.CodexInspectionResult{{
		AccountKey:      "codex.json::-::auth-1",
		FileName:        "codex.json",
		DisplayAccount:  "alice@example.com",
		AccountSnapshot: "alice@example.com",
		AuthIndex:       "auth-1",
		Provider:        "codex",
		Action:          "keep",
		StatusCode:      &statusOK,
		QuotaWindows: []model.CodexInspectionQuotaWindow{{
			ID:                 "five-hour",
			UsedPercent:        &usedPercent,
			ResetAtMS:          time.Now().Add(5 * time.Hour).UnixMilli(),
			ResetAccuracy:      "exact",
			LimitWindowSeconds: &duration,
		}},
		QuotaInventoryObserved: true,
	}}

	if failures := svc.persistInspectionResults(context.Background(), run.ID, results, runLogger{}); failures != 1 {
		t.Fatalf("first persistence failures = %d, want 1", failures)
	}
	if results[0].ID <= 0 || results[0].CreatedAtMS <= 0 {
		t.Fatalf("result identity was not retained after snapshot failure: %#v", results[0])
	}
	if failures := svc.persistInspectionResults(context.Background(), run.ID, results, runLogger{}); failures != 0 {
		t.Fatalf("retry persistence failures = %d", failures)
	}
	if failing.calls != 2 {
		t.Fatalf("quota snapshot calls = %d, want 2", failing.calls)
	}
}

func requireInspectionLogDetail(t *testing.T, entry model.CodexInspectionLog) map[string]any {
	t.Helper()
	detail, ok := entry.Detail.(map[string]any)
	if !ok {
		t.Fatalf("inspection log detail = %#v, want object", entry.Detail)
	}
	return detail
}

func TestToAccountBuildsStableDistinctFallbackKeys(t *testing.T) {
	first := toAccount(authFile{
		"name":     "shared.json",
		"provider": "codex",
		"account":  "first@example.com",
	})
	second := toAccount(authFile{
		"name":     "shared.json",
		"provider": "codex",
		"account":  "second@example.com",
	})
	if first.Key == second.Key {
		t.Fatalf("same-name fallback keys collided: %q", first.Key)
	}
	refreshedFirst := toAccount(authFile{
		"name":     "shared.json",
		"provider": "codex",
		"account":  "first@example.com",
		"disabled": true,
	})
	if refreshedFirst.Key != first.Key {
		t.Fatalf("refreshed fallback key = %q, want %q", refreshedFirst.Key, first.Key)
	}

	oldLabel := toAccount(authFile{
		"name":       "shared.json",
		"provider":   "codex",
		"account":    "old-label@example.com",
		"account_id": "account-1",
	})
	newLabel := toAccount(authFile{
		"name":       "shared.json",
		"provider":   "codex",
		"account":    "new-label@example.com",
		"account_id": "account-1",
	})
	if oldLabel.Key == newLabel.Key {
		t.Fatalf("same Codex Workspace merged different members: old=%q new=%q", oldLabel.Key, newLabel.Key)
	}
	if inspectionActionIdentityKey(resultFromAccount(first)) == inspectionActionIdentityKey(resultFromAccount(second)) {
		t.Fatal("same-name account snapshots shared an action identity")
	}
	labelOnly := toAccount(authFile{
		"id":       "runtime-label-only",
		"name":     "shared.json",
		"provider": "codex",
		"label":    "Friendly account",
	})
	if labelOnly.DisplayAccount != "Friendly account" || labelOnly.AccountSnapshot != "" {
		t.Fatalf("label-only account = %#v, want display-only label", labelOnly)
	}
	if hasInspectionActionIdentity(resultFromAccount(labelOnly)) {
		t.Fatalf("label-only account unexpectedly has an actionable identity: %#v", labelOnly)
	}
	renamedLabel := toAccount(authFile{
		"id":       "runtime-label-only",
		"name":     "shared.json",
		"provider": "codex",
		"label":    "Renamed account",
	})
	if renamedLabel.Key != labelOnly.Key {
		t.Fatalf("label-only runtime key changed with display label: old=%q new=%q", labelOnly.Key, renamedLabel.Key)
	}
}

func TestInspectionIdentityRejectsInvalidCodexAccountEvidence(t *testing.T) {
	candidate := toAccount(authFile{
		"id":         "runtime-conflict",
		"name":       "shared.json",
		"provider":   "codex",
		"auth_index": "auth-1",
		"account_id": "workspace-a",
		"account":    "alice@example.com",
		"metadata": map[string]any{
			"id_token": map[string]any{"account_id": "workspace-b"},
		},
	})
	result := model.CodexInspectionResult{
		FileName:        "shared.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-a",
		AccountSnapshot: "alice@example.com",
	}
	if inspectionResultMatchesCurrentAccount(result, candidate) {
		t.Fatalf("invalid Codex account evidence matched mutation target: %#v", candidate)
	}
}

func TestToAccountRejectsConflictingExplicitCodexMemberEvidence(t *testing.T) {
	candidate := toAccount(authFile{
		"id":               "runtime-member-conflict",
		"name":             "shared.json",
		"provider":         "codex",
		"auth_index":       "auth-1",
		"account_id":       "workspace-a",
		"account_snapshot": "bob@example.com",
		"accountSnapshot":  "alice@example.com",
		"account":          "alice@example.com",
	})
	if candidate.AccountSnapshot != "" || !candidate.AccountSnapshotInvalid {
		t.Fatalf("conflicting member evidence = %#v, want invalid and no member", candidate)
	}
	result := model.CodexInspectionResult{
		FileName:        "shared.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-a",
		AccountSnapshot: "alice@example.com",
	}
	if inspectionResultMatchesCurrentAccount(result, candidate) {
		t.Fatal("conflicting Codex member evidence matched a mutation target")
	}
}

func TestToAccountRejectsStrongCodexMemberSnapshotConflictingWithDisplayEmail(t *testing.T) {
	for _, field := range []string{"account", "email"} {
		t.Run(field, func(t *testing.T) {
			candidate := toAccount(authFile{
				"id":               "runtime-member-display-conflict",
				"name":             "shared.json",
				"provider":         "codex",
				"auth_index":       "auth-1",
				"account_id":       "workspace-a",
				"account_snapshot": "alice@example.com",
				field:              "bob@example.com",
			})
			if candidate.AccountSnapshot != "" || !candidate.AccountSnapshotInvalid {
				t.Fatalf("conflicting member evidence = %#v, want invalid and no member", candidate)
			}
		})
	}
}

func TestRunMarksOnlyRecognizedEmptyCodexQuotaInventory(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantMarker string
	}{
		{
			name:       "recognized empty inventory",
			body:       `{"status_code":200,"body":{"rate_limit":{}}}`,
			wantMarker: "[]",
		},
		{
			name:       "recognized empty code review inventory",
			body:       `{"status_code":200,"body":{"code_review_rate_limit":{}}}`,
			wantMarker: "[]",
		},
		{
			name:       "recognized empty additional inventory",
			body:       `{"status_code":200,"body":{"additional_rate_limits":[]}}`,
			wantMarker: "[]",
		},
		{
			name: "successful response without quota inventory",
			body: `{"status_code":200,"body":{"status":"ok"}}`,
		},
		{
			name: "malformed quota inventory",
			body: `{"status_code":200,"body":{"rate_limit":"schema-changed"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					_, _ = w.Write([]byte(tt.body))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			detail, err := newCodexInspectionTestService(t, db).Run(
				context.Background(),
				RunRequest{TriggerType: "manual"},
			)
			if err != nil {
				t.Fatalf("run inspection: %v", err)
			}
			if len(detail.Results) != 1 {
				t.Fatalf("inspection results = %#v", detail.Results)
			}
			result := detail.Results[0]
			if len(result.QuotaWindows) != 0 || result.QuotaWindowsJSON != tt.wantMarker {
				t.Fatalf(
					"quota inventory marker = %q windows=%#v, want %q",
					result.QuotaWindowsJSON,
					result.QuotaWindows,
					tt.wantMarker,
				)
			}
		})
	}
}

func TestBuildCodexInspectionQuotaWindowsKeepsGenericFamiliesDistinct(t *testing.T) {
	genericWindow := func(usedPercent float64) map[string]any {
		return map[string]any{
			"primary_window": map[string]any{
				"used_percent": usedPercent, "limit_window_seconds": 2 * 24 * 60 * 60,
			},
		}
	}
	windows := buildCodexInspectionQuotaWindows(map[string]any{
		"rate_limit":             genericWindow(10),
		"code_review_rate_limit": genericWindow(20),
		"additional_rate_limits": []any{
			map[string]any{"limit_name": "Credits", "rate_limit": genericWindow(30)},
			map[string]any{"limit_name": "Credits", "rate_limit": genericWindow(40)},
		},
	}, "")

	want := []string{
		"window-2d-0",
		"code-review-window-2d-0",
		"credits-0-window-2d-0",
		"credits-1-window-2d-0",
	}
	if len(windows) != len(want) {
		t.Fatalf("generic quota windows = %#v, want %d", windows, len(want))
	}
	seen := make(map[string]bool, len(windows))
	for index, window := range windows {
		if window.ID != want[index] {
			t.Fatalf("generic quota window %d id = %q, want %q", index, window.ID, want[index])
		}
		if seen[window.ID] {
			t.Fatalf("duplicate generic quota window id %q: %#v", window.ID, windows)
		}
		seen[window.ID] = true
	}
}

func TestBuildCodexInspectionQuotaWindowsAssignsCodexScopes(t *testing.T) {
	weekly := func(usedPercent float64) map[string]any {
		return map[string]any{
			"secondary_window": map[string]any{
				"used_percent": usedPercent, "limit_window_seconds": 7 * 24 * 60 * 60,
			},
		}
	}
	windows := buildCodexInspectionQuotaWindows(map[string]any{
		"rate_limit":             weekly(36),
		"code_review_rate_limit": weekly(20),
		"additional_rate_limits": []any{
			map[string]any{
				"limit_name": "Fast coding", "metered_feature": "codex_spark", "rate_limit": weekly(0),
			},
			map[string]any{
				"limit_name": "Future Feature", "metered_feature": "future_feature", "rate_limit": weekly(10),
			},
		},
	}, "")
	byID := make(map[string]model.CodexInspectionQuotaWindow, len(windows))
	for _, window := range windows {
		byID[window.ID] = window
	}
	assertScope := func(id, kind, key string, models []string, complete bool) {
		t.Helper()
		window, ok := byID[id]
		if !ok || window.ModelScope == nil {
			t.Fatalf("quota window %q scope missing: %#v", id, windows)
		}
		if window.ModelScope.Kind != kind || window.ModelScope.Key != key ||
			!reflect.DeepEqual(window.ModelScope.Models, models) || window.ModelScope.Complete != complete {
			t.Fatalf("quota window %q scope = %#v", id, window.ModelScope)
		}
	}
	assertScope("weekly", "family", codexquota.MainScopeKey, nil, true)
	assertScope("spark-weekly-0", "models", "", []string{codexquota.SparkModelID}, true)
	if !containsString(byID["spark-weekly-0"].ProviderWindowAliases, "fast-coding-weekly-0") {
		t.Fatalf("Spark legacy provider aliases = %#v", byID["spark-weekly-0"].ProviderWindowAliases)
	}
	assertScope("code-review-weekly", "feature", codexquota.CodeReviewScopeKey, nil, false)
	assertScope("future-feature-weekly-0", "feature", "future_feature", nil, false)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestBuildCodexInspectionQuotaWindowsKeepsDistinctAdditionalFamiliesStableAcrossReorder(t *testing.T) {
	family := func(name string, usedPercent float64) map[string]any {
		return map[string]any{
			"limit_name": name,
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent": usedPercent, "limit_window_seconds": 18_000,
				},
			},
		}
	}
	idsByUsage := func(items []map[string]any) map[float64]string {
		windows := buildCodexInspectionQuotaWindows(map[string]any{"additional_rate_limits": items}, "")
		result := make(map[float64]string, len(windows))
		for _, window := range windows {
			if window.UsedPercent != nil {
				result[*window.UsedPercent] = window.ID
			}
		}
		return result
	}
	want := map[float64]string{
		30: "credits-five-hour-0",
		40: "review-premium-five-hour-0",
	}
	forward := idsByUsage([]map[string]any{family("Credits", 30), family("Review Premium", 40)})
	reverse := idsByUsage([]map[string]any{family("Review Premium", 40), family("Credits", 30)})
	if !reflect.DeepEqual(forward, want) || !reflect.DeepEqual(reverse, want) {
		t.Fatalf("reordered additional family ids: forward=%#v reverse=%#v want=%#v", forward, reverse, want)
	}
}

func TestBuildCodexInspectionQuotaWindowsUsesMeteredFeatureForDuplicateNames(t *testing.T) {
	family := func(feature string, usedPercent float64) map[string]any {
		return map[string]any{
			"limit_name":      "Credits",
			"metered_feature": feature,
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent": usedPercent, "limit_window_seconds": 18_000,
				},
			},
		}
	}
	idsByUsage := func(items []map[string]any) map[float64]string {
		windows := buildCodexInspectionQuotaWindows(map[string]any{"additional_rate_limits": items}, "")
		result := make(map[float64]string, len(windows))
		for _, window := range windows {
			if window.UsedPercent != nil {
				result[*window.UsedPercent] = window.ID
			}
		}
		return result
	}
	want := map[float64]string{
		30: "credits--chat-completions-five-hour-0",
		40: "credits--code-review-five-hour-0",
		50: "credits-chat-completions-five-hour-0",
	}
	namedCollision := map[string]any{
		"limit_name": "Credits Chat Completions",
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent": 50.0, "limit_window_seconds": 18_000,
			},
		},
	}
	forward := idsByUsage([]map[string]any{family("chat_completions", 30), family("code_review", 40), namedCollision})
	reverse := idsByUsage([]map[string]any{namedCollision, family("code_review", 40), family("chat_completions", 30)})
	if !reflect.DeepEqual(forward, want) || !reflect.DeepEqual(reverse, want) {
		t.Fatalf("duplicate-name additional family ids: forward=%#v reverse=%#v want=%#v", forward, reverse, want)
	}
}

func TestXAIClassificationMatchesSharedFixtures(t *testing.T) {
	type fixtureCase struct {
		Name       string `json:"name"`
		StatusCode int    `json:"statusCode"`
		Body       any    `json:"body"`
		Expected   struct {
			Classification string `json:"classification"`
			Action         string `json:"action"`
			ReasonCode     string `json:"reasonCode"`
		} `json:"expected"`
	}
	data, err := os.ReadFile("../../../../../tests/fixtures/xai-inspection-cases.json")
	if err != nil {
		t.Fatalf("read shared xAI fixtures: %v", err)
	}
	var fixtures []fixtureCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode shared xAI fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			classification := xaiClassification(fixture.StatusCode, fixture.Body)
			decision := xaiDecision(
				fixture.StatusCode,
				classification,
				fmt.Sprint(fixture.Body),
			)
			if decision.Classification != fixture.Expected.Classification || decision.Action != fixture.Expected.Action || decision.ReasonCode != fixture.Expected.ReasonCode {
				t.Fatalf("decision = %#v, want classification=%q action=%q reasonCode=%q", decision, fixture.Expected.Classification, fixture.Expected.Action, fixture.Expected.ReasonCode)
			}
		})
	}
}

func TestRunPersistsLogsResultsAndDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"auth-a.json","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("run status = %q", result.Run.Status)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %#v", result.Results)
	}
	if result.Results[0].RunID != result.Run.ID {
		t.Fatalf("result run id = %d, want %d", result.Results[0].RunID, result.Run.ID)
	}
	if result.Results[0].Action != "delete" {
		t.Fatalf("result action = %q", result.Results[0].Action)
	}
	if result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || result.Results[0].ExecutedAction != "delete" {
		t.Fatalf("result action audit = %#v", result.Results[0])
	}
	if result.Run.DeleteCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 1/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Logs) == 0 {
		t.Fatal("expected persisted logs")
	}
	foundStart := false
	for _, logEntry := range result.Logs {
		if logEntry.Message == "凭证健康巡检开始" {
			foundStart = true
			if logEntry.Detail == nil {
				t.Fatalf("start log detail is nil: %#v", logEntry)
			}
			detail := requireInspectionLogDetail(t, logEntry)
			if detail["triggerKey"] != "manual" {
				t.Fatalf("start log triggerKey = %#v, want manual", detail["triggerKey"])
			}
			break
		}
	}
	if !foundStart {
		t.Fatalf("logs = %#v", result.Logs)
	}
}

func TestRunLogsPostActionResultWriteFailures(t *testing.T) {
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"auth-a.json","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	db.CodexInspections = &failAfterInsertCodexInspectionRepository{
		Repository: db.CodexInspections,
		failAfter:  1,
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if !deleteCalled {
		t.Fatal("automatic delete was not executed")
	}
	if !strings.Contains(result.Run.Error, "1 个巡检结果写入失败") {
		t.Fatalf("run error = %q, want result write failure", result.Run.Error)
	}
	if len(result.Results) != 1 || result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || result.Results[0].ExecutedAction != "delete" {
		t.Fatalf("returned result lost the successful external action after write failure: %#v", result.Results)
	}
	writeFailure := requireInspectionLog(t, result.Logs, "写入巡检账号结果失败")
	writeFailureDetail := requireInspectionLogDetail(t, writeFailure)
	if writeFailure.Level != "error" || writeFailureDetail["displayAccount"] != "alice@example.com" {
		t.Fatalf("result write failure log = level=%q detail=%#v", writeFailure.Level, writeFailureDetail)
	}
	completion := requireInspectionLog(t, result.Logs, "凭证健康巡检完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "warning" || completionDetail["resultWriteFailedCount"] != float64(1) {
		t.Fatalf("completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestRunMarksRecoveredLiveResultWriteForRetry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10},"secondary_window":{"used_percent":20}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	db.CodexInspections = &failFirstInsertCodexInspectionRepository{Repository: db.CodexInspections}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.Error != "" || len(result.Results) != 1 {
		t.Fatalf("recovered live write run=%#v results=%#v", result.Run, result.Results)
	}
	writeFailure := requireInspectionLog(t, result.Logs, "写入巡检账号结果失败")
	writeFailureDetail := requireInspectionLogDetail(t, writeFailure)
	if writeFailureDetail["retryScheduled"] != true || writeFailureDetail["displayAccount"] != "alice@example.com" {
		t.Fatalf("live write failure detail = %#v", writeFailureDetail)
	}
	completion := requireInspectionLog(t, result.Logs, "凭证健康巡检完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "success" || completionDetail["resultWriteFailedCount"] != float64(0) {
		t.Fatalf("recovered completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestRunCompletionLogWarnsWhenAutomaticActionFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			http.Error(w, "delete failed", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.Error == "" || len(result.Results) != 1 || result.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed {
		t.Fatalf("automatic action failure was not persisted: run=%#v results=%#v", result.Run, result.Results)
	}
	completion := requireInspectionLog(t, result.Logs, "凭证健康巡检完成")
	if completion.Level != "warning" {
		t.Fatalf("automatic action completion level = %q, want warning", completion.Level)
	}
	completionDetail := requireInspectionLogDetail(t, completion)
	if completionDetail["actionFailedCount"] != float64(1) ||
		completionDetail["actionSuccessCount"] != float64(0) ||
		completionDetail["actionSkippedCount"] != float64(0) ||
		completionDetail["actionNeedsReviewCount"] != float64(0) {
		t.Fatalf("automatic action completion detail = %#v", completionDetail)
	}
}

func TestRunXAISkipsInferenceWhenDisabled(t *testing.T) {
	var requestedURLs synchronizedStringList
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","auth_kind":"oauth","account":"xai@example.com","user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs.append(payload.URL)
			if strings.HasSuffix(payload.URL, "/responses") {
				t.Fatalf("disabled xAI inference requested %s", payload.URL)
			}
			if strings.Contains(payload.URL, "format=credits") {
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":25,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"monthly_limit":10000,"used":4000,"billing_period_end":"2026-08-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceEnabled = false
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	urls := requestedURLs.snapshot()
	if len(urls) != 2 {
		t.Fatalf("requested URLs = %#v, want weekly and monthly billing only", urls)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "keep" || result.Results[0].ErrorKind != "billing_healthy" {
		t.Fatalf("xAI billing-only result = %#v", result.Results)
	}
	if result.Results[0].StatusCode == nil || *result.Results[0].StatusCode != http.StatusOK {
		t.Fatalf("xAI billing-only status code = %#v, want %d", result.Results[0].StatusCode, http.StatusOK)
	}
	if result.Results[0].AutoRecoverEligible {
		t.Fatalf("billing-only inspection enabled auto recovery: %#v", result.Results[0])
	}
	logEntry := requireInspectionLog(t, result.Logs, "monitoring.xai_inspection_log_server_complete")
	if logEntry.Level != "info" {
		t.Fatalf("xAI billing-only log level = %q, want info", logEntry.Level)
	}
	detail := requireInspectionLogDetail(t, logEntry)
	if detail["inspectionMode"] != "billing" || detail["healthEvidence"] != "billing_healthy" {
		t.Fatalf("xAI billing-only log detail = %#v", detail)
	}
	if detail["inferenceEnabled"] != false {
		t.Fatalf("xAI billing-only inferenceEnabled = %#v, want false", detail["inferenceEnabled"])
	}
	if _, ok := detail["inferenceHealthy"]; ok {
		t.Fatalf("xAI billing-only log reported inference health: %#v", detail)
	}
}

func TestRunXAILogsMissingAuthIndexAsSkippedWarning(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-missing-auth.json","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			t.Fatal("xAI account without auth_index must not be probed")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].ErrorKind != "missing_auth_index" {
		t.Fatalf("xAI missing-auth result = %#v", result.Results)
	}
	logEntry := requireInspectionLog(t, result.Logs, "monitoring.xai_inspection_log_server_missing_auth_index")
	if logEntry.Level != "warning" {
		t.Fatalf("xAI missing-auth log level = %q, want warning", logEntry.Level)
	}
	detail := requireInspectionLogDetail(t, logEntry)
	if detail["inspectionMode"] != "skipped" || detail["healthEvidence"] != "missing_auth_index" {
		t.Fatalf("xAI missing-auth log detail = %#v", detail)
	}
	if detail["billingAvailable"] != false || detail["billingPartial"] != false {
		t.Fatalf("xAI missing-auth billing detail = %#v, want unavailable/non-partial", detail)
	}
	if detail["inferenceEnabled"] != true {
		t.Fatalf("xAI missing-auth inferenceEnabled = %#v, want true", detail["inferenceEnabled"])
	}
	if _, ok := detail["inferenceHealthy"]; ok {
		t.Fatalf("xAI missing-auth log reported inference health: %#v", detail)
	}
}

func TestRunXAIBillingOnlyIgnoresDeprecatedMonthlyBlockingFailure(t *testing.T) {
	var requestedURLs synchronizedStringList
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"xai-auth.json","name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs.append(payload.URL)
			if strings.Contains(payload.URL, "format=credits") {
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":3,"current_period":{"end":"2026-07-29T00:00:00Z"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription."}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceEnabled = false
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	urls := requestedURLs.snapshot()
	if len(urls) != 2 {
		t.Fatalf("requested URLs = %#v, want weekly and monthly billing only", urls)
	}
	if len(result.Results) != 1 {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	item := result.Results[0]
	if item.Action != "keep" || item.ErrorKind != "billing_healthy" || item.StatusCode == nil || *item.StatusCode != http.StatusOK {
		t.Fatalf("xAI unified weekly result = %#v", item)
	}
	if len(item.QuotaWindows) != 1 || item.QuotaWindows[0].ID != "xai-weekly" {
		t.Fatalf("xAI partial blocking quota windows = %#v", item.QuotaWindows)
	}
	logEntry := requireInspectionLog(t, result.Logs, "monitoring.xai_inspection_log_server_complete")
	if logEntry.Level != "info" {
		t.Fatalf("xAI unified weekly log level = %q, want info", logEntry.Level)
	}
	detail := requireInspectionLogDetail(t, logEntry)
	if detail["inspectionMode"] != "billing" || detail["healthEvidence"] != "billing_healthy" || detail["billingPartial"] != false {
		t.Fatalf("xAI unified weekly log detail = %#v", detail)
	}
	if _, ok := detail["inferenceHealthy"]; ok {
		t.Fatalf("xAI blocking billing log reported inference health: %#v", detail)
	}
}

func TestRunXAIAutoDisableDoesNotActOnDeprecatedMonthlyFailure(t *testing.T) {
	patchCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"xai-auth.json","name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			if strings.Contains(payload.URL, "format=credits") {
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":3,"current_period":{"type":"weekly","start":"2026-08-13T00:00:00Z","end":"2026-08-20T00:00:00Z"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"code":"personal-team-blocked:spending-limit"}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceEnabled = false
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if patchCalled {
		t.Fatal("automatic disable acted on a deprecated monthly failure")
	}
	if len(result.Results) != 1 || result.Results[0].Action != "keep" || result.Results[0].ErrorKind != "billing_healthy" {
		t.Fatalf("xAI automatic-action result = %#v", result.Results)
	}
}

func TestResolveXAIBasicInspectionResultClassifiesNonBlockingPartialBilling(t *testing.T) {
	usage := float64(25)
	result := resolveXAIBasicInspectionResult(
		model.CodexInspectionResult{},
		xaiBillingProbe{
			Summary:  &xaiBillingSummary{UsagePercent: &usage, HasWeeklyData: true},
			Failures: []xaiProbeDecision{*xaiDecision(http.StatusServiceUnavailable, "upstream_error", "monthly billing unavailable")},
			Partial:  true,
			Healthy:  true,
		},
	)
	if result.Action != "keep" || result.ErrorKind != "billing_partial" || result.ActionReason != "monitoring.xai_inspection_reason_billing_partial" {
		t.Fatalf("xAI partial billing result = %#v", result)
	}
	if level := xaiInspectionLogLevel(result); level != "warning" {
		t.Fatalf("xAI partial billing log level = %q, want warning", level)
	}
}

func TestResolveXAIBasicInspectionResultKeepsWeeklyBlockingFailureWithLegacySummary(t *testing.T) {
	monthlyLimit := float64(20000)
	monthlyUsed := float64(25)
	result := resolveXAIBasicInspectionResult(
		model.CodexInspectionResult{},
		xaiBillingProbe{
			Summary: &xaiBillingSummary{
				MonthlyLimitCents: &monthlyLimit,
				UsedPercent:       &monthlyUsed,
				HasMonthlyData:    true,
			},
			Failures: []xaiProbeDecision{*xaiDecision(http.StatusPaymentRequired, "spending_limit", "weekly quota exhausted")},
			Partial:  true,
			Healthy:  true,
		},
	)
	if result.Action != "disable" || result.ErrorKind != "spending_limit" || result.StatusCode == nil || *result.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("xAI weekly blocking failure with legacy summary = %#v", result)
	}
}

func TestRunXAIUsesBillingAndInferenceEndpoints(t *testing.T) {
	const customModel = "grok-custom"
	const customPrompt = "Return a short health response."
	const customUserAgent = "xai-custom-agent"
	var requestedURLs synchronizedStringList
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","auth_kind":"oauth","account":"xai@example.com","user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string            `json:"method"`
				URL    string            `json:"url"`
				Header map[string]string `json:"header"`
				Data   string            `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs.append(payload.URL)
			if strings.Contains(payload.URL, "chatgpt.com") {
				t.Fatalf("xAI inspection called Codex endpoint: %s", payload.URL)
			}
			if payload.Header["x-grok-client-version"] != xaiGrokVersion || payload.Header["x-userid"] != "user-1" {
				t.Fatalf("xAI headers = %#v", payload.Header)
			}
			if strings.Contains(payload.URL, "format=credits") {
				if payload.Method != http.MethodGet {
					t.Fatalf("weekly billing method = %q, want GET", payload.Method)
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":25,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
				return
			}
			if strings.HasSuffix(payload.URL, "/responses") {
				if payload.Method != http.MethodPost {
					t.Fatalf("xAI inference method = %q, want POST", payload.Method)
				}
				if payload.Header["Accept"] != "application/json" {
					t.Fatalf("xAI inference accept = %q, want application/json", payload.Header["Accept"])
				}
				if payload.Header["User-Agent"] != customUserAgent {
					t.Fatalf("xAI inference user agent = %q, want %q", payload.Header["User-Agent"], customUserAgent)
				}
				var requestData map[string]any
				if err := json.Unmarshal([]byte(payload.Data), &requestData); err != nil {
					t.Fatalf("decode xAI inference data: %v", err)
				}
				if requestData["model"] != customModel || requestData["stream"] != false {
					t.Fatalf("xAI inference data = %#v", requestData)
				}
				if requestData["input"] != customPrompt {
					t.Fatalf("xAI inference prompt = %#v", requestData["input"])
				}
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
				return
			}
			if payload.Method != http.MethodGet {
				t.Fatalf("monthly billing method = %q, want GET", payload.Method)
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"monthly_limit":10000,"used":4000,"billing_period_end":"2026-08-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceUserAgent = customUserAgent
	managerCfg.CodexInspection.XAIInferenceModel = customModel
	managerCfg.CodexInspection.XAIInferencePrompt = customPrompt
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	urls := requestedURLs.snapshot()
	if len(urls) != 3 || !strings.HasSuffix(urls[2], "/responses") {
		t.Fatalf("requested URLs = %#v, want weekly/monthly billing and inference", urls)
	}
	if len(result.Results) != 1 || result.Results[0].Provider != "xai" || result.Results[0].Action != "keep" {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	if result.Results[0].ErrorKind != "inference_healthy" || len(result.Results[0].QuotaWindows) != 2 {
		t.Fatalf("xAI inference result = %#v", result.Results[0])
	}
	if result.Results[0].PlanType != "" {
		t.Fatalf("xAI plan type = %q, want empty", result.Results[0].PlanType)
	}
	logEntry := requireInspectionLog(t, result.Logs, "monitoring.xai_inspection_log_server_complete")
	if logEntry.Level != "info" {
		t.Fatalf("xAI inference log level = %q, want info", logEntry.Level)
	}
	detail := requireInspectionLogDetail(t, logEntry)
	if detail["inspectionMode"] != "inference" || detail["healthEvidence"] != "inference_healthy" || detail["inferenceHealthy"] != true {
		t.Fatalf("xAI inference log detail = %#v", detail)
	}
	if detail["inferenceEnabled"] != true {
		t.Fatalf("xAI inferenceEnabled = %#v, want true", detail["inferenceEnabled"])
	}
}

func TestResolveXAIInferenceURLMatchesRuntimeUsingAPISemantics(t *testing.T) {
	tests := []struct {
		name          string
		file          authFile
		forceOfficial bool
		wantURL       string
		wantCLI       bool
	}{
		{
			name:          "verified official identity forces official api",
			file:          authFile{},
			forceOfficial: true,
			wantURL:       xaiOfficialAPIBaseURL + "/responses",
			wantCLI:       false,
		},
		{
			name:    "missing auth metadata defaults to cli proxy",
			file:    authFile{},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "missing auth metadata ignores official default base",
			file:    authFile{"base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "oauth defaults to cli proxy",
			file:    authFile{"auth_kind": "oauth", "base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "explicit false defaults to cli proxy without auth kind",
			file:    authFile{"using_api": false, "base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "api credential defaults to official api",
			file:    authFile{"auth_kind": "apikey"},
			wantURL: xaiOfficialAPIBaseURL + "/responses",
			wantCLI: false,
		},
		{
			name:    "custom base url is preserved",
			file:    authFile{"using_api": false, "base_url": "https://xai.example.test/v1"},
			wantURL: "https://xai.example.test/v1/responses",
			wantCLI: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotCLI := resolveXAIInferenceURL(tt.file, tt.forceOfficial)
			if gotURL != tt.wantURL || gotCLI != tt.wantCLI {
				t.Fatalf("resolveXAIInferenceURL() = %q, %t; want %q, %t", gotURL, gotCLI, tt.wantURL, tt.wantCLI)
			}
		})
	}
}

func TestRunCombinedTargetsSamplesEachCredentialProvider(t *testing.T) {
	var requestedInference synchronizedCounter
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"codex.json","auth_index":"codex-1","provider":"codex","account":"codex@example.com"},{"name":"xai-a.json","auth_index":"xai-1","provider":"xai","account":"xai-a@example.com"},{"name":"xai-b.json","auth_index":"xai-2","provider":"xai","account":"xai-b@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			switch {
			case strings.Contains(payload.URL, "chatgpt.com"):
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}}`))
			case strings.HasSuffix(payload.URL, "/responses"):
				requestedInference.increment()
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
			case strings.Contains(payload.URL, "/billing"):
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":20,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
			default:
				t.Fatalf("unexpected provider URL %q", payload.URL)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetTypes = []string{model.CodexInspectionTargetCodex, model.CodexInspectionTargetXAI}
	managerCfg.CodexInspection.TargetType = model.CodexInspectionTargetCodex
	managerCfg.CodexInspection.SampleSize = 1
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run combined credential inspection: %v", err)
	}
	if detail.Run.ProbeSetCount != 3 || detail.Run.SampledCount != 2 || len(detail.Results) != 2 {
		t.Fatalf("combined run = %#v, results=%#v", detail.Run, detail.Results)
	}
	providers := map[string]bool{}
	for _, result := range detail.Results {
		providers[result.Provider] = true
	}
	inferenceCount := requestedInference.load()
	if !providers["codex"] || !providers["xai"] || inferenceCount != 1 {
		t.Fatalf("providers=%#v inference=%d", providers, inferenceCount)
	}
}

func TestRunXAIFallsBackToOfficialAPIIdentityHealth(t *testing.T) {
	var requestedURLs synchronizedStringList
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com","disabled":true,"user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string            `json:"method"`
				URL    string            `json:"url"`
				Header map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs.append(payload.URL)
			if strings.HasSuffix(payload.URL, "/responses") {
				if payload.Method != http.MethodPost {
					t.Fatalf("xAI inference method = %q, want POST", payload.Method)
				}
				if payload.URL != xaiOfficialAPIBaseURL+"/responses" {
					t.Fatalf("xAI inference URL = %q, want official API", payload.URL)
				}
				if payload.Header["x-xai-token-auth"] != "" || payload.Header["x-grok-client-version"] != "" || payload.Header["x-userid"] != "" {
					t.Fatalf("xAI official inference headers = %#v", payload.Header)
				}
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
				return
			}
			if payload.Method != http.MethodGet {
				t.Fatalf("xAI billing health method = %q, want GET", payload.Method)
			}
			if payload.URL == xaiOfficialAPIMeURL {
				if payload.Header["Authorization"] != "Bearer $TOKEN$" || payload.Header["x-grok-client-version"] != "" {
					t.Fatalf("xAI official API headers = %#v", payload.Header)
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"user_id":"user-1","team_id":"team-1","team_blocked":false}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":403,"body":{"error":"Access denied"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	urls := requestedURLs.snapshot()
	if len(urls) != 4 || urls[2] != xaiOfficialAPIMeURL || urls[3] != xaiOfficialAPIBaseURL+"/responses" {
		t.Fatalf("requested URLs = %#v, want billing, identity fallback, and inference", urls)
	}
	if len(result.Results) != 1 {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	item := result.Results[0]
	if item.Action != "keep" || item.ErrorKind != "inference_healthy" || item.StatusCode == nil || *item.StatusCode != http.StatusOK {
		t.Fatalf("xAI inference result = %#v", item)
	}
	if item.ActionReason != "monitoring.xai_inspection_reason_inference_manual_disable" {
		t.Fatalf("xAI inference action reason = %q", item.ActionReason)
	}
	if item.UsedPercent != nil || len(item.QuotaWindows) != 0 || item.AutoRecoverEligible {
		t.Fatalf("xAI official API synthesized quota or recovery = %#v", item)
	}
}

func TestResolveXAIBasicInspectionResultUsesOfficialAPIHealthyKind(t *testing.T) {
	result := resolveXAIBasicInspectionResult(
		model.CodexInspectionResult{},
		xaiBillingProbe{OfficialAPIHealthy: true},
	)
	if result.Action != "keep" || result.ErrorKind != "official_api_healthy" || result.ActionReason != "monitoring.xai_inspection_reason_official_api_healthy" {
		t.Fatalf("official API health result = %#v", result)
	}
}

func TestXAISummaryWindowsSkipsZeroOnDemandCapWithoutUsage(t *testing.T) {
	zero := float64(0)
	windows := xaiSummaryWindows(&xaiBillingSummary{OnDemandCapCents: &zero})
	for _, window := range windows {
		if window.ID == "xai-on-demand" {
			t.Fatalf("zero on-demand cap produced quota window: %#v", windows)
		}
	}
}

func TestXAISummaryWindowsDoesNotCreateMonthlyWindowFromOnDemandOnlyData(t *testing.T) {
	capCents := float64(5000)
	usedPercent := float64(20)
	windows := xaiSummaryWindows(&xaiBillingSummary{
		OnDemandCapCents:    &capCents,
		OnDemandUsedPercent: &usedPercent,
		HasMonthlyData:      true,
		BillingPeriodEnd:    "2026-08-01T00:00:00Z",
	})
	if len(windows) != 1 || windows[0].ID != "xai-on-demand" {
		t.Fatalf("on-demand-only windows = %#v, want on-demand only", windows)
	}
}

func TestParseXAIBillingSummaryDoesNotCreateMonthlyWindowFromWeeklyZeroOnDemandData(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type": "USAGE_PERIOD_TYPE_WEEKLY",
			"end":  "2026-07-29T00:00:00+00:00",
		},
		"onDemandCap":      map[string]any{"val": 0},
		"onDemandUsed":     map[string]any{"val": 0},
		"billingPeriodEnd": "2026-07-29T00:00:00+00:00",
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	windows := xaiSummaryWindows(summary)
	if len(windows) != 1 || windows[0].ID != "xai-weekly" {
		t.Fatalf("weekly zero on-demand windows = %#v, want weekly only", windows)
	}
}

func TestParseXAIBillingSummaryTreatsOmittedWeeklyPercentAsResetZeroOnlyForValidPeriod(t *testing.T) {
	valid := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "USAGE_PERIOD_TYPE_WEEKLY",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
	})
	if valid == nil || valid.UsagePercent == nil || *valid.UsagePercent != 0 {
		t.Fatalf("valid weekly reset summary = %#v, want zero usage", valid)
	}

	incomplete := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type": "USAGE_PERIOD_TYPE_WEEKLY",
			"end":  "2026-08-20T00:00:00Z",
		},
	})
	if incomplete == nil || incomplete.UsagePercent != nil {
		t.Fatalf("incomplete weekly period summary = %#v, want unknown usage", incomplete)
	}
}

func TestParseXAIBillingSummaryTreatsNestedEmptyCentValuesAsPlaceholders(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "USAGE_PERIOD_TYPE_WEEKLY",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
		"onDemandCap": map[string]any{},
		"usage": map[string]any{
			"includedUsed": map[string]any{},
			"onDemandUsed": map[string]any{},
			"totalUsed":    map[string]any{},
		},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})
	if summary == nil || summary.UsagePercent == nil || *summary.UsagePercent != 0 {
		t.Fatalf("nested weekly summary = %#v", summary)
	}
	for name, value := range map[string]*float64{
		"used": summary.UsedCents, "included": summary.IncludedUsedCents,
		"on-demand cap": summary.OnDemandCapCents, "on-demand used": summary.OnDemandUsedCents,
	} {
		if value != nil {
			t.Fatalf("%s cents = %#v, want absent", name, value)
		}
	}
	if summary.MonthlyLimitCents != nil {
		t.Fatalf("nested usage invented monthly limit: %#v", summary.MonthlyLimitCents)
	}
	if windows := xaiSummaryWindows(summary); len(windows) != 1 || windows[0].ID != "xai-weekly" {
		t.Fatalf("nested zero-value windows = %#v, want weekly only", windows)
	}
}

func TestParseXAIBillingSummaryKeepsEmptyLimitAbsentWithRealMonthlyUsage(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthlyLimit":     map[string]any{},
		"used":             map[string]any{"val": 500},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})
	if summary == nil || summary.UsedCents == nil || *summary.UsedCents != 500 || summary.IncludedUsedCents == nil || *summary.IncludedUsedCents != 500 {
		t.Fatalf("mixed monthly summary = %#v", summary)
	}
	if summary.MonthlyLimitCents != nil || summary.UsedPercent != nil || summary.HasMonthlyLimit {
		t.Fatalf("empty monthly limit became evidence: %#v", summary)
	}
	if windows := xaiSummaryWindows(summary); len(windows) != 0 {
		t.Fatalf("limitless monthly usage produced quota window: %#v", windows)
	}
}

func TestParseXAIBillingSummaryFallsThroughAliasPlaceholders(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthlyLimit":  map[string]any{},
		"monthly_limit": map[string]any{"val": 10000},
		"onDemandCap":   map[string]any{"val": "invalid"},
		"on_demand_cap": map[string]any{"val": 5000},
		"used":          map[string]any{"val": 2500},
	})
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"monthly limit": {summary.MonthlyLimitCents, 10000},
		"used":          {summary.UsedCents, 2500},
		"included":      {summary.IncludedUsedCents, 2500},
		"monthly pct":   {summary.UsedPercent, 25},
		"payg cap":      {summary.OnDemandCapCents, 5000},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
}

func TestParseXAIBillingSummaryDerivesIncludedUsagePastEmptyPlaceholder(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthlyLimit": map[string]any{"val": 10000},
		"used":         map[string]any{"val": 12500},
		"onDemandCap":  map[string]any{"val": 5000},
		"usage": map[string]any{
			"includedUsed": map[string]any{},
		},
	})
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"used":           {summary.UsedCents, 12500},
		"included":       {summary.IncludedUsedCents, 10000},
		"monthly pct":    {summary.UsedPercent, 100},
		"on-demand used": {summary.OnDemandUsedCents, 2500},
		"on-demand pct":  {summary.OnDemandUsedPercent, 50},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
	if summary.HasIncludedUsed {
		t.Fatalf("empty included usage became evidence: %#v", summary)
	}
}

func TestMergeXAIBillingSummaryUsesLegacyBillingGroupsOverWeeklyPlaceholders(t *testing.T) {
	weekly := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "USAGE_PERIOD_TYPE_WEEKLY",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
		"onDemandCap": map[string]any{},
		"usage": map[string]any{
			"includedUsed": map[string]any{},
			"onDemandUsed": map[string]any{},
			"totalUsed":    map[string]any{},
		},
	})
	legacy := parseXAIBillingSummary(map[string]any{
		"monthlyLimit":     map[string]any{"val": 10000},
		"used":             map[string]any{"val": 12500},
		"onDemandCap":      map[string]any{"val": 5000},
		"onDemandUsed":     map[string]any{"val": 2500},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})

	merged := mergeXAIBillingSummary(weekly, legacy)
	if merged == nil || merged.UsagePercent == nil || *merged.UsagePercent != 0 {
		t.Fatalf("merged weekly summary = %#v", merged)
	}
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"monthly limit": {merged.MonthlyLimitCents, 10000},
		"used":          {merged.UsedCents, 12500},
		"included":      {merged.IncludedUsedCents, 10000},
		"monthly pct":   {merged.UsedPercent, 100},
		"payg cap":      {merged.OnDemandCapCents, 5000},
		"payg used":     {merged.OnDemandUsedCents, 2500},
		"payg pct":      {merged.OnDemandUsedPercent, 50},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
	if merged.BillingPeriodEnd != "2026-09-01T00:00:00Z" {
		t.Fatalf("billing period end = %q", merged.BillingPeriodEnd)
	}
}

func TestMergeXAIBillingSummaryRecomputesUsedCentsAcrossMixedSources(t *testing.T) {
	weekly := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "USAGE_PERIOD_TYPE_WEEKLY",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
		"onDemandCap":      map[string]any{"val": 5000},
		"onDemandUsed":     map[string]any{"val": 2500},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})
	legacy := parseXAIBillingSummary(map[string]any{
		"monthlyLimit": map[string]any{"val": 10000},
		"usage": map[string]any{
			"includedUsed": map[string]any{"val": 8000},
		},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})

	merged := mergeXAIBillingSummary(weekly, legacy)
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"used":        {merged.UsedCents, 10500},
		"included":    {merged.IncludedUsedCents, 8000},
		"monthly pct": {merged.UsedPercent, 80},
		"payg used":   {merged.OnDemandUsedCents, 2500},
		"payg pct":    {merged.OnDemandUsedPercent, 50},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
}

func TestMergeXAIBillingSummaryKeepsConflictingUnifiedFields(t *testing.T) {
	unified := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "weekly",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
		"monthlyLimit":     map[string]any{"val": 20000},
		"used":             map[string]any{"val": 5000},
		"onDemandCap":      map[string]any{"val": 6000},
		"onDemandUsed":     map[string]any{"val": 1000},
		"billingPeriodEnd": "2026-09-02T00:00:00Z",
	})
	legacy := parseXAIBillingSummary(map[string]any{
		"monthlyLimit":     map[string]any{"val": 10000},
		"used":             map[string]any{"val": 9000},
		"onDemandCap":      map[string]any{"val": 2000},
		"onDemandUsed":     map[string]any{"val": 1500},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})

	merged := mergeXAIBillingSummary(unified, legacy)
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"monthly limit": {merged.MonthlyLimitCents, 20000},
		"used":          {merged.UsedCents, 5000},
		"included":      {merged.IncludedUsedCents, 5000},
		"monthly pct":   {merged.UsedPercent, 25},
		"payg cap":      {merged.OnDemandCapCents, 6000},
		"payg used":     {merged.OnDemandUsedCents, 1000},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
	if merged.BillingPeriodEnd != "2026-09-02T00:00:00Z" {
		t.Fatalf("billing period end = %q", merged.BillingPeriodEnd)
	}
}

func TestMergeXAIBillingSummaryUsesPartialLegacyOnlyForMissingFields(t *testing.T) {
	unified := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type":  "weekly",
			"start": "2026-08-13T00:00:00Z",
			"end":   "2026-08-20T00:00:00Z",
		},
		"usage": map[string]any{
			"includedUsed": map[string]any{"val": 5000},
		},
		"onDemandCap":  map[string]any{"val": 6000},
		"onDemandUsed": map[string]any{"val": 1000},
	})
	legacy := parseXAIBillingSummary(map[string]any{
		"monthlyLimit":     map[string]any{"val": 20000},
		"billingPeriodEnd": "2026-09-01T00:00:00Z",
	})

	merged := mergeXAIBillingSummary(unified, legacy)
	for name, value := range map[string]struct {
		actual *float64
		want   float64
	}{
		"monthly limit": {merged.MonthlyLimitCents, 20000},
		"used":          {merged.UsedCents, 6000},
		"included":      {merged.IncludedUsedCents, 5000},
		"monthly pct":   {merged.UsedPercent, 25},
		"payg cap":      {merged.OnDemandCapCents, 6000},
		"payg used":     {merged.OnDemandUsedCents, 1000},
	} {
		if value.actual == nil || *value.actual != value.want {
			t.Fatalf("%s = %#v, want %.0f", name, value.actual, value.want)
		}
	}
	if merged.BillingPeriodEnd != "2026-09-01T00:00:00Z" {
		t.Fatalf("billing period end = %q", merged.BillingPeriodEnd)
	}
}

func TestRequestXAIBillingBoundsLegacyEnrichmentLatency(t *testing.T) {
	legacyStarted := make(chan struct{})
	weeklyReleased := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode api-call payload: %v", err)
		}
		if strings.Contains(payload.URL, "format=credits") {
			<-legacyStarted
			close(weeklyReleased)
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":3,"current_period":{"type":"weekly","start":"2026-08-13T00:00:00Z","end":"2026-08-20T00:00:00Z"}}}}`))
			return
		}
		close(legacyStarted)
		<-weeklyReleased
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	settings := model.DefaultCodexInspectionConfig()
	settings.Timeout = 100
	startedAt := time.Now()
	probe, err := New(nil, nil, upstream.Client()).requestXAIBilling(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		settings,
		account{AuthIndex: "xai-1", File: authFile{}},
	)
	if err != nil {
		t.Fatalf("request xAI billing: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("legacy enrichment delayed weekly billing for %s", elapsed)
	}
	if probe.Summary == nil || probe.Summary.UsagePercent == nil || *probe.Summary.UsagePercent != 3 || !probe.Healthy {
		t.Fatalf("weekly billing probe = %#v", probe)
	}
}

func TestHasCompletedXAIInferenceOutput(t *testing.T) {
	tests := []struct {
		name string
		body any
		want bool
	}{
		{
			name: "completed output",
			body: map[string]any{
				"status": "completed",
				"error":  nil,
				"output": []any{map[string]any{
					"type":    "message",
					"content": []any{map[string]any{"type": "output_text", "text": "OK"}},
				}},
			},
			want: true,
		},
		{name: "empty body", body: nil, want: false},
		{name: "incomplete status", body: map[string]any{"status": "incomplete"}, want: false},
		{name: "completed without output", body: map[string]any{"status": "completed", "output": []any{}}, want: false},
		{name: "completed with error", body: map[string]any{"status": "completed", "error": map[string]any{"message": "failed"}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := hasCompletedXAIInferenceOutput(tc.body, "")
			if got != tc.want {
				t.Fatalf("hasCompletedXAIInferenceOutput() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRunXAIDoesNotFallbackToOfficialAPIForExplicitBillingDenials(t *testing.T) {
	tests := []struct {
		name           string
		apiCallBody    string
		classification string
		reason         string
	}{
		{name: "entitlement denied", apiCallBody: `{"status_code":403,"body":{"error":"Need a Grok subscription"}}`, classification: "entitlement_denied", reason: "monitoring.xai_inspection_reason_entitlement_disable"},
		{name: "payment required", apiCallBody: `{"status_code":402,"body":{"error":"Payment required"}}`, classification: "quota_or_entitlement_unknown", reason: "monitoring.xai_inspection_reason_inference_quota_unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requestedURLs synchronizedStringList
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					var payload struct {
						URL string `json:"url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode api-call payload: %v", err)
					}
					requestedURLs.append(payload.URL)
					_, _ = w.Write([]byte(tc.apiCallBody))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			urls := requestedURLs.snapshot()
			if len(urls) != 3 || !strings.HasSuffix(urls[2], "/responses") {
				t.Fatalf("requested URLs = %#v, want billing requests followed by inference", urls)
			}
			for _, requestedURL := range urls {
				if requestedURL == xaiOfficialAPIMeURL {
					t.Fatalf("explicit billing denial called official API fallback: %#v", urls)
				}
			}
			if len(result.Results) != 1 || result.Results[0].ErrorKind != tc.classification || result.Results[0].ActionReason != tc.reason {
				t.Fatalf("xAI result = %#v, want classification=%q reason=%q", result.Results, tc.classification, tc.reason)
			}
		})
	}
}

func TestRunXAIRejectsInvalidOfficialAPIIdentityPayload(t *testing.T) {
	tests := []struct {
		name        string
		apiCallBody string
	}{
		{name: "null team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":"","team_id":"","team_blocked":null}}`},
		{name: "invalid team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":" ","team_id":"","team_blocked":"unknown"}}`},
		{name: "numeric team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":"","team_id":"","team_blocked":0}}`},
		{name: "non-string identity", apiCallBody: `{"status_code":200,"body":{"user_id":false,"team_id":"","team_blocked":null}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requestedURLs synchronizedStringList
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					var payload struct {
						URL string `json:"url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode api-call payload: %v", err)
					}
					requestedURLs.append(payload.URL)
					if payload.URL == xaiOfficialAPIMeURL {
						_, _ = w.Write([]byte(tc.apiCallBody))
						return
					}
					_, _ = w.Write([]byte(`{"status_code":403,"body":{"error":"Access denied"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			urls := requestedURLs.snapshot()
			if len(urls) != 4 || urls[2] != xaiOfficialAPIMeURL || urls[3] != xaiCLIChatProxyBaseURL+"/responses" {
				t.Fatalf("requested URLs = %#v, want billing, rejected identity fallback, and CLI inference", urls)
			}
			if len(result.Results) != 1 || result.Results[0].ErrorKind == "official_api_healthy" {
				t.Fatalf("invalid official API payload reported healthy: %#v", result.Results)
			}
		})
	}
}

func TestRunXAIFailedBillingNeverReportsHealthyAndRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name           string
		apiCallBody    string
		classification string
		statusCode     int
		reason         string
	}{
		{name: "rate limited", apiCallBody: `{"status_code":429,"body":{"error":"too many requests"}}`, classification: "rate_limited", statusCode: http.StatusTooManyRequests, reason: "monitoring.xai_inspection_reason_rate_limited"},
		{name: "upstream error", apiCallBody: `{"status_code":503,"body":{"error":"service unavailable"}}`, classification: "upstream_error", statusCode: http.StatusServiceUnavailable, reason: "monitoring.xai_inspection_reason_upstream_error"},
		{name: "empty payload", apiCallBody: `{"status_code":200,"body":{"config":{}}}`, classification: "protocol_changed", statusCode: http.StatusOK, reason: "monitoring.xai_inspection_reason_inference_protocol_changed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requestCount synchronizedCounter
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					requestCount.increment()
					_, _ = w.Write([]byte(tc.apiCallBody))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.Retries = 1
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			wantRequestCount := 6
			gotRequestCount := requestCount.load()
			if gotRequestCount != wantRequestCount {
				t.Fatalf("billing and inference requests = %d, want %d", gotRequestCount, wantRequestCount)
			}
			if len(detail.Results) != 1 {
				t.Fatalf("results = %#v", detail.Results)
			}
			result := detail.Results[0]
			if result.ErrorKind != tc.classification || result.ErrorKind == "billing_healthy" || result.Action != "keep" {
				t.Fatalf("result = %#v, want classification %q and keep", result, tc.classification)
			}
			if result.ActionReason != tc.reason {
				t.Fatalf("action reason = %q, want %q", result.ActionReason, tc.reason)
			}
			if tc.statusCode > 0 && (result.StatusCode == nil || *result.StatusCode != tc.statusCode) {
				t.Fatalf("status code = %#v, want %d", result.StatusCode, tc.statusCode)
			}
			logEntry := requireInspectionLog(t, detail.Logs, "monitoring.xai_inspection_log_server_complete")
			if logEntry.Level != "warning" {
				t.Fatalf("xAI failed inference log level = %q, want warning", logEntry.Level)
			}
			logDetail := requireInspectionLogDetail(t, logEntry)
			if logDetail["inspectionMode"] != "inference" || logDetail["healthEvidence"] != tc.classification || logDetail["inferenceHealthy"] != false {
				t.Fatalf("xAI failed inference log detail = %#v", logDetail)
			}
		})
	}
}

func TestXAIInferenceDecisionUsesInferenceSpecificReasonKeys(t *testing.T) {
	tests := []struct {
		classification string
		want           string
	}{
		{classification: "quota_or_entitlement_unknown", want: "monitoring.xai_inspection_reason_inference_quota_unknown"},
		{classification: "probe_invalid", want: "monitoring.xai_inspection_reason_inference_probe_invalid"},
		{classification: "protocol_changed", want: "monitoring.xai_inspection_reason_inference_protocol_changed"},
		{classification: "model_unavailable", want: "monitoring.xai_inspection_reason_model_unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.classification, func(t *testing.T) {
			decision := xaiInferenceDecision(http.StatusBadRequest, tc.classification, "detail")
			if decision.Reason != tc.want {
				t.Fatalf("reason = %q, want %q", decision.Reason, tc.want)
			}
		})
	}
}

func TestXAIRelevantFailureUsesFrontendPriority(t *testing.T) {
	tests := []struct {
		name       string
		failures   []xaiProbeDecision
		wantClass  string
		wantAction string
	}{
		{
			name: "auth invalid over generic forbidden",
			failures: []xaiProbeDecision{
				*xaiDecision(http.StatusForbidden, "permission_unknown", "forbidden"),
				*xaiDecision(http.StatusUnauthorized, "auth_invalid", "expired"),
			},
			wantClass:  "auth_invalid",
			wantAction: "reauth",
		},
		{
			name: "entitlement denial over earlier generic forbidden",
			failures: []xaiProbeDecision{
				*xaiDecision(http.StatusForbidden, "permission_unknown", "forbidden"),
				*xaiDecision(http.StatusForbidden, "entitlement_denied", "subscription required"),
			},
			wantClass:  "entitlement_denied",
			wantAction: "disable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failure, ok := xaiRelevantFailure(tc.failures, true)
			if !ok || failure.Classification != tc.wantClass || failure.Action != tc.wantAction {
				t.Fatalf("selected failure = %#v, ok=%v", failure, ok)
			}
		})
	}
}

func TestParseXAIBillingSummarySupportsCentsObjectsCamelCaseAndOnDemand(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthlyLimit": map[string]any{"val": "10000"},
		"used":         map[string]any{"val": "15000"},
		"onDemandCap":  map[string]any{"val": "10000"},
		"productUsage": []any{map[string]any{"product": "grok", "usagePercent": 25.0}},
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.UsedPercent == nil || *summary.UsedPercent != 100 {
		t.Fatalf("used percent = %#v, want 100", summary.UsedPercent)
	}
	if summary.MonthlyLimitCents == nil || *summary.MonthlyLimitCents != 10000 {
		t.Fatalf("monthly limit = %#v, want 10000", summary.MonthlyLimitCents)
	}
	if summary.OnDemandCapCents == nil || *summary.OnDemandCapCents != 10000 {
		t.Fatalf("on-demand cap = %#v, want 10000", summary.OnDemandCapCents)
	}
	if summary.OnDemandUsedPercent == nil || *summary.OnDemandUsedPercent != 50 {
		t.Fatalf("on-demand percent = %#v, want 50", summary.OnDemandUsedPercent)
	}
	if len(summary.ProductUsage) != 1 || summary.ProductUsage[0].Product != "grok" || summary.ProductUsage[0].UsagePercent == nil || *summary.ProductUsage[0].UsagePercent != 25 {
		t.Fatalf("product usage = %#v", summary.ProductUsage)
	}
}

func TestXAIMonthlyOnlySummaryDoesNotCreateWeeklyWindow(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthly_limit":      10000,
		"used":               2500,
		"billing_period_end": "2026-08-01T00:00:00Z",
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	windows := xaiSummaryWindows(summary)
	if len(windows) != 1 || windows[0].ID != "xai-monthly" {
		t.Fatalf("monthly-only windows = %#v", windows)
	}
}

func TestExecuteManualActionsAllowsXAIReauthDeleteOverride(t *testing.T) {
	deleteCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"xai-auth.json","name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"code":"unauthenticated:bad-credentials"}}`))
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			deleteCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "reauth" {
		t.Fatalf("xAI reauth result = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("delete xAI reauth result: %v", err)
	}
	if !deleteCalled {
		t.Fatal("xAI reauth delete override did not delete auth file")
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Success || result.Outcomes[0].Action != "delete" {
		t.Fatalf("delete outcomes = %#v", result.Outcomes)
	}
	if len(result.Detail.Results) != 1 || result.Detail.Results[0].ExecutedAction != "delete" {
		t.Fatalf("updated result = %#v", result.Detail.Results)
	}

	repeated, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("repeat xAI reauth delete: %v", err)
	}
	if len(repeated.Detail.Results) != 1 || repeated.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || repeated.Detail.Results[0].ExecutedAction != "delete" {
		t.Fatalf("repeated result lost successful delete state: %#v", repeated.Detail.Results)
	}
}

func TestExecuteManualActionsRejectsXAIReauthDeleteAfterSamePathReplacement(t *testing.T) {
	var authFilesCalls int
	deleteCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			account := "original@example.com"
			if authFilesCalls > 1 {
				account = "replacement@example.com"
			}
			_, _ = fmt.Fprintf(w, `{"files":[{"id":"xai-auth.json","name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":%q}]}`, account)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"code":"unauthenticated:bad-credentials"}}`))
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "reauth" {
		t.Fatalf("xAI reauth result = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("execute xAI delete override: %v", err)
	}
	if deleteCalled {
		t.Fatal("xAI replacement account was deleted through a historical reauth result")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success ||
		result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed ||
		!strings.Contains(result.Outcomes[0].Error, "账号标识已变化") {
		t.Fatalf("replacement outcomes = %#v", result.Outcomes)
	}
}

func TestExecuteManualActionsReturnsLiveOutcomeWhenResultWriteFails(t *testing.T) {
	deleteCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"xai-auth.json","name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"code":"unauthenticated:bad-credentials"}}`))
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(runDetail.Results) != 1 {
		t.Fatalf("xAI results = %#v", runDetail.Results)
	}

	db.CodexInspections = &failAfterInsertCodexInspectionRepository{
		Repository: db.CodexInspections,
		failAfter:  0,
	}
	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("execute manual delete: %v", err)
	}
	if !deleteCalled {
		t.Fatal("manual delete was not executed")
	}
	if !strings.Contains(result.Detail.Run.Error, "1 个巡检结果写入失败") {
		t.Fatalf("run error = %q, want result write failure", result.Detail.Run.Error)
	}
	if len(result.Detail.Results) != 1 || result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || result.Detail.Results[0].ExecutedAction != "delete" {
		t.Fatalf("returned manual result lost the successful external action: %#v", result.Detail.Results)
	}
}

func TestMatchCurrentAccountRejectsProviderReplacement(t *testing.T) {
	result := model.CodexInspectionResult{FileName: "shared.json", Provider: "xai", AuthIndex: "shared-auth", DisplayAccount: "xai@example.com"}
	if _, ok := matchCurrentAccount([]account{{FileName: "shared.json", Provider: "codex", AuthIndex: "shared-auth", DisplayAccount: "xai@example.com"}}, result); ok {
		t.Fatal("xAI inspection result matched a Codex replacement")
	}
	if _, ok := matchCurrentAccount([]account{{FileName: "shared.json", Provider: "x-ai", AuthIndex: "shared-auth", DisplayAccount: "xai@example.com"}}, result); !ok {
		t.Fatal("normalized xAI provider alias did not match")
	}
	if _, ok := matchCurrentAccount([]account{
		{FileName: "shared.json", Provider: "xai", AuthIndex: "shared-auth", DisplayAccount: "xai@example.com"},
		{FileName: "shared.json", Provider: "xai", AuthIndex: "shared-auth", DisplayAccount: "xai@example.com"},
	}, result); ok {
		t.Fatal("duplicate current candidates produced a non-unique match")
	}
	result.AuthIndex = ""
	result.DisplayAccount = result.FileName
	if _, ok := matchCurrentAccount([]account{{FileName: "shared.json", Provider: "xai", DisplayAccount: "shared.json"}}, result); ok {
		t.Fatal("file-name fallback was accepted as an account identity")
	}
}

func TestHistoricalDisplayAccountAloneRequiresActionReview(t *testing.T) {
	result := model.CodexInspectionResult{
		ID:             1,
		FileName:       "legacy.json",
		Provider:       "codex",
		DisplayAccount: "legacy@example.com",
		Action:         "disable",
	}

	manualItems, manualOutcomes := selectManualActionItems(
		[]model.CodexInspectionResult{result},
		map[int64]struct{}{result.ID: {}},
	)
	if len(manualItems) != 0 || len(manualOutcomes) != 1 ||
		manualOutcomes[0].Status != model.CodexInspectionActionStatusNeedsReview {
		t.Fatalf("manual historical display-only selection = items %#v outcomes %#v", manualItems, manualOutcomes)
	}

	autoItems, autoOutcomes := selectAutoActionItems(
		model.CodexInspectionAutoActionDisable,
		false,
		[]model.CodexInspectionResult{result},
	)
	if len(autoItems) != 0 || len(autoOutcomes) != 1 ||
		autoOutcomes[0].Status != model.CodexInspectionActionStatusNeedsReview {
		t.Fatalf("automatic historical display-only selection = items %#v outcomes %#v", autoItems, autoOutcomes)
	}
}

func TestHistoricalAuthIndexStillMatchesCurrentAccount(t *testing.T) {
	result := model.CodexInspectionResult{
		ID:             1,
		FileName:       "legacy.json",
		Provider:       "codex",
		AuthIndex:      "auth-1",
		DisplayAccount: "old-label@example.com",
		Action:         "disable",
	}
	current := account{
		FileName:        "legacy.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		DisplayAccount:  "new-label@example.com",
		AccountSnapshot: "new-label@example.com",
	}

	if _, ok := matchCurrentAccount([]account{current}, result); !ok {
		t.Fatal("historical auth_index result did not match the current account")
	}
	manualItems, manualOutcomes := selectManualActionItems(
		[]model.CodexInspectionResult{result},
		map[int64]struct{}{result.ID: {}},
	)
	if len(manualItems) != 1 || len(manualOutcomes) != 0 {
		t.Fatalf("historical auth_index selection = items %#v outcomes %#v", manualItems, manualOutcomes)
	}
}

func TestApplyManualActionOverridesRejectsUnsafeTransitions(t *testing.T) {
	results := []model.CodexInspectionResult{
		{ID: 1, Action: "reauth"},
		{ID: 2, Action: "keep"},
	}
	selected := map[int64]struct{}{1: {}, 2: {}}

	for _, overrides := range [][]ManualActionOverride{
		{{ResultID: 1, Action: "disable"}},
		{{ResultID: 2, Action: "delete"}},
		{{ResultID: 3, Action: "delete"}},
	} {
		if _, err := applyManualActionOverrides(results, selected, overrides); !errors.Is(err, ErrInvalidActionOverride) {
			t.Fatalf("overrides %#v error = %v, want ErrInvalidActionOverride", overrides, err)
		}
	}
}

func TestFetchAuthFilesStreamsResponsesLargerThanEightMiB(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 8*1024*1024)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	t.Cleanup(upstream.Close)

	svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
	files, err := svc.fetchAuthFiles(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	})
	if err != nil {
		t.Fatalf("fetch auth files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if name := readString(files[0], "name"); name != "auth-a.json" {
		t.Fatalf("file name = %q, want auth-a.json", name)
	}
}

func TestRequestCodexUsageStreamsResponsesLargerThanEightMiB(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-call" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status_code":200,"body":{"padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 8*1024*1024)))
		_, _ = w.Write([]byte(`"}}`))
	}))
	t.Cleanup(upstream.Close)

	svc := New(nil, nil, upstream.Client())
	result, _, err := svc.requestCodexUsageAt(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.ManagerCodexInspectionConfig{},
		account{AuthIndex: "auth-1"},
		"/v0/management/api-call",
	)
	if err != nil {
		t.Fatalf("request Codex usage: %v", err)
	}
	body, ok := result.Body.(map[string]any)
	if !ok {
		t.Fatalf("body = %#v, want map", result.Body)
	}
	if padding := readString(body, "padding"); len(padding) != 8*1024*1024 {
		t.Fatalf("padding length = %d, want %d", len(padding), 8*1024*1024)
	}
}

func TestDecodeCPAAPICallResponseRejectsOversizedBody(t *testing.T) {
	body := `{"status_code":200,"body":{"padding":"` + strings.Repeat("x", 256) + `"}}`
	var raw map[string]any
	err := decodeCPAAPICallResponse(strings.NewReader(body), 128, &raw)
	if !errors.Is(err, errCPAAPICallResponseTooLarge) {
		t.Fatalf("decodeCPAAPICallResponse() error = %v, want errCPAAPICallResponseTooLarge", err)
	}
}

func TestDoCPAActionRejectsLargeBusinessFailureResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024)))
		_, _ = w.Write([]byte(`","failed":["denied"]}`))
	}))
	t.Cleanup(upstream.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	svc := New(nil, nil, upstream.Client())
	actionErr, statusCode := svc.doCPAAction(req, "management-key")
	if actionErr == nil || !strings.Contains(actionErr.Error(), "denied") {
		t.Fatalf("action error = %v, want denied failure", actionErr)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", statusCode, http.StatusOK)
	}
}

func TestRunPersistsPlanQuotaWindowsAndErrorDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready","plan_type":"plus"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{
				"status_code":402,
				"body":{
					"message":"short window exhausted but monthly quota remains",
					"plan_type":"team",
					"rate_limit":{
						"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_after_seconds":3600},
						"secondary_window":{"used_percent":72,"limit_window_seconds":2592000,"reset_at":1782895966}
					},
					"code_review_rate_limit":{
						"primary_window":{"used_percent":22,"limit_window_seconds":18000}
					},
					"additional_rate_limits":[{
						"limit_name":"credits",
						"rate_limit":{
							"primary_window":{"used_percent":44,"limit_window_seconds":604800}
						}
					}]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %#v, want 1", result.Results)
	}
	item := result.Results[0]
	if item.PlanType != "team" {
		t.Fatalf("plan type = %q, want team", item.PlanType)
	}
	if item.ErrorKind != "http_status" || !strings.Contains(item.ErrorDetail, "short window exhausted") {
		t.Fatalf("error detail = kind %q detail %q, want HTTP detail", item.ErrorKind, item.ErrorDetail)
	}
	windowsByID := map[string]model.CodexInspectionQuotaWindow{}
	for _, window := range item.QuotaWindows {
		windowsByID[window.ID] = window
	}
	for _, id := range []string{"five-hour", "monthly", "code-review-five-hour", "credits-weekly-0"} {
		if _, ok := windowsByID[id]; !ok {
			t.Fatalf("quota windows missing %q: %#v", id, item.QuotaWindows)
		}
	}
	if windowsByID["monthly"].UsedPercent == nil || *windowsByID["monthly"].UsedPercent != 72 {
		t.Fatalf("monthly window = %#v, want used percent 72", windowsByID["monthly"])
	}
	if windowsByID["monthly"].ResetLabel == "" || windowsByID["monthly"].ResetLabel == "-" {
		t.Fatalf("monthly reset label = %q, want concrete reset label", windowsByID["monthly"].ResetLabel)
	}
	if windowsByID["monthly"].ResetAtMS != 1_782_895_966_000 || windowsByID["monthly"].ResetAccuracy != "exact" {
		t.Fatalf("monthly normalized reset = %#v", windowsByID["monthly"])
	}
	if windowsByID["five-hour"].ResetAtMS <= 0 || windowsByID["five-hour"].ResetAccuracy != "derived" {
		t.Fatalf("five-hour normalized reset = %#v", windowsByID["five-hour"])
	}
	if windowsByID["credits-weekly-0"].LabelParams["name"] != "credits" {
		t.Fatalf("additional window params = %#v, want credits name", windowsByID["credits-weekly-0"].LabelParams)
	}

	stored, err := db.ListCodexInspectionResults(context.Background(), result.Run.ID)
	if err != nil {
		t.Fatalf("list stored results: %v", err)
	}
	if len(stored) != 1 || stored[0].PlanType != "team" || len(stored[0].QuotaWindows) != len(item.QuotaWindows) {
		t.Fatalf("stored result = %#v, want persisted enhanced fields", stored)
	}
	if stored[0].ErrorKind != "http_status" || !strings.Contains(stored[0].ErrorDetail, "short window exhausted") {
		t.Fatalf("stored error detail = %#v, want persisted HTTP detail", stored[0])
	}
	snapshots, err := quotasnapshotsvc.New(db).Query(context.Background(), quotasnapshotsvc.QueryRequest{
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey: "row-1", Provider: "codex", Account: quotasnapshotsvc.AccountTarget{
				AuthFileSnapshot: "auth-a.json", AuthProviderSnapshot: "codex", AuthIndex: "auth-1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("query inspection quota snapshots: %v", err)
	}
	if len(snapshots.Items) != 1 || len(snapshots.Items[0].Windows) != len(item.QuotaWindows) {
		t.Fatalf("inspection quota snapshots = %#v", snapshots)
	}
	for _, window := range snapshots.Items[0].Windows {
		if window.Source != "inspection" {
			t.Fatalf("inspection snapshot source = %#v", window)
		}
	}
}

func TestRunAutoActionNoneDoesNotExecuteActions(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.EnableCount != 0 || result.Run.KeepCount != 1 {
		t.Fatalf("run counts enable=%d keep=%d, want 0/1", result.Run.EnableCount, result.Run.KeepCount)
	}
	if patchCalled {
		t.Fatal("server inspection executed action in none mode")
	}
}

func TestRunAutoActionSkipsCodexRuntimeOnlyCredentialBeforeMutation(t *testing.T) {
	var patchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","provider":"codex","account_id":"workspace-1","account":"alice@example.com","disabled":false,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if patchCalls != 0 {
		t.Fatalf("runtime-only Codex automatic action made %d mutation calls, want 0", patchCalls)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "keep" ||
		result.Results[0].ErrorKind != "missing_auth_index" ||
		result.Results[0].Error != "缺少 auth_index" {
		t.Fatalf("runtime-only Codex result = %#v, want safe missing-auth-index skip", result.Results)
	}
}

func TestRunAutoActionEnableEnablesRecoveredDisabledAccount(t *testing.T) {
	var patchCalled bool
	var patchedDisabled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-auth-1" {
				t.Fatalf("patch name = %q, want runtime-auth-1", payload.Name)
			}
			patchedDisabled = payload.Disabled
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	managerCfg.CodexInspection.AutoRecoverEnabled = true
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "auth-a.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if !patchCalled {
		t.Fatal("server inspection did not auto-enable recovered account")
	}
	if patchedDisabled {
		t.Fatal("server inspection disabled a recovered account, want enable")
	}
	if result.Run.EnableCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d keep=%d, want 1/0", result.Run.EnableCount, result.Run.KeepCount)
	}
	if result.Results[0].Action != "enable" ||
		!result.Results[0].AutoRecoverEligible ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "enable" ||
		result.Results[0].Disabled {
		t.Fatalf("result after enable = %#v", result.Results[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 0 {
		t.Fatalf("ownership after enable = %#v, want empty", ownership)
	}
}

func TestRunAutoRecoverSkipsManuallyDisabledAccount(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	managerCfg.CodexInspection.AutoRecoverEnabled = true
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if patchCalled {
		t.Fatal("auto recovery enabled a manually disabled account")
	}
	if len(result.Results) != 1 || result.Results[0].Action != "enable" || result.Results[0].AutoRecoverEligible {
		t.Fatalf("result = %#v, want manual-only enable suggestion", result.Results)
	}
	if !strings.Contains(result.Results[0].ActionReason, "仅允许手动启用") {
		t.Fatalf("action reason = %q, want manual-only explanation", result.Results[0].ActionReason)
	}
}

func TestRunWithDifferentTargetTypePreservesDisableOwnership(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "anthropic"
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "auth-a.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	svc := newCodexInspectionTestService(t, db)
	if _, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"}); err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].FileName != "auth-a.json" {
		t.Fatalf("ownership = %#v, want preserved auth-a.json", ownership)
	}
}

func TestApplyDisableOwnershipIsolatedByProvider(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "shared-auth",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{
		{FileName: "shared-auth.json", Provider: "xai", AuthIndex: "shared-auth", DisplayAccount: "alice@example.com", AccountSnapshot: "alice@example.com", Disabled: true},
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "shared-auth", DisplayAccount: "alice@example.com", AccountSnapshot: "alice@example.com", Disabled: true},
	}
	svc := New(db, nil)
	svc.applyDisableOwnership(context.Background(), accounts, runLogger{})

	if accounts[0].AutoRecoverOwned {
		t.Fatal("xAI account inherited Codex disable ownership")
	}
	if !accounts[1].AutoRecoverOwned {
		t.Fatal("Codex account did not retain matching disable ownership")
	}
}

func TestApplyDisableOwnershipIsolatedBySameFileAuthIndex(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	for _, authIndex := range []string{"auth-1", "auth-2"} {
		if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
			FileName:        "shared-auth.json",
			Provider:        "codex",
			AuthIndex:       authIndex,
			AccountSnapshot: authIndex + "@example.com",
		}); err != nil {
			t.Fatalf("save inspection disable ownership %s: %v", authIndex, err)
		}
	}

	accounts := []account{
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "auth-1", DisplayAccount: "auth-1@example.com", AccountSnapshot: "auth-1@example.com", Disabled: true},
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "auth-2", DisplayAccount: "auth-2@example.com", AccountSnapshot: "auth-2@example.com", Disabled: true},
	}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})

	if !accounts[0].AutoRecoverOwned || !accounts[1].AutoRecoverOwned {
		t.Fatalf("same-file ownership = %#v, want both identities owned", accounts)
	}
}

func TestApplyDisableOwnershipAcceptsAuthIndexOnlyIdentity(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save auth-index-only inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		Disabled:  true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})

	if !accounts[0].AutoRecoverOwned {
		t.Fatalf("auth-index-only ownership was not restored: %#v", accounts[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list auth-index-only ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AuthIndex != "auth-1" {
		t.Fatalf("auth-index-only ownership = %#v, want preserved", ownership)
	}
}

func TestApplyDisableOwnershipDeletesAmbiguousLegacyRecordPermanently(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName: "shared-auth.json",
		Provider: "codex",
	}); err != nil {
		t.Fatalf("save legacy inspection disable ownership: %v", err)
	}

	svc := New(db, nil)
	accounts := []account{
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "auth-1", Disabled: true},
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "auth-2", Disabled: true},
	}
	svc.applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned || accounts[1].AutoRecoverOwned {
		t.Fatalf("ambiguous legacy ownership granted recovery: %#v", accounts)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership after ambiguity: %v", err)
	}
	if len(ownership) != 0 {
		t.Fatalf("ambiguous ownership = %#v, want permanently removed", ownership)
	}

	shrunk := []account{
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "auth-1", Disabled: true},
	}
	svc.applyDisableOwnership(context.Background(), shrunk, runLogger{})
	if shrunk[0].AutoRecoverOwned {
		t.Fatalf("removed ambiguous ownership was re-granted after candidate shrink: %#v", shrunk)
	}
}

func TestApplyDisableOwnershipRejectsSameFileReplacement(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		DisplayAccount:  "bob@example.com",
		AccountSnapshot: "bob@example.com",
		Disabled:        true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned {
		t.Fatalf("replacement account inherited ownership: %#v", accounts[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(ownership) != 0 {
		t.Fatalf("replacement ownership = %#v, want removed", ownership)
	}
}

func TestApplyDisableOwnershipAllowsMissingCodexMemberEvidence(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "account-1",
		AccountSnapshot: "old-label@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:       "shared-auth.json",
		Provider:       "codex",
		AuthIndex:      "auth-1",
		AccountID:      "account-1",
		DisplayAccount: "new-label@example.com",
		Disabled:       true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if !accounts[0].AutoRecoverOwned {
		t.Fatalf("missing current Codex member evidence blocked recovery: %#v", accounts[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AccountSnapshot != "old-label@example.com" {
		t.Fatalf("missing-member ownership = %#v, want preserved", ownership)
	}
}

func TestApplyDisableOwnershipRejectsCodexWorkspaceConflict(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-2",
		AccountSnapshot: "alice@example.com",
		Disabled:        true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned {
		t.Fatalf("workspace conflict granted recovery: %#v", accounts[0])
	}
}

func TestApplyDisableOwnershipRejectsCodexMemberConflict(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "bob@example.com",
		Disabled:        true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned {
		t.Fatalf("member conflict granted recovery: %#v", accounts[0])
	}
}

func TestApplyDisableOwnershipRejectsInvalidCodexEvidence(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:         "shared-auth.json",
		Provider:         "codex",
		AuthIndex:        "auth-1",
		AccountID:        "workspace-1",
		AccountSnapshot:  "alice@example.com",
		AccountIDInvalid: true,
		Disabled:         true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned {
		t.Fatalf("invalid Codex evidence granted recovery: %#v", accounts[0])
	}
}

func TestApplyDisableOwnershipRestoresWorkspaceOnlyCodexOwnershipByUniqueLocator(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:     "shared-auth.json",
		Provider:     "codex",
		AuthIndex:    "auth-1",
		AccountID:    "workspace-1",
		DisabledAtMS: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("save workspace-only inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "bob@example.com",
		Disabled:        true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if !accounts[0].AutoRecoverOwned {
		t.Fatalf("unique workspace-only Codex ownership was not restored: %#v", accounts[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AuthIndex != "auth-1" {
		t.Fatalf("workspace-only ownership = %#v, want preserved ownership", ownership)
	}
}

func TestApplyDisableOwnershipKeepsAmbiguousCodexOwnership(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "shared-auth.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "workspace-1",
	}); err != nil {
		t.Fatalf("save workspace-only inspection disable ownership: %v", err)
	}

	accounts := []account{
		{
			FileName:        "shared-auth.json",
			Provider:        "codex",
			AuthIndex:       "auth-1",
			AccountID:       "workspace-1",
			AccountSnapshot: "alice@example.com",
			Disabled:        true,
		},
		{
			FileName:        "shared-auth.json",
			Provider:        "codex",
			AuthIndex:       "auth-1",
			AccountID:       "workspace-1",
			AccountSnapshot: "bob@example.com",
			Disabled:        true,
		},
	}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned || accounts[1].AutoRecoverOwned {
		t.Fatalf("ambiguous workspace-only ownership granted recovery: %#v", accounts)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership after ambiguity: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AuthIndex != "auth-1" {
		t.Fatalf("ambiguous workspace-only ownership = %#v, want preserved ownership", ownership)
	}
}

func TestApplyDisableOwnershipDoesNotNarrowDuplicateLocatorByMember(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "shared-auth.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountID:       "workspace-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save member-specific inspection disable ownership: %v", err)
	}

	accounts := []account{
		{
			FileName:        "shared-auth.json",
			Provider:        "codex",
			AuthIndex:       "auth-1",
			AccountID:       "workspace-1",
			AccountSnapshot: "alice@example.com",
			Disabled:        true,
		},
		{
			FileName:        "shared-auth.json",
			Provider:        "codex",
			AuthIndex:       "auth-1",
			AccountID:       "workspace-1",
			AccountSnapshot: "bob@example.com",
			Disabled:        true,
		},
	}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned || accounts[1].AutoRecoverOwned {
		t.Fatalf("duplicate locator ownership was narrowed by member: %#v", accounts)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership after duplicate locator: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("duplicate locator ownership = %#v, want preserved Alice record", ownership)
	}
}

func TestApplyDisableOwnershipRejectsMissingProvider(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "shared-auth.json",
		AuthIndex: "auth-1",
		AccountID: "account-1",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{{
		FileName:  "shared-auth.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Disabled:  true,
	}}
	New(db, nil).applyDisableOwnership(context.Background(), accounts, runLogger{})
	if accounts[0].AutoRecoverOwned {
		t.Fatalf("provider-less ownership granted recovery: %#v", accounts[0])
	}
}

func TestInspectionResultMatchesCurrentAccountRequiresProvider(t *testing.T) {
	result := model.CodexInspectionResult{
		FileName:       "auth-a.json",
		DisplayAccount: "alice@example.com",
		AuthIndex:      "auth-1",
	}
	current := account{
		FileName:       "auth-a.json",
		DisplayAccount: "alice@example.com",
		AuthIndex:      "auth-1",
		Provider:       "codex",
	}
	if inspectionResultMatchesCurrentAccount(result, current) {
		t.Fatal("provider-less inspection result matched a current Codex account")
	}
}

func TestRunAutoActionDisableExecutesDeleteSuggestionAsDisable(t *testing.T) {
	var deleteCalled bool
	var patchCalled bool
	var patchedDisabled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-auth-1" {
				t.Fatalf("patch name = %q, want runtime-auth-1", payload.Name)
			}
			patchedDisabled = payload.Disabled
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled {
		t.Fatal("auto disable mode deleted a delete suggestion")
	}
	if !patchCalled || !patchedDisabled {
		t.Fatalf("auto disable patch called=%v disabled=%v, want true/true", patchCalled, patchedDisabled)
	}
	if result.Run.DeleteCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 1/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if result.Results[0].Action != "delete" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "disable" ||
		!result.Results[0].Disabled {
		t.Fatalf("result after auto disable = %#v", result.Results[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].FileName != "auth-a.json" || ownership[0].Provider != "codex" || ownership[0].AuthIndex != "auth-1" || ownership[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("ownership after auto disable = %#v", ownership)
	}
}

func TestRunAutoActionSkipsDuplicateFileNameResults(t *testing.T) {
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"auth-a.json","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"id":"runtime-auth-2","name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
	if result.Run.DeleteCount != 2 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 2/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %#v, want 2", result.Results)
	}

	byAuthIndex := map[string]model.CodexInspectionResult{}
	for _, item := range result.Results {
		byAuthIndex[item.AuthIndex] = item
		if item.Action != "delete" {
			t.Fatalf("result action = %q, want delete: %#v", item.Action, item)
		}
	}
	canonical := byAuthIndex["auth-1"]
	if canonical.ActionStatus != model.CodexInspectionActionStatusSuccess ||
		canonical.ExecutedAction != "delete" ||
		canonical.ActionError != "" {
		t.Fatalf("canonical result = %#v, want success delete", canonical)
	}
	duplicate := byAuthIndex["auth-2"]
	if duplicate.ActionStatus != model.CodexInspectionActionStatusSkipped ||
		duplicate.ExecutedAction != "" ||
		duplicate.ActionError == "" {
		t.Fatalf("duplicate result = %#v, want skipped with action error", duplicate)
	}
	skippedLog := requireInspectionLog(t, result.Logs, "自动处理账号跳过")
	if skippedLog.Level != "info" {
		t.Fatalf("automatic duplicate skip level = %q, want info", skippedLog.Level)
	}
	skippedDetail := requireInspectionLogDetail(t, skippedLog)
	if skippedDetail["status"] != model.CodexInspectionActionStatusSkipped ||
		skippedDetail["reason"] == "" {
		t.Fatalf("automatic duplicate skip detail = %#v", skippedDetail)
	}
	completion := requireInspectionLog(t, result.Logs, "凭证健康巡检完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "success" ||
		completionDetail["actionSuccessCount"] != float64(1) ||
		completionDetail["actionSkippedCount"] != float64(1) ||
		completionDetail["actionFailedCount"] != float64(0) ||
		completionDetail["actionNeedsReviewCount"] != float64(0) {
		t.Fatalf("automatic duplicate completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestRunAutoActionSkipsMixedActionsInSameFile(t *testing.T) {
	result := runMixedAutoActionInspection(t, model.CodexInspectionAutoActionDelete, mixedAutoActionFixtureEnableDelete)
	assertMixedNeedsReviewRun(t, result, "enable", "delete")
}

func TestRunAutoEnableSkipsMixedActionsInSameFile(t *testing.T) {
	result := runMixedAutoActionInspection(t, model.CodexInspectionAutoActionEnable, mixedAutoActionFixtureEnableDelete)
	assertMixedNeedsReviewRun(t, result, "enable", "delete")
}

func TestSelectAutoActionItemsNeedsReviewForDeleteAndKeepSiblings(t *testing.T) {
	results := []model.CodexInspectionResult{
		{ID: 1, AccountKey: "auth-1", FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", Action: "delete"},
		{ID: 2, AccountKey: "auth-2", FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", Action: "keep"},
	}

	items, outcomes := selectAutoActionItems(model.CodexInspectionAutoActionDelete, false, results)
	if len(items) != 0 {
		t.Fatalf("action items = %#v, want none for delete plus keep siblings", items)
	}
	if len(outcomes) != 1 ||
		outcomes[0].ResultID != 1 ||
		outcomes[0].Status != model.CodexInspectionActionStatusNeedsReview ||
		outcomes[0].Error != fileActionMixedReason {
		t.Fatalf("outcomes = %#v, want delete result needs_review", outcomes)
	}
}

func TestSelectAutoActionItemsNeedsReviewForCodexWithoutAuthIndex(t *testing.T) {
	result := model.CodexInspectionResult{
		ID:              1,
		AccountKey:      "runtime-only",
		FileName:        "auth-a.json",
		Provider:        "codex",
		AccountID:       "workspace-1",
		AccountSnapshot: "alice@example.com",
		Action:          "disable",
	}

	items, outcomes := selectAutoActionItems(model.CodexInspectionAutoActionDisable, false, []model.CodexInspectionResult{result})
	if len(items) != 0 {
		t.Fatalf("runtime-only Codex action items = %#v, want none", items)
	}
	if len(outcomes) != 1 || outcomes[0].Status != model.CodexInspectionActionStatusNeedsReview ||
		outcomes[0].Error != inspectionIdentityMissingReason {
		t.Fatalf("runtime-only Codex outcomes = %#v, want needs_review", outcomes)
	}
}

func TestRunAutoDeleteNeedsReviewWhenSampleOmitsSameFileSibling(t *testing.T) {
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"shared.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"id":"runtime-auth-2","name":"shared.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	managerCfg.CodexInspection.SampleSize = 1
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0 when a live sibling was not sampled", deleteCalls)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Action != "delete" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusNeedsReview ||
		result.Results[0].ExecutedAction != "" ||
		result.Results[0].ActionError != fileDeleteCoverageReason {
		t.Fatalf("sampled result = %#v, want delete needs_review", result.Results)
	}
}

func TestRunAutoDisableTargetsSameNameCredentialByRuntimeID(t *testing.T) {
	var patchCalls int
	var patchedName string
	var patchedAuthIndex string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"id":"runtime-auth-2","name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				AuthIndex string `json:"authIndex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			if payload.AuthIndex == "auth-1" {
				_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patchedName = payload.Name
			patchedAuthIndex = payload.AuthIndex
			_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	managerCfg.CodexInspection.SampleSize = 1
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("same-file status patch calls = %d, want 1", patchCalls)
	}
	if len(result.Results) != 1 {
		t.Fatalf("sampled results = %#v, want one", result.Results)
	}
	item := result.Results[0]
	if item.ActionStatus != model.CodexInspectionActionStatusSuccess ||
		item.ExecutedAction != "disable" ||
		!item.Disabled {
		t.Fatalf("result = %#v, want successful credential-scoped disable", item)
	}
	if patchedAuthIndex != item.AuthIndex || patchedName != "runtime-"+item.AuthIndex {
		t.Fatalf("status target name=%q authIndex=%q item=%#v", patchedName, patchedAuthIndex, item)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AuthIndex != item.AuthIndex {
		t.Fatalf("ownership = %#v, want selected credential ownership", ownership)
	}
}

func TestExecuteManualActionsNeedsReviewForMixedFileNameActions(t *testing.T) {
	var deleteCalled bool
	var patchCalled bool
	upstream := newMixedAutoActionServer(t, mixedAutoActionFixtureEnableDelete, &deleteCalled, &patchCalled)
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 2 {
		t.Fatalf("initial results = %#v, want 2", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID, runDetail.Results[1].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalled || patchCalled {
		t.Fatalf("manual mixed same-file actions executed delete=%v patch=%v, want false/false", deleteCalled, patchCalled)
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v, want 2", result.Outcomes)
	}
	for _, outcome := range result.Outcomes {
		if !outcome.Success ||
			outcome.Status != model.CodexInspectionActionStatusNeedsReview ||
			!strings.Contains(outcome.Error, "多个不同建议动作") {
			t.Fatalf("manual mixed outcome = %#v, want needs_review", outcome)
		}
	}
	assertMixedNeedsReviewRun(t, result.Detail, "enable", "delete")
	completion := requireInspectionLog(t, result.Detail.Logs, "手动处理账号完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "warning" ||
		completionDetail["needsReviewCount"] != float64(2) ||
		completionDetail["successCount"] != float64(0) ||
		completionDetail["skippedCount"] != float64(0) ||
		completionDetail["failedCount"] != float64(0) {
		t.Fatalf("manual mixed completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestExecuteManualActionsRequiresEverySharedDeleteResultToBeSelected(t *testing.T) {
	for _, tc := range []struct {
		name       string
		selectAll  bool
		wantDelete bool
	}{
		{name: "single selected result", selectAll: false, wantDelete: false},
		{name: "all selected results", selectAll: true, wantDelete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deleteCalled bool
			var patchCalled bool
			upstream := newMixedAutoActionServer(t, mixedAutoActionFixtureDeleteDelete, &deleteCalled, &patchCalled)
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}
			svc := newCodexInspectionTestService(t, db)

			runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
			if err != nil {
				t.Fatalf("run inspection: %v", err)
			}
			if len(runDetail.Results) != 2 || runDetail.Results[0].Action != "delete" || runDetail.Results[1].Action != "delete" {
				t.Fatalf("initial results = %#v, want two delete suggestions", runDetail.Results)
			}
			resultIDs := []int64{runDetail.Results[0].ID}
			if tc.selectAll {
				resultIDs = append(resultIDs, runDetail.Results[1].ID)
			}

			result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{ResultIDs: resultIDs})
			if err != nil {
				t.Fatalf("execute manual actions: %v", err)
			}
			if deleteCalled != tc.wantDelete || patchCalled {
				t.Fatalf("manual shared delete called=%v patch=%v, want delete=%v patch=false", deleteCalled, patchCalled, tc.wantDelete)
			}
			if tc.wantDelete {
				if summarizeActionOutcomes(result.Outcomes).Success != 1 {
					t.Fatalf("full-selection outcomes = %#v, want one successful physical-file delete", result.Outcomes)
				}
				return
			}
			if len(result.Outcomes) != 1 || result.Outcomes[0].Status != model.CodexInspectionActionStatusNeedsReview || !strings.Contains(result.Outcomes[0].Error, "完整覆盖") {
				t.Fatalf("partial-selection outcomes = %#v, want needs_review coverage failure", result.Outcomes)
			}
		})
	}
}

func TestRunClassifiesExpiredUnauthorizedAsReauth(t *testing.T) {
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Provided authentication token is expired. Please try signing in again."}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled {
		t.Fatal("expired token reauth suggestion should not execute delete action")
	}
	if result.Run.ReauthCount != 1 || result.Run.DeleteCount != 0 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts reauth=%d delete=%d keep=%d, want 1/0/0", result.Run.ReauthCount, result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "reauth" {
		t.Fatalf("result action = %#v, want reauth", result.Results)
	}
	probeLog := requireInspectionLog(t, result.Logs, "账号探测完成")
	if probeLog.Level != "error" {
		t.Fatalf("reauth probe log level = %q, want error", probeLog.Level)
	}
	completionDetail := requireInspectionLogDetail(t, requireInspectionLog(t, result.Logs, "凭证健康巡检完成"))
	if completionDetail["reauthCount"] != float64(1) {
		t.Fatalf("completion reauthCount = %#v, want 1", completionDetail["reauthCount"])
	}
}

func TestRunClassifiesInvalidatedUnauthorizedAsReauth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated. Please try signing in again."}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.ReauthCount != 1 || result.Run.DeleteCount != 0 {
		t.Fatalf("run counts reauth=%d delete=%d, want 1/0", result.Run.ReauthCount, result.Run.DeleteCount)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "reauth" {
		t.Fatalf("result action = %#v, want reauth", result.Results)
	}
}

func TestResolveProbeActionUsesMonthlyWindowAsLongQuota(t *testing.T) {
	item := account{DisplayAccount: "user@example.test"}
	threshold := 100.0

	t.Run("deletes deactivated workspace payment required response", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(
			item,
			http.StatusPaymentRequired,
			`{"detail":{"code":"deactivated_workspace"}}`,
			rateLimit,
			deriveRateLimitUsedPercent(rateLimit),
			true,
			threshold,
		)

		if decision.Action != "delete" ||
			decision.ActionReason != "接口返回 402，工作区已停用，建议删除账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want delete deactivated workspace", decision)
		}
	})

	t.Run("keeps healthy monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), false, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "月额度仍可用，无需处理" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want keep healthy monthly quota", decision)
		}
	})

	t.Run("disables exhausted monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(100),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "disable" ||
			decision.ActionReason != "月额度达到阈值，建议禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 100 ||
			!decision.IsQuota {
			t.Fatalf("decision = %#v, want disable exhausted monthly quota", decision)
		}
	})

	t.Run("keeps exhausted short window with healthy monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(100),
				LimitWindowSeconds: ptrFloat(codexFiveHourWindow),
			},
			SecondaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want keep exhausted short window with healthy monthly quota", decision)
		}
	})

	t.Run("keeps disabled account while short window remains exhausted", func(t *testing.T) {
		disabledItem := item
		disabledItem.Disabled = true
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(100),
				LimitWindowSeconds: ptrFloat(codexFiveHourWindow),
			},
			SecondaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(disabledItem, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度仍达到阈值，月额度可用但继续保持禁用" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			!decision.IsQuota {
			t.Fatalf("decision = %#v, want keep disabled account until short window recovers", decision)
		}
	})

	t.Run("keeps disabled account when quota is unknown", func(t *testing.T) {
		disabledItem := item
		disabledItem.Disabled = true
		decision := resolveProbeAction(disabledItem, http.StatusOK, `{"ok":true}`, nil, nil, false, threshold)

		if decision.Action != "keep" || decision.UsedPercent != nil || decision.IsQuota {
			t.Fatalf("decision = %#v, want keep unknown quota", decision)
		}
	})

	t.Run("treats team secondary window without duration as monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent: ptrFloat(100),
			},
			SecondaryWindow: &codexWindow{
				UsedPercent: ptrFloat(5),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold, "team")

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want team secondary window treated as monthly quota", decision)
		}
	})
}

func TestRunSuggestsDeleteForDeactivatedWorkspace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.DeleteCount != 1 || result.Run.DisableCount != 0 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d disable=%d keep=%d, want 1/0/0", result.Run.DeleteCount, result.Run.DisableCount, result.Run.KeepCount)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Action != "delete" ||
		result.Results[0].ActionReason != "接口返回 402，工作区已停用，建议删除账号" ||
		result.Results[0].IsQuota {
		t.Fatalf("result = %#v, want delete deactivated workspace", result.Results)
	}
}

func TestRunSendsDirectCodexAccountIDHeader(t *testing.T) {
	var accountIDHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"acct-direct","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Header map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			accountIDHeader = payload.Header["Chatgpt-Account-Id"]
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	if _, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	}); err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if accountIDHeader != "acct-direct" {
		t.Fatalf("Chatgpt-Account-Id = %q, want %q", accountIDHeader, "acct-direct")
	}
}

func TestExecuteManualActionsProcessesCompletedRunResults(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-auth-1" || payload.Disabled {
				t.Fatalf("patch payload = %#v, want enable runtime-auth-1", payload)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "enable" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if !patchCalled {
		t.Fatal("manual action did not patch auth file")
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Success || result.Outcomes[0].Action != "enable" {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Run.EnableCount != 1 || result.Detail.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d keep=%d, want 1/0", result.Detail.Run.EnableCount, result.Detail.Run.KeepCount)
	}
	if result.Detail.Results[0].Action != "enable" ||
		result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Detail.Results[0].ExecutedAction != "enable" ||
		result.Detail.Results[0].Disabled {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
}

func TestExecuteManualActionsStartsPersistenceBudgetAfterSlowAction(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalled = true
			time.Sleep(1250 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	svc.manualActionPersistenceTimeout = time.Second

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute slow manual action: %v", err)
	}
	if !patchCalled {
		t.Fatal("slow manual action did not patch auth file")
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Success ||
		len(result.Detail.Results) != 1 ||
		result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess {
		t.Fatalf("slow manual action result = %#v", result)
	}
}

func TestExecuteManualActionsRejectsChangedAuthIndex(t *testing.T) {
	var authFilesCalls int
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "delete" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalled {
		t.Fatal("manual delete executed after auth_index changed")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success || result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Results[0].Action != "delete" ||
		result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed ||
		result.Detail.Results[0].ActionError == "" {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
	validationLog := requireInspectionLog(t, result.Detail.Logs, "手动处理账号校验失败")
	validationDetail := requireInspectionLogDetail(t, validationLog)
	if validationLog.Level != "error" || validationDetail["action"] != "delete" ||
		validationDetail["error"] == "" {
		t.Fatalf("manual identity validation log = level=%q detail=%#v", validationLog.Level, validationDetail)
	}
}

func TestRunAutoActionsRejectsChangedAuthIdentity(t *testing.T) {
	var authFilesCalls int
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if authFilesCalls != 2 {
		t.Fatalf("auth files calls = %d, want inspection plus action validation", authFilesCalls)
	}
	if deleteCalled {
		t.Fatal("automatic delete executed after auth identity changed")
	}
	if len(detail.Results) != 1 ||
		detail.Results[0].Action != "delete" ||
		detail.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed ||
		!strings.Contains(detail.Results[0].ActionError, "账号标识已变化") {
		t.Fatalf("result = %#v, want failed automatic identity validation", detail.Results)
	}
	validationLog := requireInspectionLog(t, detail.Logs, "自动处理账号校验失败")
	validationDetail := requireInspectionLogDetail(t, validationLog)
	if validationLog.Level != "error" || validationDetail["action"] != "delete" {
		t.Fatalf("automatic validation log = level=%q detail=%#v", validationLog.Level, validationDetail)
	}
}

func TestRunAutoDisableSkipsAlreadyDisabledCurrentAuthFile(t *testing.T) {
	var authFilesCalls int
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			disabled := authFilesCalls > 1
			_, _ = fmt.Fprintf(w, `{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":%t,"status":"ok","state":"ready"}]}`, disabled)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if patchCalled {
		t.Fatal("automatic disable repeated an already applied state")
	}
	if len(detail.Results) != 1 ||
		detail.Results[0].Action != "delete" ||
		detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSkipped ||
		detail.Results[0].ExecutedAction != "" ||
		!detail.Results[0].Disabled ||
		!strings.Contains(detail.Results[0].ActionError, "已是禁用状态") {
		t.Fatalf("result = %#v, want skipped disable with current state", detail.Results)
	}
	skippedLog := requireInspectionLog(t, detail.Logs, "自动处理账号跳过")
	skippedDetail := requireInspectionLogDetail(t, skippedLog)
	if skippedLog.Level != "info" || skippedDetail["action"] != "disable" {
		t.Fatalf("automatic skipped log = level=%q detail=%#v", skippedLog.Level, skippedDetail)
	}
}

func TestExecuteManualActionsRejectsMissingAuthFile(t *testing.T) {
	var authFilesCalls int
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "disable" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if patchCalled {
		t.Fatal("manual disable patched a missing auth file")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success || result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed ||
		result.Detail.Results[0].ActionError == "" {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
	completion := requireInspectionLog(t, result.Detail.Logs, "手动处理账号完成")
	if completion.Level != "warning" {
		t.Fatalf("manual action completion level = %q, want warning", completion.Level)
	}
}

func TestExecuteManualActionsRecordsAuthFileRefreshFailure(t *testing.T) {
	var authFilesCalls int
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			http.Error(w, "auth files unavailable", http.StatusServiceUnavailable)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if patchCalled {
		t.Fatal("manual action executed after auth-file refresh failed")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success ||
		result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed ||
		!strings.Contains(result.Outcomes[0].Error, "刷新认证文件失败") {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	validation := requireInspectionLog(t, result.Detail.Logs, "手动处理账号校验失败")
	if validation.Level != "error" {
		t.Fatalf("validation level = %q, want error", validation.Level)
	}
	completion := requireInspectionLog(t, result.Detail.Logs, "手动处理账号完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "warning" || completionDetail["failedCount"] != float64(1) {
		t.Fatalf("completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestExecuteManualActionsSkipsDuplicateFileNameSelections(t *testing.T) {
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"auth-a.json","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"id":"runtime-auth-2","name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 2 {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID, runDetail.Results[1].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	statuses := map[string]int{}
	for _, outcome := range result.Outcomes {
		statuses[outcome.Status]++
	}
	if statuses[model.CodexInspectionActionStatusSuccess] != 1 ||
		statuses[model.CodexInspectionActionStatusSkipped] != 1 {
		t.Fatalf("outcome statuses = %#v", result.Outcomes)
	}
	skippedLog := requireInspectionLog(t, result.Detail.Logs, "手动处理账号跳过")
	if skippedLog.Level != "info" {
		t.Fatalf("manual duplicate skip level = %q, want info", skippedLog.Level)
	}
	completion := requireInspectionLog(t, result.Detail.Logs, "手动处理账号完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "success" ||
		completionDetail["successCount"] != float64(1) ||
		completionDetail["skippedCount"] != float64(1) ||
		completionDetail["needsReviewCount"] != float64(0) ||
		completionDetail["failedCount"] != float64(0) {
		t.Fatalf("manual duplicate completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
}

func TestSelectManualActionItemsUsesFirstSelectedResultForGroup(t *testing.T) {
	results := []model.CodexInspectionResult{
		{ID: 1, FileName: "auth-a.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1", Action: "disable"},
		{ID: 2, FileName: "auth-a.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1", Action: "disable"},
	}
	items, outcomes := selectManualActionItems(results, map[int64]struct{}{2: {}})
	if len(items) != 1 || items[0].ID != 2 {
		t.Fatalf("selected items = %#v, want result 2", items)
	}
	if len(outcomes) != 0 {
		t.Fatalf("preflight outcomes = %#v, want none", outcomes)
	}
}

func TestRunFinalizesAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			cancel()
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(ctx, RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection after cancellation: %v", err)
	}
	if result.Run.Status != model.CodexInspectionStatusFailed {
		t.Fatalf("run status = %q, want failed: %#v", result.Run.Status, result.Run)
	}

	runs, err := db.ListCodexInspectionRuns(context.Background(), 1)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[0].Status != model.CodexInspectionStatusFailed || runs[0].FinishedAtMS == 0 {
		t.Fatalf("persisted run was not marked failed: %#v", runs[0])
	}
}

func TestRunCancelsDuringAutomaticActionWithoutReportingCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			cancel()
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	managerCfg.CodexInspection.AutoRecoverEnabled = true
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:        "auth-a.json",
		Provider:        "codex",
		AuthIndex:       "auth-1",
		AccountSnapshot: "alice@example.com",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(ctx, RunRequest{TriggerType: "scheduled", TriggerKey: "interval:30:1"})
	if err != nil {
		t.Fatalf("run inspection after automatic action cancellation: %v", err)
	}
	if !patchCalled {
		t.Fatal("automatic action was not started")
	}
	if result.Run.Status != model.CodexInspectionStatusFailed || !strings.Contains(result.Run.Error, context.Canceled.Error()) {
		t.Fatalf("run after automatic action cancellation = %#v", result.Run)
	}
	if len(result.Results) != 1 || result.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed {
		t.Fatalf("result after automatic action cancellation = %#v", result.Results)
	}
	requireInspectionLog(t, result.Logs, "自动处理账号失败")
	requireInspectionLog(t, result.Logs, "凭证健康巡检已取消")
	for _, entry := range result.Logs {
		if entry.Message == "凭证健康巡检完成" {
			t.Fatalf("cancelled run emitted completion log: %#v", entry)
		}
	}
}

func TestValidateActionItemsRequiresCompleteSourceFileCoverage(t *testing.T) {
	var patchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"shared.json","name":"shared.json","auth_index":"auth-1","provider":"codex","account_id":"account-1","account":"alice@example.com","disabled":false},{"id":"runtime-auth-2","name":"shared.json","auth_index":"auth-2","provider":"codex","account_id":"account-2","account":"bob@example.com","disabled":false}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patchCalls++
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	source := model.CodexInspectionResult{
		ID:         1,
		AccountKey: "source",
		FileName:   "shared.json",
		Provider:   "codex",
		AuthIndex:  "auth-1",
		AccountID:  "account-1",
		Action:     "disable",
	}
	child := model.CodexInspectionResult{
		ID:         2,
		AccountKey: "child",
		FileName:   "shared.json",
		Provider:   "codex",
		AuthIndex:  "auth-2",
		AccountID:  "account-2",
		Action:     "disable",
	}
	tests := []struct {
		name              string
		items             []model.CodexInspectionResult
		wantValid         int
		wantMembers       int
		wantOutcomeStatus map[string]string
	}{
		{
			name:        "complete consistent group",
			items:       []model.CodexInspectionResult{source, child},
			wantValid:   1,
			wantMembers: 2,
			wantOutcomeStatus: map[string]string{
				"child": model.CodexInspectionActionStatusSkipped,
			},
		},
		{
			name:        "source only",
			items:       []model.CodexInspectionResult{source},
			wantValid:   0,
			wantMembers: 0,
			wantOutcomeStatus: map[string]string{
				"source": model.CodexInspectionActionStatusNeedsReview,
			},
		},
		{
			name: "mixed actions",
			items: []model.CodexInspectionResult{
				source,
				func() model.CodexInspectionResult {
					item := child
					item.Action = "enable"
					return item
				}(),
			},
			wantValid:   0,
			wantMembers: 0,
			wantOutcomeStatus: map[string]string{
				"source": model.CodexInspectionActionStatusNeedsReview,
				"child":  model.CodexInspectionActionStatusNeedsReview,
			},
		},
		{
			name:        "expanded child only",
			items:       []model.CodexInspectionResult{child},
			wantValid:   0,
			wantMembers: 0,
			wantOutcomeStatus: map[string]string{
				"child": model.CodexInspectionActionStatusNeedsReview,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
			valid, sourceMembers, outcomes, err := svc.validateActionItems(
				context.Background(),
				context.Background(),
				store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
				tt.items,
				tt.items,
				runLogger{},
				"测试",
				func(item model.CodexInspectionResult) string { return item.Action },
			)
			if err != nil {
				t.Fatalf("validate action items: %v", err)
			}
			if len(valid) != tt.wantValid {
				t.Fatalf("valid items = %#v, want %d", valid, tt.wantValid)
			}
			memberCount := 0
			for _, members := range sourceMembers {
				memberCount += len(members)
			}
			if memberCount != tt.wantMembers {
				t.Fatalf("source members = %#v, want %d total", sourceMembers, tt.wantMembers)
			}
			gotStatuses := make(map[string]string, len(outcomes))
			for _, outcome := range outcomes {
				gotStatuses[outcome.AccountKey] = outcome.Status
			}
			if len(gotStatuses) != len(tt.wantOutcomeStatus) {
				t.Fatalf("outcomes = %#v, want statuses %#v", outcomes, tt.wantOutcomeStatus)
			}
			for accountKey, wantStatus := range tt.wantOutcomeStatus {
				if gotStatuses[accountKey] != wantStatus {
					t.Fatalf("outcome statuses = %#v, want %#v", gotStatuses, tt.wantOutcomeStatus)
				}
			}
		})
	}
	if patchCalls != 0 {
		t.Fatalf("validation issued %d status patches, want 0", patchCalls)
	}
}

func TestValidateActionItemsPlansCompleteSharedFileWithoutSourceRuntimeRow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"id":"virtual-a","name":"source.json","auth_index":"auth-a","provider":"gemini-cli","account_id":"project-a","disabled":false},{"id":"virtual-b","name":"source.json","auth_index":"auth-b","provider":"gemini-cli","account_id":"project-b","disabled":false}]}`))
	}))
	t.Cleanup(upstream.Close)

	items := []model.CodexInspectionResult{
		{ID: 1, AccountKey: "project-a", FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-a", AccountID: "project-a", Action: "disable"},
		{ID: 2, AccountKey: "project-b", FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-b", AccountID: "project-b", Action: "disable"},
	}
	valid, sourceMembers, outcomes, err := New(newCodexInspectionTestStore(t), nil, upstream.Client()).validateActionItems(
		context.Background(),
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		items,
		items,
		runLogger{},
		"测试",
		func(item model.CodexInspectionResult) string { return item.Action },
	)
	if err != nil {
		t.Fatalf("validate action items: %v", err)
	}
	if len(valid) != 1 || valid[0].AccountKey != "project-a" {
		t.Fatalf("valid items = %#v, want project-a as canonical", valid)
	}
	members := sourceMembers[inspectionActionIdentityKey(valid[0])]
	if len(members) != 2 {
		t.Fatalf("source members = %#v, want both virtual children", sourceMembers)
	}
	if len(outcomes) != 1 || outcomes[0].AccountKey != "project-b" ||
		outcomes[0].Status != model.CodexInspectionActionStatusSkipped {
		t.Fatalf("outcomes = %#v, want project-b skipped as covered", outcomes)
	}
}

func TestExecuteActionSharedFileWithoutSourceRuntimeRow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		action        string
		automatic     bool
		initiallyOff  bool
		seedOwnership bool
		wantOwnership int
	}{
		{name: "manual disable", action: "disable", initiallyOff: false},
		{name: "manual enable", action: "enable", initiallyOff: true, seedOwnership: true},
		{name: "automatic disable", action: "disable", automatic: true, initiallyOff: false, wantOwnership: 2},
		{name: "automatic enable", action: "enable", automatic: true, initiallyOff: true, seedOwnership: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patches := make([]string, 0, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
					_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
						{"id": "virtual-a", "name": "source.json", "auth_index": "auth-a", "provider": "gemini-cli", "account_id": "project-a", "disabled": tc.initiallyOff},
						{"id": "virtual-b", "name": "source.json", "auth_index": "auth-b", "provider": "gemini-cli", "account_id": "project-b", "disabled": tc.initiallyOff},
					}})
				case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
					var payload struct {
						Name      string `json:"name"`
						AuthIndex string `json:"auth_index"`
						Disabled  bool   `json:"disabled"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode status payload: %v", err)
					}
					patches = append(patches, payload.Name)
					if payload.Name == "virtual-a" {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": "plugin virtual auth cannot be modified directly; edit or delete the source auth file"})
						return
					}
					if payload.Name != "source.json" || payload.AuthIndex != "auth-a" || payload.Disabled != (tc.action == "disable") {
						t.Fatalf("source patch payload = %#v", payload)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": payload.Disabled})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			if tc.seedOwnership {
				for _, authIndex := range []string{"auth-a", "auth-b"} {
					if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
						FileName:  "source.json",
						Provider:  "gemini-cli",
						AuthIndex: authIndex,
					}); err != nil {
						t.Fatalf("seed ownership: %v", err)
					}
				}
			}
			item := model.CodexInspectionResult{
				FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-a", AccountID: "project-a", Action: tc.action,
			}
			members := []model.CodexInspectionResult{
				item,
				{FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-b", AccountID: "project-b", Action: tc.action},
			}
			if err := New(db, nil, upstream.Client()).executeAction(
				context.Background(),
				store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
				item,
				members,
				tc.automatic,
			); err != nil {
				t.Fatalf("execute shared-file action: %v", err)
			}
			if !reflect.DeepEqual(patches, []string{"virtual-a", "source.json"}) {
				t.Fatalf("status patch selectors = %#v, want runtime then physical source", patches)
			}
			ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
			if err != nil {
				t.Fatalf("list ownership: %v", err)
			}
			if len(ownership) != tc.wantOwnership {
				t.Fatalf("ownership = %#v, want %d records", ownership, tc.wantOwnership)
			}
		})
	}
}

func TestExecuteActionSharedOrdinaryFilePatchesEveryRuntimeCredential(t *testing.T) {
	patches := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-a","name":"shared.json","auth_index":"auth-a","provider":"codex","account_id":"account-a"},{"id":"runtime-b","name":"shared.json","auth_index":"auth-b","provider":"codex","account_id":"account-b"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patches = append(patches, payload.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	item := model.CodexInspectionResult{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-a", AccountID: "account-a", Action: "disable"}
	members := []model.CodexInspectionResult{
		item,
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-b", AccountID: "account-b", Action: "disable"},
	}
	if err := New(newCodexInspectionTestStore(t), nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		item,
		members,
		false,
	); err != nil {
		t.Fatalf("execute ordinary shared-file action: %v", err)
	}
	if !reflect.DeepEqual(patches, []string{"runtime-a", "runtime-b"}) {
		t.Fatalf("status patch selectors = %#v, want both runtime credentials", patches)
	}
}

func TestExecuteActionItemsSerializesSamePhysicalFile(t *testing.T) {
	firstPatchStarted := make(chan struct{})
	secondPatchStarted := make(chan struct{})
	var firstPatchOnce sync.Once
	var secondPatchOnce sync.Once
	var stateMu sync.Mutex
	activePatches := 0
	maxActivePatches := 0
	patchCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-a","name":"shared.json","auth_index":"auth-a","provider":"codex","account_id":"account-a"},{"id":"runtime-b","name":"shared.json","auth_index":"auth-b","provider":"codex","account_id":"account-b"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			stateMu.Lock()
			patchCalls++
			call := patchCalls
			activePatches++
			if activePatches > maxActivePatches {
				maxActivePatches = activePatches
			}
			stateMu.Unlock()
			if call == 1 {
				firstPatchOnce.Do(func() { close(firstPatchStarted) })
				select {
				case <-secondPatchStarted:
				case <-time.After(150 * time.Millisecond):
				}
			} else {
				secondPatchOnce.Do(func() { close(secondPatchStarted) })
			}
			stateMu.Lock()
			activePatches--
			stateMu.Unlock()
			_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	settings := model.DefaultCodexInspectionConfig()
	settings.DeleteWorkers = 2
	items := []model.CodexInspectionResult{
		{ID: 1, AccountKey: "account-a", FileName: "shared.json", Provider: "codex", AuthIndex: "auth-a", AccountID: "account-a", Action: "disable"},
		{ID: 2, AccountKey: "account-b", FileName: "shared.json", Provider: "codex", AuthIndex: "auth-b", AccountID: "account-b", Action: "enable"},
	}
	outcomes := New(newCodexInspectionTestStore(t), nil, upstream.Client()).executeActionItems(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		settings,
		items,
		nil,
		runLogger{},
		"测试",
		false,
		nil,
	)
	<-firstPatchStarted
	if len(outcomes) != 2 || !outcomes[0].Success || !outcomes[1].Success {
		t.Fatalf("outcomes = %#v, want two successes", outcomes)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if patchCalls != 2 || maxActivePatches != 1 {
		t.Fatalf("same-file patches calls=%d maxActive=%d, want 2 and 1", patchCalls, maxActivePatches)
	}
}

func TestExecuteActionSharedOrdinaryFileRollsBackPartialFailure(t *testing.T) {
	disabled := map[string]bool{"runtime-a": false, "runtime-b": false}
	type statusPatch struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}
	patches := make([]statusPatch, 0, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"id": "runtime-a", "name": "shared.json", "auth_index": "auth-a", "provider": "codex", "account_id": "account-a", "disabled": disabled["runtime-a"]},
				{"id": "runtime-b", "name": "shared.json", "auth_index": "auth-b", "provider": "codex", "account_id": "account-b", "disabled": disabled["runtime-b"]},
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload statusPatch
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patches = append(patches, payload)
			if payload.Name == "runtime-b" && payload.Disabled {
				http.Error(w, "forced second credential failure", http.StatusInternalServerError)
				return
			}
			disabled[payload.Name] = payload.Disabled
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	item := model.CodexInspectionResult{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-a", AccountID: "account-a", Action: "disable"}
	members := []model.CodexInspectionResult{
		item,
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-b", AccountID: "account-b", Action: "disable"},
	}
	err := New(newCodexInspectionTestStore(t), nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		item,
		members,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "forced second credential failure") {
		t.Fatalf("execute partial failure error = %v", err)
	}
	wantPatches := []statusPatch{
		{Name: "runtime-a", Disabled: true},
		{Name: "runtime-b", Disabled: true},
		{Name: "runtime-a", Disabled: false},
	}
	if !reflect.DeepEqual(patches, wantPatches) {
		t.Fatalf("status patches = %#v, want %#v", patches, wantPatches)
	}
	if disabled["runtime-a"] || disabled["runtime-b"] {
		t.Fatalf("disabled state after rollback = %#v, want both enabled", disabled)
	}
}

func TestExecuteActionPluginSourceFallbackRejectsMembershipGrowth(t *testing.T) {
	getCalls := 0
	patches := make([]string, 0, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			getCalls++
			files := []map[string]any{
				{"id": "virtual-a", "name": "source.json", "auth_index": "auth-a", "provider": "gemini-cli", "account_id": "project-a"},
				{"id": "virtual-b", "name": "source.json", "auth_index": "auth-b", "provider": "gemini-cli", "account_id": "project-b"},
			}
			if getCalls > 1 {
				files = append(files, map[string]any{"id": "virtual-c", "name": "source.json", "auth_index": "auth-c", "provider": "gemini-cli", "account_id": "project-c"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patches = append(patches, payload.Name)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "plugin virtual auth cannot be modified directly; edit or delete the source auth file"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	item := model.CodexInspectionResult{FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-a", AccountID: "project-a", Action: "disable"}
	members := []model.CodexInspectionResult{
		item,
		{FileName: "source.json", Provider: "gemini-cli", AuthIndex: "auth-b", AccountID: "project-b", Action: "disable"},
	}
	err := New(newCodexInspectionTestStore(t), nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		item,
		members,
		false,
	)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("membership growth error = %v, want ErrIdentityMismatch", err)
	}
	if !reflect.DeepEqual(patches, []string{"virtual-a"}) {
		t.Fatalf("status patch selectors = %#v, want no physical source fallback", patches)
	}
}

func TestExecuteActionSourceFileDisablePersistsEveryMemberOwnership(t *testing.T) {
	var patchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"shared.json","name":"shared.json","auth_index":"auth-1","provider":"codex","account_id":"account-1"},{"id":"runtime-auth-2","name":"shared.json","auth_index":"auth-2","provider":"codex","account_id":"account-2"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Disabled  bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			if payload.Name != "shared.json" || payload.AuthIndex != "auth-1" || !payload.Disabled {
				t.Fatalf("source-file patch payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	for _, ownership := range []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex"},
		{FileName: "other.json", Provider: "codex", AuthIndex: "other-auth"},
	} {
		if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), ownership); err != nil {
			t.Fatalf("save initial ownership %#v: %v", ownership, err)
		}
	}
	source := model.CodexInspectionResult{
		FileName:  "shared.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Action:    "disable",
	}
	members := []model.CodexInspectionResult{
		source,
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2", Action: "disable"},
	}
	if err := New(db, nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		source,
		members,
		true,
	); err != nil {
		t.Fatalf("execute grouped source-file disable: %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("source-file status patch calls = %d, want 1", patchCalls)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	owned := make(map[string]model.CodexInspectionDisableOwnership, len(ownership))
	for _, item := range ownership {
		owned[item.FileName+"::"+item.AuthIndex] = item
	}
	if len(owned) != 3 || owned["shared.json::auth-1"].AccountID != "account-1" || owned["shared.json::auth-2"].AccountID != "account-2" {
		t.Fatalf("ownership after grouped disable = %#v", ownership)
	}
	if _, ok := owned["shared.json::"]; ok {
		t.Fatalf("legacy source-file ownership survived grouped disable: %#v", ownership)
	}
	if _, ok := owned["other.json::other-auth"]; !ok {
		t.Fatalf("unrelated ownership was removed: %#v", ownership)
	}
}

func TestExecuteActionAutomaticDisableDoesNotClaimAlreadyDisabledCredential(t *testing.T) {
	patchCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"account-1","disabled":true}]`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patchCalls++
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	err := New(db, nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.CodexInspectionResult{
			FileName:  "auth-a.json",
			Provider:  "codex",
			AuthIndex: "auth-1",
			AccountID: "account-1",
			Action:    "disable",
		},
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "already disabled") {
		t.Fatalf("automatic disable race error = %v, want already-disabled refusal", err)
	}
	if patchCalls != 0 {
		t.Fatalf("status patch calls = %d, want 0", patchCalls)
	}
	ownership, listErr := db.ListCodexInspectionDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	if len(ownership) != 0 {
		t.Fatalf("automatic disable claimed manual state: %#v", ownership)
	}
}

func TestExecuteActionSourceFileAutomaticDisableRejectsDisabledMember(t *testing.T) {
	patchCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`[{"id":"shared.json","name":"shared.json","auth_index":"auth-1","provider":"codex","account_id":"account-1","disabled":false},{"id":"runtime-auth-2","name":"shared.json","auth_index":"auth-2","provider":"codex","account_id":"account-2","disabled":true}]`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patchCalls++
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	source := model.CodexInspectionResult{
		FileName:  "shared.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Action:    "disable",
	}
	err := New(db, nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		source,
		[]model.CodexInspectionResult{
			source,
			{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2", Action: "disable"},
		},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "already disabled") {
		t.Fatalf("source-file automatic disable error = %v, want disabled-member refusal", err)
	}
	if patchCalls != 0 {
		t.Fatalf("source-file status patch calls = %d, want 0", patchCalls)
	}
	ownership, listErr := db.ListCodexInspectionDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	if len(ownership) != 0 {
		t.Fatalf("source-file automatic disable claimed mixed state: %#v", ownership)
	}
}

func TestExecuteActionSourceFileOwnershipFailureRollsBackAndRestoresSnapshot(t *testing.T) {
	type statusPatch struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	patches := make([]statusPatch, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"id":"shared.json","name":"shared.json","auth_index":"auth-1","provider":"codex","account_id":"account-1"},{"id":"runtime-auth-2","name":"shared.json","auth_index":"auth-2","provider":"codex","account_id":"account-2"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload statusPatch
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patches = append(patches, payload)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	initial := []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AuthIndex: "legacy-auth", AccountID: "legacy-account"},
		{FileName: "other.json", Provider: "codex", AuthIndex: "other-auth"},
	}
	for _, ownership := range initial {
		if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), ownership); err != nil {
			t.Fatalf("save initial ownership %#v: %v", ownership, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	db.CodexInspections = &failDisableOwnershipBatchRepository{
		Repository: db.CodexInspections,
		cancel:     cancel,
	}
	source := model.CodexInspectionResult{
		FileName:  "shared.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Action:    "disable",
	}
	err := New(db, nil, upstream.Client()).executeAction(
		ctx,
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		source,
		[]model.CodexInspectionResult{
			source,
			{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2", Action: "disable"},
		},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "forced grouped ownership write failure") {
		t.Fatalf("execute grouped source-file disable error = %v", err)
	}
	if strings.Contains(err.Error(), "rollback source-file enable failed") ||
		strings.Contains(err.Error(), "revoke partial source-file ownership failed") ||
		strings.Contains(err.Error(), "restore inspection disable ownership failed") {
		t.Fatalf("grouped rollback did not complete cleanly: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("action context error = %v, want cancelled", ctx.Err())
	}
	if len(patches) != 2 || !patches[0].Disabled || patches[1].Disabled {
		t.Fatalf("source-file patches = %#v, want disable then rollback enable", patches)
	}
	for _, patch := range patches {
		if patch.Name != "shared.json" || patch.AuthIndex != "auth-1" {
			t.Fatalf("source-file patch target = %#v", patch)
		}
	}
	ownership, listErr := db.ListCodexInspectionDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	owned := make(map[string]model.CodexInspectionDisableOwnership, len(ownership))
	for _, item := range ownership {
		owned[item.FileName+"::"+item.AuthIndex] = item
	}
	if len(owned) != 2 || owned["shared.json::legacy-auth"].AccountID != "legacy-account" {
		t.Fatalf("ownership after grouped rollback = %#v, want original snapshot", ownership)
	}
	if _, ok := owned["other.json::other-auth"]; !ok {
		t.Fatalf("unrelated ownership changed during grouped rollback: %#v", ownership)
	}
}

func TestExecuteActionReturnsPatchError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"account-1"}]`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload struct {
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.AuthIndex != "auth-1" {
				t.Fatalf("patch auth_index = %q, want auth-1", payload.AuthIndex)
			}
			http.Error(w, "status patch failed", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-2",
	}); err != nil {
		t.Fatalf("save sibling inspection ownership: %v", err)
	}
	svc := New(db, nil, upstream.Client())
	err := svc.executeAction(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
		Action:    "disable",
	}, nil, false)
	if err == nil {
		t.Fatal("execute action succeeded, want patch error")
	}
	message := err.Error()
	if !strings.Contains(message, "status patch failed") {
		t.Fatalf("patch error = %q", message)
	}
	ownership, listErr := db.ListCodexInspectionDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	if len(ownership) != 2 || ownership[0].AuthIndex != "auth-1" || ownership[1].AuthIndex != "auth-2" {
		t.Fatalf("ownership after failed patch = %#v, want preserved", ownership)
	}
}

func TestExecuteActionDeleteRevalidatesSamePathIdentity(t *testing.T) {
	deleteCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_, _ = w.Write([]byte(`[{"id":"runtime-replacement","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"replacement-account"}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v0/management/auth-files":
			deleteCalls++
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
	err := svc.executeAction(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "original-account",
		Action:    "delete",
	}, nil, false)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("delete replacement error = %v, want ErrIdentityMismatch", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", deleteCalls)
	}
}

func TestExecuteActionRollsBackAutomaticDisableAfterOwnershipWriteCancelsContext(t *testing.T) {
	type statusPatch struct {
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	patches := make([]statusPatch, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex"}]`))
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/v0/management/auth-files/status" {
			http.NotFound(w, r)
			return
		}
		var payload statusPatch
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode patch payload: %v", err)
		}
		patches = append(patches, payload)
		_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
	}))
	t.Cleanup(upstream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	db := newCodexInspectionTestStore(t)
	db.CodexInspections = &failDisableOwnershipUpsertRepository{
		Repository: db.CodexInspections,
		cancel:     cancel,
	}
	svc := New(db, nil, upstream.Client())
	err := svc.executeAction(ctx, store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		Action:    "disable",
	}, nil, true)
	if err == nil || !strings.Contains(err.Error(), "forced ownership write failure") {
		t.Fatalf("execute action error = %v, want ownership write failure", err)
	}
	if strings.Contains(err.Error(), "rollback enable failed") {
		t.Fatalf("rollback reused cancelled context: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("action context error = %v, want cancelled", ctx.Err())
	}
	if len(patches) != 2 {
		t.Fatalf("status patches = %#v, want disable and rollback enable", patches)
	}
	if patches[0].AuthIndex != "auth-1" || !patches[0].Disabled {
		t.Fatalf("disable patch = %#v", patches[0])
	}
	if patches[1].AuthIndex != "auth-1" || patches[1].Disabled {
		t.Fatalf("rollback patch = %#v", patches[1])
	}
}

func TestExecuteActionDoesNotRollbackAutomaticDisableOntoSamePathReplacement(t *testing.T) {
	type statusPatch struct {
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	getCalls := 0
	patches := make([]statusPatch, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files" {
			getCalls++
			accountID := "account-1"
			runtimeID := "runtime-auth-1"
			if getCalls > 1 {
				accountID = "replacement-account"
				runtimeID = "runtime-replacement"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         runtimeID,
				"name":       "auth-a.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account_id": accountID,
				"disabled":   getCalls > 1,
			}})
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/v0/management/auth-files/status" {
			http.NotFound(w, r)
			return
		}
		var payload statusPatch
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode patch payload: %v", err)
		}
		patches = append(patches, payload)
		_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
	}))
	t.Cleanup(upstream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	db := newCodexInspectionTestStore(t)
	db.CodexInspections = &failDisableOwnershipUpsertRepository{
		Repository: db.CodexInspections,
		cancel:     cancel,
	}
	err := New(db, nil, upstream.Client()).executeAction(ctx, store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Action:    "disable",
	}, nil, true)
	if err == nil || !strings.Contains(err.Error(), "forced ownership write failure") ||
		!strings.Contains(err.Error(), "rollback enable failed") ||
		!errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("execute replacement rollback error = %v", err)
	}
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and rollback verification", getCalls)
	}
	if len(patches) != 1 || !patches[0].Disabled {
		t.Fatalf("status patches = %#v, replacement must not be enabled", patches)
	}
}

func TestExecuteActionClearsCompatibleLegacyOwnership(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"account-1"}]`))
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/v0/management/auth-files/status" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			AuthIndex string `json:"auth_index"`
			Disabled  bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode patch payload: %v", err)
		}
		if payload.AuthIndex != "auth-1" || !payload.Disabled {
			t.Fatalf("patch payload = %#v", payload)
		}
		_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	for _, item := range []model.CodexInspectionDisableOwnership{
		{FileName: "auth-a.json", Provider: "codex"},
		{FileName: "auth-a.json", Provider: "codex", AuthIndex: "auth-2"},
	} {
		if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), item); err != nil {
			t.Fatalf("save inspection disable ownership %#v: %v", item, err)
		}
	}

	svc := New(db, nil, upstream.Client())
	if err := svc.executeAction(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName:  "auth-a.json",
		Provider:  "codex",
		AuthIndex: "auth-1",
		AccountID: "account-1",
		Action:    "disable",
	}, nil, false); err != nil {
		t.Fatalf("execute action: %v", err)
	}

	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].AuthIndex != "auth-2" {
		t.Fatalf("ownership after manual disable = %#v, want auth-2 only", ownership)
	}
}

func TestCodexInspectionScheduleDue(t *testing.T) {
	enabled := true
	now := mustParseTime(t, "2026-05-22T10:30:00+08:00")

	intervalCfg := model.DefaultCodexInspectionConfig()
	intervalCfg.Enabled = &enabled
	intervalCfg.Schedule.Mode = model.CodexInspectionScheduleModeInterval
	intervalCfg.Schedule.IntervalMinutes = 30
	if !model.CodexInspectionScheduleDue(now, mustParseTime(t, "2026-05-22T09:59:00+08:00"), intervalCfg) {
		t.Fatal("expected interval schedule to be due")
	}

	timePointCfg := model.DefaultCodexInspectionConfig()
	timePointCfg.Enabled = &enabled
	timePointCfg.Schedule.Mode = model.CodexInspectionScheduleModeTimePoints
	timePointCfg.Schedule.TimePoints = []string{"10:30", "18:00"}
	timePointCfg.Schedule.TimeZone = "Asia/Shanghai"
	if !model.CodexInspectionScheduleDue(now, time.Time{}, timePointCfg) {
		t.Fatal("expected time_points schedule to be due")
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

type mixedAutoActionFixture string

const (
	mixedAutoActionFixtureEnableDelete  mixedAutoActionFixture = "enable_delete"
	mixedAutoActionFixtureDisableDelete mixedAutoActionFixture = "disable_delete"
	mixedAutoActionFixtureDeleteDelete  mixedAutoActionFixture = "delete_delete"
)

func runMixedAutoActionInspection(t *testing.T, mode string, fixture mixedAutoActionFixture) RunDetail {
	t.Helper()
	var deleteCalled bool
	var patchCalled bool
	upstream := newMixedAutoActionServer(t, fixture, &deleteCalled, &patchCalled)
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = mode
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled || patchCalled {
		t.Fatalf("mixed same-file actions executed delete=%v patch=%v, want false/false", deleteCalled, patchCalled)
	}
	completion := requireInspectionLog(t, result.Logs, "凭证健康巡检完成")
	completionDetail := requireInspectionLogDetail(t, completion)
	if completion.Level != "warning" ||
		completionDetail["actionNeedsReviewCount"] != float64(2) ||
		completionDetail["actionSuccessCount"] != float64(0) ||
		completionDetail["actionSkippedCount"] != float64(0) ||
		completionDetail["actionFailedCount"] != float64(0) {
		t.Fatalf("mixed automatic completion = level=%q detail=%#v", completion.Level, completionDetail)
	}
	preflight := requireInspectionLog(t, result.Logs, "自动处理账号跳过")
	preflightDetail := requireInspectionLogDetail(t, preflight)
	if preflight.Level != "warning" ||
		preflightDetail["status"] != model.CodexInspectionActionStatusNeedsReview ||
		preflightDetail["reason"] == "" {
		t.Fatalf("mixed automatic preflight = level=%q detail=%#v", preflight.Level, preflightDetail)
	}
	return result
}

func newMixedAutoActionServer(
	t *testing.T,
	fixture mixedAutoActionFixture,
	deleteCalled *bool,
	patchCalled *bool,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			switch fixture {
			case mixedAutoActionFixtureEnableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			case mixedAutoActionFixtureDisableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			case mixedAutoActionFixtureDeleteDelete:
				_, _ = w.Write([]byte(`{"files":[{"id":"auth-a.json","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"id":"runtime-auth-2","name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			default:
				t.Fatalf("unexpected mixed fixture %q", fixture)
			}
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				AuthIndex string `json:"authIndex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			switch payload.AuthIndex {
			case "auth-1":
				if fixture == mixedAutoActionFixtureDisableDelete {
					_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
					return
				}
				if fixture == mixedAutoActionFixtureDeleteDelete {
					_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
					return
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
			case "auth-2":
				_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
			default:
				t.Fatalf("unexpected authIndex %q", payload.AuthIndex)
			}
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			*deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			*patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertMixedNeedsReviewRun(t *testing.T, result RunDetail, firstAction string, secondAction string) {
	t.Helper()
	if result.Run.EnableCount != boolToInt(firstAction == "enable")+boolToInt(secondAction == "enable") ||
		result.Run.DisableCount != boolToInt(firstAction == "disable")+boolToInt(secondAction == "disable") ||
		result.Run.DeleteCount != boolToInt(firstAction == "delete")+boolToInt(secondAction == "delete") ||
		result.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d disable=%d delete=%d keep=%d",
			result.Run.EnableCount, result.Run.DisableCount, result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %#v, want 2", result.Results)
	}
	byAuthIndex := map[string]model.CodexInspectionResult{}
	for _, item := range result.Results {
		byAuthIndex[item.AuthIndex] = item
		if item.ActionStatus != model.CodexInspectionActionStatusNeedsReview ||
			item.ExecutedAction != "" ||
			!strings.Contains(item.ActionError, "多个不同建议动作") {
			t.Fatalf("mixed result = %#v, want needs_review with conflict reason", item)
		}
	}
	if byAuthIndex["auth-1"].Action != firstAction {
		t.Fatalf("auth-1 action = %q, want %s", byAuthIndex["auth-1"].Action, firstAction)
	}
	if byAuthIndex["auth-2"].Action != secondAction {
		t.Fatalf("auth-2 action = %q, want %s", byAuthIndex["auth-2"].Action, secondAction)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func newCodexInspectionManagerConfig(upstreamURL string) store.ManagerConfig {
	enabled := true
	cfg := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstreamURL,
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "auto",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
			QueryLimit:     50000,
		},
		CodexInspection: store.DefaultCodexInspectionConfig(),
	}
	cfg.CodexInspection.Enabled = &enabled
	cfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	cfg.CodexInspection.XAIInferenceEnabled = true
	cfg.CodexInspection.Workers = 1
	cfg.CodexInspection.DeleteWorkers = 1
	return cfg
}

func newCodexInspectionTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	testutil.EnsureAdminCredential(t, db)
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func newCodexInspectionTestService(t *testing.T, db *store.Store) *Service {
	t.Helper()
	cfg := config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CORSOrigins:   []string{"*"},
		CollectorMode: "auto",
	}
	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collector.New(manager)
	managerCfg := managerconfigsvc.New(cfg, db, collectorService)
	return New(db, managerCfg, &http.Client{})
}
