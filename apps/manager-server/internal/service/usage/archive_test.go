package usage

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageArchiveServiceFullLifecycle(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(4))
	ctx := context.Background()

	preview, err := service.PreviewArchive(ctx, 3_500)
	if err != nil {
		t.Fatalf("preview archive: %v", err)
	}
	if preview.EventCount != 3 || preview.TargetEventID != 3 {
		t.Fatalf("preview = %#v", preview)
	}
	created, err := service.CreateArchive(ctx, 3_500)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if created.Run.Status != usagearchive.StatusPreviewed || !validArchiveRunID(created.Run.ID) {
		t.Fatalf("created archive = %#v", created)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || len(archived.Segments) != 2 || archived.Run.ArchiveDigest == "" {
		t.Fatalf("archived = %#v", archived)
	}
	assertPrivateArchiveTree(t, archiveDirectory, archived)
	manifestPath := filepath.Join(archiveDirectory, filepath.FromSlash(archived.Run.ManifestFile))
	manifestPayload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read archive manifest: %v", err)
	}
	var manifest ArchiveManifest
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		t.Fatalf("decode archive manifest: %v", err)
	}
	if manifest.FirstEventID != 1 || manifest.LastEventID != 3 ||
		manifest.MinTimestampMS != 1_000 || manifest.MaxTimestampMS != 3_000 || len(manifest.Segments) != 2 {
		t.Fatalf("archive manifest range = %#v", manifest)
	}

	catchUpUsageAggregate(t, st)
	verified, err := service.VerifyArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if verified.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("verified = %#v", verified)
	}
	deleted, err := service.DeleteArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("delete archived usage: %v", err)
	}
	if deleted.Run.Status != usagearchive.StatusCompleted || deleted.Run.DeletedEventCount != 3 {
		t.Fatalf("deleted = %#v", deleted)
	}
	coverage, err := st.UsageArchives.RawCoverage(ctx, 1, 3_500)
	if err != nil {
		t.Fatalf("raw coverage: %v", err)
	}
	if coverage.RawEventCount != 0 || coverage.RawDeletedEventCount != 3 || coverage.MinDeletedTimestampMS != 1_000 || coverage.MaxDeletedTimestampMS != 3_000 {
		t.Fatalf("coverage = %#v", coverage)
	}
	eventCount, err := st.UsageEvents.Count(ctx)
	if err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("event count = %d, want hot event only", eventCount)
	}
	disabled := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		AggregateReadsEnabled: false,
	}))
	idempotent, err := disabled.DeleteArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("repeat completed delete with aggregate reads disabled: %v", err)
	}
	if idempotent.Run.Status != usagearchive.StatusCompleted || idempotent.Run.DeletedEventCount != 3 {
		t.Fatalf("idempotent completed delete = %#v", idempotent)
	}

	for _, segment := range deleted.Segments {
		path, err := service.archive.resolveArchivePath(segment.FileName)
		if err != nil {
			t.Fatalf("resolve segment: %v", err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open segment: %v", err)
		}
		zipper, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			t.Fatalf("open segment gzip: %v", err)
		}
		result, _, importErr := service.Import(ctx, zipper)
		closeErr := errors.Join(zipper.Close(), file.Close())
		if importErr != nil || closeErr != nil {
			t.Fatalf("re-import archived segment: %v", errors.Join(importErr, closeErr))
		}
		if result.Added != 0 || result.Skipped != int(segment.EventCount) {
			t.Fatalf("archive re-import result = %#v", result)
		}
	}
}

func TestUsageArchiveServiceRequiresCoverageReadyBeforeManualArchive(t *testing.T) {
	for _, migrationStatus := range []string{"pending", "failed"} {
		t.Run(migrationStatus, func(t *testing.T) {
			service, st, rawDB, archiveDirectory := newRawArchiveTestService(t, 1, 1)
			if _, err := st.InsertEvents(context.Background(), archiveTestServiceEvents(1)); err != nil {
				t.Fatalf("insert archive test event: %v", err)
			}
			if _, err := rawDB.ExecContext(context.Background(), `update usage_data_migrations set
				status = ?, last_error = case when ? = 'failed' then 'test failure' else null end
				where name = 'usage_cache_accounting_v2'`, migrationStatus, migrationStatus); err != nil {
				t.Fatalf("set migration status: %v", err)
			}

			created, err := service.CreateArchive(context.Background(), 2_000)
			if err != nil {
				t.Fatalf("create archive: %v", err)
			}
			if _, err := service.ResumeArchive(context.Background(), created.Run.ID); !errors.Is(err, ErrArchiveCoverageIncomplete) {
				t.Fatalf("resume archive error = %v, want coverage incomplete", err)
			}
			status, err := service.ArchiveStatus(context.Background(), created.Run.ID)
			if err != nil {
				t.Fatalf("archive status: %v", err)
			}
			if status.Run.Status != usagearchive.StatusPreviewed || len(status.Segments) != 0 {
				t.Fatalf("not-ready archive status = %#v", status)
			}
			files, err := filepath.Glob(filepath.Join(archiveDirectory, created.Run.ID, "*.jsonl.gz"))
			if err != nil {
				t.Fatalf("find archive segments: %v", err)
			}
			if len(files) != 0 {
				t.Fatalf("not-ready archive wrote segments: %#v", files)
			}
		})
	}
}

