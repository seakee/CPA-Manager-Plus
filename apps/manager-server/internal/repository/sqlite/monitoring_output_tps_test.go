package sqlite

import (
	"path/filepath"
	"testing"
)

func TestMonitoringOutputTPSKeepsArchivedHistoryUnknown(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 无逐请求证据的历史日表行不得被删掉或伪造样本。
	_, err = db.Exec(`insert into usage_monitoring_account_daily_rollups_v1
 (structure_revision,bucket_ms,account_snapshot,auth_label_snapshot,provider,auth_provider_snapshot,auth_index,source,source_hash,
 auth_file_snapshot,api_key_hash,executor_type,model,billing_model,pricing_model,service_tier,context_threshold_tokens,failed,calls,last_seen_ms,updated_at_ms)
 values('',0,'','','','','','','','','','','a','a','a','',-1,0,5,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`delete from settings where key=?`, monitoringOutputTPSBackfillKey); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ensureMonitoringOutputTPS(db); err != nil {
			t.Fatal(err)
		}
	}
	var calls, samples int64
	var sum float64
	if err := db.QueryRow(`select calls,output_tps_sum,output_tps_samples from usage_monitoring_account_daily_rollups_v1`).Scan(&calls, &sum, &samples); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || sum != 0 || samples != 0 {
		t.Fatalf("calls=%d sum=%f samples=%d", calls, sum, samples)
	}
}
