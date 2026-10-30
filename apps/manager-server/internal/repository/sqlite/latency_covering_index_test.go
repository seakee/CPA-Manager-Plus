package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

const testLatencyCoverIndex = "idx_usage_events_ts_latency_ttft_cover"

// Optional large offline validation accepts only a marked synthetic fixture.
func TestLatencyCoverIndexLargeOfflineExperiment(t *testing.T) {
	path := os.Getenv("CPAMP_LATENCY_PERF_MAINT_DB")
	if path == "" {
		t.Skip("opt-in offline validation on synthetic large fixture")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("fixture missing")
	}
	probe, err := sql.Open("sqlite", dataSourceName(path)+"&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = probe.QueryRow(`select event_count from cpamp_latency_benchmark_fixture`).Scan(&count)
	_ = probe.Close()
	if err != nil || count < 3000000 {
		t.Fatal("refusing unmarked or undersized fixture")
	}
	lock, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	before := latencyCoverRawSnapshot(t, db)
	var timestampRoot int
	if err := db.QueryRow(`select rootpage from sqlite_schema where name='idx_usage_events_timestamp'`).Scan(&timestampRoot); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"insert", "update", "delete"} {
		if _, err := db.Exec(`create trigger guard_large_latency_` + action + ` before ` + action + ` on usage_events begin select raise(abort,'immutable synthetic history'); end`); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`drop trigger if exists guard_large_latency_` + action)
	}
	if _, err := db.Exec(`drop index if exists ` + testLatencyCoverIndex); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatal(err)
	}
	startupMS := float64(time.Since(start).Microseconds()) / 1000
	if latencyCoverExists(t, db) {
		t.Fatal("large startup synchronously created index")
	}
	latencyCoverAssertPending(t, db, true)
	if _, err := db.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	// modernc libc snapshots the environment. Set this before starting go test,
	// rather than using t.Setenv after the first SQLite connection has opened.
	temp := os.Getenv("SQLITE_TMPDIR")
	if info, err := os.Stat(temp); err != nil || !info.IsDir() || temp == "" {
		t.Fatal("launch go test with SQLITE_TMPDIR pointing to a task-owned directory")
	}
	baseline, err := latencyCoverOpenDiskBytes(path, temp)
	if err != nil {
		t.Fatal(err)
	}
	peak := baseline
	var monitorErr error
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				bytes, err := latencyCoverOpenDiskBytes(path, temp)
				if err != nil {
					monitorErr = err
					return
				}
				if bytes > peak {
					peak = bytes
				}
			}
		}
	}()
	start = time.Now()
	result, buildErr := CleanupDerivedOffline(ctx, db)
	buildMS := float64(time.Since(start).Microseconds()) / 1000
	close(stop)
	wg.Wait()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if monitorErr != nil {
		t.Fatal(monitorErr)
	}
	if result.PreparedIndexes != 1 {
		t.Fatalf("unexpected offline work %+v", result)
	}
	if !latencyCoverExists(t, db) {
		t.Fatal("large offline build missing")
	}
	latencyCoverAssertPending(t, db, false)
	var indexBytes, walBytes int64
	if err := db.QueryRow(`select sum(pgsize) from dbstat where name=?`, testLatencyCoverIndex).Scan(&indexBytes); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		walBytes = info.Size()
	}
	var finalRoot int
	if err := db.QueryRow(`select rootpage from sqlite_schema where name='idx_usage_events_timestamp'`).Scan(&finalRoot); err != nil {
		t.Fatal(err)
	}
	if timestampRoot != finalRoot {
		t.Fatal("offline cleanup rebuilt timestamp index")
	}
	if got := latencyCoverRawSnapshot(t, db); got != before {
		t.Fatal("large offline cleanup changed raw history")
	}
	t.Logf("OFFLINE events=%d startup_ms=%.3f build_ms=%.3f index_bytes=%d wal_bytes=%d peak_open_disk_growth_bytes=%d", count, startupMS, buildMS, indexBytes, walBytes, peak-baseline)
}