func TestArchiveStatusSummarySanitizesInternalMetadata(t *testing.T) {
	const secretPath = "/private/archive/run/manifest.json"
	status := ArchiveStatus{
		Run: store.UsageArchiveRun{
			ID:             strings.Repeat("a", 32),
			Mode:           usagearchive.RunModeManual,
			SchemaVersion:  99,
			Format:         "internal-format",
			Status:         usagearchive.StatusFailed,
			ArchiveDigest:  "internal-archive-digest",
			ManifestFile:   secretPath,
			ManifestSHA256: "internal-manifest-digest",
			LastError:      "open " + secretPath + ": permission denied",
		},
		Segments: []store.UsageArchiveSegment{{
			RunID:           strings.Repeat("a", 32),
			Sequence:        1,
			Status:          usagearchive.SegmentStatusPublished,
			FileName:        "/private/archive/run/segment.jsonl.gz",
			ContentSHA256:   "internal-content-digest",
			EventHashDigest: "internal-event-digest",
		}},
	}

	payload, err := json.Marshal(NewArchiveStatusSummary(status))
	if err != nil {
		t.Fatalf("marshal archive summary: %v", err)
	}
	response := string(payload)
	for _, forbidden := range []string{
		"raw_json",
		"fail_body",
		"schema_version",
		"format",
		"archive_digest",
		"manifest_file",
		"manifest_sha256",
		"file_name",
		"content_sha256",
		"event_hash_digest",
		"last_error",
		secretPath,
		"internal-content-digest",
	} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("archive summary leaked %q: %s", forbidden, response)
		}
	}
	if !strings.Contains(response, `"has_error":true`) {
		t.Fatalf("archive summary did not preserve safe error state: %s", response)
	}
}

func TestUsageMaintenanceRejectsStaleHourlyAggregateState(t *testing.T) {
	for _, test := range []struct {
		name       string
		updateSQL  string
		updateArgs []any
	}{
		{
			name: "legacy schema",
			updateSQL: `update usage_hourly_aggregate_state set
				schema_version = ?, status = 'ready', coverage_event_id = target_event_id
				where aggregate_name = ?`,
			updateArgs: []any{usageaggregate.SchemaVersion - 1, usageaggregate.AggregateName},
		},
		{
			name: "legacy structure revision",
			updateSQL: `update usage_hourly_aggregate_state set
				schema_version = ?, structure_revision = 'legacy', status = 'ready', coverage_event_id = target_event_id
				where aggregate_name = ?`,
			updateArgs: []any{usageaggregate.SchemaVersion, usageaggregate.AggregateName},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, rawDB, _ := newRawArchiveTestService(t, 1, 1)
			if _, err := rawDB.ExecContext(context.Background(), test.updateSQL, test.updateArgs...); err != nil {
				t.Fatalf("set stale aggregate state: %v", err)
			}

			status, err := service.MaintenanceStatus(context.Background())
			if err != nil {
				t.Fatalf("maintenance status: %v", err)
			}
			if status.Readiness.HourlyAggregateReady {
				t.Fatal("stale aggregate state was reported ready")
			}
		})
	}
}

func TestUsageMaintenanceUsesLatestRawEventAsAggregateTarget(t *testing.T) {
	service, st, _, _ := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()
	events := archiveTestServiceEvents(2)
	if _, err := st.InsertEvents(ctx, events[:1]); err != nil {
		t.Fatalf("insert initial event: %v", err)
	}
	catchUpUsageAggregate(t, st)

	ready, err := service.MaintenanceStatus(ctx)
	if err != nil {
		t.Fatalf("read ready maintenance status: %v", err)
	}
	if !ready.Readiness.HourlyAggregateReady {
		t.Fatalf("caught-up aggregate was not ready: %#v", ready)
	}

	if _, err := st.InsertEvents(ctx, events[1:]); err != nil {
		t.Fatalf("insert later event: %v", err)
	}
	latestEventID, err := st.LatestUsageEventID(ctx)
	if err != nil {
		t.Fatalf("read latest event id: %v", err)
	}
	stale, err := service.MaintenanceStatus(ctx)
	if err != nil {
		t.Fatalf("read stale maintenance status: %v", err)
	}
	if stale.HourlyAggregate.TargetEventID != latestEventID {
		t.Fatalf("aggregate target event id = %d, want latest raw event %d", stale.HourlyAggregate.TargetEventID, latestEventID)
	}
	if stale.Readiness.HourlyAggregateReady {
		t.Fatalf("aggregate remained ready after a new raw event: %#v", stale)
	}
}

func TestUsageMaintenanceStatusIncludesRawEventRange(t *testing.T) {
	service, st, _, _ := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()

	empty, err := service.MaintenanceStatus(ctx)
	if err != nil {
		t.Fatalf("read empty maintenance status: %v", err)
	}
	if empty.RawEventCount != 0 || empty.RawMinTimestampMS != 0 || empty.RawMaxTimestampMS != 0 ||
		empty.RawArchivedEventCount != 0 {
		t.Fatalf("empty maintenance status = %#v", empty)
	}

	if _, err := st.InsertEvents(ctx, archiveTestServiceEvents(3)); err != nil {
		t.Fatalf("insert raw range events: %v", err)
	}
	populated, err := service.MaintenanceStatus(ctx)
	if err != nil {
		t.Fatalf("read populated maintenance status: %v", err)
	}
	if populated.RawEventCount != 3 || populated.RawMinTimestampMS != 1_000 || populated.RawMaxTimestampMS != 3_000 ||
		populated.RawArchivedEventCount != 0 {
		t.Fatalf("populated maintenance status = %#v", populated)
	}
}

