package usage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/decisionstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/quotaobservation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/quotaevaluation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestUsageArchiveDeletionDoesNotPersistIncompleteQuotaObservation(t *testing.T) {
	for _, metric := range []string{"request", "token"} {
		t.Run(metric, func(t *testing.T) {
			service, st, db, _ := newRawArchiveTestService(t, 2, 1)
			ctx := context.Background()
			keyID := identity.APIKeyID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			policyID := "cccccccccccccccccccccccccccccccc"
			sourceHash := canonicalArchiveTestHash("quota-archive-source")
			if err := st.Identities.CreateAPIKey(ctx, identity.APIKeyIdentity{
				ID: keyID, Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive,
				CreatedAtMS: 1, UpdatedAtMS: 1,
			}, identity.APIKeySourceBinding{
				APIKeyID: keyID, RuntimeIdentity: "archive-fixture", APIKeyHash: sourceHash,
				ObservedRuntimeGeneration: 1, FirstSeenAtMS: 1, LastSeenAtMS: 1,
			}); err != nil {
				t.Fatal(err)
			}
			limit := int64(2)
			if metric == "token" {
				limit = 10
			}
			for _, statement := range []string{
				`insert into gateway_quota_policies(id,revision,state,enforcement,action,created_at_ms,updated_at_ms)
				 values ('cccccccccccccccccccccccccccccccc',1,'active','observed','notify',1,1)`,
				`insert into gateway_api_key_policy_bindings(api_key_id,policy_id,revision,enabled,created_at_ms,updated_at_ms)
				 values ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','cccccccccccccccccccccccccccccccc',1,1,1,1)`,
			} {
				if _, err := db.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `insert into gateway_quota_policy_rules
				(policy_id,metric,limit_value,window_kind,duration_ms,anchor_at_ms,timezone)
				values (?,?,?,'fixed',10000,1,'')`, policyID, metric, limit); err != nil {
				t.Fatal(err)
			}
			events := archiveTestServiceEvents(3)
			for i := range events {
				events[i].APIKeyHash = sourceHash
				events[i].InputTokens, events[i].OutputTokens, events[i].TotalTokens = 5, 0, 5
				events[i].Failed = false
			}
			insertArchiveTestEvents(t, st, events[:2])
			catchUpUsageAggregate(t, st)
			created, err := service.CreateArchive(ctx, 1500)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
				t.Fatal(err)
			}
			deleted, err := service.DeleteArchive(ctx, created.Run.ID)
			if err != nil || deleted.Run.DeletedEventCount != 1 {
				t.Fatalf("real archive deletion: %+v err=%v", deleted, err)
			}
			evaluator := quotaevaluation.New(quotaobservation.New(db), decisionstore.New(db))
			assertIncomplete := func(id, at int64) {
				t.Helper()
				result, err := evaluator.EvaluateUsageEvent(ctx, id, at)
				if !errors.Is(err, quotaobservation.ErrSourceIncomplete) {
					t.Fatalf("%s incomplete archive window: %+v err=%v", metric, result, err)
				}
				var count int
				if err := db.QueryRowContext(ctx, `select count(*) from gateway_quota_decision_events_v1`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("incomplete source persisted %d audit rows", count)
				}
			}
			assertIncomplete(2, 2500)
			insertArchiveTestEvents(t, st, events[2:])
			catchUpUsageAggregate(t, st)
			assertIncomplete(3, 3500)
			// A later policy window excludes the deleted event and remains evaluable.
			if _, err := db.ExecContext(ctx, `update gateway_quota_policy_rules set duration_ms=2000,anchor_at_ms=1501 where policy_id=?`, policyID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `update gateway_quota_policies set revision=2,updated_at_ms=3500 where id=?`, policyID); err != nil {
				t.Fatal(err)
			}
			result, err := evaluator.EvaluateUsageEvent(ctx, 3, 3501)
			if err != nil || len(result.Events) != 1 || result.Events[0].ObservedValue == nil ||
				*result.Events[0].ObservedValue != limit || result.Events[0].Outcome != gatewaydecision.OutcomeNotifyRequired {
				t.Fatalf("complete later window: %+v err=%v", result, err)
			}
		})
	}
}

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

func TestUsageArchiveServicePersistsSubphaseProgress(t *testing.T) {
	service, st, _ := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(4))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	publishedSegments, inspectedArchive, inspectedVerify, inspectedCleanup := int64(0), int64(0), int64(0), int64(0)
	service.archive.testHook = func(point string) error {
		run, err := st.UsageArchives.Run(ctx, created.Run.ID)
		if err != nil {
			return err
		}
		wantPhase, wantCurrent, wantTotal := "", int64(0), int64(2)
		switch point {
		case "segment_published":
			wantPhase, wantCurrent, wantTotal = usagearchive.ProgressArchivingRecords, publishedSegments*2, 4
			publishedSegments++
		case "archive_finalizing_started":
			wantPhase = usagearchive.ProgressArchiveFinalizing
			if run.ArchivedEventCount != run.EventCount || run.Status != usagearchive.StatusArchiving {
				t.Fatalf("finalizing run = %#v", run)
			}
		case "archive_segment_inspected":
			inspectedArchive++
			wantPhase, wantCurrent = usagearchive.ProgressArchiveFinalizing, inspectedArchive
		case "archive_publishing_started":
			wantPhase, wantTotal = usagearchive.ProgressArchivePublishing, 0
		case "archive_verification_started":
			wantPhase = usagearchive.ProgressVerifyingArchive
		case "verification_segment_inspected":
			if run.Status == usagearchive.StatusVerifying {
				inspectedVerify++
				wantPhase, wantCurrent = usagearchive.ProgressVerifyingArchive, inspectedVerify
			} else {
				inspectedCleanup++
				wantPhase, wantCurrent = usagearchive.ProgressCleanupRevalidating, inspectedCleanup
			}
		case "cleanup_revalidation_started":
			wantPhase = usagearchive.ProgressCleanupRevalidating
		case "delete_records_started":
			wantPhase, wantCurrent, wantTotal = usagearchive.ProgressDeletingRecords, 0, 4
		case "delete_batch_committed":
			wantPhase, wantCurrent, wantTotal = usagearchive.ProgressDeletingRecords, run.DeletedEventCount, 4
		default:
			return nil
		}
		if run.ProgressPhase != wantPhase || run.ProgressCurrent != wantCurrent || run.ProgressTotal != wantTotal || run.ProgressUpdatedAtMS == 0 {
			t.Fatalf("%s progress = %#v; want %s %d/%d", point, run, wantPhase, wantCurrent, wantTotal)
		}
		return nil
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Run.ProgressPhase != "" || inspectedArchive != 2 {
		t.Fatalf("archived progress = %#v, inspections = %d", archived.Run, inspectedArchive)
	}
	catchUpUsageAggregate(t, st)
	verified, err := service.VerifyArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Run.ProgressPhase != "" || inspectedVerify != 2 {
		t.Fatalf("verified progress = %#v, inspections = %d", verified.Run, inspectedVerify)
	}
	completed, err := service.DeleteArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.ProgressPhase != "" || inspectedCleanup != 2 {
		t.Fatalf("completed progress = %#v, inspections = %d", completed.Run, inspectedCleanup)
	}
}

func TestUsageArchiveServiceKeepsProgressAtInterruptedSubphase(t *testing.T) {
	for _, testCase := range []struct {
		point, phase, resume string
		current, total       int64
	}{
		{"archive_segment_inspected", usagearchive.ProgressArchiveFinalizing, usagearchive.StatusArchiving, 1, 2},
		{"archive_publishing_started", usagearchive.ProgressArchivePublishing, usagearchive.StatusArchiving, 0, 0},
		{"verification_segment_inspected", usagearchive.ProgressVerifyingArchive, usagearchive.StatusVerifying, 1, 2},
		{"cleanup_revalidation_started", usagearchive.ProgressCleanupRevalidating, usagearchive.StatusDeleting, 0, 2},
		{"verification_segment_inspected", usagearchive.ProgressCleanupRevalidating, usagearchive.StatusDeleting, 1, 2},
		{"delete_batch_committed", usagearchive.ProgressDeletingRecords, usagearchive.StatusDeleting, 1, 4},
	} {
		t.Run(testCase.point+"/"+testCase.resume, func(t *testing.T) {
			service, st, _ := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(4))
			ctx := context.Background()
			created, err := service.CreateArchive(ctx, 5_000)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.resume != usagearchive.StatusArchiving {
				if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.resume == usagearchive.StatusDeleting {
				catchUpUsageAggregate(t, st)
				if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
					t.Fatal(err)
				}
			}
			interrupted := errors.New("test interruption")
			fired := false
			service.archive.testHook = func(point string) error {
				if point == testCase.point && !fired {
					fired = true
					return interrupted
				}
				return nil
			}
			switch testCase.resume {
			case usagearchive.StatusArchiving:
				_, err = service.ResumeArchive(ctx, created.Run.ID)
			case usagearchive.StatusVerifying:
				_, err = service.VerifyArchive(ctx, created.Run.ID)
			case usagearchive.StatusDeleting:
				_, err = service.DeleteArchive(ctx, created.Run.ID)
			}
			if !errors.Is(err, interrupted) {
				t.Fatalf("stage error = %v", err)
			}
			failed, err := service.ArchiveStatus(ctx, created.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != testCase.resume ||
				failed.Run.ProgressPhase != testCase.phase || failed.Run.ProgressCurrent != testCase.current || failed.Run.ProgressTotal != testCase.total {
				t.Fatalf("failed progress = %#v", failed.Run)
			}
		})
	}
}

