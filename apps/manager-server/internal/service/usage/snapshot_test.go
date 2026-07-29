package usage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestSnapshotProtocolReplaysAndRejectsTamperedCrossSnapshotCursor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.InsertEvents(context.Background(), []usageparser.Event{
		snapshotServiceEvent("event-a", 1),
		snapshotServiceEvent("event-b", 2),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	service := New(db, WithSnapshotProtocol(SnapshotProtocolConfig{
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		TTL:        time.Hour,
		Now:        func() time.Time { return now },
	}))
	first, err := service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{Limit: 1})
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	if first.Complete || first.NextCursor == "" || first.RowCount != 2 || first.MaxEventID != 2 || len(first.Events) != 1 {
		t.Fatalf("first page = %#v", first)
	}
	if first.Events[0].EventID == 0 || first.Events[0].EventHash != "event-a" {
		t.Fatalf("first event = %#v", first.Events[0])
	}

	replay, err := service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: first.SnapshotID,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("replay first page: %v", err)
	}
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay = %#v, want %#v", replay, first)
	}

	last, err := service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: first.SnapshotID,
		Cursor:     first.NextCursor,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("read final page: %v", err)
	}
	if !last.Complete || last.NextCursor != "" || len(last.Events) != 1 || last.Events[0].EventHash != "event-b" {
		t.Fatalf("last page = %#v", last)
	}

	tampered := first.NextCursor[:len(first.NextCursor)-1] + "x"
	_, err = service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: first.SnapshotID,
		Cursor:     tampered,
		Limit:      1,
	})
	requireSnapshotErrorCode(t, err, SnapshotErrorInvalidCursor)

	other, err := service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{Limit: 1})
	if err != nil {
		t.Fatalf("create other snapshot: %v", err)
	}
	if other.SnapshotID == first.SnapshotID {
		t.Fatal("independent snapshots reused the same snapshot_id")
	}
	_, err = service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: other.SnapshotID,
		Cursor:     first.NextCursor,
		Limit:      1,
	})
	requireSnapshotErrorCode(t, err, SnapshotErrorInvalidCursor)
}

func TestSnapshotProtocolExpiresSnapshotAndCursor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.InsertEvents(context.Background(), []usageparser.Event{
		snapshotServiceEvent("event-a", 1),
		snapshotServiceEvent("event-b", 2),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	service := New(db, WithSnapshotProtocol(SnapshotProtocolConfig{
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		TTL:        time.Minute,
		Now:        func() time.Time { return now },
	}))
	page, err := service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{Limit: 1})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	now = now.Add(2 * time.Minute)

	_, err = service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: page.SnapshotID,
		Limit:      1,
	})
	requireSnapshotErrorCode(t, err, SnapshotErrorExpired)
	_, err = service.ReadSnapshotPage(context.Background(), SnapshotPageRequest{
		SnapshotID: page.SnapshotID,
		Cursor:     page.NextCursor,
		Limit:      1,
	})
	requireSnapshotErrorCode(t, err, SnapshotErrorExpired)
}

func snapshotServiceEvent(hash string, timestampMS int64) usageparser.Event {
	return usageparser.Event{
		EventHash:    hash,
		TimestampMS:  timestampMS,
		Timestamp:    "2026-01-01T00:00:00Z",
		Model:        "gpt-test",
		Endpoint:     "POST /v1/responses",
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
		CreatedAtMS:  timestampMS,
	}
}

func requireSnapshotErrorCode(t *testing.T, err error, want SnapshotErrorCode) {
	t.Helper()
	snapshotErr, ok := err.(*SnapshotError)
	if !ok || snapshotErr.Code != want {
		t.Fatalf("snapshot error = %#v, want code %q", err, want)
	}
}