func TestUsageArchiveServiceBackfillsMetadataBeforeManualArchive(t *testing.T) {
	service, _, rawDB, archiveDirectory := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()
	rawJSON := `{"response_headers":{"X-OAI-Request-ID":["req-readiness"],"X-Codex-Plan-Type":["plus"]}}`
	if _, err := rawDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		cache_input_mode, normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"archive-readiness-event",
		1_000,
		"1970-01-01T00:00:01Z",
		"gpt-test",
		usageparser.CacheInputModeIncluded,
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		rawJSON,
		1_000,
	); err != nil {
		t.Fatalf("insert legacy metadata event: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `update usage_data_migrations set
		status = 'completed', last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration completed: %v", err)
	}
	var beforeMetadata string
	if err := rawDB.QueryRowContext(ctx, `select coalesce(response_metadata_json, '')
		from usage_events where event_hash = ?`, "archive-readiness-event").Scan(&beforeMetadata); err != nil {
		t.Fatalf("read pre-archive metadata: %v", err)
	}
	if beforeMetadata != "" {
		t.Fatalf("pre-archive metadata = %q, want empty", beforeMetadata)
	}

	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || len(archived.Segments) != 1 {
		t.Fatalf("archived = %#v", archived)
	}
	var afterMetadata string
	if err := rawDB.QueryRowContext(ctx, `select coalesce(response_metadata_json, '')
		from usage_events where event_hash = ?`, "archive-readiness-event").Scan(&afterMetadata); err != nil {
		t.Fatalf("read post-archive metadata: %v", err)
	}
	if afterMetadata == "" || !strings.Contains(afterMetadata, "req-readiness") {
		t.Fatalf("post-archive metadata = %q", afterMetadata)
	}

	segmentPath := filepath.Join(archiveDirectory, filepath.FromSlash(archived.Segments[0].FileName))
	file, err := os.Open(segmentPath)
	if err != nil {
		t.Fatalf("open archive segment: %v", err)
	}
	zipper, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		t.Fatalf("open archive gzip: %v", err)
	}
	scanner := bufio.NewScanner(zipper)
	if !scanner.Scan() {
		_ = zipper.Close()
		_ = file.Close()
		t.Fatalf("archive segment has no records: %v", scanner.Err())
	}
	var record map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		_ = zipper.Close()
		_ = file.Close()
		t.Fatalf("decode archive record: %v", err)
	}
	if err := errors.Join(scanner.Err(), zipper.Close(), file.Close()); err != nil {
		t.Fatalf("close archive segment: %v", err)
	}
	metadata, ok := record["response_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("archive response_metadata = %#v", record["response_metadata"])
	}
	trace, ok := metadata["trace"].(map[string]any)
	if !ok || trace["primary_trace_id"] != "req-readiness" {
		t.Fatalf("archive response metadata trace = %#v", metadata["trace"])
	}
}

