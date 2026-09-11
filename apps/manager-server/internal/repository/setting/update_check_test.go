package setting

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNotificationClaimIsAtomicAndDurable(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "updates.sqlite")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("create table settings(key text primary key,value text not null,updated_at_ms integer not null)"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := ClaimUpdateNotification(context.Background(), db, "v2.0.0")
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	if err = DismissUpdateNotification(context.Background(), db, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	var notified, dismissed int64
	if err = db.QueryRow("select json_extract(value,'$.notified_at'),json_extract(value,'$.dismissed_at') from settings").Scan(&notified, &dismissed); err != nil || notified == 0 || dismissed == 0 {
		t.Fatalf("%d %d %v", notified, dismissed, err)
	}
	db.Close()
	db, err = sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if ok, err := ClaimUpdateNotification(context.Background(), db, "v2.0.0"); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := ClaimUpdateNotification(context.Background(), db, "v2.0.1"); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}
