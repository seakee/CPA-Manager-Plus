package usagearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	identityprojection "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRepositoryArchiveVerifyResumeAndBoundedDelete(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()
	generate := true
	stream := false
	events[0].ResponseModel = "grok-archive-response"
	events[0].SessionID = "archive-session"
	events[0].ParentSessionID = "archive-parent-session"
	events[0].AccessTokenSHA256 = "archive-access-token-sha256"
	events[0].Generate = &generate
	events[0].Stream = &stream
	inserted, err := usageevent.New(db).InsertBatch(ctx, events)
	if err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	if inserted.Inserted != len(events) {
		t.Fatalf("insert result = %#v", inserted)
	}

	repository := New(db)
	preview, err := repository.Preview(ctx, 2_500)
	if err != nil {
		t.Fatalf("preview archive: %v", err)
	}
	if preview.EventCount != 2 || preview.TargetEventID != 2 ||
		preview.MinTimestampMS != 1_000 || preview.MaxTimestampMS != 2_000 ||
		preview.EstimatedBytes <= 0 {
		t.Fatalf("preview = %#v", preview)
	}
	run, err := repository.CreateRun(ctx, "archive-delete", 2_500, 10_000)
	if err != nil {
		t.Fatalf("create archive run: %v", err)
	}
	if _, err := repository.CreateRun(ctx, "replacement", 2_500, 10_001); !errors.Is(err, ErrMaintenanceLocked) {
		t.Fatalf("replacement run error = %v, want maintenance lock", err)
	}
	active, found, err := repository.ActiveRun(ctx)
	if err != nil || !found || active.ID != run.ID {
		t.Fatalf("active run = %#v found=%v err=%v", active, found, err)
	}

	run, err = repository.BeginArchive(ctx, run.ID, 10_002)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	if run.ProgressPhase != ProgressArchivingRecords || run.ProgressCurrent != 0 || run.ProgressTotal != 2 || run.ProgressUpdatedAtMS != 10_002 {
		t.Fatalf("initial archive progress = %#v", run)
	}
	records, err := repository.Records(ctx, run.ID, 0, 100, 1<<30)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if len(records) != 2 || records[0].EventID != 1 || records[1].EventID != 2 {
		t.Fatalf("archive records = %#v", records)
	}
	bounded, err := repository.Records(ctx, run.ID, 0, 100, int64(len(records[0].Payload)+1))
	if err != nil {
		t.Fatalf("read byte-bounded records: %v", err)
	}
	if len(bounded) != 1 || bounded[0].EventID != records[0].EventID {
		t.Fatalf("byte-bounded records = %#v", bounded)
	}

	var archivedPayload map[string]any
	if err := json.Unmarshal(records[0].Payload, &archivedPayload); err != nil {
		t.Fatalf("decode archive record: %v", err)
	}
	for key, want := range map[string]any{
		"client_ip":           events[0].ClientIP,
		"x_forwarded_for":     events[0].XForwardedFor,
		"user_agent":          events[0].UserAgent,
		"response_model":      events[0].ResponseModel,
		"session_id":          events[0].SessionID,
		"parent_session_id":   events[0].ParentSessionID,
		"access_token_sha256": events[0].AccessTokenSHA256,
		"generate":            generate,
		"stream":              stream,
		"fail_body":           events[0].FailBody,
		"raw_json":            events[0].RawJSON,
	} {
		if archivedPayload[key] != want {
			t.Fatalf("archive payload %s = %#v, want %#v", key, archivedPayload[key], want)
		}
	}
	var restored []usage.Event
	result, err := usage.StreamImportPayload(bytes.NewReader(append(records[0].Payload, '\n')), 1, func(batch []usage.Event) error {
		restored = append(restored, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("restore archive record: %v", err)
	}
	if result.Total != 1 || len(restored) != 1 ||
		restored[0].EventHash != events[0].EventHash ||
		restored[0].ClientIP != events[0].ClientIP ||
		restored[0].XForwardedFor != events[0].XForwardedFor ||
		restored[0].UserAgent != events[0].UserAgent ||
		restored[0].ResponseModel != events[0].ResponseModel ||
		restored[0].SessionID != events[0].SessionID ||
		restored[0].ParentSessionID != events[0].ParentSessionID ||
		restored[0].AccessTokenSHA256 != events[0].AccessTokenSHA256 ||
		restored[0].Generate == nil || *restored[0].Generate != generate ||
		restored[0].Stream == nil || *restored[0].Stream != stream ||
		restored[0].FailBody != events[0].FailBody {
		t.Fatalf("restored result=%#v events=%#v", result, restored)
	}

	segment := archiveTestSegment(run.ID, records)
	run, err = repository.RecordSegment(ctx, run.ID, segment, archiveRecordRefs(records), 10_003)
	if err != nil {
		t.Fatalf("record archive segment: %v", err)
	}
	if run.ArchivedEventCount != 2 || run.LastArchivedEventID != 2 {
		t.Fatalf("run after segment = %#v", run)
	}
	if run.ProgressPhase != ProgressArchivingRecords || run.ProgressCurrent != 2 || run.ProgressTotal != 2 || run.ProgressUpdatedAtMS != 10_003 {
		t.Fatalf("segment progress = %#v", run)
	}
	if err := repository.SetProgress(ctx, run.ID, StatusArchiving, ProgressArchiveFinalizing, 1, 2, "segments", 10_003); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetProgress(ctx, run.ID, StatusArchiving, "private-path", 1, 2, "segments", 10_003); err == nil {
		t.Fatal("accepted unknown progress phase")
	}
	listed, err := repository.ListRuns(ctx, RunListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Runs) != 1 || listed.Runs[0].ProgressPhase != ProgressArchiveFinalizing || listed.Runs[0].ProgressCurrent != 1 {
		t.Fatalf("list progress = %#v", listed.Runs)
	}
	rows, err := db.Query(`select
		event_hash, raw_event_id, timestamp_ms, segment_sequence, raw_deleted_at_ms
		from usage_archive_event_refs where run_id = ? order by raw_event_id`, run.ID)
	if err != nil {
		t.Fatalf("query archive event references: %v", err)
	}
	for index := 0; rows.Next(); index++ {
		var hash string
		var rawID, timestampMS int64
		var sequence int
		var deletedAt sql.NullInt64
		if err := rows.Scan(&hash, &rawID, &timestampMS, &sequence, &deletedAt); err != nil {
			_ = rows.Close()
			t.Fatalf("scan archive event reference: %v", err)
		}
		if index >= len(records) || hash != records[index].EventHash ||
			rawID != records[index].EventID || timestampMS != records[index].TimestampMS ||
			sequence != 1 || deletedAt.Valid {
			_ = rows.Close()
			t.Fatalf(
				"archive event reference %d = %q/%d/%d/%d/%v",
				index,
				hash,
				rawID,
				timestampMS,
				sequence,
				deletedAt,
			)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close archive event references: %v", err)
	}

	run, err = repository.MarkArchived(ctx, run.ID, "archive-digest", run.ID+"/manifest.json", "manifest-sha256", 10_004)
	if err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	if run.ProgressPhase != "" {
		t.Fatalf("archived progress = %#v", run)
	}
	if err := repository.SetProgress(ctx, run.ID, StatusArchiving, ProgressArchivePublishing, 0, 0, "", 10_005); err != nil {
		t.Fatal(err)
	}
	if latest, err := repository.Run(ctx, run.ID); err != nil || latest.ProgressPhase != "" {
		t.Fatalf("stale progress changed archived run: %#v %v", latest, err)
	}
	if _, found, err := repository.ActiveRun(ctx); err != nil || found {
		t.Fatalf("static manual archived run found=%v err=%v", found, err)
	}
	followup, err := repository.CreateRun(ctx, "after-archived", 4_000, 10_005)
	if err != nil {
		t.Fatalf("create run after manual archive: %v", err)
	}
	if followup.EventCount != 1 || followup.TargetEventID != 3 {
		t.Fatalf("follow-up after archived = %#v", followup)
	}
	if _, err := db.Exec(`delete from usage_archive_runs where id = ?`, followup.ID); err != nil {
		t.Fatalf("remove archived follow-up fixture: %v", err)
	}
	catchUpHourlyAggregate(t, ctx, db, 10_005)
	run, err = repository.BeginVerification(ctx, run.ID, 10_006)
	if err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	run, err = repository.MarkVerified(ctx, run.ID, 10_007)
	if err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	if run.ProgressPhase != "" {
		t.Fatalf("verified progress = %#v", run)
	}
	if _, found, err := repository.ActiveRun(ctx); err != nil || found {
		t.Fatalf("static manual verified run found=%v err=%v", found, err)
	}
	followup, err = repository.CreateRun(ctx, "after-verified", 4_000, 10_008)
	if err != nil {
		t.Fatalf("create run after manual verification: %v", err)
	}
	if followup.EventCount != 1 || followup.TargetEventID != 3 {
		t.Fatalf("follow-up after verified = %#v", followup)
	}
	if _, err := db.Exec(`delete from usage_archive_runs where id = ?`, followup.ID); err != nil {
		t.Fatalf("remove verified follow-up fixture: %v", err)
	}
	archivedCounts, err := repository.MaintenanceCounts(ctx)
	if err != nil {
		t.Fatalf("read archived maintenance counts: %v", err)
	}
	if archivedCounts.RawArchivedEventCount != 2 || archivedCounts.RawDeletedEventCount != 0 {
		t.Fatalf("archived maintenance counts = %#v", archivedCounts)
	}
	catchUpDeleteReadiness(t, ctx, db, 10_008)
	if _, err := repository.BeginDelete(ctx, run.ID, 10_009); err != nil {
		t.Fatalf("begin delete: %v", err)
	}

	firstDelete, err := repository.DeleteBatch(ctx, run.ID, 1, 10_010, func(context.Context, Run, []Segment) error { return nil })
	if err != nil {
		t.Fatalf("first delete batch: %v", err)
	}
	if firstDelete.Deleted != 1 || firstDelete.Completed || firstDelete.Run.DeletedEventCount != 1 {
		t.Fatalf("first delete = %#v", firstDelete)
	}
	if firstDelete.Run.ProgressPhase != ProgressDeletingRecords || firstDelete.Run.ProgressCurrent != 1 || firstDelete.Run.ProgressTotal != 2 {
		t.Fatalf("first delete progress = %#v", firstDelete.Run)
	}
	var firstRawID, firstDeletedAt sql.NullInt64
	if err := db.QueryRow(`select ledger.raw_event_id, archived.raw_deleted_at_ms
		from usage_archive_event_refs archived
		join usage_event_identity_ledger ledger on ledger.event_hash = archived.event_hash
		where archived.run_id = ? and archived.event_hash = ?`,
		run.ID,
		events[0].EventHash,
	).Scan(&firstRawID, &firstDeletedAt); err != nil {
		t.Fatalf("read first deleted reference: %v", err)
	}
	if firstRawID.Valid || !firstDeletedAt.Valid || firstDeletedAt.Int64 != 10_010 {
		t.Fatalf("first deleted mapping raw=%v deleted=%v", firstRawID, firstDeletedAt)
	}

	failed, err := repository.RecordFailure(ctx, run.ID, StatusDeleting, errors.New("simulated restart"), 10_011)
	if err != nil {
		t.Fatalf("record delete interruption: %v", err)
	}
	if failed.Status != StatusFailed || failed.ResumeStatus != StatusDeleting || failed.DeletedEventCount != 1 {
		t.Fatalf("failed delete run = %#v", failed)
	}
	if failed.ProgressPhase != ProgressDeletingRecords || failed.ProgressCurrent != 1 {
		t.Fatalf("failure lost progress = %#v", failed)
	}
	if _, err := repository.BeginDelete(ctx, run.ID, 10_012); err != nil {
		t.Fatalf("resume delete: %v", err)
	}
	secondDelete, err := repository.DeleteBatch(ctx, run.ID, 1, 10_013, func(context.Context, Run, []Segment) error { return nil })
	if err != nil {
		t.Fatalf("second delete batch: %v", err)
	}
	if secondDelete.Deleted != 1 || !secondDelete.Completed ||
		secondDelete.Run.Status != StatusCompleted || secondDelete.Run.DeletedEventCount != 2 {
		t.Fatalf("second delete = %#v", secondDelete)
	}
	if secondDelete.Run.ProgressPhase != "" {
		t.Fatalf("completed progress = %#v", secondDelete.Run)
	}

	coverage, err := repository.RawCoverage(ctx, 1, 2_500)
	if err != nil {
		t.Fatalf("read raw coverage: %v", err)
	}
	if coverage.RawDeletedEventCount != 2 || coverage.RawEventCount != 0 ||
		coverage.MinDeletedTimestampMS != 1_000 || coverage.MaxDeletedTimestampMS != 2_000 {
		t.Fatalf("raw coverage = %#v", coverage)
	}
	var dailyCount, dailyMin, dailyMax int64
	if err := db.QueryRow(`select deleted_event_count, min_timestamp_ms, max_timestamp_ms
		from usage_archive_deleted_coverage_daily where utc_day = 0`).Scan(&dailyCount, &dailyMin, &dailyMax); err != nil {
		t.Fatalf("read daily deleted coverage: %v", err)
	}
	if dailyCount != 2 || dailyMin != 1_000 || dailyMax != 2_000 {
		t.Fatalf("daily deleted coverage = (%d,%d,%d)", dailyCount, dailyMin, dailyMax)
	}
	counts, err := repository.MaintenanceCounts(ctx)
	if err != nil {
		t.Fatalf("read maintenance counts: %v", err)
	}
	if counts.RawEventCount != 1 || counts.RawMinTimestampMS != 3_000 || counts.RawMaxTimestampMS != 3_000 ||
		counts.RawArchivedEventCount != 0 || counts.RawDeletedEventCount != 2 {
		t.Fatalf("maintenance counts = %#v", counts)
	}
	if _, found, err := repository.ActiveRun(ctx); err != nil || found {
		t.Fatalf("completed active run found=%v err=%v", found, err)
	}
	reimported, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{events[0]})
	if err != nil {
		t.Fatalf("reimport archived event: %v", err)
	}
	if reimported.Inserted != 0 || reimported.Skipped != 1 {
		t.Fatalf("reimport result = %#v", reimported)
	}
}

func TestRepositoryCancelRunIsIdempotentAndReleasesActiveRun(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-previewed", 2_500, 30_000)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	cancelled, err := repository.CancelRun(ctx, run.ID, 30_001)
	if err != nil || cancelled.Status != StatusCancelled || cancelled.ResumeStatus != "" || cancelled.RequestedStage != "" {
		t.Fatalf("cancelled run = %#v error = %v", cancelled, err)
	}
	if _, found, err := repository.ActiveRun(ctx); err != nil || found {
		t.Fatalf("cancelled run active=%v error=%v", found, err)
	}
	again, err := repository.CancelRun(ctx, run.ID, 30_002)
	if err != nil || again.Status != StatusCancelled {
		t.Fatalf("idempotent cancel = %#v error = %v", again, err)
	}
	if _, err := repository.CreateRun(ctx, "after-cancel", 2_500, 30_003); err != nil {
		t.Fatalf("create after cancel: %v", err)
	}
	// The follow-up previewed run intentionally remains active, so remove it
	// before checking that the cancelled run itself does not block retention.
	if _, err := db.Exec(`delete from usage_archive_runs where id = ?`, "after-cancel"); err != nil {
		t.Fatalf("remove follow-up preview: %v", err)
	}
	if _, err := repository.CreateRetentionRun(ctx, "retention-after-cancel", 2_500, 30_004); err != nil {
		t.Fatalf("retention create after cancel: %v", err)
	}
}

func TestRepositoryCancelFailedRunWithoutDeletionReleasesRetentionLock(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-failed-safe", 2_500, 30_100)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := db.Exec(`update usage_archive_runs set status = ?, resume_status = ?, last_error = ? where id = ?`, StatusFailed, StatusArchiving, "temporary archive failure", run.ID); err != nil {
		t.Fatalf("prepare failed run: %v", err)
	}
	cancelled, err := repository.CancelRun(ctx, run.ID, 30_101)
	if err != nil || cancelled.Status != StatusCancelled {
		t.Fatalf("cancel failed run = %#v error=%v", cancelled, err)
	}
	if _, err := repository.CreateRetentionRun(ctx, "retention-after-failed-cancel", 2_500, 30_102); err != nil {
		t.Fatalf("retention create after failed cancel: %v", err)
	}
}

