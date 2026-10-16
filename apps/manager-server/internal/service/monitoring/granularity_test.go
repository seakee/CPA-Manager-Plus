package monitoring

import "testing"

func TestNormalizeGranularitySubHourLimits(t *testing.T) {
	const hourMS = int64(60 * 60 * 1000)
	tests := []struct {
		input   string
		rangeMS int64
		want    string
	}{
		{"1m", 24 * hourMS, "1m"},
		{"1m", 25 * hourMS, "1m"},
		{"1m", 48 * hourMS, "day"},
		{"15m", 7 * 24 * hourMS, "15m"},
		{"15m", 30 * 24 * hourMS, "day"},
		{"15m", 12 * hourMS, "15m"},
		{"bogus", 12 * hourMS, "hour"},
	}
	for _, test := range tests {
		if got := normalizeGranularity(test.input, 1_000, 1_000+test.rangeMS); got != test.want {
			t.Errorf("normalizeGranularity(%q, %dh) = %q, want %q", test.input, test.rangeMS/hourMS, got, test.want)
		}
	}
}
