package usageevent

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const compatibleUsageBenchmarkBaseTimestampMS = int64(1_700_000_000_000)

type compatibleUsageBenchmarkWriter struct {
	bytes int64
}

func (w *compatibleUsageBenchmarkWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func BenchmarkWriteCompatibleUsage(b *testing.B) {
	tests := []struct {
		name         string
		databaseRows int
		exportLimit  int
	}{
		{name: "db_50000_export_50000", databaseRows: 50_000, exportLimit: 50_000},
		{name: "db_500000_export_50000", databaseRows: 500_000, exportLimit: 50_000},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			ctx := context.Background()
			db := openCompatibleUsageBenchmarkDB(b)
			seedCompatibleUsageBenchmark(b, ctx, db, test.databaseRows)
			checkpointCompatibleUsageBenchmark(b, db)
			repo := New(db)

			b.ReportAllocs()
			var totalOutputBytes int64
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				writer := &compatibleUsageBenchmarkWriter{}
				if err := repo.WriteCompatibleUsage(ctx, writer, test.exportLimit); err != nil {
					b.Fatalf("write compatible usage: %v", err)
				}
				totalOutputBytes += writer.bytes
			}
			b.StopTimer()

			if totalOutputBytes == 0 {
				b.Fatal("compatible usage benchmark produced no output")
			}
			outputBytesPerOperation := totalOutputBytes / int64(b.N)
			b.SetBytes(outputBytesPerOperation)
			b.ReportMetric(float64(outputBytesPerOperation), "output-bytes/op")
			effectiveLimit := normalizeCompatibleUsageStreamLimit(test.exportLimit)
			expectedQueryCount := 4 + (effectiveLimit+compatibleUsageDetailBatchSize-1)/compatibleUsageDetailBatchSize
			b.ReportMetric(float64(expectedQueryCount), "expected-queries/op")
		})
	}
}

func openCompatibleUsageBenchmarkDB(b *testing.B) *sql.DB {
	b.Helper()
	db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open benchmark database: %v", err)
	}
	b.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func seedCompatibleUsageBenchmark(b *testing.B, ctx context.Context, db *sql.DB, count int) {
	b.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin benchmark seed transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert into usage_events (
		event_hash,
		timestamp_ms,
		timestamp,
		model,
		endpoint,
		source,
		auth_index,
		account_snapshot,
		resolved_model,
		input_tokens,
		output_tokens,
		reasoning_tokens,
		cached_tokens,
		cache_tokens,
		cache_read_tokens,
		cache_creation_tokens,
		total_tokens,
		failed,
		fail_status_code,
		fail_summary,
		response_metadata_json,
		created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		b.Fatalf("prepare benchmark seed: %v", err)
	}
	defer stmt.Close()

	endpoints := make([]string, 24)
	for index := range endpoints {
		endpoints[index] = fmt.Sprintf("POST /v1/responses/%02d", index)
	}
	models := make([]string, 96)
	for index := range models {
		models[index] = fmt.Sprintf("benchmark-model-%03d", index)
	}

	for index := 1; index <= count; index++ {
		timestampMS := compatibleUsageBenchmarkBaseTimestampMS + int64(index/2)
		endpoint := endpoints[index%len(endpoints)]
		model := models[index%len(models)]
		if index%211 == 0 {
			endpoint = ""
		}
		if index%223 == 0 {
			model = ""
		}
		failed := index%19 == 0
		var failStatusCode any
		failSummary := ""
		if failed {
			failStatusCode = 429
			failSummary = "benchmark rate limit"
		}
		responseMetadataJSON := ""
		if index%29 == 0 {
			responseMetadataJSON = `{"trace":{"primary_trace_id":"benchmark-trace"}}`
		}

		if _, err := stmt.ExecContext(
			ctx,
			"benchmark-"+strconv.Itoa(index),
			timestampMS,
			strconv.FormatInt(timestampMS, 10),
			model,
			endpoint,
			"benchmark-source",
			fmt.Sprintf("auth-%03d", index%128),
			fmt.Sprintf("account-%04d@example.com", index%1024),
			model,
			100+index%31,
			40+index%17,
			index%11,
			index%7,
			index%7,
			index%5,
			index%3,
			160+index%47,
			failed,
			failStatusCode,
			failSummary,
			responseMetadataJSON,
			timestampMS,
		); err != nil {
			b.Fatalf("insert benchmark row %d: %v", index, err)
		}
	}

	if err := tx.Commit(); err != nil {
		b.Fatalf("commit benchmark seed: %v", err)
	}
}

func checkpointCompatibleUsageBenchmark(b *testing.B, db *sql.DB) {
	b.Helper()
	var busy int64
	var logFrames int64
	var checkpointedFrames int64
	if err := db.QueryRow(`pragma wal_checkpoint(truncate)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		b.Fatalf("checkpoint benchmark database: %v", err)
	}
	if busy != 0 {
		b.Fatalf("benchmark checkpoint busy = %d", busy)
	}
}