func TestRepositoryCancelFailedArchivingRunWithPublishedSegmentReleasesRefs(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-published-segment", 2_000, 30_200)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_201)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_202); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	if _, err := repository.RecordFailure(ctx, run.ID, StatusArchiving, errors.New("simulated archive interruption"), 30_203); err != nil {
		t.Fatalf("record archive failure: %v", err)
	}
	failed, err := repository.Run(ctx, run.ID)
	if err != nil || failed.Status != StatusFailed || failed.ResumeStatus != StatusArchiving || failed.ArchivedEventCount != 1 {
		t.Fatalf("failed archive = %#v error=%v", failed, err)
	}

	// Verify refs exist before cancel
	var refCountBefore int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCountBefore); err != nil {
		t.Fatalf("count refs before cancel: %v", err)
	}
	if refCountBefore != 1 {
		t.Fatalf("ref count before cancel = %d, want 1", refCountBefore)
	}

	cancelled, err := repository.CancelRun(ctx, run.ID, 30_204)
	if err != nil {
		t.Fatalf("cancel failed archiving run error = %v", err)
	}
	if cancelled.Status != StatusCancelled || cancelled.ResumeStatus != "" {
		t.Fatalf("cancelled run = %#v", cancelled)
	}

	// Verify refs are released
	var refCountAfter int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCountAfter); err != nil {
		t.Fatalf("count refs after cancel: %v", err)
	}
	if refCountAfter != 0 {
		t.Fatalf("ref count after cancel = %d, want 0", refCountAfter)
	}

	// Segments are preserved
	segments, err := repository.Segments(ctx, run.ID)
	if err != nil || len(segments) != 1 {
		t.Fatalf("segments after cancel: len=%d err=%v", len(segments), err)
	}

	// Raw event is eligible for archive again
	preview, err := repository.Preview(ctx, 2_000)
	if err != nil {
		t.Fatalf("preview after cancel: %v", err)
	}
	if preview.EventCount != 1 {
		t.Fatalf("published event did not become eligible after cancel: %#v", preview)
	}
}

func TestRepositoryCancelArchivedRunWithPublishedSegmentIsRejected(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-archived-segment", 2_000, 30_300)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_301)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_302); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	run, err = repository.MarkArchived(ctx, run.ID, "test-digest", "manifest.json", "test-manifest-sha", 30_303)
	if err != nil || run.Status != StatusArchived {
		t.Fatalf("mark archived: %v", err)
	}

	if _, err := repository.CancelRun(ctx, run.ID, 30_304); !errors.Is(err, ErrCancelPublished) {
		t.Fatalf("cancel archived run error = %v, want ErrCancelPublished", err)
	}

	segments, err := repository.Segments(ctx, run.ID)
	if err != nil || len(segments) != 1 {
		t.Fatalf("segments after rejected cancel: len=%d err=%v", len(segments), err)
	}

	preview, err := repository.Preview(ctx, 2_000)
	if err != nil {
		t.Fatalf("preview after rejected cancel: %v", err)
	}
	if preview.EventCount != 0 {
		t.Fatalf("published event became eligible for preview: %#v", preview)
	}
}

func TestRepositoryCancelVerifiedRunIsRejectedAndCanDelete(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-verified-run", 2_000, 30_400)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_401)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_402); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	if _, err := repository.MarkArchived(ctx, run.ID, "test-digest", "manifest.json", "test-manifest-sha", 30_403); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	catchUpHourlyAggregate(t, ctx, db, 30_403)
	if _, err := repository.BeginVerification(ctx, run.ID, 30_404); err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	run, err = repository.MarkVerified(ctx, run.ID, 30_405)
	if err != nil || run.Status != StatusVerified {
		t.Fatalf("mark verified: %v", err)
	}

	if _, err := repository.CancelRun(ctx, run.ID, 30_406); !errors.Is(err, ErrCancelPublished) {
		t.Fatalf("cancel verified run error = %v, want ErrCancelPublished", err)
	}

	catchUpDeleteReadiness(t, ctx, db, 30_406)
	deleting, err := repository.BeginDelete(ctx, run.ID, 30_407)
	if err != nil || deleting.Status != StatusDeleting {
		t.Fatalf("begin delete after rejected cancel: %v", err)
	}
	result, err := repository.DeleteBatch(ctx, run.ID, 10, 30_408, func(context.Context, Run, []Segment) error { return nil })
	if err != nil || result.Deleted != 1 || result.Run.Status != StatusCompleted {
		t.Fatalf("delete batch after rejected cancel: result=%#v err=%v", result, err)
	}
}

func TestRepositoryCancelFailedVerificationRunWithPublishedSegmentReleasesRefs(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-failed-verify", 2_000, 30_500)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_501)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_502); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	if _, err := repository.MarkArchived(ctx, run.ID, "test-digest", "manifest.json", "test-manifest-sha", 30_503); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	if _, err := repository.BeginVerification(ctx, run.ID, 30_504); err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	if _, err := repository.RecordFailure(ctx, run.ID, StatusVerifying, errors.New("simulated verify failure"), 30_505); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	failed, err := repository.Run(ctx, run.ID)
	if err != nil || failed.Status != StatusFailed || failed.ResumeStatus != StatusVerifying || failed.ArchivedEventCount != 1 {
		t.Fatalf("failed run = %#v error=%v", failed, err)
	}

	// Verify refs exist before cancel
	var refCountBefore int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCountBefore); err != nil {
		t.Fatalf("count refs before cancel: %v", err)
	}
	if refCountBefore != 1 {
		t.Fatalf("ref count before cancel = %d, want 1", refCountBefore)
	}

	cancelled, err := repository.CancelRun(ctx, run.ID, 30_506)
	if err != nil {
		t.Fatalf("cancel failed verify error = %v", err)
	}
	if cancelled.Status != StatusCancelled || cancelled.ResumeStatus != "" {
		t.Fatalf("cancelled run = %#v", cancelled)
	}

	var refCountAfter int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCountAfter); err != nil {
		t.Fatalf("count refs after cancel: %v", err)
	}
	if refCountAfter != 0 {
		t.Fatalf("ref count after cancel = %d, want 0", refCountAfter)
	}

	segments, err := repository.Segments(ctx, run.ID)
	if err != nil || len(segments) != 1 {
		t.Fatalf("segments after cancel: len=%d err=%v", len(segments), err)
	}

	preview, err := repository.Preview(ctx, 2_000)
	if err != nil {
		t.Fatalf("preview after cancel: %v", err)
	}
	if preview.EventCount != 1 {
		t.Fatalf("published event did not become eligible after cancel: %#v", preview)
	}
}