func TestUsageArchiveSegmentsRestoreEventDataIntoFreshStore(t *testing.T) {
	events := archiveTestServiceEvents(2)
	quotaUsedPercent := 73.5
	events[0].ClientIP = "192.0.2.10"
	events[0].XForwardedFor = "198.51.100.7, 192.0.2.10"
	events[0].UserAgent = "cpamp-archive-restore/1.0"
	events[0].AuthType = "oauth"
	events[0].SourceHash = "source-hash-restore"
	events[0].APIKeyHash = "api-key-hash-restore"
	events[0].AuthLabelSnapshot = "restore-label"
	events[0].AuthFileSnapshot = "restore.json"
	events[0].AuthProviderSnapshot = "codex"
	events[0].AuthProjectIDSnapshot = "restore-project"
	events[0].AuthSnapshotAtMS = 900
	events[0].RequestedModel = "gpt-restore-alias"
	events[0].ResolvedModel = "gpt-5.6-sol"
	events[0].ReasoningEffort = "high"
	events[0].RequestServiceTier = "priority"
	events[0].ResponseServiceTier = "default"
	events[0].CachedTokens = 40
	events[0].CacheReadTokens = 30
	events[0].CacheCreationTokens = 10
	events[0].ResponseMetadata = &usageparser.ResponseHeaderMetadata{
		Quota: &usageparser.HeaderQuotaMetadata{
			PlanType:       "restore-plan",
			RecoverAtMS:    12_345,
			UsedPercent:    &quotaUsedPercent,
			ActiveLimit:    "primary",
			CreditsBalance: "42.5",
		},
		Errors:  &usageparser.HeaderErrorMetadata{Kind: "rate_limit", Code: "restore-code"},
		Trace:   &usageparser.HeaderTraceMetadata{PrimaryTraceID: "trace-restore", OpenAIRequestID: "req-restore"},
		Routing: &usageparser.HeaderRoutingMetadata{Server: "restore-upstream", Via: "restore-proxy"},
	}
	flatQuotaUsedPercent := 88.25
	events[1].ClientIP = "203.0.113.9"
	events[1].XForwardedFor = "203.0.113.9"
	events[1].UserAgent = "cpamp-flat-archive/1.0"
	events[1].HeaderQuotaRecoverAtMS = 54_321
	events[1].HeaderQuotaUsedPercent = &flatQuotaUsedPercent
	events[1].HeaderQuotaPlanType = "flat-plan"
	events[1].HeaderErrorKind = "flat-kind"
	events[1].HeaderErrorCode = "flat-code"
	events[1].HeaderTraceID = "flat-trace"
	events[1].ResponseMetadataJSON = "{}"

	service, _, sourceDB, _ := newRawArchiveTestService(t, 1, 1)
	insertArchiveTestEvents(t, service.store, events)
	ctx := context.Background()
	if _, err := sourceDB.ExecContext(ctx, `update usage_events set
		cache_input_mode = ?,
		normalized_uncached_input_tokens = ?,
		normalized_total_input_tokens = ?,
		normalized_cache_read_tokens = ?,
		normalized_cache_creation_tokens = ?,
		total_tokens = ?,
		service_tier = ?,
		request_service_tier = null,
		response_service_tier = ?,
		response_metadata_json = ' ' || json_set(response_metadata_json, '$.future_extension.value', 'preserve-me') || ' ',
		header_quota_recover_at_ms = ?,
		header_quota_used_percent = ?,
		header_quota_plan_type = ?,
		header_error_kind = ?,
		header_error_code = ?,
		header_trace_id = ?
	where event_hash = ?`,
		usageparser.CacheInputModeIncluded,
		int64(11),
		int64(222),
		int64(33),
		int64(44),
		int64(0),
		"archived-effective-tier",
		"archived-response-tier",
		int64(98_765),
		12.5,
		"stored-flat-plan",
		"stored-flat-kind",
		"stored-flat-code",
		"stored-flat-trace",
		events[0].EventHash,
	); err != nil {
		t.Fatalf("prepare archived derived fields: %v", err)
	}
	if _, err := sourceDB.ExecContext(ctx, `update usage_events set
		raw_json = ?, fail_body = ?, fail_summary = ? where event_hash = ?`,
		`  {"request":{"model":"gpt-test"},"future_extension":"preserve-me"}  `,
		"  archived failure body  ",
		"  archived failure summary  ",
		events[1].EventHash,
	); err != nil {
		t.Fatalf("prepare archived opaque fields: %v", err)
	}
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("archive usage: %v", err)
	}

	expectedRows := make(map[string]map[string]any, len(events))
	for _, event := range events {
		expectedRows[event.EventHash] = usageEventRowByHash(t, sourceDB, event.EventHash)
	}

	_, restoredStore, restoredDB, _ := newRawArchiveTestService(t, 1, 1)
	restoredService := New(restoredStore)
	expectedByHash := make(map[string]usageparser.Event, len(events))
	for _, event := range events {
		expectedByHash[event.EventHash] = event
	}
	for _, segment := range archived.Segments {
		path, err := service.archive.resolveArchivePath(segment.FileName)
		if err != nil {
			t.Fatalf("resolve archive segment: %v", err)
		}
		inspectionFile, err := os.Open(path)
		if err != nil {
			t.Fatalf("open archive segment for restore inspection: %v", err)
		}
		inspectionZipper, err := gzip.NewReader(inspectionFile)
		if err != nil {
			_ = inspectionFile.Close()
			t.Fatalf("open archive segment gzip for restore inspection: %v", err)
		}
		scanner := bufio.NewScanner(inspectionZipper)
		for scanner.Scan() {
			var record map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				t.Fatalf("decode archive record for restore inspection: %v", err)
			}
			eventHash, _ := record["event_hash"].(string)
			_, ok := expectedByHash[eventHash]
			if !ok {
				t.Fatalf("unexpected archive event hash %q", eventHash)
			}
			if got := record["_cpamp_archive_schema_version"]; got != float64(usageparser.ArchiveSchemaVersion) {
				t.Fatalf("archive schema version for %q = %#v", eventHash, got)
			}
			wantRawJSON, _ := expectedRows[eventHash]["raw_json"].(string)
			if rawJSON, ok := record["raw_json"].(string); !ok || rawJSON != wantRawJSON {
				t.Fatalf("archive raw_json for %q = %#v, want %q", eventHash, record["raw_json"], wantRawJSON)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan archive segment for restore inspection: %v", err)
		}
		if err := errors.Join(inspectionZipper.Close(), inspectionFile.Close()); err != nil {
			t.Fatalf("close archive restore inspection: %v", err)
		}

		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open archive segment: %v", err)
		}
		zipper, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			t.Fatalf("open archive segment gzip: %v", err)
		}
		result, _, importErr := restoredService.Import(ctx, zipper)
		closeErr := errors.Join(zipper.Close(), file.Close())
		if importErr != nil || closeErr != nil {
			t.Fatalf("restore archive segment: %v", errors.Join(importErr, closeErr))
		}
		if result.Added != int(segment.EventCount) || result.Skipped != 0 {
			t.Fatalf("restore result = %#v segment = %#v", result, segment)
		}
	}

	restored, err := restoredStore.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("read restored events: %v", err)
	}
	if len(restored) != len(events) {
		t.Fatalf("restored event count = %d, want %d", len(restored), len(events))
	}
	byHash := make(map[string]usageparser.Event, len(restored))
	for _, event := range restored {
		byHash[event.EventHash] = event
	}
	for _, want := range events {
		_, ok := byHash[want.EventHash]
		if !ok {
			t.Fatalf("restored event %q is missing", want.EventHash)
		}
		gotRow := usageEventRowByHash(t, restoredDB, want.EventHash)
		if !reflect.DeepEqual(gotRow, expectedRows[want.EventHash]) {
			t.Fatalf("restored database row differs for %q\ngot:  %#v\nwant: %#v", want.EventHash, gotRow, expectedRows[want.EventHash])
		}
	}
	restoredFailure := byHash[events[1].EventHash]
	if restoredFailure.ResponseMetadata != nil || restoredFailure.HeaderQuotaRecoverAtMS != 54_321 ||
		restoredFailure.HeaderQuotaUsedPercent == nil || *restoredFailure.HeaderQuotaUsedPercent != 88.25 ||
		restoredFailure.HeaderQuotaPlanType != "flat-plan" || restoredFailure.HeaderErrorKind != "flat-kind" ||
		restoredFailure.HeaderErrorCode != "flat-code" || restoredFailure.HeaderTraceID != "flat-trace" {
		t.Fatalf("restored flattened response metadata = %#v", restoredFailure)
	}
	restoredMetadata := byHash[events[0].EventHash]
	if restoredMetadata.ResponseMetadata == nil || restoredMetadata.ResponseMetadata.Trace == nil ||
		restoredMetadata.ResponseMetadata.Trace.PrimaryTraceID != "trace-restore" {
		t.Fatalf("restored response metadata = %#v", restoredMetadata.ResponseMetadata)
	}
	if restoredMetadata.HeaderQuotaRecoverAtMS != 98_765 || restoredMetadata.HeaderQuotaUsedPercent == nil ||
		*restoredMetadata.HeaderQuotaUsedPercent != 12.5 || restoredMetadata.HeaderQuotaPlanType != "stored-flat-plan" ||
		restoredMetadata.HeaderErrorKind != "stored-flat-kind" || restoredMetadata.HeaderErrorCode != "stored-flat-code" ||
		restoredMetadata.HeaderTraceID != "stored-flat-trace" {
		t.Fatalf("restored archived flattened response metadata = %#v", restoredMetadata)
	}
}