func TestUsageArchiveServiceTelemetryWriteFailureDoesNotFailSafeArchive(t *testing.T) {
	service, st, rawDB, _ := newRawArchiveTestService(t, 2, 1)
	ctx := context.Background()
	insertArchiveTestEvents(t, st, archiveTestServiceEvents(2))
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	installed := false
	service.archive.testHook = func(point string) error {
		if point == "segment_published" && !installed {
			installed = true
			_, err := rawDB.ExecContext(ctx, `create trigger fail_archive_progress before update on usage_archive_runs
				when new.progress_phase in ('archive_finalizing', 'archive_publishing')
				begin select raise(abort, 'telemetry unavailable'); end`)
			return err
		}
		return nil
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("telemetry error failed archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || archived.Run.ProgressPhase != "" {
		t.Fatalf("archive after telemetry error = %#v", archived.Run)
	}
}

func TestUsageArchiveServiceCancelReleasesMaintenanceAndRejectsUnsafeRuns(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 2, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	if _, err := service.CancelArchive(ctx, "invalid"); !errors.Is(err, ErrArchiveInvalidID) {
		t.Fatalf("invalid cancel run id error = %v, want ErrArchiveInvalidID", err)
	}
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	cancelled, err := service.CancelArchive(ctx, created.Run.ID)
	if err != nil || cancelled.Run.Status != usagearchive.StatusCancelled {
		t.Fatalf("cancelled archive = %#v error = %v", cancelled, err)
	}
	if _, err := service.CancelArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("idempotent service cancel: %v", err)
	}
	if _, err := service.CreateArchive(ctx, 3_000); err != nil {
		t.Fatalf("create archive after cancel: %v", err)
	}
}