func TestRepositoryCancelFailsWhenRawEventIsMissing(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-missing-raw", 2_000, 30_600)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_601)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_602); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	if _, err := repository.MarkArchived(ctx, run.ID, "test-digest", "manifest.json", "test-manifest-sha", 30_603); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	if _, err := repository.BeginVerification(ctx, run.ID, 30_604); err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	if _, err := repository.RecordFailure(ctx, run.ID, StatusVerifying, errors.New("simulated verify failure"), 30_605); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	// External data loss: raw usage event is deleted
	archiveTestExec(t, db, `delete from usage_events where event_hash = ?`, events[0].EventHash)

	// CancelRun must fail closed with ErrCoverageIncomplete
	if _, err := repository.CancelRun(ctx, run.ID, 30_606); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("cancel run with missing raw event error = %v, want ErrCoverageIncomplete", err)
	}

	// Status must remain failed/verifying
	runAfter, err := repository.Run(ctx, run.ID)
	if err != nil || runAfter.Status != StatusFailed || runAfter.ResumeStatus != StatusVerifying {
		t.Fatalf("run after failed cancel = %#v, err = %v", runAfter, err)
	}

	// Refs must still exist
	var refCount int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCount); err != nil {
		t.Fatalf("count refs after rejected cancel: %v", err)
	}
	if refCount != 1 {
		t.Fatalf("ref count after rejected cancel = %d, want 1", refCount)
	}
}

func TestRepositoryCancelRunRejectsArchivedCountMismatchWithRefs(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-count-mismatch", 2_000, 30_650)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 30_651)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(ctx, run.ID, archiveTestSegment(run.ID, records), archiveRecordRefs(records), 30_652); err != nil {
		t.Fatalf("record published segment: %v", err)
	}
	if _, err := repository.MarkArchived(ctx, run.ID, "test-digest", "manifest.json", "test-manifest-sha", 30_653); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	if _, err := repository.BeginVerification(ctx, run.ID, 30_654); err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	if _, err := repository.RecordFailure(ctx, run.ID, StatusVerifying, errors.New("simulated verify failure"), 30_655); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	// Corrupt run metadata: archived_event_count is set to 0 while 1 ref exists in usage_archive_event_refs
	archiveTestExec(t, db, `update usage_archive_runs set archived_event_count = 0 where id = ?`, run.ID)

	// CancelRun must fail closed with ErrCoverageIncomplete
	if _, err := repository.CancelRun(ctx, run.ID, 30_656); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("cancel run with count mismatch error = %v, want ErrCoverageIncomplete", err)
	}

	// Status must remain failed/verifying
	runAfter, err := repository.Run(ctx, run.ID)
	if err != nil || runAfter.Status != StatusFailed || runAfter.ResumeStatus != StatusVerifying {
		t.Fatalf("run after failed cancel = %#v, err = %v", runAfter, err)
	}

	// Ref must not be deleted
	var refCount int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCount); err != nil {
		t.Fatalf("count refs after rejected cancel: %v", err)
	}
	if refCount != 1 {
		t.Fatalf("ref count after rejected cancel = %d, want 1", refCount)
	}
}

func TestRepositoryCancelRejectsFailedDeletingRun(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()[:1]
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancel-failed-deleting", 2_000, 30_700)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	archiveTestExec(t, db, `update usage_archive_runs set status = ?, resume_status = ?, archived_event_count = 1 where id = ?`,
		StatusFailed, StatusDeleting, run.ID)

	if _, err := repository.CancelRun(ctx, run.ID, 30_701); !errors.Is(err, ErrCancelUnsafe) {
		t.Fatalf("cancel failed deleting run error = %v, want ErrCancelUnsafe", err)
	}
}

func TestUsageArchiveSchemaContractCoversAllUsageEventColumns(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `pragma table_info(usage_events)`)
	if err != nil {
		t.Fatalf("query table_info: %v", err)
	}
	defer rows.Close()

	var expectedColumns []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		expectedColumns = append(expectedColumns, name)
	}
	if len(expectedColumns) == 0 {
		t.Fatal("expected columns from usage_events table_info, got 0")
	}

	latency := int64(120)
	ttft := int64(30)
	quotaPct := 42.5
	event := archiveTestEvents()[0]
	event.LatencyMS = &latency
	event.TTFTMS = &ttft
	event.HeaderQuotaUsedPercent = &quotaPct
	if _, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{event}); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}

	repository := New(db)
	run, err := repository.CreateRun(ctx, "contract-test-run", 2_000, 40_000)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err = repository.BeginArchive(ctx, run.ID, 40_001)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}

	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least 1 record")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(records[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	for _, col := range expectedColumns {
		key := col
		if col == "id" {
			key = "_cpamp_archive_event_id"
		}
		if _, ok := payload[key]; !ok {
			t.Errorf("usage_events column %q (archive key %q) missing from archived payload", col, key)
		}
	}
}

func TestRepositoryCancelRunRejectsPartialDeletion(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	for _, fixture := range []struct {
		name   string
		status string
		resume string
		count  int64
	}{
		{name: "deleting", status: StatusDeleting, count: 0},
		{name: "failed-delete", status: StatusFailed, resume: StatusDeleting, count: 1},
	} {
		run, err := repository.CreateRun(ctx, "unsafe-"+fixture.name, 2_500, 31_000)
		if err != nil {
			t.Fatalf("create %s: %v", fixture.name, err)
		}
		if _, err := db.Exec(`update usage_archive_runs set status = ?, resume_status = ?, delete_started_at_ms = ?, deleted_event_count = ? where id = ?`, fixture.status, fixture.resume, 31_001, fixture.count, run.ID); err != nil {
			t.Fatalf("prepare %s: %v", fixture.name, err)
		}
		if _, err := repository.CancelRun(ctx, run.ID, 31_002); !errors.Is(err, ErrCancelUnsafe) {
			t.Fatalf("cancel %s error = %v, want ErrCancelUnsafe", fixture.name, err)
		}
		if _, err := db.Exec(`delete from usage_archive_runs where id = ?`, run.ID); err != nil {
			t.Fatalf("remove %s fixture: %v", fixture.name, err)
		}
	}
}

func TestRepositoryCancelRunRejectsInconsistentCancelledPartialDeletion(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "cancelled-partial-delete", 2_500, 31_100)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := db.Exec(`update usage_archive_runs set status = ?, delete_started_at_ms = ?, deleted_event_count = ? where id = ?`, StatusCancelled, 31_101, 1, run.ID); err != nil {
		t.Fatalf("prepare inconsistent cancelled run: %v", err)
	}
	if _, err := repository.CancelRun(ctx, run.ID, 31_102); !errors.Is(err, ErrCancelUnsafe) {
		t.Fatalf("cancel inconsistent cancelled run error = %v, want ErrCancelUnsafe", err)
	}
}

func TestRepositoryPersistsAndRecoversRequestedArchiveStages(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()[:1]); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "requested-stage", 2_000, 10_000)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	requested, newlyRequested, err := repository.RequestStage(ctx, run.ID, StatusArchiving, 10_001)
	if err != nil || !newlyRequested || requested.RequestedStage != StatusArchiving {
		t.Fatalf("request archive stage = %#v new=%t err=%v", requested, newlyRequested, err)
	}
	requestedAgain, newlyRequested, err := repository.RequestStage(ctx, run.ID, StatusArchiving, 10_002)
	if err != nil || newlyRequested || requestedAgain.UpdatedAtMS != requested.UpdatedAtMS {
		t.Fatalf("repeat archive request = %#v new=%t err=%v", requestedAgain, newlyRequested, err)
	}
	if _, _, err := repository.RequestStage(ctx, run.ID, StatusVerifying, 10_003); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("request future stage error = %v, want invalid state", err)
	}
	if err := repository.ClearRequestedStage(ctx, run.ID, StatusArchiving); err != nil {
		t.Fatalf("clear requested stage: %v", err)
	}
	if _, err := repository.BeginArchive(ctx, run.ID, 10_003); err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	if err := repository.RecoverRequestedStages(ctx); err != nil {
		t.Fatalf("recover requested stages: %v", err)
	}
	recovered, err := repository.Run(ctx, run.ID)
	if err != nil || recovered.RequestedStage != StatusArchiving {
		t.Fatalf("recovered run = %#v err=%v", recovered, err)
	}
	if _, err := db.Exec(`update usage_archive_runs set
		status = ?, resume_status = ?, requested_stage = null
		where id = ?`, StatusFailed, StatusDeleting, run.ID); err != nil {
		t.Fatalf("set failed fixture: %v", err)
	}
	if err := repository.RecoverRequestedStages(ctx); err != nil {
		t.Fatalf("recover failed stage: %v", err)
	}
	failed, err := repository.Run(ctx, run.ID)
	if err != nil || failed.RequestedStage != "" {
		t.Fatalf("failed run was automatically requested: %#v err=%v", failed, err)
	}
	if _, err := db.Exec(`update usage_archive_runs set requested_stage = ? where id = ?`, StatusDeleting, run.ID); err != nil {
		t.Fatalf("persist failed request fixture: %v", err)
	}
	if err := repository.RecoverRequestedStages(ctx); err != nil {
		t.Fatalf("retain failed requested stage: %v", err)
	}
	failedRequested, err := repository.Run(ctx, run.ID)
	if err != nil || failedRequested.RequestedStage != StatusDeleting {
		t.Fatalf("persisted failed request was lost: %#v err=%v", failedRequested, err)
	}
	if _, err := db.Exec(`update usage_archive_runs set status = ?, requested_stage = null where id = ?`, StatusArchived, run.ID); err != nil {
		t.Fatalf("set archived fixture: %v", err)
	}
	if err := repository.RecoverRequestedStages(ctx); err != nil {
		t.Fatalf("recover archived stage: %v", err)
	}
	archived, err := repository.Run(ctx, run.ID)
	if err != nil || archived.RequestedStage != "" {
		t.Fatalf("archived run unexpectedly requested a next stage: %#v err=%v", archived, err)
	}
}

func TestRepositorySerializesDuplicateRequestedArchiveStages(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()[:1]); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "concurrent-requested-stage", 2_000, 10_000)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	type result struct {
		run       Run
		requested bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := 0; index < 2; index++ {
		go func(nowMS int64) {
			ready.Done()
			<-start
			requestedRun, newlyRequested, requestErr := repository.RequestStage(
				ctx,
				run.ID,
				StatusArchiving,
				nowMS,
			)
			results <- result{run: requestedRun, requested: newlyRequested, err: requestErr}
		}(10_001 + int64(index))
	}
	ready.Wait()
	close(start)

	newRequestCount := 0
	for index := 0; index < 2; index++ {
		requestResult := <-results
		if requestResult.err != nil {
			t.Fatalf("concurrent stage request %d: %v", index, requestResult.err)
		}
		if requestResult.run.RequestedStage != StatusArchiving {
			t.Fatalf("concurrent stage request %d = %#v", index, requestResult.run)
		}
		if requestResult.requested {
			newRequestCount++
		}
	}
	if newRequestCount != 1 {
		t.Fatalf("new request count = %d, want 1", newRequestCount)
	}
}

