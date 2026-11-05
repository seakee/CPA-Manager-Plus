package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestDailyRollupsSplitSuccessAndFailure exercises the actual INSERT ... SELECT
// and ON CONFLICT paths.  Without grouping on failed (SELECT column 19), SQLite
// is free to choose an arbitrary outcome for a whole bucket/dimension group.
func TestDailyRollupsSplitSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	createDailyRollupFixture(t, db)
	insert, err := db.Prepare(`insert into usage_events (
		id, timestamp_ms, model, requested_model, api_key_hash,
		account_snapshot, provider, source_hash, input_tokens, output_tokens,
		total_tokens, latency_ms, failed
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) `)
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()

	// 100 events in a single day/key/account/model: 98 success + 2 failure.
	// Include successful zero-token events to protect that independent metric.
	const eventTimeMS int64 = 1_793_500_000_000
	for id := 1; id <= 100; id++ {
		failed := 0
		if id == 15 || id == 70 {
			failed = 1
		}
		input, output, total := 80, 20, 100
		if id == 10 || id == 20 || id == 30 {
			input, output, total = 0, 0, 0
		}
		if _, err := insert.ExecContext(ctx, id, eventTimeMS, "gpt-6-astra", "gpt-6-astra",
			"test-key-hash", "test-account", "openai", "test-source-hash",
			input, output, total, 100, failed); err != nil {
			t.Fatal(err)
		}
	}

	// Two batches also cover UPSERT into already-existing daily groups.
	for _, bounds := range [][2]int64{{0, 50}, {50, 100}} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := upsertAccountDailyBatch(ctx, tx, "fixture-revision", bounds[0], bounds[1], eventTimeMS); err != nil {
			_ = tx.Rollback()
			t.Fatalf("account rollup (%v): %v", bounds, err)
		}
		if err := upsertAPIKeyDailyBatch(ctx, tx, "fixture-revision", bounds[0], bounds[1], eventTimeMS); err != nil {
			_ = tx.Rollback()
			t.Fatalf("API key rollup (%v): %v", bounds, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	for _, table := range []string{
		"usage_monitoring_account_daily_rollups_v1",
		"usage_monitoring_api_key_daily_rollups_v1",
	} {
		t.Run(table, func(t *testing.T) {
			rows, err := db.QueryContext(ctx,
				"select failed, sum(calls), sum(total_tokens), sum(zero_token_calls) from "+table+
					" where structure_revision = ? group by failed order by failed", "fixture-revision")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got [2]int64
			var tokens, zeroTokens int64
			var states int
			for rows.Next() {
				var status int
				var calls, sumTokens, zeros int64
				if err := rows.Scan(&status, &calls, &sumTokens, &zeros); err != nil {
					t.Fatal(err)
				}
				if status < 0 || status > 1 {
					t.Fatalf("unexpected failed=%d", status)
				}
				got[status] += calls
				tokens += sumTokens
				zeroTokens += zeros
				states++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if states != 2 || got != [2]int64{98, 2} {
				t.Fatalf("failed buckets: got status count=%d, success=%d, failure=%d; want 2, 98, 2", states, got[0], got[1])
			}
			if tokens != 9700 || zeroTokens != 3 {
				t.Fatalf("token accounting: got total=%d, zero-token success=%d; want 9700, 3", tokens, zeroTokens)
			}
		})
	}
}

// The fixture deliberately uses the same rollup column names and uniqueness
// dimensions as production, but only the upstream fields read by these two
// statements. It does not open or mutate an operator's usage.sqlite.
func createDailyRollupFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`create table usage_events (
			id integer primary key, timestamp_ms integer not null,
			model text, requested_model text, resolved_model text,
			normalized_total_input_tokens integer,
			input_tokens integer, output_tokens integer, total_tokens integer,
			cached_tokens integer, cache_tokens integer,
			cache_read_tokens integer, cache_creation_tokens integer,
			reasoning_tokens integer, latency_ms integer,
			account_snapshot text, auth_label_snapshot text,
			provider text, auth_provider_snapshot text,
			auth_account_id_snapshot text, auth_index text,
			source text, source_hash text, auth_file_snapshot text,
			api_key_hash text, executor_type text, service_tier text,
			auth_project_id_snapshot text, failed integer not null
		)`,
		`create table model_prices (model text primary key)`,
		`create table model_price_context_tiers (model text, threshold_tokens integer)`,
	}
	columns := strings.Fields(`structure_revision bucket_ms api_key_hash account_snapshot
		auth_label_snapshot provider auth_provider_snapshot auth_account_id_snapshot
		auth_index source source_hash auth_file_snapshot executor_type model
		billing_model pricing_model service_tier context_threshold_tokens failed calls
		input_tokens output_tokens reasoning_tokens cached_tokens cache_read_tokens
		cache_creation_tokens long_input_tokens long_output_tokens long_cached_tokens
		long_cache_read_tokens long_cache_creation_tokens total_tokens zero_token_calls
		latency_sum_ms latency_samples last_seen_ms updated_at_ms`)
	integerColumns := map[string]bool{}
	for _, name := range strings.Fields(`bucket_ms context_threshold_tokens failed calls
		input_tokens output_tokens reasoning_tokens cached_tokens cache_read_tokens
		cache_creation_tokens long_input_tokens long_output_tokens long_cached_tokens
		long_cache_read_tokens long_cache_creation_tokens total_tokens zero_token_calls
		latency_sum_ms latency_samples last_seen_ms updated_at_ms`) {
		integerColumns[name] = true
	}
	declarations := make([]string, len(columns))
	for i, name := range columns {
		typ := "text"
		if integerColumns[name] {
			typ = "integer"
		}
		declarations[i] = name + " " + typ + " not null"
	}
	uniques := map[string]string{
		"usage_monitoring_account_daily_rollups_v1": `structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_account_id_snapshot, auth_index,
			source, source_hash, auth_file_snapshot, api_key_hash, executor_type,
			model, billing_model, pricing_model, service_tier, context_threshold_tokens, failed`,
		"usage_monitoring_api_key_daily_rollups_v1": `structure_revision, bucket_ms, api_key_hash, account_snapshot,
			auth_label_snapshot, provider, auth_provider_snapshot, auth_account_id_snapshot,
			auth_index, source, source_hash, auth_file_snapshot, executor_type,
			model, billing_model, pricing_model, service_tier, context_threshold_tokens, failed`,
	}
	for table, unique := range uniques {
		statements = append(statements, fmt.Sprintf("create table %s (%s, unique (%s))",
			table, strings.Join(declarations, ", "), unique))
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create daily rollup fixture: %v", err)
		}
	}
}