func TestUsageArchiveServiceCancelRetriesWhenTemporaryCleanupFails(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if err := service.archive.ensureRunDirectory(created.Run.ID); err != nil {
		t.Fatalf("create archive run directory: %v", err)
	}
	temporaryDirectory := filepath.Join(archiveDirectory, created.Run.ID, ".archive-tmp-blocker")
	if err := os.Mkdir(temporaryDirectory, 0o700); err != nil {
		t.Fatalf("create temporary cleanup blocker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(temporaryDirectory, "contents"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("write temporary cleanup blocker: %v", err)
	}
	if _, err := service.CancelArchive(ctx, created.Run.ID); !errors.Is(err, ErrArchiveCancelCleanupFailed) {
		t.Fatalf("cancel with cleanup failure error = %v, want ErrArchiveCancelCleanupFailed", err)
	}
	active, found, err := st.UsageArchives.ActiveRun(ctx)
	if err != nil || !found || active.ID != created.Run.ID || active.Status != usagearchive.StatusPreviewed {
		t.Fatalf("run after cleanup failure = %#v found=%t error=%v", active, found, err)
	}
	if err := os.Remove(filepath.Join(temporaryDirectory, "contents")); err != nil {
		t.Fatalf("remove temporary blocker contents: %v", err)
	}
	if err := os.Remove(temporaryDirectory); err != nil {
		t.Fatalf("remove temporary blocker: %v", err)
	}
	cancelled, err := service.CancelArchive(ctx, created.Run.ID)
	if err != nil || cancelled.Run.Status != usagearchive.StatusCancelled {
		t.Fatalf("retry cancel = %#v error=%v", cancelled, err)
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
			ID:                  strings.Repeat("a", 32),
			Mode:                usagearchive.RunModeManual,
			SchemaVersion:       99,
			Format:              "internal-format",
			Status:              usagearchive.StatusFailed,
			ArchiveDigest:       "internal-archive-digest",
			ManifestFile:        secretPath,
			ManifestSHA256:      "internal-manifest-digest",
			LastError:           "open " + secretPath + ": permission denied",
			ProgressPhase:       usagearchive.ProgressArchiveFinalizing,
			ProgressCurrent:     12,
			ProgressTotal:       14,
			ProgressUnit:        "segments",
			ProgressUpdatedAtMS: 1234,
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
	if !strings.Contains(response, `"progress":{"phase":"archive_finalizing","current":12,"total":14,"unit":"segments","updated_at_ms":1234}`) {
		t.Fatalf("archive summary lost safe progress: %s", response)
	}
	status.Run.ProgressUnit = secretPath
	if summary := NewArchiveStatusSummary(status); summary.Run.Progress == nil || summary.Run.Progress.Unit != "segments" {
		t.Fatalf("archive summary published unsafe progress unit: %#v", summary.Run.Progress)
	}
	status.Run.ProgressPhase = secretPath
	if summary := NewArchiveStatusSummary(status); summary.Run.Progress != nil {
		t.Fatalf("archive summary published unknown progress: %#v", summary.Run.Progress)
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
	service, st, rawDB, archiveDirectory := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()
	rawJSON := `{"response_headers":{"X-OAI-Request-ID":["req-readiness"],"X-Codex-Plan-Type":["plus"]}}`
	eventHash := canonicalArchiveTestHash("archive-readiness-event")
	if _, err := rawDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		cache_input_mode, normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventHash,
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
		from usage_events where event_hash = ?`, eventHash).Scan(&beforeMetadata); err != nil {
		t.Fatalf("read pre-archive metadata: %v", err)
	}
	if beforeMetadata != "" {
		t.Fatalf("pre-archive metadata = %q, want empty", beforeMetadata)
	}

	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("resume archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}
	for {
		updated, err := st.BackfillUsageResponseMetadata(ctx, 10)
		if err != nil {
			t.Fatalf("backfill response metadata: %v", err)
		}
		if updated == 0 {
			break
		}
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
		from usage_events where event_hash = ?`, eventHash).Scan(&afterMetadata); err != nil {
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
	generate := true
	stream := false
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
	events[0].ResponseModel = "gpt-5.6-sol-response"
	events[0].SessionID = "restore-session"
	events[0].ParentSessionID = "restore-parent-session"
	events[0].AccessTokenSHA256 = "restore-access-token-sha256"
	events[0].Generate = &generate
	events[0].Stream = &stream
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

	var sourceRespMeta string
	if err := sourceDB.QueryRowContext(ctx, `select response_metadata_json from usage_events where event_hash = ?`, events[0].EventHash).Scan(&sourceRespMeta); err != nil {
		t.Fatalf("read source response_metadata_json: %v", err)
	}
	var decodedMeta map[string]any
	if err := json.Unmarshal([]byte(sourceRespMeta), &decodedMeta); err != nil {
		t.Fatalf("unmarshal source response_metadata_json: %v", err)
	}
	decodedMeta["future_extension"] = map[string]any{"value": "preserve-me"}
	sanitizedMetaBytes, _ := json.Marshal(decodedMeta)
	sanitizedMeta := usageparser.SafeRawJSON(string(sanitizedMetaBytes))

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
		response_metadata_json = ?,
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
		sanitizedMeta,
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
	rawJSONFixture := usageparser.SafeRawJSON(`{"future_extension":"preserve-me","request":{"model":"gpt-test"}}`)
	failBodyFixture := "archived failure body"
	failSummaryFixture := "archived failure summary"
	if _, err := sourceDB.ExecContext(ctx, `update usage_events set
		raw_json = ?, fail_body = ?, fail_summary = ? where event_hash = ?`,
		rawJSONFixture,
		failBodyFixture,
		failSummaryFixture,
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
	if restoredMetadata.ResponseModel != events[0].ResponseModel ||
		restoredMetadata.SessionID != events[0].SessionID ||
		restoredMetadata.ParentSessionID != events[0].ParentSessionID ||
		restoredMetadata.AccessTokenSHA256 != events[0].AccessTokenSHA256 ||
		restoredMetadata.Generate == nil || *restoredMetadata.Generate != generate ||
		restoredMetadata.Stream == nil || *restoredMetadata.Stream != stream {
		t.Fatalf("restored request metadata = %#v", restoredMetadata)
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
	if failed.Run.ProgressPhase != usagearchive.ProgressArchivePublishing || failed.Run.ProgressTotal != 0 {
		t.Fatalf("published orphan progress = %#v", failed.Run)
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
	persisted, err := restarted.ArchiveStatus(ctx, created.Run.ID)
	if err != nil || persisted.Run.ProgressPhase != usagearchive.ProgressArchivePublishing {
		t.Fatalf("restarted progress = %#v err=%v", persisted.Run, err)
	}
	archived, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived ||
		archived.Run.ManifestFile == "" ||
		archived.Run.ManifestSHA256 == "" {
		t.Fatalf("resumed archive = %#v", archived)
	}
	if archived.Run.ProgressPhase != "" {
		t.Fatalf("resumed archived progress = %#v", archived.Run)
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
	inspection, err := service.archive.inspectSegment(segment, sha256.New(), false)
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
	if _, err := service.archive.inspectSegment(segment, sha256.New(), false); !errors.Is(err, usageparser.ErrInvalidArchiveRecord) {
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

func TestUsageArchiveDeleteInspectsContentOnceRegardlessOfBatchCount(t *testing.T) {
	for _, events := range []int{4, 8, 16} {
		for _, batchSize := range []int{1, 4} {
			t.Run(fmt.Sprintf("events=%d/batch=%d", events, batchSize), func(t *testing.T) {
				service, st, _, _ := newRawArchiveTestService(t, 2, batchSize)
				ctx := context.Background()
				insertArchiveTestEvents(t, st, archiveTestServiceEvents(events))
				catchUpUsageAggregate(t, st)
				created, err := service.CreateArchive(ctx, 20_000)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
					t.Fatal(err)
				}
				verified, err := service.VerifyArchive(ctx, created.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				inspections := 0
				service.archive.testHook = func(point string) error {
					if point == "archive_segment_content_inspection_started" {
						inspections++
					}
					return nil
				}
				completed, err := service.DeleteArchive(ctx, created.Run.ID)
				if err != nil || completed.Run.DeletedEventCount != int64(events) || completed.Run.Status != usagearchive.StatusCompleted {
					t.Fatalf("cleanup failed: run=%#v err=%v", completed.Run, err)
				}
				if inspections != len(verified.Segments) {
					t.Fatalf("full-content inspections=%d, want %d (one pass, independent of %d delete batches)", inspections, len(verified.Segments), (events+batchSize-1)/batchSize)
				}
				t.Logf("events=%d batches=%d segments=%d full-content inspections=%d", events, (events+batchSize-1)/batchSize, len(verified.Segments), inspections)
			})
		}
	}
}

func TestUsageArchiveDeleteRejectsEvidenceChangedBetweenBatches(t *testing.T) {
	for _, mutation := range []struct {
		name, change, restore string
	}{
		{"manifest digest", `update usage_archive_runs set manifest_sha256 = 'different-digest' where id = ?`, `update usage_archive_runs set manifest_sha256 = ? where id = ?`},
		{"segment digest", `update usage_archive_segments set content_sha256 = 'different-digest' where run_id = ? and sequence = 1`, `update usage_archive_segments set content_sha256 = ? where run_id = ? and sequence = 1`},
		{"segment path", `update usage_archive_segments set file_name = 'different-segment.jsonl.gz' where run_id = ? and sequence = 1`, `update usage_archive_segments set file_name = ? where run_id = ? and sequence = 1`},
		{"verification timestamp", `update usage_archive_runs set verified_at_ms = verified_at_ms + 1 where id = ?`, `update usage_archive_runs set verified_at_ms = ? where id = ?`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			service, _, db, _, verified := newVerifiedArchiveDeleteTestService(t)
			ctx := context.Background()
			changed := false
			service.archive.testHook = func(point string) error {
				if point != "delete_batch_committed" || changed {
					return nil
				}
				changed = true
				_, err := db.ExecContext(ctx, mutation.change, verified.Run.ID)
				return err
			}
			if _, err := service.DeleteArchive(ctx, verified.Run.ID); !errors.Is(err, ErrArchiveCoverageIncomplete) {
				t.Fatalf("changed %s reused stale content evidence: %v", mutation.name, err)
			}
			var raw, deleted int64
			if err := db.QueryRow(`select (select count(*) from usage_events), deleted_event_count from usage_archive_runs where id = ?`, verified.Run.ID).Scan(&raw, &deleted); err != nil {
				t.Fatal(err)
			}
			if !changed || raw != 3 || deleted != 1 {
				t.Fatalf("changed evidence advanced deletion: changed=%v raw=%d deleted=%d", changed, raw, deleted)
			}
			var original any
			switch mutation.name {
			case "manifest digest":
				original = verified.Run.ManifestSHA256
			case "segment digest":
				original = verified.Segments[0].ContentSHA256
			case "segment path":
				original = verified.Segments[0].FileName
			case "verification timestamp":
				original = verified.Run.VerifiedAtMS
			}
			if _, err := db.ExecContext(ctx, mutation.restore, original, verified.Run.ID); err != nil {
				t.Fatal(err)
			}
			inspections := 0
			service.archive.testHook = func(point string) error {
				if point == "archive_segment_content_inspection_started" {
					inspections++
				}
				return nil
			}
			completed, err := service.ResumeArchive(ctx, verified.Run.ID)
			if err != nil || completed.Run.Status != usagearchive.StatusCompleted || inspections != len(verified.Segments) {
				t.Fatalf("recovery must verify fresh content once: run=%#v inspections=%d err=%v", completed.Run, inspections, err)
			}
		})
	}
}

func TestUsageArchiveDeleteRejectsFileChangedDuringContentVerification(t *testing.T) {
	service, _, db, directory, verified := newVerifiedArchiveDeleteTestService(t)
	file := filepath.Join(directory, filepath.FromSlash(verified.Segments[0].FileName))
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	service.archive.testHook = func(point string) error {
		if point != "archive_segment_content_inspection_started" || changed {
			return nil
		}
		changed = true
		if err := os.WriteFile(file, original, 0o600); err != nil {
			return err
		}
		return os.Chtimes(file, info.ModTime(), info.ModTime())
	}
	if _, err := service.DeleteArchive(context.Background(), verified.Run.ID); !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("file rewritten during inspection must invalidate the witness: %v", err)
	}
	var raw, deleted int64
	if err := db.QueryRow(`select (select count(*) from usage_events), deleted_event_count from usage_archive_runs where id = ?`, verified.Run.ID).Scan(&raw, &deleted); err != nil {
		t.Fatal(err)
	}
	if !changed || raw != 4 || deleted != 0 {
		t.Fatalf("changed file published a reusable witness: changed=%v raw=%d deleted=%d", changed, raw, deleted)
	}
}

func TestUsageArchiveDeleteFileVerificationAllowsConcurrentWrites(t *testing.T) {
	service, _, db, _, verified := newVerifiedArchiveDeleteTestService(t)
	ctx := context.Background()
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `pragma busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `create table archive_verification_writer_test (id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	writes, contentWrites, batchWrites := 0, 0, 0
	service.archive.testHook = func(point string) error {
		switch point {
		case "archive_segment_content_inspection_started":
			contentWrites++
		case "delete_batch_files_check_started", "delete_batch_files_checked":
			batchWrites++
		default:
			return nil
		}
		writes++
		_, err := writer.ExecContext(ctx, `insert into archive_verification_writer_test values (?)`, writes)
		return err
	}
	completed, err := service.DeleteArchive(ctx, verified.Run.ID)
	if err != nil || completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 4 || contentWrites != len(verified.Segments) || batchWrites != 8 {
		t.Fatalf("content verification and per-batch preparation must allow independent writes: run=%#v content=%d batch=%d err=%v", completed.Run, contentWrites, batchWrites, err)
	}
}

func TestUsageArchiveDeleteRejectsFilesChangedAfterContentVerification(t *testing.T) {
	for _, damage := range []string{"missing manifest", "missing segment", "replace manifest", "replace segment", "in-place manifest", "in-place segment", "rewrite manifest", "rewrite segment"} {
		t.Run(damage, func(t *testing.T) {
			service, _, db, directory, verified := newVerifiedArchiveDeleteTestService(t)
			name := verified.Run.ManifestFile
			if strings.HasSuffix(damage, "segment") {
				name = verified.Segments[len(verified.Segments)-1].FileName
			}
			file := filepath.Join(directory, filepath.FromSlash(name))
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			batches := 0
			service.archive.testHook = func(point string) error {
				if point != "delete_batch_files_checked" {
					return nil
				}
				batches++
				if batches != 2 {
					return nil
				}
				switch {
				case strings.HasPrefix(damage, "missing"):
					return os.Remove(file)
				case strings.HasPrefix(damage, "replace"):
					replacement := file + ".replacement"
					if err := os.WriteFile(replacement, original, 0o600); err != nil {
						return err
					}
					if err := replaceArchiveFile(replacement, file); err != nil {
						return err
					}
				default:
					changed := append([]byte(nil), original...)
					if strings.HasPrefix(damage, "in-place") {
						changed[len(changed)/2] ^= 1
					}
					if err := os.WriteFile(file, changed, 0o600); err != nil {
						return err
					}
				}
				// Size and mtime alone must not let a changed file reuse the proof.
				return os.Chtimes(file, info.ModTime(), info.ModTime())
			}
			if _, err := service.DeleteArchive(context.Background(), verified.Run.ID); err == nil {
				t.Fatal("later batch accepted file evidence changed after content verification")
			}
			var raw, deleted, last int64
			if err := db.QueryRow(`select (select count(*) from usage_events), deleted_event_count, last_deleted_event_id from usage_archive_runs where id = ?`, verified.Run.ID).Scan(&raw, &deleted, &last); err != nil {
				t.Fatal(err)
			}
			if batches != 2 || raw != 3 || deleted != 1 || last != verified.Segments[0].FirstEventID {
				t.Fatalf("changed proof advanced deletion: batches=%d raw=%d deleted=%d last=%d", batches, raw, deleted, last)
			}
			service.archive.testHook = nil
			if err := os.WriteFile(file, original, 0o600); err != nil {
				t.Fatal(err)
			}
			completed, err := service.ResumeArchive(context.Background(), verified.Run.ID)
			if err != nil || completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 4 {
				t.Fatalf("fresh verification must allow recovery after restoring the file: run=%#v err=%v", completed.Run, err)
			}
		})
	}
}

func TestUsageArchiveDeleteRollsBackFileChangeBeforeCommit(t *testing.T) {
	service, st, db, directory, verified := newVerifiedArchiveDeleteTestService(t)
	ctx := context.Background()
	if _, err := st.UsageArchives.BeginDelete(ctx, verified.Run.ID, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, filepath.FromSlash(verified.Segments[0].FileName))
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	_, err = st.UsageArchives.DeleteBatch(ctx, verified.Run.ID, 1, time.Now().UnixMilli(), func(ctx context.Context, run usagearchive.Run, segments []usagearchive.Segment) (usagearchive.DeleteFileCheck, error) {
		prepare, err := service.archive.prepareDeleteFiles(ctx, run, segments)
		if err != nil {
			return nil, err
		}
		check, err := prepare(ctx, run, segments)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) error {
			checks++
			if checks == 2 {
				if err := os.WriteFile(file, append(original, 'x'), 0o600); err != nil {
					return err
				}
			}
			return check(ctx)
		}, nil
	})
	if err == nil || checks != 2 {
		t.Fatalf("pre-commit file change must reject the batch: checks=%d err=%v", checks, err)
	}
	var raw, deleted, last, deletedRefs, deletedLedger, dailyCoverage int64
	if err := db.QueryRow(`select
		(select count(*) from usage_events), deleted_event_count, last_deleted_event_id,
		(select count(*) from usage_archive_event_refs where raw_deleted_at_ms is not null),
		(select count(*) from usage_event_identity_ledger where raw_event_id is null),
		(select coalesce(sum(deleted_event_count), 0) from usage_archive_deleted_coverage_daily)
		from usage_archive_runs where id = ?`, verified.Run.ID).Scan(&raw, &deleted, &last, &deletedRefs, &deletedLedger, &dailyCoverage); err != nil {
		t.Fatal(err)
	}
	if raw != 4 || deleted != 0 || last != 0 || deletedRefs != 0 || deletedLedger != 0 || dailyCoverage != 0 {
		t.Fatalf("file change must roll back the whole destructive batch: raw=%d deleted=%d last=%d refs=%d ledger=%d coverage=%d", raw, deleted, last, deletedRefs, deletedLedger, dailyCoverage)
	}
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	completed, err := service.DeleteArchive(ctx, verified.Run.ID)
	if err != nil || completed.Run.Status != usagearchive.StatusCompleted {
		t.Fatalf("rollback must leave the delete run resumable: run=%#v err=%v", completed.Run, err)
	}
}

func newVerifiedArchiveDeleteTestService(t *testing.T) (*Service, *store.Store, *sql.DB, string, ArchiveStatus) {
	t.Helper()
	service, st, db, directory := newRawArchiveTestService(t, 2, 1)
	ctx := context.Background()
	insertArchiveTestEvents(t, st, archiveTestServiceEvents(4))
	catchUpUsageAggregate(t, st)
	created, err := service.CreateArchive(ctx, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
		t.Fatal(err)
	}
	verified, err := service.VerifyArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return service, st, db, directory, verified
}

func TestUsageArchiveDeletionRevalidatesFilesBetweenCommittedBatches(t *testing.T) {
	for _, damage := range []string{"missing manifest", "corrupt manifest", "missing next segment", "corrupt next segment"} {
		t.Run(damage, func(t *testing.T) {
			service, st, rawDB, directory := newRawArchiveTestService(t, 2, 1)
			ctx := context.Background()
			insertArchiveTestEvents(t, st, archiveTestServiceEvents(4))
			catchUpUsageAggregate(t, st)
			created, err := service.CreateArchive(ctx, 5_000)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
				t.Fatal(err)
			}
			verified, err := service.VerifyArchive(ctx, created.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			name := verified.Run.ManifestFile
			if strings.Contains(damage, "segment") {
				name = verified.Segments[len(verified.Segments)-1].FileName
			}
			file := filepath.Join(directory, filepath.FromSlash(name))
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			service.archive.testHook = func(point string) error {
				if point != "delete_batch_committed" || fired {
					return nil
				}
				fired = true
				if strings.HasPrefix(damage, "missing") {
					return os.Remove(file)
				}
				return os.WriteFile(file, append(append([]byte(nil), original...), []byte("corrupt")...), 0o600)
			}
			if _, err := service.DeleteArchive(ctx, created.Run.ID); err == nil {
				t.Fatal("later raw batch deleted after archive evidence changed between committed batches")
			}
			var remaining, retained int64
			if err := rawDB.QueryRow(`select (select count(*) from usage_events), (select count(*) from usage_event_identity_ledger)`).Scan(&remaining, &retained); err != nil {
				t.Fatal(err)
			}
			failed, err := service.ArchiveStatus(ctx, created.Run.ID)
			if !fired || err != nil || remaining != 3 || retained != 4 || failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusDeleting || failed.Run.DeletedEventCount != 1 {
				t.Fatalf("failed later batch must preserve committed checkpoint and remaining history: raw=%d ledger=%d run=%#v err=%v", remaining, retained, failed.Run, err)
			}
			service.archive.testHook = nil
			if err := os.WriteFile(file, original, 0o600); err != nil {
				t.Fatal(err)
			}
			completed, err := service.ResumeArchive(ctx, created.Run.ID)
			if err != nil || completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 4 {
				t.Fatalf("exact archive restoration must resume remaining batches: %#v %v", completed.Run, err)
			}
		})
	}
}

func TestUsageArchiveDeletionRevalidatesPublishedFiles(t *testing.T) {
	for _, stage := range []string{"verified", "deleting", "failed-deleting"} {
		for _, damage := range []string{"missing manifest", "corrupt manifest", "missing segment", "corrupt segment"} {
			t.Run(stage+"/"+damage, func(t *testing.T) {
				service, st, rawDB, archiveDirectory := newRawArchiveTestService(t, 2, 1)
				ctx := context.Background()
				insertArchiveTestEvents(t, st, archiveTestServiceEvents(4))
				catchUpUsageAggregate(t, st)
				created, err := service.CreateArchive(ctx, 5_000)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.ResumeArchive(ctx, created.Run.ID); err != nil {
					t.Fatal(err)
				}
				verified, err := service.VerifyArchive(ctx, created.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				var deletedBefore int64
				if stage != "verified" {
					deleting, err := st.UsageArchives.BeginDelete(ctx, created.Run.ID, time.Now().UnixMilli())
					if err != nil {
						t.Fatal(err)
					}
					prepare, err := service.archive.prepareDeleteFiles(ctx, deleting, verified.Segments)
					if err != nil {
						t.Fatal(err)
					}
					batch, err := st.UsageArchives.DeleteBatch(ctx, created.Run.ID, 1, time.Now().UnixMilli(), prepare)
					if err != nil || batch.Deleted != 1 {
						t.Fatalf("initial delete batch = %#v, %v", batch, err)
					}
					deletedBefore = batch.Run.DeletedEventCount
					if stage == "failed-deleting" {
						if _, err := st.UsageArchives.RecordFailure(ctx, created.Run.ID, usagearchive.StatusDeleting, errors.New("isolated interruption"), time.Now().UnixMilli()); err != nil {
							t.Fatal(err)
						}
					}
				}

				fileName := verified.Run.ManifestFile
				if strings.HasSuffix(damage, "segment") {
					fileName = verified.Segments[len(verified.Segments)-1].FileName
				}
				filePath := filepath.Join(archiveDirectory, filepath.FromSlash(fileName))
				original, err := os.ReadFile(filePath)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(damage, "missing") {
					err = os.Remove(filePath)
				} else {
					err = os.WriteFile(filePath, append(append([]byte(nil), original...), []byte("corrupt")...), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				counts := func() [5]int64 {
					t.Helper()
					var result [5]int64
					if err := rawDB.QueryRowContext(ctx, `select
						(select count(*) from usage_events),
						(select count(*) from usage_event_identity_ledger),
						(select count(*) from usage_event_identity_ledger where raw_event_id is not null),
						(select count(*) from usage_archive_event_refs),
						(select count(*) from usage_archive_event_refs where raw_deleted_at_ms is not null)`,
					).Scan(&result[0], &result[1], &result[2], &result[3], &result[4]); err != nil {
						t.Fatal(err)
					}
					return result
				}
				before := counts()
				restarted := New(st, WithArchive(ArchiveConfig{
					Directory: archiveDirectory, SegmentEventLimit: 2, DeleteBatchSize: 1, AggregateReadsEnabled: true,
				}))
				switch stage {
				case "verified":
					_, _, err = restarted.SubmitArchiveDeletion(ctx, created.Run.ID, true)
				case "deleting":
					_, err = restarted.ResumeArchiveAtStage(ctx, created.Run.ID, usagearchive.StatusDeleting)
				default:
					_, err = restarted.ResumeArchive(ctx, created.Run.ID)
				}
				if err == nil {
					t.Fatal("raw cleanup proceeded after a verified archive file was lost or corrupted")
				}
				if after := counts(); after != before {
					t.Fatalf("unsafe cleanup changed raw data or identity evidence: before=%v after=%v", before, after)
				}
				failed, err := restarted.ArchiveStatus(ctx, created.Run.ID)
				if err != nil || failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusDeleting || failed.Run.DeletedEventCount != deletedBefore {
					t.Fatalf("cleanup failure must remain recoverable without advancing: %#v, %v", failed.Run, err)
				}
				if err := os.WriteFile(filePath, original, 0o600); err != nil {
					t.Fatal(err)
				}
				completed, err := restarted.ResumeArchive(ctx, created.Run.ID)
				if err != nil || completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 4 {
					t.Fatalf("cleanup should resume after restoring the exact archive: %#v, %v", completed.Run, err)
				}
			})
		}
	}
}

func TestUsageArchiveServiceCanAbandonFailedVerificationAndRearchiveRawEvents(t *testing.T) {
	service, st, rawDB, _ := newRawArchiveTestService(t, 2, 1)
	events := archiveTestServiceEvents(2)
	if _, err := st.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert test events: %v", err)
	}
	catchUpUsageAggregate(t, st)
	ctx := context.Background()

	// 1. CreateArchive -> ResumeArchive -> archived (2 events)
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if archived.Run.ArchivedEventCount != 2 {
		t.Fatalf("archived event count = %d, want 2", archived.Run.ArchivedEventCount)
	}

	// 2. Corrupt one segment file
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

	// 3. VerifyArchive must fail -> status failed, resume_status verifying
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err == nil {
		t.Fatal("verify archive succeeded for corrupt segment, want error")
	}
	failed, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("get archive status: %v", err)
	}
	if failed.Run.Status != usagearchive.StatusFailed || failed.Run.ResumeStatus != usagearchive.StatusVerifying {
		t.Fatalf("failed verification run = %#v", failed.Run)
	}

	// 4. Confirm refs exist before cancel
	var refCountBefore int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, created.Run.ID).Scan(&refCountBefore); err != nil {
		t.Fatalf("query refs before cancel: %v", err)
	}
	if refCountBefore != 2 {
		t.Fatalf("refs count before cancel = %d, want 2", refCountBefore)
	}

	// 5. CancelArchive must succeed -> status cancelled, resume_status empty
	cancelled, err := service.CancelArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("cancel archive: %v", err)
	}
	if cancelled.Run.Status != usagearchive.StatusCancelled || cancelled.Run.ResumeStatus != "" {
		t.Fatalf("cancelled run = %#v", cancelled.Run)
	}

	// 6. Confirm refs are released (count == 0)
	var refCountAfter int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, created.Run.ID).Scan(&refCountAfter); err != nil {
		t.Fatalf("query refs after cancel: %v", err)
	}
	if refCountAfter != 0 {
		t.Fatalf("refs count after cancel = %d, want 0", refCountAfter)
	}

	// 7. Raw events in usage_events must be fully preserved (count == 2)
	var rawCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("query raw events: %v", err)
	}
	if rawCount != 2 {
		t.Fatalf("raw events count = %d, want 2", rawCount)
	}

	// 8. Identity ledger rows must be preserved
	var ledgerCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_event_identity_ledger`).Scan(&ledgerCount); err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if ledgerCount != 2 {
		t.Fatalf("ledger count = %d, want 2", ledgerCount)
	}

	// 9. Segments and files must be preserved
	var segmentCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_segments where run_id = ?`, created.Run.ID).Scan(&segmentCount); err != nil {
		t.Fatalf("query segments: %v", err)
	}
	if segmentCount != len(archived.Segments) {
		t.Fatalf("segment count = %d, want %d", segmentCount, len(archived.Segments))
	}
	if _, err := os.Stat(segmentPath); err != nil {
		t.Fatalf("corrupted segment file missing: %v", err)
	}

	// 10. Old run is no longer active
	active, found, err := service.ActiveArchiveRun(ctx)
	if err != nil {
		t.Fatalf("active archive run check: %v", err)
	}
	if found && active.ID == created.Run.ID {
		t.Fatalf("cancelled run is still active = %#v", active)
	}

	// 11. Same raw events can be re-archived with same cutoff
	second, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create second archive: %v", err)
	}
	secondArchived, err := service.ResumeArchive(ctx, second.Run.ID)
	if err != nil {
		t.Fatalf("resume second archive: %v", err)
	}
	if secondArchived.Run.Status != usagearchive.StatusArchived || secondArchived.Run.ArchivedEventCount != 2 {
		t.Fatalf("second archive status = %#v", secondArchived.Run)
	}

	// 12. Verify refs for second run now exist with second run ID
	var secondRefCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, second.Run.ID).Scan(&secondRefCount); err != nil {
		t.Fatalf("query second run refs: %v", err)
	}
	if secondRefCount != 2 {
		t.Fatalf("second run ref count = %d, want 2", secondRefCount)
	}

	// 13. Verify second archive succeeds
	secondVerified, err := service.VerifyArchive(ctx, second.Run.ID)
	if err != nil {
		t.Fatalf("verify second archive: %v", err)
	}
	if secondVerified.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("second verified status = %#v", secondVerified.Run)
	}
}