func TestUsageArchiveServiceResumesPublishedOrphanSegment(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	interrupted := errors.New("simulated interruption after segment publication")
	fired := false
	service.archive.testHook = func(point string) error {
		if point == "segment_published" && !fired {
			fired = true
			return interrupted
		}
		return nil
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); !errors.Is(err, interrupted) {
		t.Fatalf("archive interruption error = %v", err)
	}
	failed, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("failed archive status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusArchiving || len(failed.Segments) != 0 {
		t.Fatalf("failed archive = %#v", failed)
	}
	files, err := filepath.Glob(filepath.Join(archiveDirectory, created.Run.ID, "*.jsonl.gz"))
	if err != nil || len(files) != 1 {
		t.Fatalf("orphan files = %#v err=%v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("corrupt orphan"), 0o600); err != nil {
		t.Fatalf("corrupt orphan segment: %v", err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     1,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	archived, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || len(archived.Segments) != 2 {
		t.Fatalf("resumed archive = %#v", archived)
	}
	catchUpUsageAggregate(t, st)
	if _, err := restarted.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify overwritten orphan archive: %v", err)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(archiveDirectory, created.Run.ID, ".archive-tmp-*"))
	if err != nil || len(temporaryFiles) != 0 {
		t.Fatalf("temporary files = %#v err=%v", temporaryFiles, err)
	}
}

func TestUsageArchiveServicePersistsCancellationForRestart(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	created, err := service.CreateArchive(context.Background(), 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fired := false
	service.archive.testHook = func(point string) error {
		if point == "segment_published" && !fired {
			fired = true
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("archive cancellation error = %v", err)
	}
	failed, err := service.ArchiveStatus(context.Background(), created.Run.ID)
	if err != nil {
		t.Fatalf("cancelled archive status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed ||
		failed.Run.ResumeStatus != usagearchive.StatusArchiving ||
		!strings.Contains(failed.Run.LastError, context.Canceled.Error()) {
		t.Fatalf("cancelled archive = %#v", failed)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     1,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	archived, err := restarted.ResumeArchive(context.Background(), created.Run.ID)
	if err != nil {
		t.Fatalf("resume cancelled archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || len(archived.Segments) != 2 {
		t.Fatalf("resumed cancelled archive = %#v", archived)
	}
}

func TestUsageArchiveServiceResumesPublishedOrphanManifest(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	interrupted := errors.New("simulated interruption after manifest publication")
	fired := false
	service.archive.testHook = func(point string) error {
		if point == "manifest_published" && !fired {
			fired = true
			return interrupted
		}
		return nil
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); !errors.Is(err, interrupted) {
		t.Fatalf("archive interruption error = %v", err)
	}
	failed, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("failed archive status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed ||
		failed.Run.ResumeStatus != usagearchive.StatusArchiving ||
		failed.Run.ManifestFile != "" ||
		len(failed.Segments) != 1 {
		t.Fatalf("failed archive = %#v", failed)
	}
	manifestPath := filepath.Join(archiveDirectory, created.Run.ID, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte("corrupt orphan manifest"), 0o600); err != nil {
		t.Fatalf("corrupt orphan manifest: %v", err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     2,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	archived, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived ||
		archived.Run.ManifestFile == "" ||
		archived.Run.ManifestSHA256 == "" {
		t.Fatalf("resumed archive = %#v", archived)
	}
	catchUpUsageAggregate(t, st)
	if _, err := restarted.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify overwritten orphan manifest: %v", err)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(archiveDirectory, created.Run.ID, ".archive-tmp-*"))
	if err != nil || len(temporaryFiles) != 0 {
		t.Fatalf("temporary files = %#v err=%v", temporaryFiles, err)
	}
}

func TestUsageArchiveServiceResumesActiveVerification(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)
	if _, err := st.UsageArchives.BeginVerification(ctx, created.Run.ID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("begin verification: %v", err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     2,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	verified, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume verification: %v", err)
	}
	if verified.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("resumed verification = %#v", verified)
	}
}

func TestUsageArchiveServiceResumesActiveDelete(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 3, 1, archiveTestServiceEvents(3))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 4_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if _, err := st.UsageArchives.BeginDelete(ctx, created.Run.ID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("begin delete: %v", err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     3,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	completed, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume delete: %v", err)
	}
	if completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 3 {
		t.Fatalf("resumed delete = %#v", completed)
	}
}

func TestUsageArchiveServiceStageBoundResumeDoesNotAdvanceLaterDelete(t *testing.T) {
	service, st, _ := newArchiveTestService(t, 3, 1, archiveTestServiceEvents(3))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 4_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	deleting, err := st.UsageArchives.BeginDelete(ctx, created.Run.ID, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("begin delete: %v", err)
	}

	status, err := service.ResumeArchiveAtStage(ctx, created.Run.ID, usagearchive.StatusVerifying)
	if err != nil {
		t.Fatalf("stage-bound resume: %v", err)
	}
	if status.Run.Status != usagearchive.StatusDeleting || status.Run.DeletedEventCount != 0 {
		t.Fatalf("stage-bound resume advanced delete = %#v", status.Run)
	}
	counts, err := st.UsageArchives.MaintenanceCounts(ctx)
	if err != nil {
		t.Fatalf("read maintenance counts: %v", err)
	}
	if counts.RawEventCount != 3 || deleting.DeletedEventCount != 0 {
		t.Fatalf("stage-bound resume deleted raw events: counts=%#v run=%#v", counts, deleting)
	}
}

func TestUsageArchiveServiceRejectsInvalidExpectedResumeStage(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	if _, err := service.ResumeArchiveAtStage(context.Background(), strings.Repeat("a", 32), "future-stage"); !errors.Is(err, ErrArchiveInvalidRequest) {
		t.Fatalf("invalid expected stage error = %v", err)
	}
}

func TestUsageArchiveServiceValidatesCutoff(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	ctx := context.Background()
	for _, cutoff := range []int64{0, -1} {
		if _, err := service.PreviewArchive(ctx, cutoff); !errors.Is(err, ErrArchiveInvalidRequest) {
			t.Fatalf("preview cutoff %d error = %v", cutoff, err)
		}
		if _, err := service.CreateArchive(ctx, cutoff); !errors.Is(err, ErrArchiveInvalidRequest) {
			t.Fatalf("create cutoff %d error = %v", cutoff, err)
		}
		if _, err := service.CreateRetentionArchive(ctx, cutoff); !errors.Is(err, ErrArchiveInvalidRequest) {
			t.Fatalf("retention cutoff %d error = %v", cutoff, err)
		}
	}
}

func TestUsageArchiveRecordLimitMatchesImporter(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, nil)
	runID := strings.Repeat("a", 32)
	if err := service.archive.ensureRunDirectory(runID); err != nil {
		t.Fatalf("prepare archive directory: %v", err)
	}
	buildPayload := func(size int) []byte {
		t.Helper()
		prefix := []byte(`{"_cpamp_archive_schema_version":1,"_cpamp_archive_event_id":1,"event_hash":"record-limit","timestamp_ms":1,"timestamp":"1970-01-01T00:00:00.001Z","model":"gpt-test","input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"cache_tokens":0,"cache_read_tokens":0,"cache_creation_tokens":0,"failed":0,"fail_status_code":0,"fail_summary":"","fail_body":"","created_at_ms":1,"cache_input_mode":"included_in_input","normalized_uncached_input_tokens":0,"normalized_total_input_tokens":0,"normalized_cache_read_tokens":0,"normalized_cache_creation_tokens":0,"total_tokens":0,"header_quota_recover_at_ms":0,"raw_json":"`)
		suffix := []byte(`"}`)
		if size < len(prefix)+len(suffix) {
			t.Fatalf("archive payload size %d is too small", size)
		}
		payload := make([]byte, 0, size)
		payload = append(payload, prefix...)
		payload = append(payload, strings.Repeat("x", size-len(prefix)-len(suffix))...)
		payload = append(payload, suffix...)
		return payload
	}
	record := store.UsageArchiveRecord{
		EventID:     1,
		EventHash:   "record-limit",
		TimestampMS: 1,
		Payload:     buildPayload(maxArchiveRecordBytes),
	}
	segment, _, err := service.archive.writeSegment(runID, 1, []store.UsageArchiveRecord{record})
	if err != nil {
		t.Fatalf("write exact-limit archive record: %v", err)
	}
	inspection, err := service.archive.inspectSegment(segment, sha256.New())
	if err != nil {
		t.Fatalf("inspect exact-limit archive record: %v", err)
	}
	if err := compareSegmentInspection(segment, inspection); err != nil {
		t.Fatalf("compare exact-limit archive record: %v", err)
	}

	record.Payload = buildPayload(maxArchiveRecordBytes + 1)
	if _, _, err := service.archive.writeSegment(runID, 2, []store.UsageArchiveRecord{record}); err == nil {
		t.Fatal("oversized archive record was accepted")
	}
}

func TestUsageArchiveInspectionRejectsUnrestorableRecord(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, nil)
	runID := strings.Repeat("c", 32)
	if err := service.archive.ensureRunDirectory(runID); err != nil {
		t.Fatalf("prepare archive directory: %v", err)
	}
	record := store.UsageArchiveRecord{
		EventID:     1,
		EventHash:   "unrestorable-record",
		TimestampMS: 1,
		Payload:     []byte(`{"_cpamp_archive_schema_version":1,"_cpamp_archive_event_id":1,"event_hash":"unrestorable-record","timestamp_ms":1,"timestamp":"1970-01-01T00:00:00.001Z","model":"gpt-test","cache_input_mode":"included_in_input","normalized_uncached_input_tokens":0,"normalized_cache_read_tokens":0,"normalized_cache_creation_tokens":0,"total_tokens":0,"header_quota_recover_at_ms":0}`),
	}
	segment, _, err := service.archive.writeSegment(runID, 1, []store.UsageArchiveRecord{record})
	if err != nil {
		t.Fatalf("write unrestorable archive record: %v", err)
	}
	if _, err := service.archive.inspectSegment(segment, sha256.New()); !errors.Is(err, usageparser.ErrInvalidArchiveRecord) {
		t.Fatalf("inspect unrestorable archive record error = %v", err)
	}
}

func TestUsageArchiveServiceRejectsOversizedManifestBeforePublish(t *testing.T) {
	service, _, archiveDirectory := newArchiveTestService(t, 1, 1, nil)
	runID := strings.Repeat("b", 32)
	if err := service.archive.ensureRunDirectory(runID); err != nil {
		t.Fatalf("prepare archive directory: %v", err)
	}
	manifest := ArchiveManifest{
		SchemaVersion: usagearchive.SchemaVersion,
		Format:        usagearchive.FormatGzipJSONLV1,
		RunID:         runID,
		Segments: []ArchiveManifestSegment{{
			Sequence:        1,
			FileName:        strings.Repeat("x", maxArchiveManifestBytes),
			EventCount:      1,
			ContentSHA256:   strings.Repeat("a", 64),
			EventHashDigest: strings.Repeat("b", 64),
		}},
	}
	if _, _, err := service.archive.writeManifest(runID, manifest); err == nil || !strings.Contains(err.Error(), "manifest exceeds") {
		t.Fatalf("oversized manifest error = %v", err)
	}
	manifestPath := filepath.Join(archiveDirectory, runID, "manifest.json")
	if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized manifest path stat error = %v", err)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(archiveDirectory, runID, ".archive-tmp-*"))
	if err != nil {
		t.Fatalf("find temporary manifest files: %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary manifest files = %#v", temporaryFiles)
	}
}

func TestUsageArchiveDetectsPublishedFileReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "segment.jsonl.gz")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original archive file: %v", err)
	}
	file, info, err := openPrivateRegularFile(directory, path)
	if err != nil {
		t.Fatalf("open original archive file: %v", err)
	}
	defer file.Close()
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement archive file: %v", err)
	}
	if err := replaceArchiveFile(replacement, path); err != nil {
		t.Fatalf("publish replacement archive file: %v", err)
	}
	if err := ensureOpenFileStillPublished(directory, path, info); err == nil {
		t.Fatal("archive path replacement was not detected")
	}
}

func TestUsageArchiveServiceRejectsChecksumMismatch(t *testing.T) {
	service, st, _ := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)
	segmentPath, err := service.archive.resolveArchivePath(archived.Segments[0].FileName)
	if err != nil {
		t.Fatalf("resolve segment: %v", err)
	}
	file, err := os.OpenFile(segmentPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open segment for corruption: %v", err)
	}
	if _, err := file.WriteString("corrupt"); err != nil {
		_ = file.Close()
		t.Fatalf("corrupt segment: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt segment: %v", err)
	}
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err == nil {
		t.Fatalf("verify checksum error = %v", err)
	}
	failed, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("failed verification status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusVerifying {
		t.Fatalf("failed verification = %#v", failed)
	}
	if _, err := service.DeleteArchive(ctx, created.Run.ID); !errors.Is(err, ErrArchiveInvalidState) {
		t.Fatalf("delete corrupt archive error = %v", err)
	}
}

func TestUsageArchiveServiceRejectsSymlinkDirectory(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	insertArchiveTestEvents(t, st, archiveTestServiceEvents(1))
	target := t.TempDir()
	archiveDirectory := filepath.Join(t.TempDir(), "archives")
	if err := os.Symlink(target, archiveDirectory); err != nil {
		t.Fatalf("create archive symlink: %v", err)
	}
	service := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		AggregateReadsEnabled: true,
	}))
	created, err := service.CreateArchive(context.Background(), 2_000)
	if err != nil {
		t.Fatalf("create archive run: %v", err)
	}
	if _, err := service.ResumeArchive(context.Background(), created.Run.ID); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink archive error = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("archive wrote through symlink: %#v", entries)
	}
}

