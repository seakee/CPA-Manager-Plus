package resourcepolicy

import (
	"errors"
	"math"
	"time"
)

var ErrWindowUnresolvable = errors.New("quota window unresolvable")

type ResolvedWindow struct {
	StartMS int64
	EndMS   int64
}

func ResolveWindow(spec WindowSpec, referenceMS int64) (ResolvedWindow, error) {
	if err := spec.Validate(); err != nil || referenceMS <= 0 {
		return ResolvedWindow{}, ErrWindowUnresolvable
	}
	var start, end int64
	switch {
	case spec.Kind == WindowRolling:
		if referenceMS == math.MaxInt64 {
			return ResolvedWindow{}, ErrWindowUnresolvable
		}
		end = referenceMS + 1
		if *spec.DurationMS >= end {
			return ResolvedWindow{}, ErrWindowUnresolvable
		}
		start = end - *spec.DurationMS
	case spec.DurationMS != nil:
		duration, anchor := *spec.DurationMS, *spec.AnchorAtMS
		// Subtract without overflowing when the reference precedes the anchor.
		if referenceMS < anchor {
			difference := uint64(anchor) - uint64(referenceMS)
			cycles := (difference + uint64(duration) - 1) / uint64(duration)
			if cycles > uint64(math.MaxInt64)/uint64(duration) {
				return ResolvedWindow{}, ErrWindowUnresolvable
			}
			start = anchor - int64(cycles*uint64(duration))
		} else {
			cycles := (referenceMS - anchor) / duration
			if cycles > (math.MaxInt64-anchor)/duration {
				return ResolvedWindow{}, ErrWindowUnresolvable
			}
			start = anchor + cycles*duration
		}
		if start > math.MaxInt64-duration {
			return ResolvedWindow{}, ErrWindowUnresolvable
		}
		end = start + duration
	default:
		return resolveCalendarWindow(spec, referenceMS)
	}
	if start <= 0 || end <= start {
		return ResolvedWindow{}, ErrWindowUnresolvable
	}
	return ResolvedWindow{start, end}, nil
}

func resolveCalendarWindow(spec WindowSpec, referenceMS int64) (ResolvedWindow, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ResolvedWindow{}, ErrWindowUnresolvable
	}
	anchor := time.UnixMilli(*spec.AnchorAtMS).In(loc)
	reference := time.UnixMilli(referenceMS).In(loc)
	anchorMonth := int64(anchor.Year())*12 + int64(anchor.Month()) - 1
	referenceMonth := int64(reference.Year())*12 + int64(reference.Month()) - 1
	months := *spec.CalendarMonths
	cycle := floorDiv(referenceMonth-anchorMonth, months)
	boundary := func(index int64) (int64, bool) {
		if index > 0 && index > (math.MaxInt64-anchorMonth)/months || index < 0 && index < (math.MinInt64-anchorMonth)/months {
			return 0, false
		}
		monthIndex := anchorMonth + index*months
		year := floorDiv(monthIndex, 12)
		if year > math.MaxInt32 || year < math.MinInt32 {
			return 0, false
		}
		month := time.Month(monthIndex - year*12 + 1)
		day := anchor.Day()
		last := time.Date(int(year), month+1, 0, 12, 0, 0, 0, loc).Day()
		if day > last {
			day = last
		}
		date := time.Date(int(year), month, day, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), loc)
		ms := date.UnixMilli()
		if ms <= 0 || !time.UnixMilli(ms).Equal(date) {
			return 0, false
		}
		return ms, true
	}
	// Month arithmetic narrows the answer to one cycle; DST and day clamping
	// may move a boundary across the reference within that cycle.
	for i := 0; i < 3; i++ {
		start, ok := boundary(cycle)
		if !ok || cycle == math.MaxInt64 {
			return ResolvedWindow{}, ErrWindowUnresolvable
		}
		end, ok := boundary(cycle + 1)
		if !ok || end <= start {
			return ResolvedWindow{}, ErrWindowUnresolvable
		}
		if referenceMS < start {
			cycle--
			continue
		}
		if referenceMS >= end {
			cycle++
			continue
		}
		return ResolvedWindow{start, end}, nil
	}
	return ResolvedWindow{}, ErrWindowUnresolvable
}

func floorDiv(a, b int64) int64 {
	q, r := a/b, a%b
	if r < 0 {
		q--
	}
	return q
}