// lsof sees SQLite sort files even when SQLite has already unlinked them.
// Count each device/inode once because the connection pool shares files.
func latencyCoverOpenDiskBytes(path, temp string) (int64, error) {
	// lsof resolves /var to /private/var on macOS.
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, err
	}
	temp, err = filepath.EvalSymlinks(temp)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "lsof", "-nP", "-p", strconv.Itoa(os.Getpid()), "-F", "fDins").Output()
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	var total, size int64
	var device, inode, name string
	flush := func() {
		key := device + "/" + inode
		if (strings.HasPrefix(name, path) || strings.HasPrefix(name, temp+string(os.PathSeparator))) && inode != "" && !seen[key] {
			seen[key] = true
			total += size
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case 'f':
			flush()
			device = ""
			inode = ""
			name = ""
			size = 0
		case 'D':
			device = line[1:]
		case 'i':
			inode = line[1:]
		case 'n':
			name = line[1:]
		case 's':
			size, _ = strconv.ParseInt(line[1:], 10, 64)
		}
	}
	flush()
	return total, nil
}

func TestLatencyCoverIndexLifecycle(t *testing.T) {
	for _, count := range []int{0, 10, 100000} {
		t.Run(fmt.Sprintf("rows_%d", count), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if count > 0 {
				_, err = db.Exec(`with recursive ids(i) as (select 1 union all select i+1 from ids where i < ?)
insert into usage_events(event_hash,timestamp_ms,timestamp,model,latency_ms,ttft_ms,raw_json,created_at_ms)
select printf('%064x',i),i,'synthetic','model',case when i%3=0 then null else i end,i%100,'{"synthetic":true}',i from ids`, count)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`create index idx_usage_events_timestamp on usage_events(timestamp_ms)`); err != nil {
				t.Fatal(err)
			}
			var originalRoot int
			if err := db.QueryRow(`select rootpage from sqlite_schema where name='idx_usage_events_timestamp'`).Scan(&originalRoot); err != nil {
				t.Fatal(err)
			}
			before := latencyCoverRawSnapshot(t, db)
			for _, action := range []string{"insert", "update", "delete"} {
				if _, err := db.Exec(`create trigger forbid_latency_maintenance_` + action + ` before ` + action + ` on usage_events begin select raise(abort,'raw events must remain immutable'); end`); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if latencyCoverExists(t, db) {
				t.Fatal("Open synchronously built latency index")
			}
			if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
				t.Fatal(err)
			}
			if count == 0 {
				if !latencyCoverExists(t, db) {
					t.Fatal("empty startup did not build index")
				}
			} else {
				if latencyCoverExists(t, db) {
					t.Fatal("nonempty startup built index")
				}
				latencyCoverAssertPending(t, db, true)
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if _, err := CleanupDerivedOffline(cancelled, db); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled cleanup: %v", err)
				}
				latencyCoverAssertPending(t, db, true)
				if _, err := CleanupDerivedOffline(ctx, db); err != nil {
					t.Fatal(err)
				}
				if !latencyCoverExists(t, db) {
					t.Fatal("offline index missing")
				}
			}
			latencyCoverAssertPending(t, db, false)
			rows, err := db.Query(`pragma index_info('idx_usage_events_ts_latency_ttft_cover')`)
			if err != nil {
				t.Fatal(err)
			}
			var columns []string
			for rows.Next() {
				var seq, cid int
				var name string
				if err := rows.Scan(&seq, &cid, &name); err != nil {
					t.Fatal(err)
				}
				columns = append(columns, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(columns, []string{"timestamp_ms", "latency_ms", "ttft_ms"}) {
				t.Fatalf("index columns %v", columns)
			}
			for range 2 {
				if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
					t.Fatal(err)
				}
				if _, err := CleanupDerivedOffline(ctx, db); err != nil {
					t.Fatal(err)
				}
			}
			var finalRoot int
			if err := db.QueryRow(`select rootpage from sqlite_schema where name='idx_usage_events_timestamp'`).Scan(&finalRoot); err != nil {
				t.Fatal(err)
			}
			if originalRoot != finalRoot {
				t.Fatal("existing timestamp index rebuilt")
			}
			if got := latencyCoverRawSnapshot(t, db); got != before {
				t.Fatalf("raw history changed: %v -> %v", before, got)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if !latencyCoverExists(t, db) {
				t.Fatal("index missing after restart")
			}
			latencyCoverAssertPending(t, db, false)
		})
	}
}