func TestRepositoryListsArchiveRunsWithFiltersCountsAndKeysetCursor(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	fixtures := []struct {
		id        string
		mode      string
		status    string
		createdAt int64
	}{
		{id: fmt.Sprintf("%032x", 4), mode: RunModeManual, status: StatusCompleted, createdAt: 400},
		{id: fmt.Sprintf("%032x", 3), mode: RunModeManual, status: StatusFailed, createdAt: 300},
		{id: fmt.Sprintf("%032x", 2), mode: RunModeRetention, status: StatusCompleted, createdAt: 200},
		{id: fmt.Sprintf("%032x", 1), mode: RunModeManual, status: StatusCompleted, createdAt: 100},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, `insert into usage_archive_runs (
			id, mode, schema_version, format, status, cutoff_timestamp_ms,
			target_event_id, event_count, estimated_bytes, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, 1, 1, 1, 1, ?, ?)`,
			fixture.id,
			fixture.mode,
			SchemaVersion,
			FormatGzipJSONLV1,
			fixture.status,
			fixture.createdAt,
			fixture.createdAt,
		); err != nil {
			t.Fatalf("insert run %s: %v", fixture.id, err)
		}
	}
	repository := New(db)
	first, err := repository.ListRuns(ctx, RunListFilter{Mode: RunModeManual, Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if first.Total != 3 || !first.HasMore || len(first.Runs) != 2 ||
		first.Runs[0].ID != fixtures[0].id || first.Runs[1].ID != fixtures[1].id ||
		first.StatusCounts[StatusCompleted] != 2 || first.StatusCounts[StatusFailed] != 1 {
		t.Fatalf("first page = %#v", first)
	}
	second, err := repository.ListRuns(ctx, RunListFilter{
		Mode:              RunModeManual,
		Limit:             2,
		BeforeCreatedAtMS: first.Runs[1].CreatedAtMS,
		BeforeID:          first.Runs[1].ID,
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if second.Total != 3 || second.HasMore || len(second.Runs) != 1 || second.Runs[0].ID != fixtures[3].id {
		t.Fatalf("second page = %#v", second)
	}
	completed, err := repository.ListRuns(ctx, RunListFilter{Status: StatusCompleted, Limit: 10})
	if err != nil || completed.Total != 3 || len(completed.Runs) != 3 {
		t.Fatalf("completed filter = %#v err=%v", completed, err)
	}
}

func TestRepositoryMaintenanceCountsReturnsZeroRangeWhenEmpty(t *testing.T) {
	repository := New(openArchiveTestDB(t))

	counts, err := repository.MaintenanceCounts(context.Background())
	if err != nil {
		t.Fatalf("read empty maintenance counts: %v", err)
	}
	if counts.RawEventCount != 0 || counts.RawMinTimestampMS != 0 || counts.RawMaxTimestampMS != 0 ||
		counts.RawArchivedEventCount != 0 || counts.RawDeletedEventCount != 0 {
		t.Fatalf("empty maintenance counts = %#v", counts)
	}
}

func TestRepositoryRetentionRunRemainsActiveUntilDeleteCompletes(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	events := archiveTestEvents()
	if _, err := usageevent.New(db).InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}

	repository := New(db)
	run, err := repository.CreateRetentionRun(ctx, "retention-run", 2_500, 20_000)
	if err != nil {
		t.Fatalf("create retention run: %v", err)
	}
	for _, status := range []string{StatusArchived, StatusVerified} {
		if _, err := db.Exec(`update usage_archive_runs set status = ? where id = ?`, status, run.ID); err != nil {
			t.Fatalf("set retention status %s: %v", status, err)
		}
		active, found, err := repository.ActiveRun(ctx)
		if err != nil || !found || active.ID != run.ID {
			t.Fatalf("active retention %s = %#v found=%v err=%v", status, active, found, err)
		}
		if _, err := repository.CreateRun(ctx, "manual-after-"+status, 4_000, 20_001); !errors.Is(err, ErrMaintenanceLocked) {
			t.Fatalf("manual create with %s retention run error = %v, want maintenance lock", status, err)
		}
	}
}

func TestRepositoryRecordSegmentRollsBackPartialReferenceBinding(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()[:2]); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "segment-atomicity", 2_500, 20_000)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := repository.BeginArchive(ctx, run.ID, 20_001); err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 10, 1<<30)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	if _, err := db.Exec(`update usage_event_identity_ledger set raw_event_id = 999
		where event_hash = ?`, records[1].EventHash); err != nil {
		t.Fatalf("break second identity mapping: %v", err)
	}
	if _, err := repository.RecordSegment(
		ctx,
		run.ID,
		archiveTestSegment(run.ID, records),
		archiveRecordRefs(records),
		20_002,
	); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("record inconsistent segment error = %v, want coverage incomplete", err)
	}

	var segmentCount, refCount int64
	if err := db.QueryRow(`select count(*) from usage_archive_segments where run_id = ?`, run.ID).Scan(&segmentCount); err != nil {
		t.Fatalf("count rolled-back segments: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_archive_event_refs where run_id = ?`, run.ID).Scan(&refCount); err != nil {
		t.Fatalf("count rolled-back references: %v", err)
	}
	stored, err := repository.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("read rolled-back run: %v", err)
	}
	if segmentCount != 0 || refCount != 0 || stored.LastArchivedEventID != 0 ||
		stored.ArchivedEventCount != 0 || stored.ArchivedUncompressedBytes != 0 ||
		stored.ArchivedCompressedBytes != 0 {
		t.Fatalf("partial segment mutation persisted: segments=%d refs=%d run=%#v", segmentCount, refCount, stored)
	}
}

func TestRepositoryRecordsRejectsPayloadTooLargeForImporterRestore(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	event := archiveTestEvents()[0]
	if _, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{event}); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	oversizedRawJSON := "{\"payload\":\"" + strings.Repeat("x", usage.MaxJSONLRecordBytes) + "\"}"
	if _, err := db.Exec(
		"update usage_events set raw_json = ? where event_hash = ?",
		oversizedRawJSON,
		event.EventHash,
	); err != nil {
		t.Fatalf("store oversized raw JSON fixture: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, "oversized-record", 2_000, 25_000)
	if err != nil {
		t.Fatalf("create archive run: %v", err)
	}
	if _, err := repository.BeginArchive(ctx, run.ID, 25_001); err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	if _, err := repository.Records(ctx, run.ID, 0, 10, 64*1024*1024); !errors.Is(err, usage.ErrJSONLRecordTooLarge) {
		t.Fatalf("oversized archive record error = %v, want JSONL record limit", err)
	}
	stored, err := repository.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("read oversized archive run: %v", err)
	}
	if stored.ArchivedEventCount != 0 || stored.LastArchivedEventID != 0 {
		t.Fatalf("oversized archive record advanced run = %#v", stored)
	}
}

func TestRepositoryRecordsRejectsUnsupportedRunContract(t *testing.T) {
	tests := []struct {
		name       string
		assignment string
		value      any
	}{
		{name: "schema version", assignment: "schema_version = ?", value: SchemaVersion + 1},
		{name: "format", assignment: "format = ?", value: "future-format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openArchiveTestDB(t)
			ctx := context.Background()
			event := archiveTestEvents()[0]
			if _, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{event}); err != nil {
				t.Fatalf("insert usage event: %v", err)
			}
			repository := New(db)
			run, err := repository.CreateRun(ctx, "unsupported-contract-"+strings.ReplaceAll(tt.name, " ", "-"), 2_000, 26_000)
			if err != nil {
				t.Fatalf("create archive run: %v", err)
			}
			if _, err := repository.BeginArchive(ctx, run.ID, 26_001); err != nil {
				t.Fatalf("begin archive: %v", err)
			}
			if _, err := db.Exec("update usage_archive_runs set "+tt.assignment+" where id = ?", tt.value, run.ID); err != nil {
				t.Fatalf("mutate archive contract: %v", err)
			}
			if _, err := repository.Records(ctx, run.ID, 0, 10, 1<<20); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("records error = %v, want invalid state", err)
			}
		})
	}
}

func TestRepositoryStageEntryRejectsUnsupportedRunContract(t *testing.T) {
	tests := []struct {
		name       string
		assignment string
		value      any
	}{
		{name: "schema version", assignment: "schema_version = ?", value: SchemaVersion + 1},
		{name: "format", assignment: "format = ?", value: "future-format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openArchiveTestDB(t)
			ctx := context.Background()
			event := archiveTestEvents()[0]
			if _, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{event}); err != nil {
				t.Fatalf("insert usage event: %v", err)
			}
			repository := New(db)
			run, err := repository.CreateRun(ctx, "stage-unsupported-"+strings.ReplaceAll(tt.name, " ", "-"), 2_000, 27_000)
			if err != nil {
				t.Fatalf("create archive run: %v", err)
			}
			if _, err := db.Exec("update usage_archive_runs set "+tt.assignment+" where id = ?", tt.value, run.ID); err != nil {
				t.Fatalf("mutate archive contract: %v", err)
			}
			if _, err := repository.BeginArchive(ctx, run.ID, 27_001); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("begin archive error = %v, want invalid state", err)
			}
			stored, err := repository.Run(ctx, run.ID)
			if err != nil {
				t.Fatalf("read archive run: %v", err)
			}
			if stored.Status != StatusPreviewed {
				t.Fatalf("archive status = %q, want %q", stored.Status, StatusPreviewed)
			}
		})
	}
}

func TestRepositoryDeleteRejectsUnsupportedRunContractWithoutDeletingRawEvents(t *testing.T) {
	tests := []struct {
		name       string
		assignment string
		value      any
	}{
		{name: "schema version", assignment: "schema_version = ?", value: SchemaVersion + 1},
		{name: "format", assignment: "format = ?", value: "future-format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, repository, run := prepareVerifiedArchiveRun(t, "delete-unsupported-"+strings.ReplaceAll(tt.name, " ", "-"))
			ctx := context.Background()
			if _, err := db.Exec("update usage_archive_runs set "+tt.assignment+" where id = ?", tt.value, run.ID); err != nil {
				t.Fatalf("mutate archive contract: %v", err)
			}
			if _, err := repository.BeginDelete(ctx, run.ID, 30_000); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("begin delete error = %v, want invalid state", err)
			}
			var rawCount int64
			if err := db.QueryRow("select count(*) from usage_events").Scan(&rawCount); err != nil {
				t.Fatalf("count raw usage events: %v", err)
			}
			if rawCount != run.EventCount {
				t.Fatalf("raw event count = %d, want %d", rawCount, run.EventCount)
			}
		})
	}
}