func TestUsageArchiveServicePinsSymlinkedParentTarget(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	insertArchiveTestEvents(t, st, archiveTestServiceEvents(1))
	firstTarget := t.TempDir()
	secondTarget := t.TempDir()
	linkRoot := t.TempDir()
	dataLink := filepath.Join(linkRoot, "data")
	if err := os.Symlink(firstTarget, dataLink); err != nil {
		t.Fatalf("create data directory symlink: %v", err)
	}
	service := New(st, WithArchive(ArchiveConfig{
		Directory:             filepath.Join(dataLink, "usage-archives"),
		AggregateReadsEnabled: true,
	}))
	created, err := service.CreateArchive(context.Background(), 2_000)
	if err != nil {
		t.Fatalf("create archive run: %v", err)
	}
	if err := os.Remove(dataLink); err != nil {
		t.Fatalf("remove original data symlink: %v", err)
	}
	if err := os.Symlink(secondTarget, dataLink); err != nil {
		t.Fatalf("replace data directory symlink: %v", err)
	}
	if _, err := service.ResumeArchive(context.Background(), created.Run.ID); err != nil {
		t.Fatalf("archive through pinned parent target: %v", err)
	}
	firstEntries, err := os.ReadDir(filepath.Join(firstTarget, "usage-archives", created.Run.ID))
	if err != nil || len(firstEntries) == 0 {
		t.Fatalf("pinned archive entries = %#v err=%v", firstEntries, err)
	}
	secondEntries, err := os.ReadDir(secondTarget)
	if err != nil {
		t.Fatalf("read replacement target: %v", err)
	}
	if len(secondEntries) != 0 {
		t.Fatalf("archive followed replaced parent symlink: %#v", secondEntries)
	}
}

