package desktopmonitor

import (
	"math"
	"testing"
	"time"
)

func number(v float64) *float64 { return &v }
func TestFormatUnknownZeroAndUnits(t *testing.T) {
	for _, tc := range []struct {
		v    int64
		want string
	}{{-1, "—"}, {0, "0"}, {18432, "18,432"}, {1860000, "1,860,000"}} {
		if got := FormatNumber(tc.v); got != tc.want {
			t.Errorf("number %d: %s", tc.v, got)
		}
	}
	for _, tc := range []struct {
		v                     *float64
		duration, speed, cost string
	}{{nil, "—", "—", "—"}, {number(0), "0.00 s", "0.0 tok/s", "$0.0000"}, {number(420), "0.42 s", "420.0 tok/s", "$420.0000"}, {number(-1), "—", "—", "—"}, {number(math.NaN()), "—", "—", "—"}} {
		if FormatDuration(tc.v) != tc.duration || FormatSpeed(tc.v) != tc.speed || FormatCost(tc.v) != tc.cost {
			t.Errorf("format %+v", tc)
		}
	}
}
func TestPresentationRetentionScopeAuthMidnightAndLateResult(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	var state ViewState
	g := state.Reset()
	snap := Snapshot{UpdatedAt: now, Today: DaySummary{Calls: 8, Tokens: 99, Cost: number(2)}, Requests: []Request{{Model: "old", Timestamp: now}}}
	state.Apply(g, snap, nil, now)
	state.Apply(g, Snapshot{}, errUnavailable, now)
	if state.Snapshot.Today.Tokens != 99 || state.Status != StatusOffline {
		t.Fatal("transient erased last good")
	}
	state.Tick(now.Add(2 * time.Second))
	if state.Snapshot.Today.Tokens != -1 || state.Snapshot.Today.Cost != nil || len(state.Snapshot.Requests) != 1 {
		t.Fatal("midnight leaked yesterday or removed latest")
	}
	next := state.Reset()
	state.Apply(g, snap, nil, now)
	if len(state.Snapshot.Requests) != 0 {
		t.Fatal("old scope restored")
	}
	state.Apply(next, snap, nil, now)
	state.Apply(next, Snapshot{}, ErrUnauthorized, now)
	if len(state.Snapshot.Requests) != 0 || state.Status != StatusUnauthorized {
		t.Fatal("auth loss retained data")
	}
	state.Apply(next, snap, nil, now)
	if len(state.Snapshot.Requests) != 0 {
		t.Fatal("late result after auth loss")
	}
	state.Reset()
	state.Close()
	state.Apply(state.Generation, snap, nil, now)
	if len(state.Snapshot.Requests) != 0 {
		t.Fatal("closed view accepted data")
	}
}