func TestRepositoryDeleteRejectsRawRowsRemovedOutsideMaintenance(t *testing.T) {
	db, repository, run := prepareVerifiedArchiveRun(t, "missing-raw")
	ctx := context.Background()
	if _, err := repository.BeginDelete(ctx, run.ID, 30_000); err != nil {
		t.Fatalf("begin delete: %v", err)
	}
	if _, err := db.Exec(`delete from usage_events where id = 1`); err != nil {
		t.Fatalf("remove archived raw event outside maintenance: %v", err)
	}
	if _, err := repository.DeleteBatch(ctx, run.ID, 10, 30_001, func(context.Context, Run, []Segment) error { return nil }); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("delete missing raw error = %v, want coverage incomplete", err)
	}
	var rawCount, deletedCount, deletedRefCount int64
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("count remaining raw events: %v", err)
	}
	if err := db.QueryRow(`select deleted_event_count from usage_archive_runs where id = ?`, run.ID).Scan(&deletedCount); err != nil {
		t.Fatalf("read recorded delete count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_archive_event_refs
		where run_id = ? and raw_deleted_at_ms is not null`, run.ID).Scan(&deletedRefCount); err != nil {
		t.Fatalf("count deleted archive references: %v", err)
	}
	if rawCount != 1 || deletedCount != 0 || deletedRefCount != 0 {
		t.Fatalf("failed delete changed state raw=%d deleted=%d refs=%d", rawCount, deletedCount, deletedRefCount)
	}
}

func TestRepositoryDeleteRevalidatesOwnershipAndEvidenceBetweenBatches(t *testing.T) {
	mutations := map[string]struct {
		statement string
		want      error
	}{
		"run state":                  {`update usage_archive_runs set status = 'verified'`, ErrInvalidState},
		"run schema":                 {`update usage_archive_runs set schema_version = 99`, ErrInvalidState},
		"run mode":                   {`update usage_archive_runs set mode = 'unknown'`, ErrInvalidState},
		"delete authorization":       {`update usage_archive_runs set requested_stage = 'archiving'`, ErrInvalidState},
		"verification revoked":       {`update usage_archive_runs set verified_at_ms = null`, ErrCoverageIncomplete},
		"lock missing":               {`delete from usage_maintenance_locks`, ErrMaintenanceLocked},
		"lock owner":                 {`update usage_maintenance_locks set run_id = 'another-run'`, ErrMaintenanceLocked},
		"lock operation":             {`update usage_maintenance_locks set operation = 'verifying'`, ErrMaintenanceLocked},
		"manifest evidence":          {`update usage_archive_runs set manifest_sha256 = ''`, ErrCoverageIncomplete},
		"segment verification":       {`update usage_archive_segments set status = 'published'`, ErrCoverageIncomplete},
		"segment verification time":  {`update usage_archive_segments set verified_at_ms = null`, ErrCoverageIncomplete},
		"segment count":              {`update usage_archive_segments set event_count = event_count + 1`, ErrCoverageIncomplete},
		"deleted reference removed":  {`delete from usage_archive_event_refs where raw_deleted_at_ms is not null`, ErrCoverageIncomplete},
		"retained ledger revision":   {`update usage_event_identity_ledger set aggregate_structure_revision = 'stale' where raw_event_id is null`, ErrCoverageIncomplete},
		"retained ledger schema":     {`update usage_event_identity_ledger set aggregate_schema_version = 99 where raw_event_id is null`, ErrCoverageIncomplete},
		"retained ledger timestamp":  {`update usage_event_identity_ledger set timestamp_ms = timestamp_ms + 1 where raw_event_id is null`, ErrCoverageIncomplete},
		"retained ledger bucket":     {`update usage_event_identity_ledger set bucket_ms = bucket_ms + 1 where raw_event_id is null`, ErrCoverageIncomplete},
		"deleted canonical evidence": {`delete from gateway_usage_identity_projection_v1 where usage_event_id = 1`, ErrCoverageIncomplete},
		"deleted canonical hash":     {`update gateway_usage_identity_projection_v1 set event_hash = 'changed' where usage_event_id = 1`, ErrCoverageIncomplete},
		"binding revision":           {`update gateway_source_binding_revision set revision = revision + 1`, ErrCoverageIncomplete},
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			db, repository, run := prepareVerifiedArchiveRun(t, "batch-evidence")
			ctx := context.Background()
			if name == "lock owner" {
				archiveTestExec(t, db, `insert into usage_archive_runs
					(id, mode, schema_version, format, status, cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms)
					select 'another-run', mode, schema_version, format, 'previewed', cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms
					from usage_archive_runs where id = ?`, run.ID)
			}
			if _, err := repository.BeginDelete(ctx, run.ID, 60_000); err != nil {
				t.Fatal(err)
			}
			first, err := repository.DeleteBatch(ctx, run.ID, 1, 60_001, func(context.Context, Run, []Segment) error { return nil })
			if err != nil || first.Completed || first.Deleted != 1 {
				t.Fatalf("first batch=%#v err=%v", first, err)
			}
			archiveTestExec(t, db, mutation.statement)
			if _, err := repository.DeleteBatch(ctx, run.ID, 1, 60_002, func(context.Context, Run, []Segment) error { return nil }); !errors.Is(err, mutation.want) {
				t.Fatalf("next destructive batch after %s: err=%v, want %v", name, err, mutation.want)
			}
			var rawCount, deletedCount, lastDeletedID int64
			if err := db.QueryRow(`select (select count(*) from usage_events), deleted_event_count, last_deleted_event_id from usage_archive_runs where id = ?`, run.ID).Scan(&rawCount, &deletedCount, &lastDeletedID); err != nil {
				t.Fatal(err)
			}
			if rawCount != run.EventCount-1 || deletedCount != 1 || lastDeletedID != first.LastID {
				t.Fatalf("rejected batch advanced raw deletion: raw=%d deleted=%d last=%d", rawCount, deletedCount, lastDeletedID)
			}
		})
	}
}

func TestRepositoryDeleteRequiresFileVerifierAndRollsBackItsFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			db, repository, run := prepareVerifiedArchiveRun(t, "batch-file-verifier")
			ctx := context.Background()
			if _, err := repository.BeginDelete(ctx, run.ID, 60_000); err != nil {
				t.Fatal(err)
			}
			var verifier func(context.Context, Run, []Segment) error
			want := error(ErrCoverageIncomplete)
			if fail {
				want = errors.New("archive file changed")
				verifier = func(_ context.Context, current Run, segments []Segment) error {
					if current.ID != run.ID || current.Status != StatusDeleting || len(segments) != 1 {
						t.Fatalf("verifier did not receive current transaction evidence: run=%#v segments=%#v", current, segments)
					}
					return want
				}
			}
			if _, err := repository.DeleteBatch(ctx, run.ID, 1, 60_001, verifier); !errors.Is(err, want) {
				t.Fatalf("unverified batch error=%v want=%v", err, want)
			}
			var raw, deleted int64
			if err := db.QueryRow(`select (select count(*) from usage_events), deleted_event_count from usage_archive_runs where id = ?`, run.ID).Scan(&raw, &deleted); err != nil {
				t.Fatal(err)
			}
			if raw != run.EventCount || deleted != 0 {
				t.Fatalf("unverified batch deleted raw: raw=%d deleted=%d", raw, deleted)
			}
		})
	}
}

func TestRepositoryDeleteDoesNotRequireLegacyDashboardCheckpoint(t *testing.T) {
	for _, checkpointState := range []string{"missing", "behind"} {
		t.Run(checkpointState, func(t *testing.T) {
			db, repository, run := prepareVerifiedArchiveRun(t, "legacy-dashboard-"+checkpointState)
			ctx := context.Background()
			if checkpointState == "missing" {
				archiveTestExec(t, db, `delete from usage_rollup_checkpoints where name = ?`,
					usagerollup.DashboardHourlyCheckpointName)
			} else {
				result, err := usagerollup.New(db).CatchUpDashboardHourly(ctx, 100, 50_008)
				if err != nil || result.Pending {
					t.Fatalf("prepare legacy dashboard checkpoint: result=%#v err=%v", result, err)
				}
				archiveTestExec(t, db, `update usage_rollup_checkpoints set last_event_id = ? where name = ?`,
					run.TargetEventID-1, usagerollup.DashboardHourlyCheckpointName)
			}

			if _, err := repository.BeginDelete(ctx, run.ID, 50_009); err != nil {
				t.Fatalf("begin delete with %s legacy dashboard checkpoint: %v", checkpointState, err)
			}
			first, err := repository.DeleteBatch(ctx, run.ID, 1, 50_010, func(context.Context, Run, []Segment) error { return nil })
			if err != nil {
				t.Fatalf("first bounded delete: %v", err)
			}
			if first.Completed || first.Run.Status != StatusDeleting || first.Run.DeletedEventCount != 1 {
				t.Fatalf("first bounded delete = %#v", first)
			}
			// Legacy state also remains irrelevant when a later batch rechecks readiness.
			archiveTestExec(t, db, `delete from usage_rollup_checkpoints where name = ?`,
				usagerollup.DashboardHourlyCheckpointName)
			last, err := repository.DeleteBatch(ctx, run.ID, 1, 50_011, func(context.Context, Run, []Segment) error { return nil })
			if err != nil {
				t.Fatalf("last bounded delete: %v", err)
			}
			if !last.Completed || last.Run.Status != StatusCompleted || last.Run.DeletedEventCount != run.EventCount {
				t.Fatalf("completed bounded delete = %#v", last)
			}
			var rawCount, retainedIdentities, deletedReferences int64
			if err := db.QueryRowContext(ctx, `select
				(select count(*) from usage_events),
				(select count(*) from usage_event_identity_ledger where raw_event_id is null),
				(select count(*) from usage_archive_event_refs where run_id = ? and raw_deleted_at_ms is not null)`,
				run.ID,
			).Scan(&rawCount, &retainedIdentities, &deletedReferences); err != nil {
				t.Fatalf("inspect completed archive: %v", err)
			}
			if rawCount != 0 || retainedIdentities != run.EventCount || deletedReferences != run.EventCount {
				t.Fatalf("completed archive counts: raw=%d identities=%d deleted_refs=%d", rawCount, retainedIdentities, deletedReferences)
			}
		})
	}
}

func TestRepositoryDeleteRequiresCanonicalIdentityCoverage(t *testing.T) {
	mutations := map[string]string{
		"checkpoint pending":      `update gateway_usage_identity_projection_state set status = 'pending'`,
		"checkpoint incompatible": `update gateway_usage_identity_projection_state set schema_version = 99`,
		"checkpoint behind":       `update gateway_usage_identity_projection_state set last_processed_event_id = 0`,
		"binding revision stale":  `update gateway_usage_identity_projection_state set binding_revision = -1`,
		"checkpoint missing":      `delete from gateway_usage_identity_projection_state`,
		"row missing":             `delete from gateway_usage_identity_projection_v1 where usage_event_id = (select min(id) from usage_events)`,
		"row event hash mismatch": `update gateway_usage_identity_projection_v1 set event_hash = 'wrong-event' where usage_event_id = (select min(id) from usage_events)`,
		"row request ID mismatch": `update gateway_usage_identity_projection_v1 set request_id = 'wrong-request' where usage_event_id = (select min(id) from usage_events)`,
		"row timestamp mismatch":  `update gateway_usage_identity_projection_v1 set evidence_timestamp_ms = 0 where usage_event_id = (select min(id) from usage_events)`,
	}
	for name, statement := range mutations {
		for _, stage := range []string{"begin", "batch"} {
			t.Run(name+"/"+stage, func(t *testing.T) {
				db, repository, run := prepareVerifiedArchiveRun(t, "canonical-gate")
				ctx := context.Background()
				if stage == "batch" {
					if _, err := repository.BeginDelete(ctx, run.ID, 60_000); err != nil {
						t.Fatal(err)
					}
				}
				archiveTestExec(t, db, statement)
				var err error
				if stage == "begin" {
					_, err = repository.BeginDelete(ctx, run.ID, 60_001)
				} else {
					_, err = repository.DeleteBatch(ctx, run.ID, 100, 60_001, func(context.Context, Run, []Segment) error { return nil })
				}
				if !errors.Is(err, ErrCoverageIncomplete) {
					t.Fatalf("incomplete canonical coverage error=%v", err)
				}
				var rawCount, deletedCount int64
				if err := db.QueryRow(`select count(*) from usage_events`).Scan(&rawCount); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`select count(*) from usage_archive_event_refs where raw_deleted_at_ms is not null`).Scan(&deletedCount); err != nil {
					t.Fatal(err)
				}
				if rawCount != run.EventCount || deletedCount != 0 {
					t.Fatalf("rejected cleanup changed history: raw=%d want=%d deleted=%d", rawCount, run.EventCount, deletedCount)
				}
			})
		}
	}
}

func TestRepositoryDeleteRequiresEveryCurrentDerivedCoverageGate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, Run) func()
	}{
		{
			name: "accounting migration completed",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_data_migrations set status = 'discovering' where name = ?`,
					datamigration.UsageCacheAccountingMigrationName)
				return func() {
					archiveTestExec(t, db, `update usage_data_migrations set status = ? where name = ?`,
						datamigration.StatusCompleted, datamigration.UsageCacheAccountingMigrationName)
				}
			},
		},
		{
			name: "hourly aggregate schema",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_hourly_aggregate_state set schema_version = 99
					where aggregate_name = ?`, usageaggregate.AggregateName)
				return func() {
					archiveTestExec(t, db, `update usage_hourly_aggregate_state set schema_version = ?
						where aggregate_name = ?`, usageaggregate.SchemaVersion, usageaggregate.AggregateName)
				}
			},
		},
		{
			name: "hourly aggregate revision",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				revision := archiveTestString(t, db, `select structure_revision
					from usage_hourly_aggregate_state where aggregate_name = ?`, usageaggregate.AggregateName)
				archiveTestExec(t, db, `update usage_hourly_aggregate_state set structure_revision = 'stale'
					where aggregate_name = ?`, usageaggregate.AggregateName)
				return func() {
					archiveTestExec(t, db, `update usage_hourly_aggregate_state set structure_revision = ?
						where aggregate_name = ?`, revision, usageaggregate.AggregateName)
				}
			},
		},
		{
			name: "hourly aggregate status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_hourly_aggregate_state set status = 'backfilling'
					where aggregate_name = ?`, usageaggregate.AggregateName)
				return func() {
					archiveTestExec(t, db, `update usage_hourly_aggregate_state set status = 'ready'
						where aggregate_name = ?`, usageaggregate.AggregateName)
				}
			},
		},
		{
			name: "hourly aggregate coverage",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_hourly_aggregate_state set coverage_event_id = ?
					where aggregate_name = ?`, run.TargetEventID-1, usageaggregate.AggregateName)
				return func() {
					archiveTestExec(t, db, `update usage_hourly_aggregate_state set coverage_event_id = ?
						where aggregate_name = ?`, run.TargetEventID, usageaggregate.AggregateName)
				}
			},
		},
		{
			name: "pricing schema",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_pricing_rollup_state set schema_version = 99
					where rollup_name = ?`, usagepricing.RollupName)
				return func() {
					archiveTestExec(t, db, `update usage_pricing_rollup_state set schema_version = ?
						where rollup_name = ?`, usagepricing.SchemaVersion, usagepricing.RollupName)
				}
			},
		},
		{
			name: "pricing revision",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				revision := archiveTestString(t, db, `select structure_revision
					from usage_pricing_rollup_state where rollup_name = ?`, usagepricing.RollupName)
				archiveTestExec(t, db, `update usage_pricing_rollup_state set structure_revision = 'stale'
					where rollup_name = ?`, usagepricing.RollupName)
				return func() {
					archiveTestExec(t, db, `update usage_pricing_rollup_state set structure_revision = ?
						where rollup_name = ?`, revision, usagepricing.RollupName)
				}
			},
		},
		{
			name: "pricing status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_pricing_rollup_state set status = 'rebuilding'
					where rollup_name = ?`, usagepricing.RollupName)
				return func() {
					archiveTestExec(t, db, `update usage_pricing_rollup_state set status = 'ready'
						where rollup_name = ?`, usagepricing.RollupName)
				}
			},
		},
		{
			name: "pricing coverage",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_pricing_rollup_state set coverage_event_id = ?
					where rollup_name = ?`, run.TargetEventID-1, usagepricing.RollupName)
				return func() {
					archiveTestExec(t, db, `update usage_pricing_rollup_state set coverage_event_id = ?
						where rollup_name = ?`, run.TargetEventID, usagepricing.RollupName)
				}
			},
		},
		{
			name: "monitoring stats revision",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				revision := archiveTestString(t, db, `select structure_revision
					from usage_monitoring_rollup_state where rollup_name = ?`, usagemonitoring.StatsRollupName)
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = 'stale'
					where rollup_name = ?`, usagemonitoring.StatsRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = ?
						where rollup_name = ?`, revision, usagemonitoring.StatsRollupName)
				}
			},
		},
		{
			name: "monitoring stats status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'rebuilding'
					where rollup_name = ?`, usagemonitoring.StatsRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'ready'
						where rollup_name = ?`, usagemonitoring.StatsRollupName)
				}
			},
		},
		{
			name: "monitoring metadata coverage",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set coverage_event_id = ?
					where rollup_name = ?`, run.TargetEventID-1, usagemonitoring.MetadataRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set coverage_event_id = ?
						where rollup_name = ?`, run.TargetEventID, usagemonitoring.MetadataRollupName)
				}
			},
		},
		{
			name: "monitoring metadata status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'rebuilding'
					where rollup_name = ?`, usagemonitoring.MetadataRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'ready'
						where rollup_name = ?`, usagemonitoring.MetadataRollupName)
				}
			},
		},
		{
			name: "monitoring projection schema",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set schema_version = 99
					where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set schema_version = ?
						where rollup_name = ?`, usagemonitoring.SchemaVersion, usagemonitoring.ProjectionRollupName)
				}
			},
		},
		{
			name: "monitoring projection revision",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				revision := archiveTestString(t, db, `select structure_revision
					from usage_monitoring_rollup_state where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = 'stale'
					where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = ?
						where rollup_name = ?`, revision, usagemonitoring.ProjectionRollupName)
				}
			},
		},
		{
			name: "monitoring projection status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'rebuilding'
					where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'ready'
						where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				}
			},
		},
		{
			name: "codex legacy identity evidence coverage",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set coverage_event_id = ?
					where rollup_name = ?`, run.TargetEventID-1, usageevent.CodexLegacyIdentityRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set coverage_event_id = ?
						where rollup_name = ?`, run.TargetEventID, usageevent.CodexLegacyIdentityRollupName)
				}
			},
		},
		{
			name: "codex legacy identity evidence revision",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				revision := archiveTestString(t, db, `select structure_revision
					from usage_monitoring_rollup_state where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = 'stale'
					where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set structure_revision = ?
						where rollup_name = ?`, revision, usageevent.CodexLegacyIdentityRollupName)
				}
			},
		},
		{
			name: "codex legacy identity evidence status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'clearing'
					where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'ready'
						where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				}
			},
		},
		{
			name: "codex legacy identity evidence schema version",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set schema_version = 99
					where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set schema_version = ?
						where rollup_name = ?`, usageevent.CodexLegacyIdentityEvidenceSchemaVersion, usageevent.CodexLegacyIdentityRollupName)
				}
			},
		},
		{
			name: "codex legacy identity evidence state row missing",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `delete from usage_monitoring_rollup_state where rollup_name = ?`, usageevent.CodexLegacyIdentityRollupName)
				return func() {
					archiveTestExec(t, db, `insert into usage_monitoring_rollup_state(rollup_name, schema_version, structure_revision, status, coverage_event_id, updated_at_ms)
						values(?, ?, ?, 'ready', ?, 1)`, usageevent.CodexLegacyIdentityRollupName, usageevent.CodexLegacyIdentityEvidenceSchemaVersion, usageevent.CodexLegacyIdentityEvidenceRevision, run.TargetEventID)
				}
			},
		},
		{
			name: "monitoring search index",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_search_index_state set ready = 0 where id = 1`)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_search_index_state set ready = 1 where id = 1`)
				}
			},
		},
		{
			name: "account history checkpoint",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_rollup_checkpoints set last_event_id = ?
					where name = ?`, run.TargetEventID-1, usagerollup.AccountHistoryCheckpointName)
				return func() {
					archiveTestExec(t, db, `update usage_rollup_checkpoints set last_event_id = ?
						where name = ?`, run.TargetEventID, usagerollup.AccountHistoryCheckpointName)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, repository, run := prepareVerifiedArchiveRun(t, "gate-"+fmt.Sprint(time.Now().UnixNano()))
			restore := test.mutate(t, db, run)
			if _, err := repository.BeginDelete(context.Background(), run.ID, 40_000); !errors.Is(err, ErrCoverageIncomplete) {
				t.Fatalf("begin delete with broken %s error = %v, want coverage incomplete", test.name, err)
			}
			restore()
			if _, err := repository.BeginDelete(context.Background(), run.ID, 40_001); err != nil {
				t.Fatalf("begin delete after restoring %s: %v", test.name, err)
			}
			first, err := repository.DeleteBatch(context.Background(), run.ID, 1, 40_002, func(context.Context, Run, []Segment) error { return nil })
			if err != nil || first.Deleted != 1 || first.Completed {
				t.Fatalf("first committed batch: result=%#v err=%v", first, err)
			}
			restore = test.mutate(t, db, run)
			if _, err := repository.DeleteBatch(context.Background(), run.ID, 100, 40_002, func(context.Context, Run, []Segment) error { return nil }); !errors.Is(err, ErrCoverageIncomplete) {
				t.Fatalf("delete batch with broken %s error = %v, want coverage incomplete", test.name, err)
			}
			var rawCount int64
			if err := db.QueryRow(`select count(*) from usage_events`).Scan(&rawCount); err != nil {
				t.Fatalf("inspect raw events after rejected delete: %v", err)
			}
			if rawCount != run.EventCount-1 {
				t.Fatalf("rejected delete changed raw count: got %d, want %d", rawCount, run.EventCount-1)
			}
			persisted, err := repository.Run(context.Background(), run.ID)
			if err != nil || persisted.DeletedEventCount != 1 || persisted.LastDeletedEventID != first.LastID {
				t.Fatalf("rejected later batch changed delete checkpoint: run=%#v err=%v", persisted, err)
			}
			restore()
			result, err := repository.DeleteBatch(context.Background(), run.ID, 100, 40_003, func(context.Context, Run, []Segment) error { return nil })
			if err != nil || !result.Completed || result.Run.DeletedEventCount != run.EventCount {
				t.Fatalf("delete batch after restoring %s: result=%#v err=%v", test.name, result, err)
			}
		})
	}
}

func TestRepositoryDeleteAllowsCommittedCoverageInStableRuntimeStates(t *testing.T) {
	for _, status := range []string{derivedStatusReady, "catching_up"} {
		t.Run("derived "+status, func(t *testing.T) {
			if err := validateDerivedCoverage(
				"test",
				1,
				1,
				"revision",
				"revision",
				status,
				10,
				10,
			); err != nil {
				t.Fatalf("covered derived state %q rejected: %v", status, err)
			}
		})
		t.Run("hourly "+status, func(t *testing.T) {
			err := validateHourlyAggregateCoverage(
				Run{TargetEventID: 10},
				hourlyAggregateCoverageState{
					SchemaVersion:     usageaggregate.SchemaVersion,
					StructureRevision: usageaggregate.StructureRevision,
					Status:            status,
					CoverageEventID:   10,
				},
			)
			if err != nil {
				t.Fatalf("covered hourly state %q rejected: %v", status, err)
			}
		})
	}

	for _, status := range []string{"failed", "pending", "clearing", "rebuilding", "backfilling", "unknown"} {
		t.Run("unsafe "+status, func(t *testing.T) {
			if err := validateDerivedCoverage(
				"test",
				1,
				1,
				"revision",
				"revision",
				status,
				10,
				10,
			); !errors.Is(err, ErrCoverageIncomplete) {
				t.Fatalf("unsafe derived state %q error = %v, want coverage incomplete", status, err)
			}
		})
	}
}

func TestRepositoryDeleteAllowsPricingSQLiteBusyAfterCommittedCoverage(t *testing.T) {
	ctx := context.Background()
	db, repository, run := prepareVerifiedArchiveRun(t, "pricing-busy-covered-"+fmt.Sprint(time.Now().UnixNano()))

	pricing := usagepricing.New(db)
	if err := pricing.RecordFailure(ctx, errors.New("database is locked (SQLITE_BUSY)"), 60_000); err != nil {
		t.Fatalf("record pricing sqlite busy: %v", err)
	}

	var status string
	var coverage int64
	if err := db.QueryRow(`select status, coverage_event_id
		from usage_pricing_rollup_state where rollup_name = ?`, usagepricing.RollupName).Scan(&status, &coverage); err != nil {
		t.Fatalf("read pricing state after sqlite busy: %v", err)
	}
	if status != derivedStatusReady || coverage < run.TargetEventID {
		t.Fatalf("pricing state = status:%q coverage:%d target:%d, want ready with committed coverage", status, coverage, run.TargetEventID)
	}

	if _, err := repository.BeginDelete(ctx, run.ID, 60_001); err != nil {
		t.Fatalf("begin delete after covered sqlite busy: %v", err)
	}
	result, err := repository.DeleteBatch(ctx, run.ID, 100, 60_002, func(context.Context, Run, []Segment) error { return nil })
	if err != nil {
		t.Fatalf("delete after covered sqlite busy: %v", err)
	}
	if !result.Completed || result.Run.DeletedEventCount != run.EventCount {
		t.Fatalf("delete result = %#v, want completed count %d", result, run.EventCount)
	}
}

func TestRepositoryDeleteRejectsFailedPricingAfterNonTransientCatchUpError(t *testing.T) {
	ctx := context.Background()
	db, repository, run := prepareVerifiedArchiveRun(t, "pricing-hard-failure-"+fmt.Sprint(time.Now().UnixNano()))

	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()[2:]); err != nil {
		t.Fatalf("insert pricing tail event: %v", err)
	}
	archiveTestExec(t, db, `drop table usage_pricing_hourly_rollups_v1`)

	pricing := usagepricing.New(db)
	_, catchUpErr := pricing.CatchUp(ctx, 100, 61_000)
	if catchUpErr == nil {
		t.Fatal("pricing catch-up after dropping rollup table succeeded, want failure")
	}
	if err := pricing.RecordFailure(ctx, catchUpErr, 61_001); err != nil {
		t.Fatalf("record non-transient pricing failure: %v", err)
	}

	var status string
	var coverage int64
	if err := db.QueryRow(`select status, coverage_event_id
		from usage_pricing_rollup_state where rollup_name = ?`, usagepricing.RollupName).Scan(&status, &coverage); err != nil {
		t.Fatalf("read failed pricing state: %v", err)
	}
	if status != "failed" || coverage < run.TargetEventID {
		t.Fatalf("pricing state = status:%q coverage:%d target:%d, want failed with retained committed coverage", status, coverage, run.TargetEventID)
	}

	if _, err := repository.BeginDelete(ctx, run.ID, 61_002); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("begin delete with failed pricing state error = %v, want coverage incomplete", err)
	}
	var rawCount int64
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("count raw events after rejected delete: %v", err)
	}
	if rawCount != 3 {
		t.Fatalf("raw events after rejected delete = %d, want 3", rawCount)
	}
}

func prepareVerifiedArchiveRun(t *testing.T, runID string) (*sql.DB, *Repository, Run) {
	t.Helper()
	db := openArchiveTestDB(t)
	ctx := context.Background()
	if _, err := usageevent.New(db).InsertBatch(ctx, archiveTestEvents()[:2]); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	repository := New(db)
	run, err := repository.CreateRun(ctx, runID, 2_500, 50_000)
	if err != nil {
		t.Fatalf("create archive run: %v", err)
	}
	if _, err := repository.BeginArchive(ctx, run.ID, 50_001); err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	records, err := repository.Records(ctx, run.ID, 0, 100, 1<<30)
	if err != nil {
		t.Fatalf("read archive records: %v", err)
	}
	if _, err := repository.RecordSegment(
		ctx,
		run.ID,
		archiveTestSegment(run.ID, records),
		archiveRecordRefs(records),
		50_002,
	); err != nil {
		t.Fatalf("record archive segment: %v", err)
	}
	if _, err := repository.MarkArchived(
		ctx,
		run.ID,
		"archive-digest",
		run.ID+"/manifest.json",
		"manifest-sha256",
		50_003,
	); err != nil {
		t.Fatalf("mark archived: %v", err)
	}
	catchUpHourlyAggregate(t, ctx, db, 50_004)
	if _, err := repository.BeginVerification(ctx, run.ID, 50_005); err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	run, err = repository.MarkVerified(ctx, run.ID, 50_006)
	if err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	catchUpDeleteReadiness(t, ctx, db, 50_007)
	return db, repository, run
}

func catchUpHourlyAggregate(t *testing.T, ctx context.Context, db *sql.DB, nowMS int64) {
	t.Helper()
	repository := usageaggregate.New(db)
	for attempt := 0; attempt < 10; attempt++ {
		result, err := repository.CatchUp(ctx, 100, nowMS+int64(attempt))
		if err != nil {
			t.Fatalf("catch up hourly aggregate: %v", err)
		}
		if !result.Pending {
			return
		}
	}
	t.Fatal("hourly aggregate catch-up remained pending")
}

func catchUpDeleteReadiness(t *testing.T, ctx context.Context, db *sql.DB, nowMS int64) {
	t.Helper()
	canonical := identityprojection.New(db)
	for attempt := 0; attempt < 10; attempt++ {
		result, err := canonical.CatchUp(ctx, 100, nowMS+int64(attempt))
		if err != nil {
			t.Fatalf("catch up canonical identity: %v", err)
		}
		if !result.Pending {
			break
		}
		if attempt == 9 {
			t.Fatal("canonical identity catch-up remained pending")
		}
	}
	pricing := usagepricing.New(db)
	for attempt := 0; attempt < 10; attempt++ {
		result, err := pricing.CatchUp(ctx, 100, nowMS+int64(attempt))
		if err != nil {
			t.Fatalf("catch up pricing: %v", err)
		}
		if !result.Pending {
			break
		}
		if attempt == 9 {
			t.Fatal("pricing catch-up remained pending")
		}
	}

	monitoring := usagemonitoring.New(db)
	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (usagemonitoring.CatchUpResult, error)
	}{
		{name: "stats", run: monitoring.CatchUpStats},
		{name: "metadata", run: monitoring.CatchUpMetadata},
		{name: "projection", run: monitoring.CatchUpProjection},
		{name: "codex legacy identity evidence", run: monitoring.CatchUpCodexLegacyIdentityEvidence},
	} {
		completed := false
		for attempt := 0; attempt < 10; attempt++ {
			result, err := catchUp.run(ctx, 100, nowMS+100+int64(attempt))
			if err != nil {
				t.Fatalf("catch up monitoring %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("monitoring %s catch-up remained pending", catchUp.name)
		}
	}

	rollups := usagerollup.New(db)
	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (usagerollup.CatchUpResult, error)
	}{
		{name: "account history", run: rollups.CatchUpAccountHistory},
	} {
		completed := false
		for attempt := 0; attempt < 10; attempt++ {
			result, err := catchUp.run(ctx, 100, nowMS+200+int64(attempt))
			if err != nil {
				t.Fatalf("catch up %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("%s catch-up remained pending", catchUp.name)
		}
	}
}

func openArchiveTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func BenchmarkRawCoverage100k(b *testing.B) {
	for _, deleted := range []bool{false, true} {
		name := "uncleaned"
		if deleted {
			name = "deleted"
		}
		b.Run(name, func(b *testing.B) {
			db, err := sql.Open("sqlite", filepath.Join(b.TempDir(), "coverage.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			for _, statement := range []string{
				`create table usage_events (id integer primary key, timestamp_ms integer not null)`,
				`create table usage_archive_event_refs (event_hash text primary key, timestamp_ms integer not null, raw_deleted_at_ms integer)`,
				`create index idx_usage_archive_event_refs_timestamp_deleted on usage_archive_event_refs(timestamp_ms, raw_deleted_at_ms)`,
				`create table usage_archive_deleted_coverage_daily (utc_day integer primary key, deleted_event_count integer not null, min_timestamp_ms integer not null, max_timestamp_ms integer not null)`,
			} {
				if _, err := db.Exec(statement); err != nil {
					b.Fatal(err)
				}
			}
			start := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			insert, err := tx.Prepare(`insert into usage_archive_event_refs(event_hash, timestamp_ms, raw_deleted_at_ms) values (?, ?, ?)`)
			if err != nil {
				b.Fatal(err)
			}
			insertRaw, err := tx.Prepare(`insert into usage_events(id, timestamp_ms) values (?, ?)`)
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 100_000; i++ {
				var deletedAt any
				if deleted {
					deletedAt = start + 31*24*time.Hour.Milliseconds()
				}
				timestampMS := start + int64(i)*30*24*time.Hour.Milliseconds()/100_000
				if _, err := insert.Exec(fmt.Sprintf("event-%d", i), timestampMS, deletedAt); err != nil {
					b.Fatal(err)
				}
				if _, err := insertRaw.Exec(i+1, timestampMS); err != nil {
					b.Fatal(err)
				}
			}
			if err := insert.Close(); err != nil {
				b.Fatal(err)
			}
			if err := insertRaw.Close(); err != nil {
				b.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if deleted {
				if _, err := db.Exec(`delete from usage_events`); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := db.Exec(`insert into usage_archive_deleted_coverage_daily
				select timestamp_ms / 86400000, count(*), min(timestamp_ms), max(timestamp_ms)
				from usage_archive_event_refs where raw_deleted_at_ms is not null group by timestamp_ms / 86400000`); err != nil {
				b.Fatal(err)
			}
			if _, err := db.Exec(`vacuum`); err != nil {
				b.Fatal(err)
			}
			coverage := New(db)
			fromMS, toMS := start+1_000, start+30*24*time.Hour.Milliseconds()-1_000
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := coverage.RawCoverage(context.Background(), fromMS, toMS); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestRawCoverageDailyAndPartialEdgesMatchExactRefs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "coverage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`create table usage_events (id integer primary key, timestamp_ms integer not null)`,
		`create index usage_events_timestamp on usage_events(timestamp_ms)`,
		`create table usage_archive_event_refs (event_hash text primary key, timestamp_ms integer not null, raw_deleted_at_ms integer)`,
		`create index idx_usage_archive_event_refs_timestamp_deleted on usage_archive_event_refs(timestamp_ms, raw_deleted_at_ms)`,
		`create table usage_archive_deleted_coverage_daily (utc_day integer primary key, deleted_event_count integer not null, min_timestamp_ms integer not null, max_timestamp_ms integer not null)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	assertExact := func(name string, fromMS, toMS int64) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			got, err := New(db).RawCoverage(context.Background(), fromMS, toMS)
			if err != nil {
				t.Fatal(err)
			}
			var count, minMS, maxMS int64
			if err := db.QueryRow(`select count(*), coalesce(min(timestamp_ms), 0), coalesce(max(timestamp_ms), 0)
				from usage_archive_event_refs where raw_deleted_at_ms is not null and timestamp_ms >= ? and timestamp_ms < ?`,
				fromMS, toMS).Scan(&count, &minMS, &maxMS); err != nil {
				t.Fatal(err)
			}
			if got.RawDeletedEventCount != count || got.MinDeletedTimestampMS != minMS || got.MaxDeletedTimestampMS != maxMS {
				t.Fatalf("coverage = %#v, exact=(%d,%d,%d)", got, count, minMS, maxMS)
			}
		})
	}
	assertExact("no archive", start, start+day)
	if _, err := db.Exec(`insert into usage_archive_event_refs values ('undeleted', ?, null)`, start+10); err != nil {
		t.Fatal(err)
	}
	assertExact("archive without deletion", start, start+day)
	for i, timestamp := range []int64{
		start + 20, start + day/2, start + day + 20, start + 2*day + 100,
		start + 3*day + 300, start + 3*day + day/2, start + 4*day + 50,
	} {
		if _, err := db.Exec(`insert into usage_archive_event_refs values (?, ?, 1)`, fmt.Sprintf("deleted-%d", i), timestamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`insert into usage_archive_deleted_coverage_daily
		select timestamp_ms / 86400000, count(*), min(timestamp_ms), max(timestamp_ms)
		from usage_archive_event_refs where raw_deleted_at_ms is not null group by timestamp_ms / 86400000`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		fromMS, toMS int64
	}{
		{"deleted raw", start, start + 5*day},
		{"one full UTC day", start + day, start + 2*day},
		{"multiple full UTC days", start + day, start + 4*day},
		{"partial first day", start + day + 10, start + 4*day},
		{"partial last day", start + day, start + 4*day + 100},
		{"both partial days", start + 10, start + 4*day + 100},
		{"daily and edge refs", start + day/2 + 1, start + 4*day + 51},
	} {
		assertExact(tc.name, tc.fromMS, tc.toMS)
	}
}

func archiveTestExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("execute archive test mutation: %v", err)
	}
}

func archiveTestString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("read archive test value: %v", err)
	}
	return value
}

func canonicalArchiveTestHash(raw string) string {
	if usage.IsCanonicalSHA256Hex(raw) {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func archiveTestEvents() []model.UsageEvent {
	return []model.UsageEvent{
		{
			RequestID:            "request-1",
			EventHash:            canonicalArchiveTestHash("archive-event-1"),
			TimestampMS:          1_000,
			Timestamp:            time.UnixMilli(1_000).UTC().Format(time.RFC3339Nano),
			Provider:             "xai",
			ExecutorType:         "XAIExecutor",
			Model:                "grok-test",
			Endpoint:             "POST /v1/responses",
			Method:               "POST",
			Path:                 "/v1/responses",
			ClientIP:             "198.51.100.10",
			XForwardedFor:        "203.0.113.7, 198.51.100.10",
			UserAgent:            "cpamp-archive-test/1.0",
			InputTokens:          10,
			OutputTokens:         5,
			TotalTokens:          15,
			Failed:               true,
			FailStatusCode:       429,
			FailSummary:          "rate limited",
			FailBody:             `{"error":{"code":"rate_limit"}}`,
			ResponseMetadataJSON: `{"trace":{"request_id":"trace-1"}}`,
			RawJSON:              `{"request":{"model":"grok-test"}}`,
			CreatedAtMS:          1_000,
		},
		{
			RequestID:    "request-2",
			EventHash:    canonicalArchiveTestHash("archive-event-2"),
			TimestampMS:  2_000,
			Timestamp:    time.UnixMilli(2_000).UTC().Format(time.RFC3339Nano),
			Provider:     "codex",
			ExecutorType: "CodexExecutor",
			Model:        "gpt-test",
			Endpoint:     "POST /v1/responses",
			InputTokens:  20,
			OutputTokens: 10,
			TotalTokens:  30,
			CreatedAtMS:  2_000,
		},
		{
			RequestID:    "request-3",
			EventHash:    canonicalArchiveTestHash("archive-event-3"),
			TimestampMS:  3_000,
			Timestamp:    time.UnixMilli(3_000).UTC().Format(time.RFC3339Nano),
			Provider:     "gemini",
			ExecutorType: "GeminiExecutor",
			Model:        "gemini-test",
			Endpoint:     "POST /v1/generate",
			InputTokens:  30,
			OutputTokens: 15,
			TotalTokens:  45,
			CreatedAtMS:  3_000,
		},
	}
}

func archiveTestSegment(runID string, records []Record) Segment {
	var uncompressed int64
	minTimestamp := records[0].TimestampMS
	maxTimestamp := records[0].TimestampMS
	for _, record := range records {
		uncompressed += int64(len(record.Payload) + 1)
		minTimestamp = min(minTimestamp, record.TimestampMS)
		maxTimestamp = max(maxTimestamp, record.TimestampMS)
	}
	return Segment{
		RunID:             runID,
		Sequence:          1,
		Status:            SegmentStatusPublished,
		FileName:          fmt.Sprintf("%s/segment-000001.jsonl.gz", runID),
		FirstEventID:      records[0].EventID,
		LastEventID:       records[len(records)-1].EventID,
		MinTimestampMS:    minTimestamp,
		MaxTimestampMS:    maxTimestamp,
		EventCount:        int64(len(records)),
		UncompressedBytes: uncompressed,
		CompressedBytes:   max(uncompressed/2, 1),
		ContentSHA256:     "segment-sha256",
		EventHashDigest:   "event-hash-digest",
	}
}

func archiveRecordRefs(records []Record) []RecordRef {
	refs := make([]RecordRef, 0, len(records))
	for _, record := range records {
		refs = append(refs, RecordRef{EventID: record.EventID, EventHash: record.EventHash})
	}
	return refs
}

func TestRepositoryDeleteAcceptsHourlyAggregateRebuildRevision(t *testing.T) {
	db, repository, run := prepareVerifiedArchiveRun(t, "hourly-rev-"+fmt.Sprint(time.Now().UnixNano()))
	rebuildRev := usageaggregate.StructureRevision + ":rebuild-0123456789abcdef0123456789abcdef"
	archiveTestExec(t, db, `update usage_hourly_aggregate_state set structure_revision = ?
		where aggregate_name = ?`, rebuildRev, usageaggregate.AggregateName)
	archiveTestExec(t, db, `update usage_event_identity_ledger set aggregate_structure_revision = ?`, rebuildRev)

	if _, err := repository.BeginDelete(context.Background(), run.ID, 40_000); err != nil {
		t.Fatalf("begin delete with valid rebuild revision error = %v, want nil", err)
	}

	// Now test malformed rebuild revision
	db2, repo2, run2 := prepareVerifiedArchiveRun(t, "hourly-rev-bad-"+fmt.Sprint(time.Now().UnixNano()))
	archiveTestExec(t, db2, `update usage_hourly_aggregate_state set structure_revision = ?
		where aggregate_name = ?`, usageaggregate.StructureRevision+":rebuild-short", usageaggregate.AggregateName)
	archiveTestExec(t, db2, `update usage_event_identity_ledger set aggregate_structure_revision = ?`, usageaggregate.StructureRevision+":rebuild-short")
	if _, err := repo2.BeginDelete(context.Background(), run2.ID, 40_000); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("begin delete with malformed rebuild revision error = %v, want coverage incomplete", err)
	}
}

func TestRepositoryPreflightRejectsNoncanonicalEventHash(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	legacyHash := "legacy-noncanonical-repo-hash"

	archiveTestExec(t, db, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, total_tokens, created_at_ms
	) values (?, ?, ?, ?, ?, ?)`,
		legacyHash, 1_000, "1970-01-01T00:00:01Z", "gpt-test", 100, 1_000,
	)

	repository := New(db)

	// Preview must fail with ErrCoverageIncomplete and ErrUnrestorableEventHash
	_, err := repository.Preview(ctx, 2_000)
	if err == nil {
		t.Fatal("preview succeeded for noncanonical event hash, want error")
	}
	if !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("preview error = %v, want ErrCoverageIncomplete", err)
	}
	if !errors.Is(err, ErrUnrestorableEventHash) {
		t.Fatalf("preview error = %v, want ErrUnrestorableEventHash", err)
	}
	if !strings.Contains(err.Error(), "cannot be restored under the current persistence policy") {
		t.Fatalf("preview error = %v, want policy error message", err)
	}

	// CreateRun must fail with ErrCoverageIncomplete and ErrUnrestorableEventHash
	_, err = repository.CreateRun(ctx, "run-legacy-preflight", 2_000, 10_000)
	if err == nil {
		t.Fatal("create run succeeded for noncanonical event hash, want error")
	}
	if !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("create run error = %v, want ErrCoverageIncomplete", err)
	}
	if !errors.Is(err, ErrUnrestorableEventHash) {
		t.Fatalf("create run error = %v, want ErrUnrestorableEventHash", err)
	}

	// Verify no run was inserted
	active, found, err := repository.ActiveRun(ctx)
	if err != nil {
		t.Fatalf("active run check: %v", err)
	}
	if found {
		t.Fatalf("active run exists = %#v, want none", active)
	}
}

func TestRepositoryPreflightCanonicalAndNoncanonicalEventHashes(t *testing.T) {
	tests := []struct {
		name      string
		eventHash string
		wantPass  bool
	}{
		{
			name:      "64-char lowercase hex",
			eventHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantPass:  true,
		},
		{
			name:      "64-char uppercase hex",
			eventHash: "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
			wantPass:  true,
		},
		{
			name:      "64-char mixed-case hex",
			eventHash: "0123456789aBcDeF0123456789AbCdEf0123456789aBcDeF0123456789AbCdEf",
			wantPass:  true,
		},
		{
			name:      "length 63 (short)",
			eventHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde",
			wantPass:  false,
		},
		{
			name:      "length 65 (long)",
			eventHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
			wantPass:  false,
		},
		{
			name:      "64-char containing g",
			eventHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg",
			wantPass:  false,
		},
		{
			name:      "64-char containing z",
			eventHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdez",
			wantPass:  false,
		},
		{
			name:      "64-char containing hyphen",
			eventHash: "0123456789abcdef0123456789abcdef-123456789abcdef0123456789abcdef",
			wantPass:  false,
		},
		{
			name:      "64-char containing underscore",
			eventHash: "0123456789abcdef0123456789abcdef_123456789abcdef0123456789abcdef",
			wantPass:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openArchiveTestDB(t)
			ctx := context.Background()

			archiveTestExec(t, db, `insert into usage_events (
				event_hash, timestamp_ms, timestamp, model, total_tokens, created_at_ms
			) values (?, ?, ?, ?, ?, ?)`,
				tc.eventHash, 1_000, "1970-01-01T00:00:01Z", "gpt-test", 100, 1_000,
			)

			repo := New(db)
			preview, err := repo.Preview(ctx, 2_000)
			if tc.wantPass {
				if err != nil {
					t.Fatalf("preview unexpectedly failed: %v", err)
				}
				if preview.EventCount != 1 {
					t.Fatalf("preview event count = %d, want 1", preview.EventCount)
				}
			} else {
				if err == nil {
					t.Fatal("preview succeeded, want rejection")
				}
				if !errors.Is(err, ErrCoverageIncomplete) {
					t.Fatalf("preview error = %v, want ErrCoverageIncomplete", err)
				}
				if !errors.Is(err, ErrUnrestorableEventHash) {
					t.Fatalf("preview error = %v, want ErrUnrestorableEventHash", err)
				}
			}
		})
	}

	// Also verify CreateRun succeeds with uppercase canonical hash
	t.Run("CreateRun with uppercase canonical hash", func(t *testing.T) {
		db := openArchiveTestDB(t)
		ctx := context.Background()
		upperHash := "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

		archiveTestExec(t, db, `insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, total_tokens, created_at_ms
		) values (?, ?, ?, ?, ?, ?)`,
			upperHash, 1_000, "1970-01-01T00:00:01Z", "gpt-test", 100, 1_000,
		)

		repo := New(db)
		run, err := repo.CreateRun(ctx, "run-upper-preflight", 2_000, 10_000)
		if err != nil {
			t.Fatalf("CreateRun failed for uppercase canonical hash: %v", err)
		}
		if run.EventCount != 1 {
			t.Fatalf("run event count = %d, want 1", run.EventCount)
		}
	})
}
