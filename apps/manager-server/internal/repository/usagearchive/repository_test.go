package usagearchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
		"client_ip":       events[0].ClientIP,
		"x_forwarded_for": events[0].XForwardedFor,
		"user_agent":      events[0].UserAgent,
		"fail_body":       events[0].FailBody,
		"raw_json":        events[0].RawJSON,
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

	firstDelete, err := repository.DeleteBatch(ctx, run.ID, 1, 10_010)
	if err != nil {
		t.Fatalf("first delete batch: %v", err)
	}
	if firstDelete.Deleted != 1 || firstDelete.Completed || firstDelete.Run.DeletedEventCount != 1 {
		t.Fatalf("first delete = %#v", firstDelete)
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
	if _, err := repository.BeginDelete(ctx, run.ID, 10_012); err != nil {
		t.Fatalf("resume delete: %v", err)
	}
	secondDelete, err := repository.DeleteBatch(ctx, run.ID, 1, 10_013)
	if err != nil {
		t.Fatalf("second delete batch: %v", err)
	}
	if secondDelete.Deleted != 1 || !secondDelete.Completed ||
		secondDelete.Run.Status != StatusCompleted || secondDelete.Run.DeletedEventCount != 2 {
		t.Fatalf("second delete = %#v", secondDelete)
	}

	coverage, err := repository.RawCoverage(ctx, 1, 2_500)
	if err != nil {
		t.Fatalf("read raw coverage: %v", err)
	}
	if coverage.RawDeletedEventCount != 2 || coverage.RawEventCount != 0 ||
		coverage.MinDeletedTimestampMS != 1_000 || coverage.MaxDeletedTimestampMS != 2_000 {
		t.Fatalf("raw coverage = %#v", coverage)
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
	if _, err := repository.DeleteBatch(ctx, run.ID, 10, 30_001); !errors.Is(err, ErrCoverageIncomplete) {
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
				archiveTestExec(t, db, `update usage_hourly_aggregate_state set status = 'failed'
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
				archiveTestExec(t, db, `update usage_pricing_rollup_state set status = 'failed'
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
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'failed'
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
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'failed'
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
			name: "monitoring projection status",
			mutate: func(t *testing.T, db *sql.DB, _ Run) func() {
				archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'failed'
					where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
				return func() {
					archiveTestExec(t, db, `update usage_monitoring_rollup_state set status = 'ready'
						where rollup_name = ?`, usagemonitoring.ProjectionRollupName)
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
		{
			name: "dashboard hourly checkpoint",
			mutate: func(t *testing.T, db *sql.DB, run Run) func() {
				archiveTestExec(t, db, `update usage_rollup_checkpoints set last_event_id = ?
					where name = ?`, run.TargetEventID-1, usagerollup.DashboardHourlyCheckpointName)
				return func() {
					archiveTestExec(t, db, `update usage_rollup_checkpoints set last_event_id = ?
						where name = ?`, run.TargetEventID, usagerollup.DashboardHourlyCheckpointName)
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
		})
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
		{name: "dashboard hourly", run: rollups.CatchUpDashboardHourly},
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

func archiveTestEvents() []model.UsageEvent {
	return []model.UsageEvent{
		{
			RequestID:            "request-1",
			EventHash:            "archive-event-1",
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
			EventHash:    "archive-event-2",
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
			EventHash:    "archive-event-3",
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
