package usagemonitoring_test

import (
	"context"
	"fmt"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	monitoringrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
)

func TestOutputTPSRawDailyEdgesTailAndUpgrade(t *testing.T) {
	db, _ := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	raw := usageevent.New(db)
	repo := monitoringrepo.New(db)
	insert := func(id int, timestamp, latency, output int64, failed int, model string) {
		t.Helper()
		_, err := db.Exec(`insert into usage_events(event_hash,timestamp_ms,timestamp,model,output_tokens,latency_ms,failed,created_at_ms) values(?,?,'1',?,?,?,?,1)`, fmt.Sprintf("tps-%d", id), timestamp, model, output, latency, failed)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(1, testDayMS+1, 1000, 100, 0, "a")
	insert(2, testDayMS+2, 10000, 100, 0, "a")
	insert(3, testDayMS+3, 1000, 100, 1, "a")
	insert(4, testDayMS+4, 0, 100, 0, "a")
	insert(5, testDayMS+5, -1, 100, 0, "a")
	insert(6, testDayMS+6, 1000, 0, 0, "a")
	insert(7, testDayMS+7, 1000, -1, 0, "a")
	insert(8, 2*testDayMS+1, 1000, 20, 0, "b")
	if _, err := db.Exec(`insert into usage_events(event_hash,timestamp_ms,timestamp,model,output_tokens,created_at_ms) values('tps-null',?,'1','a',100,1)`, testDayMS+8); err != nil {
		t.Fatal(err)
	}
	catchUp := func() {
		t.Helper()
		if _, err := repo.CatchUpProjection(ctx, 1000, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CatchUpStats(ctx, 1000, 1); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		for _, filter := range []usageevent.AnalyticsFilter{
			{FromMS: 0, ToMS: 3 * testDayMS},
			{FromMS: 1, ToMS: 2*testDayMS + 2},
			{FromMS: testDayMS, ToMS: 2 * testDayMS, Models: []string{"a"}},
			{FromMS: testDayMS + 2, ToMS: testDayMS + 3},
		} {
			want, err := raw.AccountModelStatsWithFilter(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			got, _, ok, err := repo.LoadAccountStats(ctx, filter)
			if err != nil || !ok {
				t.Fatalf("available=%v err=%v", ok, err)
			}
			if len(got) != len(want) {
				t.Fatalf("row count: got=%+v want=%+v", got, want)
			}
			for i := range got {
				if got[i].Model != want[i].Model || got[i].OutputTPSSum != want[i].OutputTPSSum || got[i].OutputTPSSamples != want[i].OutputTPSSamples {
					t.Fatalf("filter=%+v\ngot=%+v\nwant=%+v", filter, got, want)
				}
			}
		}
		rows, err := raw.AccountModelStatsWithFilter(ctx, usageevent.AnalyticsFilter{FromMS: testDayMS, ToMS: 2 * testDayMS, Models: []string{"a"}})
		if err != nil || len(rows) != 1 || rows[0].OutputTPSSamples != 2 || rows[0].OutputTPSSum/2 != 55 {
			t.Fatalf("55 TPS: %+v %v", rows, err)
		}
	}
	catchUp()
	check()
	insert(10, 2*testDayMS+2, 1000, 40, 0, "b")
	check()
	catchUp()
	// 模拟旧版本数据库，迁移必须补齐列并原位回填历史 TPS。
	for _, column := range []string{"output_tps_sum", "output_tps_samples"} {
		if _, err := db.Exec(`alter table usage_monitoring_account_daily_rollups_v1 drop column ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatal(err)
	}
	check()
	catchUp()
	check()
}

func TestOutputTPSBackfillRetainedHistory(t *testing.T) {
	db, _ := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	repo := monitoringrepo.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id int, model, tier string, output int) {
		exec(`insert into usage_events(event_hash,timestamp_ms,timestamp,model,service_tier,output_tokens,latency_ms,failed,created_at_ms)
		values(?,86400001,'1',?,?,?,1000,0,1)`, fmt.Sprintf("repair-%d", id), model, tier, output)
	}
	insert(1, "a", "", 100)
	insert(2, "a", "", 10)
	insert(3, "a", "priority", 30)
	insert(4, "b", "", 40)
	if _, err := repo.CatchUpProjection(ctx, 1000, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CatchUpStats(ctx, 1000, 1); err != nil {
		t.Fatal(err)
	}
	// 一条只保留投影，一条完全删除；水位以后的原始和投影只应由尾部读取。
	exec(`delete from usage_events where event_hash in ('repair-2','repair-4')`)
	exec(`delete from usage_monitoring_event_projection_v1 where event_id=4`)
	exec(`pragma foreign_keys=off`)
	exec(`insert into usage_archive_event_refs(event_hash,run_id,segment_sequence,raw_event_id,timestamp_ms,archived_at_ms,raw_deleted_at_ms)
	 values('repair-4','historical',1,4,86400001,1,1)`)
	exec(`pragma foreign_keys=on`)
	insert(5, "a", "", 50)
	if _, err := repo.CatchUpProjection(ctx, 1000, 1); err != nil {
		t.Fatal(err)
	}
	// 复现上一版已存在 TPS 列但所有历史指标为零的状态。
	exec(`update usage_monitoring_account_daily_rollups_v1 set output_tps_sum=0,output_tps_samples=0`)
	exec(`delete from settings where key='usage_monitoring_output_tps_backfill_v2'`)
	var before string
	if err := db.QueryRow(`select group_concat(calls || ':' || output_tokens || ':' || latency_sum_ms) from usage_monitoring_account_daily_rollups_v1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := sqliterepo.Migrate(db); err != nil {
			t.Fatal(err)
		}
		var sum float64
		var samples, coverage int64
		if err := db.QueryRow(`select sum(output_tps_sum),sum(output_tps_samples) from usage_monitoring_account_daily_rollups_v1`).Scan(&sum, &samples); err != nil {
			t.Fatal(err)
		}
		if sum != 140 || samples != 3 {
			t.Fatalf("backfill sum=%v samples=%d", sum, samples)
		}
		if err := db.QueryRow(`select coverage_event_id from usage_monitoring_rollup_state where rollup_name='stats_v1'`).Scan(&coverage); err != nil {
			t.Fatal(err)
		}
		if coverage != 4 {
			t.Fatalf("coverage=%d", coverage)
		}
		var after string
		if err := db.QueryRow(`select group_concat(calls || ':' || output_tokens || ':' || latency_sum_ms) from usage_monitoring_account_daily_rollups_v1`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("statistics changed: %s -> %s", before, after)
		}
		rows, _, ok, err := repo.LoadAccountStats(ctx, usageevent.AnalyticsFilter{FromMS: 0, ToMS: 3 * testDayMS, IncludeFailed: true})
		if err != nil || !ok {
			t.Fatalf("available=%v err=%v", ok, err)
		}
		var total float64
		var n int64
		for _, row := range rows {
			total += row.OutputTPSSum
			n += row.OutputTPSSamples
		}
		if total != 190 || n != 4 {
			t.Fatalf("with tail sum=%v samples=%d", total, n)
		}
	}
	if _, err := repo.CatchUpStats(ctx, 1000, 2); err != nil {
		t.Fatal(err)
	}
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatal(err)
	}
	var sum float64
	var samples int64
	if err := db.QueryRow(`select sum(output_tps_sum),sum(output_tps_samples) from usage_monitoring_account_daily_rollups_v1`).Scan(&sum, &samples); err != nil {
		t.Fatal(err)
	}
	if sum != 190 || samples != 4 {
		t.Fatalf("after catchup sum=%v samples=%d", sum, samples)
	}
}
