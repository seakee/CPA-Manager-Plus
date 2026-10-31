// Package desktopmonitor implements the read-only Windows monitor's portable data model.
package desktopmonitor

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Negative token/count values mean unavailable, not zero. No raw event metadata
// or credentials belong in these view types.
type Snapshot struct {
	UpdatedAt time.Time
	Today     DaySummary
	Requests  []Request
	Partial   bool
}
type DaySummary struct {
	Calls, Tokens int64
	Cost          *float64
}
type Request struct {
	Model                                                string
	Timestamp                                            time.Time
	Failed, Stream                                       bool
	InputTokens, OutputTokens, CachedTokens, TotalTokens int64
	LatencyMS, TTFTMS, TokensPerSecond, Cost             *float64
}

var ErrUnauthorized = errors.New("administrator key rejected")
var errUnavailable = errors.New("monitor service unavailable")

func validNumber(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0
}
func FormatNumber(v int64) string {
	if v < 0 {
		return "—"
	}
	s := strconv.FormatInt(v, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func FormatDuration(v *float64) string {
	if !validNumber(v) {
		return "—"
	}
	return fmt.Sprintf("%.2f s", *v/1000)
}
func FormatSpeed(v *float64) string {
	if !validNumber(v) {
		return "—"
	}
	return fmt.Sprintf("%.1f tok/s", *v)
}
func FormatCost(v *float64) string {
	if !validNumber(v) {
		return "—"
	}
	return fmt.Sprintf("$%.4f", *v)
}
func unknownSnapshot() Snapshot { return Snapshot{Today: DaySummary{Calls: -1, Tokens: -1}} }
func dayStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

const (
	StatusDisconnected = "disconnected"
	StatusConnected    = "connected"
	StatusOffline      = "offline"
	StatusUnauthorized = "unauthorized"
)

// ViewState is owned by the UI thread. A generation identifies a connection and
// active visibility scope. Network workers never touch it or a window handle.
type ViewState struct {
	Snapshot   Snapshot
	Status     string
	Generation uint64
	closed     bool
}

func (s *ViewState) Reset() uint64 {
	s.Generation++
	s.Snapshot = unknownSnapshot()
	s.Status = StatusDisconnected
	return s.Generation
}
func (s *ViewState) Invalidate() uint64 { s.Generation++; return s.Generation }
func (s *ViewState) Close()             { s.closed = true; s.Invalidate(); s.Snapshot = unknownSnapshot() }
func (s *ViewState) Apply(g uint64, snap Snapshot, err error, now time.Time) {
	if s.closed || g != s.Generation {
		return
	}
	switch {
	case errors.Is(err, ErrUnauthorized):
		s.Reset()
		s.Status = StatusUnauthorized
	case err != nil:
		s.Status = StatusOffline
	default:
		s.Snapshot = snap
		s.Status = StatusConnected
	}
	s.Tick(now)
}
func (s *ViewState) Tick(now time.Time) {
	if !s.Snapshot.UpdatedAt.IsZero() && !dayStart(s.Snapshot.UpdatedAt.In(now.Location())).Equal(dayStart(now)) {
		s.Snapshot.Today = DaySummary{Calls: -1, Tokens: -1}
	}
}
func displayModel(names ...string) string {
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			return strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, truncate(name, 160))
		}
	}
	return "—"
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