func TestUsageArchiveServiceRejectsReplacedRunDirectorySymlink(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)

	runDirectory := filepath.Join(archiveDirectory, created.Run.ID)
	savedDirectory := filepath.Join(filepath.Dir(archiveDirectory), "saved-"+created.Run.ID)
	if err := os.Rename(runDirectory, savedDirectory); err != nil {
		t.Fatalf("move archive run directory: %v", err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, runDirectory); err != nil {
		t.Fatalf("replace run directory with symlink: %v", err)
	}

	if _, err := service.VerifyArchive(ctx, created.Run.ID); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("run directory symlink verification error = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read run symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("verification read through run symlink: %#v", entries)
	}
}

func TestUsageArchiveServiceResumesBoundedDelete(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 3, 1, archiveTestServiceEvents(3))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 4_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("archive usage: %v", err)
	}
	catchUpUsageAggregate(t, st)
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	interrupted := errors.New("simulated interruption after delete batch")
	fired := false
	service.archive.testHook = func(point string) error {
		if point == "delete_batch_committed" && !fired {
			fired = true
			return interrupted
		}
		return nil
	}
	if _, err := service.DeleteArchive(ctx, created.Run.ID); !errors.Is(err, interrupted) {
		t.Fatalf("delete interruption error = %v", err)
	}
	failed, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("failed delete status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusDeleting || failed.Run.DeletedEventCount != 1 {
		t.Fatalf("failed delete = %#v", failed)
	}
	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     3,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	completed, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume delete: %v", err)
	}
	if completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 3 {
		t.Fatalf("completed delete = %#v", completed)
	}
}

func newArchiveTestService(t *testing.T, segmentLimit, deleteBatchSize int, events []usageparser.Event) (*Service, *store.Store, string) {
	t.Helper()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	insertArchiveTestEvents(t, st, events)
	archiveDirectory := filepath.Join(t.TempDir(), "usage-archives")
	service := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     segmentLimit,
		DeleteBatchSize:       deleteBatchSize,
		AggregateReadsEnabled: true,
	}))
	return service, st, archiveDirectory
}

