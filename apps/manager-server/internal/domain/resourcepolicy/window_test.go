package resourcepolicy

import (
	"errors"
	"math"
	"testing"
	"time"
)

func ptr(n int64) *int64 { return &n }

func TestResolveDurationWindows(t *testing.T) {
	tests := []struct {
		name    string
		spec    WindowSpec
		ref     int64
		want    ResolvedWindow
		invalid bool
	}{
		{"rolling exact ms", WindowSpec{Kind: WindowRolling, DurationMS: ptr(10)}, 100, ResolvedWindow{91, 101}, false},
		{"rolling start nonpositive", WindowSpec{Kind: WindowRolling, DurationMS: ptr(101)}, 100, ResolvedWindow{}, true},
		{"rolling overflow", WindowSpec{Kind: WindowRolling, DurationMS: ptr(10)}, math.MaxInt64, ResolvedWindow{}, true},
		{"fixed anchor", WindowSpec{Kind: WindowFixed, DurationMS: ptr(10), AnchorAtMS: ptr(100)}, 100, ResolvedWindow{100, 110}, false},
		{"fixed before anchor floor", WindowSpec{Kind: WindowFixed, DurationMS: ptr(10), AnchorAtMS: ptr(100)}, 89, ResolvedWindow{80, 90}, false},
		{"fixed before anchor boundary", WindowSpec{Kind: WindowFixed, DurationMS: ptr(10), AnchorAtMS: ptr(100)}, 90, ResolvedWindow{90, 100}, false},
		{"fixed after anchor", WindowSpec{Kind: WindowFixed, DurationMS: ptr(10), AnchorAtMS: ptr(100)}, 119, ResolvedWindow{110, 120}, false},
		{"fixed end overflow", WindowSpec{Kind: WindowFixed, DurationMS: ptr(10), AnchorAtMS: ptr(100)}, math.MaxInt64, ResolvedWindow{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWindow(tt.spec, tt.ref)
			if tt.invalid {
				if !errors.Is(err, ErrWindowUnresolvable) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func ms(t *testing.T, zone, value string) int64 {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	date, err := time.ParseInLocation("2006-01-02 15:04:05.000", value, loc)
	if err != nil {
		t.Fatal(err)
	}
	return date.UnixMilli()
}

func TestResolveCalendarWindows(t *testing.T) {
	tests := []struct {
		name, zone, anchor, reference, start, end string
		months                                    int64
	}{
		{"basic", "UTC", "2025-01-15 10:20:30.123", "2025-02-01 00:00:00.000", "2025-01-15 10:20:30.123", "2025-02-15 10:20:30.123", 1},
		{"Jan31 clamp common", "UTC", "2025-01-31 10:00:00.000", "2025-02-28 10:00:00.000", "2025-02-28 10:00:00.000", "2025-03-31 10:00:00.000", 1},
		{"Jan31 leap", "UTC", "2024-01-31 10:00:00.000", "2024-02-29 10:00:00.000", "2024-02-29 10:00:00.000", "2024-03-31 10:00:00.000", 1},
		{"Mar31 no drift", "UTC", "2025-01-31 10:00:00.000", "2025-03-31 10:00:00.000", "2025-03-31 10:00:00.000", "2025-04-30 10:00:00.000", 1},
		{"two months", "UTC", "2025-01-31 10:00:00.000", "2025-04-01 00:00:00.000", "2025-03-31 10:00:00.000", "2025-05-31 10:00:00.000", 2},
		{"timezone", "Asia/Shanghai", "2025-01-15 10:20:30.123", "2025-02-15 10:20:30.123", "2025-02-15 10:20:30.123", "2025-03-15 10:20:30.123", 1},
		{"spring DST", "America/New_York", "2025-02-09 03:30:00.000", "2025-03-09 03:30:00.000", "2025-03-09 03:30:00.000", "2025-04-09 03:30:00.000", 1},
		{"fall DST", "America/New_York", "2025-10-02 03:30:00.000", "2025-11-02 03:30:00.000", "2025-11-02 03:30:00.000", "2025-12-02 03:30:00.000", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := WindowSpec{Kind: WindowFixed, AnchorAtMS: ptr(ms(t, tt.zone, tt.anchor)), CalendarMonths: ptr(tt.months), Timezone: tt.zone}
			got, err := ResolveWindow(spec, ms(t, tt.zone, tt.reference))
			if err != nil || got != (ResolvedWindow{ms(t, tt.zone, tt.start), ms(t, tt.zone, tt.end)}) {
				t.Fatalf("got %+v, %v; want %s to %s", got, err, tt.start, tt.end)
			}
		})
	}
}

func TestResolveCalendarDSTGoTimeDateSemantics(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name               string
		anchor, start, end time.Time
	}{
		{"spring missing wall time",
			time.Date(2025, 2, 9, 2, 30, 0, 0, loc),
			time.Date(2025, 3, 9, 2, 30, 0, 0, loc),
			time.Date(2025, 4, 9, 2, 30, 0, 0, loc)},
		{"fall repeated wall time",
			time.Date(2025, 10, 2, 1, 30, 0, 0, loc),
			time.Date(2025, 11, 2, 1, 30, 0, 0, loc),
			time.Date(2025, 12, 2, 1, 30, 0, 0, loc)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := WindowSpec{Kind: WindowFixed, AnchorAtMS: ptr(tt.anchor.UnixMilli()), CalendarMonths: ptr(1), Timezone: loc.String()}
			got, err := ResolveWindow(spec, tt.start.UnixMilli())
			want := ResolvedWindow{tt.start.UnixMilli(), tt.end.UnixMilli()}
			if err != nil || got != want {
				t.Fatalf("got %+v, %v; want %+v", got, err, want)
			}
		})
	}
}
