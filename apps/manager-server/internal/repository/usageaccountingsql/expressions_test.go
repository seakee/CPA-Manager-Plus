package usageaccountingsql

import (
	"database/sql"
	"math"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLegacyProjectionSaturatesOverflowingLowerBound(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expressions := For("")
	query := `select ` + expressions.Total + `, ` + expressions.Unclassified + `, ` + expressions.Quality + `
		from (select
			? as input_tokens, 1 as output_tokens, 0 as reasoning_tokens,
			0 as cached_tokens, 0 as cache_tokens, 0 as cache_read_tokens, 0 as cache_creation_tokens,
			0 as accounting_version, 0 as accounting_valid, '' as accounting_quality,
			null as normalized_uncached_input_tokens, null as normalized_total_input_tokens,
			null as normalized_cache_read_tokens, null as normalized_cache_creation_tokens,
			null as normalized_non_reasoning_output_tokens, null as normalized_reasoning_output_tokens,
			null as normalized_total_output_tokens, null as unclassified_tokens,
			0 as total_tokens
		)`
	var total, unclassified int64
	var quality string
	if err := db.QueryRow(query, int64(math.MaxInt64)).Scan(&total, &unclassified, &quality); err != nil {
		t.Fatalf("scan overflowing legacy projection: %v", err)
	}
	if total != math.MaxInt64 || unclassified != math.MaxInt64 || quality != "unclassified" {
		t.Fatalf("legacy projection = total:%d unclassified:%d quality:%q", total, unclassified, quality)
	}
}

func TestCanonicalReadinessAcceptsExactMaxAndRejectsOverflow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expressions := For("")
	query := `select ` + expressions.Ready + ` from (select
		0 as input_tokens, 0 as output_tokens, 0 as reasoning_tokens,
		0 as cached_tokens, 0 as cache_tokens, 0 as cache_read_tokens, 0 as cache_creation_tokens,
		2 as accounting_version, 1 as accounting_valid, 'complete' as accounting_quality,
		? as normalized_uncached_input_tokens, ? as normalized_total_input_tokens,
		? as normalized_cache_read_tokens, ? as normalized_cache_creation_tokens,
		0 as normalized_non_reasoning_output_tokens, 0 as normalized_reasoning_output_tokens,
		0 as normalized_total_output_tokens, 0 as unclassified_tokens,
		? as total_tokens
	)`

	for _, tt := range []struct {
		name        string
		uncached    int64
		inputTotal  int64
		cacheRead   int64
		cacheCreate int64
		total       int64
		wantReady   int
	}{
		{
			name:       "exact max",
			uncached:   math.MaxInt64,
			inputTotal: math.MaxInt64,
			total:      math.MaxInt64,
			wantReady:  1,
		},
		{
			name:        "overflowing input buckets",
			uncached:    math.MaxInt64,
			inputTotal:  math.MaxInt64,
			cacheRead:   math.MaxInt64,
			cacheCreate: 5,
			total:       math.MaxInt64,
			wantReady:   0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ready int
			if err := db.QueryRow(
				query,
				tt.uncached,
				tt.inputTotal,
				tt.cacheRead,
				tt.cacheCreate,
				tt.total,
			).Scan(&ready); err != nil {
				t.Fatalf("read canonical readiness: %v", err)
			}
			if ready != tt.wantReady {
				t.Fatalf("ready = %d, want %d", ready, tt.wantReady)
			}
		})
	}
}