func newRawArchiveTestService(t *testing.T, segmentLimit, deleteBatchSize int) (*Service, *store.Store, *sql.DB, string) {
	t.Helper()
	cfg := testutil.NewConfig(t)
	rawDB, err := sqliterepo.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open raw archive store: %v", err)
	}
	st := store.New(rawDB)
	testutil.EnsureAdminCredential(t, st)
	t.Cleanup(func() { _ = st.Close() })
	archiveDirectory := filepath.Join(t.TempDir(), "usage-archives")
	service := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     segmentLimit,
		DeleteBatchSize:       deleteBatchSize,
		AggregateReadsEnabled: true,
	}))
	return service, st, rawDB, archiveDirectory
}

func insertArchiveTestEvents(t *testing.T, st *store.Store, events []usageparser.Event) {
	t.Helper()
	result, err := st.InsertEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("insert archive test events: %v", err)
	}
	if result.Inserted != len(events) {
		t.Fatalf("insert result = %#v", result)
	}
}

func usageEventRowByHash(t *testing.T, db *sql.DB, eventHash string) map[string]any {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `select * from usage_events where event_hash = ?`, eventHash)
	if err != nil {
		t.Fatalf("query usage event %q: %v", eventHash, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("read usage event columns: %v", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Fatalf("read usage event %q: %v", eventHash, err)
		}
		t.Fatalf("usage event %q is missing", eventHash)
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		t.Fatalf("scan usage event %q: %v", eventHash, err)
	}
	if rows.Next() {
		t.Fatalf("usage event %q is duplicated", eventHash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("finish usage event %q query: %v", eventHash, err)
	}
	row := make(map[string]any, len(columns)-1)
	for index, column := range columns {
		if column == "id" {
			continue
		}
		value := values[index]
		if bytes, ok := value.([]byte); ok {
			value = string(bytes)
		}
		row[column] = value
	}
	return row
}

func archiveTestServiceEvents(count int) []usageparser.Event {
	events := make([]usageparser.Event, 0, count)
	for index := range count {
		timestampMS := int64(index+1) * 1_000
		failed := index%2 == 1
		events = append(events, usageparser.Event{
			RequestID:       "archive-request-" + strconv.Itoa(index),
			EventHash:       "archive-service-event-" + strconv.Itoa(index),
			TimestampMS:     timestampMS,
			Timestamp:       time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Provider:        "codex",
			ExecutorType:    "CodexExecutor",
			Model:           "gpt-test",
			Endpoint:        "POST /v1/responses",
			Method:          "POST",
			Path:            "/v1/responses",
			AuthIndex:       "auth-1",
			Source:          "account@example.com",
			AccountSnapshot: "account@example.com",
			InputTokens:     int64(100 + index),
			OutputTokens:    int64(20 + index),
			TotalTokens:     int64(120 + index*2),
			Failed:          failed,
			FailStatusCode:  map[bool]int{true: 429}[failed],
			FailSummary:     map[bool]string{true: "rate limited"}[failed],
			FailBody:        map[bool]string{true: `{"error":"rate limited"}`}[failed],
			RawJSON:         `{"request":{"model":"gpt-test"}}`,
			CreatedAtMS:     timestampMS,
		})
	}
	return events
}

func catchUpUsageAggregate(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	nowMS := time.Now().UnixMilli()
	aggregateComplete := false
	for iteration := 0; iteration < 100; iteration++ {
		result, err := st.CatchUpUsageHourlyAggregate(ctx, 2, nowMS+int64(iteration))
		if err != nil {
			t.Fatalf("catch up usage aggregate: %v", err)
		}
		if !result.Pending {
			aggregateComplete = true
			break
		}
	}
	if !aggregateComplete {
		t.Fatal("usage aggregate catch-up did not complete")
	}

	pricingComplete := false
	for iteration := 0; iteration < 100; iteration++ {
		result, err := st.CatchUpUsagePricing(ctx, 2, nowMS+100+int64(iteration))
		if err != nil {
			t.Fatalf("catch up usage pricing: %v", err)
		}
		if !result.Pending {
			pricingComplete = true
			break
		}
	}
	if !pricingComplete {
		t.Fatal("usage pricing catch-up did not complete")
	}

	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (store.UsageMonitoringCatchUpResult, error)
	}{
		{name: "stats", run: st.CatchUpUsageMonitoringStats},
		{name: "metadata", run: st.CatchUpUsageMonitoringMetadata},
		{name: "projection", run: st.CatchUpUsageMonitoringProjection},
	} {
		completed := false
		for iteration := 0; iteration < 100; iteration++ {
			result, err := catchUp.run(ctx, 2, nowMS+200+int64(iteration))
			if err != nil {
				t.Fatalf("catch up usage monitoring %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("usage monitoring %s catch-up did not complete", catchUp.name)
		}
	}

	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (store.UsageRollupCatchUpResult, error)
	}{
		{name: "account history", run: st.CatchUpAccountHistoryRollups},
		{name: "dashboard hourly", run: st.CatchUpDashboardHourlyRollups},
	} {
		completed := false
		for iteration := 0; iteration < 100; iteration++ {
			result, err := catchUp.run(ctx, 2, nowMS+300+int64(iteration))
			if err != nil {
				t.Fatalf("catch up %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("%s catch-up did not complete", catchUp.name)
		}
	}
}

func assertPrivateArchiveTree(t *testing.T, archiveDirectory string, status ArchiveStatus) {
	t.Helper()
	for _, directory := range []string{archiveDirectory, filepath.Join(archiveDirectory, status.Run.ID)} {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("stat archive directory %s: %v", directory, err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("archive directory mode %s = %v", directory, info.Mode())
		}
	}
	files := append([]store.UsageArchiveSegment(nil), status.Segments...)
	for _, segment := range files {
		path := filepath.Join(archiveDirectory, filepath.FromSlash(segment.FileName))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat archive segment: %v", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("archive segment mode = %v", info.Mode())
		}
	}
	manifestPath := filepath.Join(archiveDirectory, filepath.FromSlash(status.Run.ManifestFile))
	info, err := os.Lstat(manifestPath)
	if err != nil {
		t.Fatalf("stat archive manifest: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive manifest mode = %v", info.Mode())
	}
}
