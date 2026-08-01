package usage

import (
	"testing"
	"time"
)

func TestAnalyticsBucketMSAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	tests := []struct {
		name        string
		timestampMS int64
		granularity string
		wantMS      int64
	}{
		{
			name:        "hour before spring transition",
			timestampMS: time.Date(2026, time.March, 8, 6, 30, 0, 0, time.UTC).UnixMilli(),
			granularity: "hour",
			wantMS:      time.Date(2026, time.March, 8, 6, 0, 0, 0, time.UTC).UnixMilli(),
		},
		{
			name:        "hour after spring transition",
			timestampMS: time.Date(2026, time.March, 8, 7, 30, 0, 0, time.UTC).UnixMilli(),
			granularity: "hour",
			wantMS:      time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC).UnixMilli(),
		},
		{
			name:        "local day",
			timestampMS: time.Date(2026, time.March, 8, 18, 0, 0, 0, time.UTC).UnixMilli(),
			granularity: "day",
			wantMS:      time.Date(2026, time.March, 8, 5, 0, 0, 0, time.UTC).UnixMilli(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AnalyticsBucketMS(test.timestampMS, test.granularity, location); got != test.wantMS {
				t.Fatalf("bucket = %d, want %d", got, test.wantMS)
			}
		})
	}
}

func TestAnalyticsFullUTCHourRange(t *testing.T) {
	hourMS := int64(time.Hour / time.Millisecond)
	fromMS := int64(1_800_000_000_000) + 15*60*1000
	toMS := fromMS + 3*hourMS + 20*60*1000
	startMS, endMS := AnalyticsFullUTCHourRange(fromMS, toMS)
	if startMS%hourMS != 0 || endMS%hourMS != 0 {
		t.Fatalf("range = [%d, %d), want UTC-hour alignment", startMS, endMS)
	}
	if startMS < fromMS || startMS-fromMS >= hourMS {
		t.Fatalf("start = %d, from = %d", startMS, fromMS)
	}
	if endMS > toMS || toMS-endMS >= hourMS {
		t.Fatalf("end = %d, to = %d", endMS, toMS)
	}
}

func TestCanMapUTCWholeHours(t *testing.T) {
	hourMS := int64(time.Hour / time.Millisecond)
	fromMS := time.Date(2026, time.March, 7, 0, 0, 0, 0, time.UTC).UnixMilli()
	toMS := fromMS + 72*hourMS

	for _, name := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		location, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if !CanMapUTCWholeHours(fromMS, toMS, "hour", location) {
			t.Fatalf("%s unexpectedly not representable", name)
		}
	}

	for _, name := range []string{"Asia/Kolkata", "Australia/Eucla"} {
		location, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if CanMapUTCWholeHours(fromMS, toMS, "hour", location) {
			t.Fatalf("%s unexpectedly representable", name)
		}
	}

	if CanMapUTCWholeHours(fromMS+1, toMS, "hour", time.UTC) {
		t.Fatal("unaligned range unexpectedly representable")
	}
}
