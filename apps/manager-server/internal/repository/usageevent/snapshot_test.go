package usageevent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageSnapshotFreezesHighWaterAndUsesStableEventIDs(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	first := snapshotTestEvent("same-shape-a", 1)
	second := snapshotTestEvent("same-shape-b", 1)
	third := snapshotTestEvent("later", 2)
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{first, second, third}); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}

	snapshot, err := repo.CaptureSnapshot(context.Background())
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	if snapshot.MaxEventID != 3 || snapshot.RowCount != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	if _, err := repo.InsertBatch(context.Background(), []usage.Event{snapshotTestEvent("after-high-water", 3)}); err != nil {
		t.Fatalf("insert concurrent event: %v", err)
	}

	var events []SnapshotEvent
	var afterID int64
	for {
		page, err := repo.SnapshotPage(context.Background(), snapshot.MaxEventID, afterID, 2)
		if err != nil {
			t.Fatalf("read page after %d: %v", afterID, err)
		}
		events = append(events, page.Events...)
		if !page.HasMore {
			break
		}
		afterID = page.Events[len(page.Events)-1].EventID
	}

	if len(events) != 3 {
		t.Fatalf("snapshot event count = %d, events = %#v", len(events), events)
	}
	for index, event := range events {
		wantID := int64(index + 1)
		if event.EventID != wantID {
			t.Fatalf("event %d id = %d, want %d", index, event.EventID, wantID)
		}
		if event.EventHash == "after-high-water" {
			t.Fatalf("snapshot included concurrent event: %#v", event)
		}
	}
	if events[0].TimestampMS != events[1].TimestampMS || events[0].Model != events[1].Model ||
		events[0].Endpoint != events[1].Endpoint || events[0].EventID == events[1].EventID {
		t.Fatalf("same-shape events lost distinct identity: %#v", events[:2])
	}
	if got := snapshotDigest(events); got != snapshot.Digest {
		t.Fatalf("digest = %q, want %q", got, snapshot.Digest)
	}

	next, err := repo.CaptureSnapshot(context.Background())
	if err != nil {
		t.Fatalf("capture next snapshot: %v", err)
	}
	if next.MaxEventID != 4 || next.RowCount != 4 || next.Digest == snapshot.Digest {
		t.Fatalf("next snapshot = %#v", next)
	}
}

func TestUsageSnapshotPagesAllRowsBeyondLegacy50000Limit(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	stmt, err := tx.Prepare(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, endpoint, input_tokens, output_tokens, total_tokens, created_at_ms
	) values (?, ?, '2026-01-01T00:00:00Z', 'gpt-test', 'POST /v1/responses', 1, 2, 3, ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare fixture insert: %v", err)
	}
	const rowCount = 50_001
	for index := 1; index <= rowCount; index++ {
		if _, err := stmt.Exec(fmt.Sprintf("snapshot-%05d", index), index, index); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("insert fixture %d: %v", index, err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close fixture statement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixtures: %v", err)
	}

	repo := New(db)
	snapshot, err := repo.CaptureSnapshot(context.Background())
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	if snapshot.RowCount != rowCount || snapshot.MaxEventID != rowCount {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	hash := sha256.New()
	var delivered int64
	var afterID int64
	for {
		page, err := repo.SnapshotPage(context.Background(), snapshot.MaxEventID, afterID, 4096)
		if err != nil {
			t.Fatalf("read page after %d: %v", afterID, err)
		}
		for _, event := range page.Events {
			_, _ = fmt.Fprintf(hash, "%d\x00%s\n", event.EventID, event.EventHash)
			afterID = event.EventID
			delivered++
		}
		if !page.HasMore {
			break
		}
	}
	if delivered != rowCount || afterID != rowCount {
		t.Fatalf("delivered = %d, last id = %d", delivered, afterID)
	}
	if got := "sha256:" + hex.EncodeToString(hash.Sum(nil)); got != snapshot.Digest {
		t.Fatalf("digest = %q, want %q", got, snapshot.Digest)
	}
}

func snapshotTestEvent(hash string, timestampMS int64) usage.Event {
	return usage.Event{
		EventHash:    hash,
		TimestampMS:  timestampMS,
		Timestamp:    "2026-01-01T00:00:00Z",
		Model:        "gpt-test",
		Endpoint:     "POST /v1/responses",
		Source:       "masked-source",
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
		FailBody:     "Bearer must-not-leak",
		RawJSON:      `{"authorization":"must-not-leak"}`,
		CreatedAtMS:  timestampMS,
	}
}

func snapshotDigest(events []SnapshotEvent) string {
	hash := sha256.New()
	for _, event := range events {
		_, _ = fmt.Fprintf(hash, "%d\x00%s\n", event.EventID, event.EventHash)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