func TestUsageArchiveServiceCancelFailsWhenRawEventIsMissing(t *testing.T) {
	service, st, rawDB, _ := newRawArchiveTestService(t, 2, 1)
	events := archiveTestServiceEvents(2)
	if _, err := st.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert test events: %v", err)
	}
	catchUpUsageAggregate(t, st)
	ctx := context.Background()

	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}

	// Corrupt a segment so verification fails
	segmentPath, err := service.archive.resolveArchivePath(archived.Segments[0].FileName)
	if err != nil {
		t.Fatalf("resolve segment: %v", err)
	}
	file, err := os.OpenFile(segmentPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open segment: %v", err)
	}
	_, _ = file.WriteString("corrupt")
	_ = file.Close()

	if _, err := service.VerifyArchive(ctx, created.Run.ID); err == nil {
		t.Fatal("verify should fail for corrupt segment")
	}

	// Simulate raw event data loss externally
	if _, err := rawDB.ExecContext(ctx, `delete from usage_events where event_hash = ?`, events[0].EventHash); err != nil {
		t.Fatalf("delete raw event: %v", err)
	}

	// CancelArchive must fail closed
	_, err = service.CancelArchive(ctx, created.Run.ID)
	if err == nil {
		t.Fatal("cancel archive succeeded with missing raw event, want error")
	}
	if !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("cancel archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}

	// Status must remain failed/verifying
	status, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("get archive status: %v", err)
	}
	if status.Run.Status != usagearchive.StatusFailed || status.Run.ResumeStatus != usagearchive.StatusVerifying {
		t.Fatalf("status after rejected cancel = %#v", status.Run)
	}

	// Refs must still exist
	var refCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, created.Run.ID).Scan(&refCount); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if refCount != 2 {
		t.Fatalf("ref count after rejected cancel = %d, want 2", refCount)
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
	inspections := 0
	service.archive.testHook = func(point string) error {
		if point == "archive_segment_content_inspection_started" {
			inspections++
		}
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
	if inspections != 1 {
		t.Fatalf("interrupted invocation content inspections=%d, want 1", inspections)
	}
	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     3,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	inspections = 0
	restarted.archive.testHook = func(point string) error {
		if point == "archive_segment_content_inspection_started" {
			inspections++
		}
		return nil
	}
	completed, err := restarted.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume delete: %v", err)
	}
	if completed.Run.Status != usagearchive.StatusCompleted || completed.Run.DeletedEventCount != 3 {
		t.Fatalf("completed delete = %#v", completed)
	}
	if inspections != 1 {
		t.Fatalf("restarted invocation content inspections=%d, want 1 fresh pass", inspections)
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

func canonicalArchiveTestHash(raw string) string {
	if usageparser.IsCanonicalSHA256Hex(raw) {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func archiveTestServiceEvents(count int) []usageparser.Event {
	events := make([]usageparser.Event, 0, count)
	for index := range count {
		timestampMS := int64(index+1) * 1_000
		failed := index%2 == 1
		events = append(events, usageparser.Event{
			RequestID:       "archive-request-" + strconv.Itoa(index),
			EventHash:       canonicalArchiveTestHash("archive-service-event-" + strconv.Itoa(index)),
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
	for iteration := 0; iteration < 100; iteration++ {
		result, err := st.IdentityProjections.CatchUp(ctx, 2, nowMS+int64(iteration))
		if err != nil {
			t.Fatalf("catch up canonical identity: %v", err)
		}
		if !result.Pending {
			break
		}
		if iteration == 99 {
			t.Fatal("canonical identity catch-up did not complete")
		}
	}
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
		{name: "codex legacy identity evidence", run: st.CatchUpCodexLegacyIdentityEvidence},
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

func TestUsageArchiveRoundTripPreservesAuthAccountIDSnapshot(t *testing.T) {
	events := []usageparser.Event{
		{
			RequestID:             "archive-id-req-0",
			EventHash:             canonicalArchiveTestHash("archive-id-hash-0"),
			TimestampMS:           1_000,
			Timestamp:             time.UnixMilli(1_000).UTC().Format(time.RFC3339Nano),
			Provider:              "codex",
			ExecutorType:          "CodexExecutor",
			Model:                 "gpt-4o",
			Endpoint:              "POST /v1/responses",
			Method:                "POST",
			Path:                  "/v1/responses",
			AuthIndex:             "workspace-common",
			AccountSnapshot:       "member-a@example.com",
			AuthAccountIDSnapshot: "acc-user-member-a",
			InputTokens:           100,
			OutputTokens:          20,
			TotalTokens:           120,
			RawJSON:               `{"request":{"model":"gpt-4o"}}`,
			CreatedAtMS:           1_000,
		},
		{
			RequestID:             "archive-id-req-1",
			EventHash:             canonicalArchiveTestHash("archive-id-hash-1"),
			TimestampMS:           2_000,
			Timestamp:             time.UnixMilli(2_000).UTC().Format(time.RFC3339Nano),
			Provider:              "codex",
			ExecutorType:          "CodexExecutor",
			Model:                 "gpt-4o",
			Endpoint:              "POST /v1/responses",
			Method:                "POST",
			Path:                  "/v1/responses",
			AuthIndex:             "workspace-common",
			AccountSnapshot:       "member-b@example.com",
			AuthAccountIDSnapshot: "acc-user-member-b",
			InputTokens:           100,
			OutputTokens:          20,
			TotalTokens:           120,
			RawJSON:               `{"request":{"model":"gpt-4o"}}`,
			CreatedAtMS:           2_000,
		},
	}

	service, st, archiveDirectory := newArchiveTestService(t, 2, 2, events)
	ctx := context.Background()

	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if len(archived.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(archived.Segments))
	}

	segmentPath := filepath.Join(archiveDirectory, filepath.FromSlash(archived.Segments[0].FileName))
	segmentBytes, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("read segment file: %v", err)
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(segmentBytes))
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer gzReader.Close()

	uncompressedJSONL, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("read uncompressed jsonl: %v", err)
	}

	// Verify decoded lines have auth_account_id_snapshot
	scanner := bufio.NewScanner(bytes.NewReader(uncompressedJSONL))
	var decodedEvents []map[string]any
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("unmarshal jsonl: %v", err)
		}
		decodedEvents = append(decodedEvents, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	if len(decodedEvents) != 2 {
		t.Fatalf("expected 2 decoded events, got %d", len(decodedEvents))
	}

	if got := decodedEvents[0]["auth_account_id_snapshot"]; got != "acc-user-member-a" {
		t.Errorf("event 0 auth_account_id_snapshot = %v, want acc-user-member-a", got)
	}
	if got := decodedEvents[1]["auth_account_id_snapshot"]; got != "acc-user-member-b" {
		t.Errorf("event 1 auth_account_id_snapshot = %v, want acc-user-member-b", got)
	}

	// Round-trip import into a new target database
	importResult, err := usageparser.ParseImportPayload(uncompressedJSONL)
	if err != nil {
		t.Fatalf("parse import payload: %v", err)
	}
	if len(importResult.Events) != 2 {
		t.Fatalf("expected 2 parsed events, got %d", len(importResult.Events))
	}

	targetCfg := testutil.NewConfig(t)
	targetDB, err := sqliterepo.Open(targetCfg.DBPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	defer targetDB.Close()
	targetStore := store.New(targetDB)

	if _, err := targetStore.UsageEvents.InsertBatch(ctx, importResult.Events); err != nil {
		t.Fatalf("insert imported events: %v", err)
	}

	rows, err := targetDB.Query(`select id, auth_index, coalesce(auth_account_id_snapshot, ''), coalesce(account_snapshot, '') from usage_events order by id asc`)
	if err != nil {
		t.Fatalf("query target usage_events: %v", err)
	}
	defer rows.Close()

	type eventCheck struct {
		id            int64
		authIndex     string
		authAccountID string
		account       string
	}
	var checks []eventCheck
	for rows.Next() {
		var c eventCheck
		if err := rows.Scan(&c.id, &c.authIndex, &c.authAccountID, &c.account); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		checks = append(checks, c)
	}
	if len(checks) != 2 {
		t.Fatalf("expected 2 imported rows, got %d", len(checks))
	}
	if checks[0].authAccountID != "acc-user-member-a" || checks[0].account != "member-a@example.com" {
		t.Errorf("imported event 0 mismatch: %+v", checks[0])
	}
	if checks[1].authAccountID != "acc-user-member-b" || checks[1].account != "member-b@example.com" {
		t.Errorf("imported event 1 mismatch: %+v", checks[1])
	}
	if checks[0].authAccountID == checks[1].authAccountID {
		t.Errorf("expected distinct member auth_account_id_snapshot, but they were equal")
	}

	catchUpUsageAggregate(t, st)
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if _, err := service.DeleteArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("delete archive: %v", err)
	}
}

func TestUsageArchiveRoundTripPreservesCodexMemberIdentity(t *testing.T) {
	events := []usageparser.Event{
		{
			RequestID:             "archive-codex-req-a",
			EventHash:             canonicalArchiveTestHash("archive-codex-hash-a"),
			TimestampMS:           1_000,
			Timestamp:             time.UnixMilli(1_000).UTC().Format(time.RFC3339Nano),
			Provider:              "codex",
			AuthProviderSnapshot:  "codex",
			ExecutorType:          "CodexExecutor",
			Model:                 "gpt-4o",
			Endpoint:              "POST /v1/responses",
			Method:                "POST",
			Path:                  "/v1/responses",
			AuthIndex:             "workspace-common",
			AuthAccountIDSnapshot: "workspace-shared",
			AccountSnapshot:       "member-a@example.com",
			InputTokens:           100,
			OutputTokens:          20,
			TotalTokens:           120,
			RawJSON:               `{"request":{"model":"gpt-4o"}}`,
			CreatedAtMS:           1_000,
		},
		{
			RequestID:             "archive-codex-req-b",
			EventHash:             canonicalArchiveTestHash("archive-codex-hash-b"),
			TimestampMS:           2_000,
			Timestamp:             time.UnixMilli(2_000).UTC().Format(time.RFC3339Nano),
			Provider:              "codex",
			AuthProviderSnapshot:  "codex",
			ExecutorType:          "CodexExecutor",
			Model:                 "gpt-4o",
			Endpoint:              "POST /v1/responses",
			Method:                "POST",
			Path:                  "/v1/responses",
			AuthIndex:             "workspace-common",
			AuthAccountIDSnapshot: "workspace-shared",
			AccountSnapshot:       "member-b@example.com",
			InputTokens:           100,
			OutputTokens:          20,
			TotalTokens:           120,
			RawJSON:               `{"request":{"model":"gpt-4o"}}`,
			CreatedAtMS:           2_000,
		},
	}

	service, st, archiveDirectory := newArchiveTestService(t, 2, 2, events)
	ctx := context.Background()

	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive: %v", err)
	}
	if len(archived.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(archived.Segments))
	}

	segmentPath := filepath.Join(archiveDirectory, filepath.FromSlash(archived.Segments[0].FileName))
	segmentBytes, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("read segment file: %v", err)
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(segmentBytes))
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer gzReader.Close()

	uncompressedJSONL, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("read uncompressed jsonl: %v", err)
	}

	importResult, err := usageparser.ParseImportPayload(uncompressedJSONL)
	if err != nil {
		t.Fatalf("parse import payload: %v", err)
	}
	if len(importResult.Events) != 2 {
		t.Fatalf("expected 2 parsed events, got %d", len(importResult.Events))
	}

	targetCfg := testutil.NewConfig(t)
	targetDB, err := sqliterepo.Open(targetCfg.DBPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	defer targetDB.Close()
	targetStore := store.New(targetDB)

	if _, err := targetStore.UsageEvents.InsertBatch(ctx, importResult.Events); err != nil {
		t.Fatalf("insert imported events: %v", err)
	}

	rows, err := targetDB.Query(`select id, coalesce(auth_account_id_snapshot, ''), coalesce(account_snapshot, ''), coalesce(auth_provider_snapshot, ''), coalesce(auth_file_snapshot, ''), coalesce(auth_index, ''), coalesce(source, '') from usage_events order by id asc`)
	if err != nil {
		t.Fatalf("query target usage_events: %v", err)
	}
	defer rows.Close()

	type restoredRow struct {
		id                   int64
		authAccountID        string
		account              string
		authProviderSnapshot string
		authFileSnapshot     string
		authIndex            string
		source               string
	}
	var restoredList []restoredRow
	for rows.Next() {
		var r restoredRow
		if err := rows.Scan(&r.id, &r.authAccountID, &r.account, &r.authProviderSnapshot, &r.authFileSnapshot, &r.authIndex, &r.source); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		restoredList = append(restoredList, r)
	}
	if len(restoredList) != 2 {
		t.Fatalf("expected 2 imported rows, got %d", len(restoredList))
	}

	if restoredList[0].authAccountID != "workspace-shared" || restoredList[0].account != "member-a@example.com" {
		t.Errorf("imported event 0 mismatch: %+v", restoredList[0])
	}
	if restoredList[1].authAccountID != "workspace-shared" || restoredList[1].account != "member-b@example.com" {
		t.Errorf("imported event 1 mismatch: %+v", restoredList[1])
	}

	keyA, okA := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot:  restoredList[0].authProviderSnapshot,
		AuthAccountIDSnapshot: restoredList[0].authAccountID,
		AccountSnapshot:       restoredList[0].account,
		AuthFileSnapshot:      restoredList[0].authFileSnapshot,
		AuthIndex:             restoredList[0].authIndex,
		Source:                restoredList[0].source,
	})
	keyB, okB := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot:  restoredList[1].authProviderSnapshot,
		AuthAccountIDSnapshot: restoredList[1].authAccountID,
		AccountSnapshot:       restoredList[1].account,
		AuthFileSnapshot:      restoredList[1].authFileSnapshot,
		AuthIndex:             restoredList[1].authIndex,
		Source:                restoredList[1].source,
	})

	if !okA || keyA == "" {
		t.Fatalf("expected valid keyA, got ok=%v keyA=%q", okA, keyA)
	}
	if !okB || keyB == "" {
		t.Fatalf("expected valid keyB, got ok=%v keyB=%q", okB, keyB)
	}
	if keyA == keyB {
		t.Fatalf("expected distinct member keys for same workspace, but got equal keys: %q", keyA)
	}

	catchUpUsageAggregate(t, st)
	if _, err := service.VerifyArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if _, err := service.DeleteArchive(ctx, created.Run.ID); err != nil {
		t.Fatalf("delete archive: %v", err)
	}
}