func TestLatencyCoverIndexBuildFailureKeepsPendingState(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into usage_events(event_hash,timestamp_ms,timestamp,model,created_at_ms) values('synthetic',1,'synthetic','model',1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatal(err)
	}
	latencyCoverAssertPending(t, db, true)
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `begin immediate`); err != nil {
		t.Fatal(err)
	}
	failedCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, buildErr := CleanupDerivedOffline(failedCtx, db)
	if _, err := writer.ExecContext(ctx, `rollback`); err != nil {
		t.Fatal(err)
	}
	if buildErr == nil {
		t.Fatal("cleanup succeeded while writer lock held")
	}
	if latencyCoverExists(t, db) {
		t.Fatal("failed build left index")
	}
	latencyCoverAssertPending(t, db, true)
	if _, err := CleanupDerivedOffline(ctx, db); err != nil {
		t.Fatal(err)
	}
	if !latencyCoverExists(t, db) {
		t.Fatal("retry did not build index")
	}
	latencyCoverAssertPending(t, db, false)
}

func TestLatencyCoverIndexInterruptedDDLCanResume(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`with recursive ids(i) as (select 1 union all select i+1 from ids where i < 300000)
insert into usage_events(event_hash,timestamp_ms,timestamp,model,latency_ms,ttft_ms,raw_json,created_at_ms)
select printf('%064x',i),i,'synthetic','model',i%10000,i%1000,'{"synthetic":true}',i from ids`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatal(err)
	}
	latencyCoverAssertPending(t, db, true)
	before := latencyCoverRawSnapshot(t, db)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	_, buildErr := conn.ExecContext(interrupted, `create index idx_usage_events_ts_latency_ttft_cover on usage_events(timestamp_ms,latency_ms,ttft_ms)`)
	deadlineErr := interrupted.Err()
	cancel()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if buildErr == nil {
		t.Fatal("cancelled DDL unexpectedly completed")
	}
	if !errors.Is(deadlineErr, context.DeadlineExceeded) {
		t.Fatalf("DDL failed without deadline interruption: %v (context %v)", buildErr, deadlineErr)
	}
	if latencyCoverExists(t, db) {
		t.Fatal("interrupted DDL left a partial index")
	}
	latencyCoverAssertPending(t, db, true)
	if got := latencyCoverRawSnapshot(t, db); got != before {
		t.Fatal("interrupted DDL changed raw history")
	}
	if _, err := CleanupDerivedOffline(ctx, db); err != nil {
		t.Fatal(err)
	}
	if !latencyCoverExists(t, db) {
		t.Fatal("offline retry did not complete interrupted index")
	}
	latencyCoverAssertPending(t, db, false)
	if got := latencyCoverRawSnapshot(t, db); got != before {
		t.Fatal("offline retry changed raw history")
	}
}

func latencyCoverExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`select count(*) from sqlite_schema where type='index' and name=? and tbl_name='usage_events'`, testLatencyCoverIndex).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}
func latencyCoverAssertPending(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	var n int
	if err := db.QueryRow(`select count(*) from usage_derived_deferred_indexes where index_name=?`, testLatencyCoverIndex).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if (n == 1) != want {
		t.Fatalf("pending index count=%d want=%t", n, want)
	}
	state, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if want && (!state.Required || !state.PerformanceDegraded || state.Command != "cleanup-derived") {
		t.Fatalf("maintenance state %+v", state)
	}
	if !want && (state.Required || state.PerformanceDegraded || state.DeferredIndexes != 0) {
		t.Fatalf("completed maintenance still pending: %+v", state)
	}
}
func latencyCoverRawSnapshot(t *testing.T, db *sql.DB) [6]int64 {
	t.Helper()
	var v [6]int64
	if err := db.QueryRow(`select count(*),coalesce(sum(id),0),coalesce(sum(timestamp_ms),0),coalesce(sum(latency_ms),0),coalesce(sum(ttft_ms),0),coalesce(sum(length(raw_json)),0) from usage_events`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
		t.Fatal(err)
	}
	return v
}