func TestCanonicalProjectionRejectsFutureAccountingVersion(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expressions := For("")
	query := `select ` + expressions.Ready + `, ` + expressions.Quality + `,
		` + expressions.TotalInput + `, ` + expressions.TotalOutput + `,
		` + expressions.Unclassified + `, ` + expressions.Total + `, ` + expressions.Incomplete + `
		from (select
			100 as input_tokens, 20 as output_tokens, 0 as reasoning_tokens,
			0 as cached_tokens, 0 as cache_tokens, 0 as cache_read_tokens, 0 as cache_creation_tokens,
			3 as accounting_version, 1 as accounting_valid, 'complete' as accounting_quality,
			100 as normalized_uncached_input_tokens, 100 as normalized_total_input_tokens,
			0 as normalized_cache_read_tokens, 0 as normalized_cache_creation_tokens,
			20 as normalized_non_reasoning_output_tokens, 0 as normalized_reasoning_output_tokens,
			20 as normalized_total_output_tokens, 0 as unclassified_tokens,
			120 as total_tokens
		)`

	var ready, incomplete int
	var quality string
	var input, output, unclassified, total int64
	if err := db.QueryRow(query).Scan(
		&ready,
		&quality,
		&input,
		&output,
		&unclassified,
		&total,
		&incomplete,
	); err != nil {
		t.Fatalf("scan future accounting projection: %v", err)
	}
	if ready != 0 || quality != "inconsistent" || input != 0 || output != 0 ||
		unclassified != 120 || total != 120 || incomplete != 1 {
		t.Fatalf(
			"future accounting projection = ready:%d quality:%q input:%d output:%d unclassified:%d total:%d incomplete:%d",
			ready,
			quality,
			input,
			output,
			unclassified,
			total,
			incomplete,
		)
	}
}

func TestCanonicalReadinessRejectsFractionalTokenBuckets(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expressions := For("")
	query := `select ` + expressions.Ready + `, ` + expressions.Quality + `, ` + expressions.Incomplete + `
		from (select
			2 as input_tokens, 1 as output_tokens, 0 as reasoning_tokens,
			0 as cached_tokens, 0 as cache_tokens, 0 as cache_read_tokens, 0 as cache_creation_tokens,
			2 as accounting_version, 1 as accounting_valid, 'complete' as accounting_quality,
			1.5 as normalized_uncached_input_tokens, 2.0 as normalized_total_input_tokens,
			0.5 as normalized_cache_read_tokens, 0.0 as normalized_cache_creation_tokens,
			0.5 as normalized_non_reasoning_output_tokens, 0.5 as normalized_reasoning_output_tokens,
			1.0 as normalized_total_output_tokens, 0.0 as unclassified_tokens,
			3.0 as total_tokens
		)`

	var ready, incomplete int
	var quality string
	if err := db.QueryRow(query).Scan(&ready, &quality, &incomplete); err != nil {
		t.Fatalf("scan fractional canonical readiness: %v", err)
	}
	if ready != 0 || quality != "inconsistent" || incomplete != 1 {
		t.Fatalf(
			"fractional canonical readiness = ready:%d quality:%q incomplete:%d",
			ready,
			quality,
			incomplete,
		)
	}
}

func TestCanonicalValidityFailsClosed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expressions := For("")
	query := `select ` + expressions.Ready + `, ` + expressions.Valid + ` from (select
		0 as input_tokens, 0 as output_tokens, 0 as reasoning_tokens,
		0 as cached_tokens, 0 as cache_tokens, 0 as cache_read_tokens, 0 as cache_creation_tokens,
		? as accounting_version, ? as accounting_valid, 'complete' as accounting_quality,
		? as normalized_uncached_input_tokens, 10 as normalized_total_input_tokens,
		0 as normalized_cache_read_tokens, 0 as normalized_cache_creation_tokens,
		5 as normalized_non_reasoning_output_tokens, 0 as normalized_reasoning_output_tokens,
		5 as normalized_total_output_tokens, 0 as unclassified_tokens,
		15 as total_tokens
	)`

	for _, tt := range []struct {
		name        string
		version     int
		storedValid int
		uncached    int
		wantReady   int
		wantValid   int
	}{
		{name: "valid v2", version: 2, storedValid: 1, uncached: 10, wantReady: 1, wantValid: 1},
		{name: "explicit invalid v2", version: 2, storedValid: 0, uncached: 10, wantReady: 1, wantValid: 0},
		{name: "version zero with invalid valid flag", version: 0, storedValid: 1, uncached: 10, wantReady: 1, wantValid: 0},
		{name: "corrupt v2 buckets", version: 2, storedValid: 1, uncached: 9, wantReady: 0, wantValid: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ready, valid int
			if err := db.QueryRow(query, tt.version, tt.storedValid, tt.uncached).Scan(&ready, &valid); err != nil {
				t.Fatalf("read canonical validity: %v", err)
			}
			if ready != tt.wantReady || valid != tt.wantValid {
				t.Fatalf("canonical state = ready:%d valid:%d, want ready:%d valid:%d", ready, valid, tt.wantReady, tt.wantValid)
			}
		})
	}
}