func TestManualArchiveReadinessRequiresResponseMetadataBackfill(t *testing.T) {
	ctx := context.Background()
	service, st, rawDB, archiveDirectory := newRawArchiveTestService(t, 2, 1)

	rawJSON := `{"response_headers":{"Retry-After":["45"],"X-Codex-Plan-Type":["plus"],"X-OAI-Request-ID":["req-readiness"],"Set-Cookie":["session=secret"]}}`
	if _, err := rawDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, failed, fail_status_code,
		cache_input_mode, normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		canonicalArchiveTestHash("event-readiness-unbackfilled"),
		int64(1_000),
		"2026-01-01T00:00:01Z",
		"gpt-test",
		1,
		429,
		usageparser.CacheInputModeIncluded,
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		rawJSON,
		int64(1_000),
	); err != nil {
		t.Fatalf("insert unbackfilled event: %v", err)
	}

	// Test A: Cache accounting migration is completed, but response metadata is unbackfilled.
	// ResumeArchive must fail with ErrArchiveCoverageIncomplete.
	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if created.Run.Status != usagearchive.StatusPreviewed {
		t.Fatalf("created run status = %s, want previewed", created.Run.Status)
	}

	if _, err := service.ResumeArchive(ctx, created.Run.ID); !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("resume archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}

	status, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("archive status: %v", err)
	}
	if status.Run.Status != usagearchive.StatusPreviewed {
		t.Fatalf("run status after blocked resume = %s, want previewed", status.Run.Status)
	}
	if len(status.Segments) != 0 {
		t.Fatalf("segments after blocked resume = %d, want 0", len(status.Segments))
	}
	segmentFiles, err := filepath.Glob(filepath.Join(archiveDirectory, "*", "*.gz"))
	if err != nil {
		t.Fatalf("glob segment files: %v", err)
	}
	if len(segmentFiles) != 0 {
		t.Fatalf("found %d segment files after blocked resume: %#v", len(segmentFiles), segmentFiles)
	}
	var rawCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if rawCount != 1 {
		t.Fatalf("raw events count = %d, want 1", rawCount)
	}

	// Test B: Run backfill until no pending updates remain.
	for {
		updated, err := st.BackfillUsageResponseMetadata(ctx, 10)
		if err != nil {
			t.Fatalf("backfill response metadata: %v", err)
		}
		if updated == 0 {
			break
		}
	}

	// ResumeArchive must now succeed.
	archived, err := service.ResumeArchive(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("resume archive after backfill: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived {
		t.Fatalf("archived run status = %s, want archived", archived.Run.Status)
	}
	if len(archived.Segments) != 1 {
		t.Fatalf("archived segments = %d, want 1", len(archived.Segments))
	}

	// Verify archived record fields match the backfilled raw event.
	segmentPath := filepath.Join(archiveDirectory, filepath.FromSlash(archived.Segments[0].FileName))
	file, err := os.Open(segmentPath)
	if err != nil {
		t.Fatalf("open archive segment: %v", err)
	}
	defer file.Close()
	zipper, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open archive gzip: %v", err)
	}
	defer zipper.Close()
	scanner := bufio.NewScanner(zipper)
	if !scanner.Scan() {
		t.Fatalf("archive segment has no records: %v", scanner.Err())
	}
	var recordMap map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &recordMap); err != nil {
		t.Fatalf("unmarshal archive record payload: %v", err)
	}
	metaJSON, ok := recordMap["response_metadata_json"].(string)
	if !ok || metaJSON == "" {
		t.Fatalf("missing or empty response_metadata_json in archive record: %#v", recordMap)
	}
	if !strings.Contains(metaJSON, "Retry-After") && !strings.Contains(metaJSON, "45") && !strings.Contains(metaJSON, "retry_after") {
		t.Fatalf("response_metadata_json = %q, expected retry_after data", metaJSON)
	}
}

