package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type cacheAccountingFixture struct {
	Name    string            `json:"name"`
	Context CacheInputContext `json:"context"`
	Tokens  struct {
		Input    int64 `json:"input"`
		Cached   int64 `json:"cached"`
		Cache    int64 `json:"cache"`
		Read     int64 `json:"read"`
		Creation int64 `json:"creation"`
	} `json:"tokens"`
	Expected struct {
		Mode          string `json:"mode"`
		Uncached      int64  `json:"uncached"`
		TotalInput    int64  `json:"totalInput"`
		CacheRead     int64  `json:"cacheRead"`
		CacheCreation int64  `json:"cacheCreation"`
	} `json:"expected"`
}

func TestSharedCacheAccountingFixtures(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "src", "utils", "cacheInputAccounting.fixtures.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared cache accounting fixtures: %v", err)
	}
	var fixtures []cacheAccountingFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode shared cache accounting fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("shared cache accounting fixtures are empty")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			got := NormalizeCacheAccounting(
				fixture.Context,
				fixture.Tokens.Input,
				fixture.Tokens.Cached,
				fixture.Tokens.Cache,
				fixture.Tokens.Read,
				fixture.Tokens.Creation,
			)
			if got.Mode != fixture.Expected.Mode ||
				got.UncachedInputTokens != fixture.Expected.Uncached ||
				got.TotalInputTokens != fixture.Expected.TotalInput ||
				got.CacheReadTokens != fixture.Expected.CacheRead ||
				got.CacheCreationTokens != fixture.Expected.CacheCreation {
				t.Fatalf("accounting = %+v, want %+v", got, fixture.Expected)
			}
		})
	}
}
