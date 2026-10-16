package usage

import "time"

const analyticsHourMS = int64(time.Hour / time.Millisecond)

// Sub-hour analytics granularities. They are always served from raw usage
// events because the permanent rollups are hourly.
const (
	AnalyticsGranularityMinute      = "1m"
	AnalyticsGranularityQuarterHour = "15m"
	analyticsQuarterHourMinutes     = 15
	analyticsMinuteMS               = int64(time.Minute / time.Millisecond)
)

// IsSubHourAnalyticsGranularity reports whether granularity is finer than an hour.
func IsSubHourAnalyticsGranularity(granularity string) bool {
	return granularity == AnalyticsGranularityMinute || granularity == AnalyticsGranularityQuarterHour
}

// AnalyticsBucketSizeMS returns the nominal width of one analytics bucket.
func AnalyticsBucketSizeMS(granularity string) int64 {
	switch granularity {
	case "day":
		return 24 * analyticsHourMS
	case AnalyticsGranularityQuarterHour:
		return analyticsQuarterHourMinutes * analyticsMinuteMS
	case AnalyticsGranularityMinute:
		return analyticsMinuteMS
	}
	return analyticsHourMS
}

// AnalyticsBucketMS resolves an event timestamp to the start of its local
// analytics minute, quarter hour, hour or day bucket.
func AnalyticsBucketMS(timestampMS int64, granularity string, location *time.Location) int64 {
	if location == nil {
		location = time.UTC
	}
	tm := time.UnixMilli(timestampMS).In(location)
	switch granularity {
	case "day":
		return time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, location).UnixMilli()
	case AnalyticsGranularityQuarterHour:
		minute := tm.Minute() - tm.Minute()%analyticsQuarterHourMinutes
		return time.Date(tm.Year(), tm.Month(), tm.Day(), tm.Hour(), minute, 0, 0, location).UnixMilli()
	case AnalyticsGranularityMinute:
		return time.Date(tm.Year(), tm.Month(), tm.Day(), tm.Hour(), tm.Minute(), 0, 0, location).UnixMilli()
	}
	return time.Date(tm.Year(), tm.Month(), tm.Day(), tm.Hour(), 0, 0, 0, location).UnixMilli()
}

// AnalyticsFullUTCHourRange returns the complete UTC hours contained by the
// half-open analytics range [fromMS, toMS).
func AnalyticsFullUTCHourRange(fromMS, toMS int64) (int64, int64) {
	startMS := fromMS - fromMS%analyticsHourMS
	if fromMS%analyticsHourMS != 0 {
		startMS += analyticsHourMS
	}
	endMS := toMS - toMS%analyticsHourMS
	return startMS, endMS
}

// CanMapUTCWholeHours reports whether every complete UTC hour in the supplied
// aligned range maps to one local analytics bucket without being split.
func CanMapUTCWholeHours(fromMS, toMS int64, granularity string, location *time.Location) bool {
	if IsSubHourAnalyticsGranularity(granularity) {
		return false
	}
	if fromMS >= toMS || fromMS%analyticsHourMS != 0 || toMS%analyticsHourMS != 0 {
		return false
	}
	if location == nil {
		location = time.UTC
	}
	if granularity != "day" {
		granularity = "hour"
	}
	for hourMS := fromMS; hourMS < toMS; hourMS += analyticsHourMS {
		if AnalyticsBucketMS(hourMS, granularity, location) != AnalyticsBucketMS(hourMS+analyticsHourMS-1, granularity, location) {
			return false
		}
	}
	return true
}