func TestUsageArchiveInspectionRequireRestorableEventHash(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, nil)
	runID := strings.Repeat("d", 32)
	if err := service.archive.ensureRunDirectory(runID); err != nil {
		t.Fatalf("prepare archive directory: %v", err)
	}
	legacyHash := "legacy-noncanonical-test-hash"
	payload := fmt.Sprintf(`{"_cpamp_archive_schema_version":1,"_cpamp_archive_event_id":1,"event_hash":%q,"timestamp_ms":1000,"timestamp":"1970-01-01T00:00:01Z","model":"gpt-test","input_tokens":100,"output_tokens":20,"reasoning_tokens":0,"cached_tokens":0,"cache_tokens":0,"cache_read_tokens":0,"cache_creation_tokens":0,"failed":0,"fail_status_code":0,"fail_summary":"","fail_body":"","created_at_ms":1000,"cache_input_mode":"included_in_input","normalized_uncached_input_tokens":100,"normalized_total_input_tokens":100,"normalized_cache_read_tokens":0,"normalized_cache_creation_tokens":0,"total_tokens":120,"header_quota_recover_at_ms":0,"raw_json":"{}"}`, legacyHash)
	record := store.UsageArchiveRecord{
		EventID:     1,
		EventHash:   legacyHash,
		TimestampMS: 1_000,
		Payload:     []byte(payload),
	}
	segment, _, err := service.archive.writeSegment(runID, 1, []store.UsageArchiveRecord{record})
	if err != nil {
		t.Fatalf("write segment with legacy hash: %v", err)
	}

	// With requireRestorableEventHash = false, inspection should succeed
	inspection, err := service.archive.inspectSegment(segment, sha256.New(), false)
	if err != nil {
		t.Fatalf("inspectSegment with requireRestorableEventHash=false failed: %v", err)
	}
	if inspection.EventCount != 1 {
		t.Fatalf("inspection event count = %d, want 1", inspection.EventCount)
	}

	// With requireRestorableEventHash = true, inspection must fail
	_, err = service.archive.inspectSegment(segment, sha256.New(), true)
	if err == nil {
		t.Fatal("inspectSegment with requireRestorableEventHash=true succeeded, want error")
	}
	if !errors.Is(err, usagearchive.ErrUnrestorableEventHash) {
		t.Fatalf("inspectSegment error = %v, want ErrUnrestorableEventHash", err)
	}
	if !errors.Is(err, usagearchive.ErrCoverageIncomplete) {
		t.Fatalf("inspectSegment error = %v, want ErrCoverageIncomplete", err)
	}
	if !strings.Contains(err.Error(), "cannot be restored under the current persistence policy") {
		t.Fatalf("inspectSegment error = %v, want policy error", err)
	}
}

func TestUsageArchivePreflightRejectsLegacyNoncanonicalEventHash(t *testing.T) {
	service, st, rawDB, archiveDirectory := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()

	legacyHash := "legacy-noncanonical-archive-hash"
	timestampMS := int64(1_000)
	rawJSON := `{"request":{"model":"gpt-test"}}`
	responseMetadataJSON := `{}`

	// Directly insert a legacy usage event with a noncanonical event_hash into usage_events.
	// All other fields are valid and fully compliant with current schema and derived rules.
	if _, err := rawDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		input_tokens, output_tokens, total_tokens,
		cache_input_mode,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		response_metadata_json, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		legacyHash,
		timestampMS,
		time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		"gpt-test",
		int64(100),
		int64(20),
		int64(120),
		usageparser.CacheInputModeIncluded,
		int64(100),
		int64(100),
		int64(0),
		int64(0),
		responseMetadataJSON,
		rawJSON,
		timestampMS,
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	// Ensure migration status is marked completed so readiness checks pass
	if _, err := rawDB.ExecContext(ctx, `update usage_data_migrations set
		status = 'completed', last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration completed: %v", err)
	}

	// Catch up aggregates so aggregate reads requirement is satisfied
	catchUpUsageAggregate(t, st)

	// Step 1: PreviewArchive must fail closed on preflight check
	_, err := service.PreviewArchive(ctx, 2_000)
	if err == nil {
		t.Fatal("preview archive succeeded for noncanonical event hash, want failure")
	}
	if !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("preview archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}
	if !errors.Is(err, ErrArchiveUnrestorableEventHash) {
		t.Fatalf("preview archive error = %v, want ErrArchiveUnrestorableEventHash", err)
	}
	if !strings.Contains(err.Error(), "cannot be restored") && !strings.Contains(err.Error(), "current persistence policy") {
		t.Fatalf("preview archive error = %v, want persistence policy error", err)
	}

	// Step 2: CreateArchive (manual) must fail closed before creating any run
	_, err = service.CreateArchive(ctx, 2_000)
	if err == nil {
		t.Fatal("create archive succeeded for noncanonical event hash, want failure")
	}
	if !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("create archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}
	if !errors.Is(err, ErrArchiveUnrestorableEventHash) {
		t.Fatalf("create archive error = %v, want ErrArchiveUnrestorableEventHash", err)
	}
	if !strings.Contains(err.Error(), "cannot be restored") && !strings.Contains(err.Error(), "current persistence policy") {
		t.Fatalf("create archive error = %v, want persistence policy error", err)
	}

	// Step 3: CreateRetentionArchive (automatic retention) must fail closed before creating any run
	_, err = service.CreateRetentionArchive(ctx, 2_000)
	if err == nil {
		t.Fatal("create retention archive succeeded for noncanonical event hash, want failure")
	}
	if !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("create retention archive error = %v, want ErrArchiveCoverageIncomplete", err)
	}
	if !errors.Is(err, ErrArchiveUnrestorableEventHash) {
		t.Fatalf("create retention archive error = %v, want ErrArchiveUnrestorableEventHash", err)
	}

	// Step 4: Verify no active run exists and no run record was written
	active, found, err := service.ActiveArchiveRun(ctx)
	if err != nil {
		t.Fatalf("check active run: %v", err)
	}
	if found {
		t.Fatalf("active archive run exists = %#v, want none", active)
	}

	var runCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_runs`).Scan(&runCount); err != nil {
		t.Fatalf("query runs count: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("usage_archive_runs count = %d, want 0", runCount)
	}

	// Step 5: Verify no segments or files were generated
	var refCount int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs`).Scan(&refCount); err != nil {
		t.Fatalf("query event refs count: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("usage_archive_event_refs count = %d, want 0", refCount)
	}

	files, err := filepath.Glob(filepath.Join(archiveDirectory, "*"))
	if err != nil {
		t.Fatalf("glob archive directory: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("archive directory files = %#v, want empty", files)
	}

	// Step 6: Verify raw event still safely exists in usage_events
	var count int
	if err := rawDB.QueryRowContext(ctx, `select count(*) from usage_events where event_hash = ?`, legacyHash).Scan(&count); err != nil {
		t.Fatalf("query legacy event count: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy event count = %d, want 1 (raw data must be preserved)", count)
	}

	// Step 7: Verify maintenance state machine is not deadlocked:
	// No active run exists, so no ErrArchiveMaintenanceLocked is thrown
	preview, err := service.PreviewArchive(ctx, 500)
	if err != nil {
		t.Fatalf("preview archive before legacy event: %v", err)
	}
	if preview.EventCount != 0 {
		t.Fatalf("preview event count = %d, want 0", preview.EventCount)
	}
	if _, err := service.CreateArchive(ctx, 500); !errors.Is(err, ErrArchiveNoEvents) {
		t.Fatalf("create archive before legacy event error = %v, want ErrArchiveNoEvents", err)
	}
}

func TestUsageArchiveVerificationRejectsLegacyNoncanonicalEventHashBeforeRawDelete(t *testing.T) {
	TestUsageArchivePreflightRejectsLegacyNoncanonicalEventHash(t)
}
