package usage

import "time"

const analyticsHourMS = int64(time.Hour / time.Millisecond)

// AnalyticsBucketMS resolves an event timestamp to the start of its local
// analytics hour or day bucket.
func AnalyticsBucketMS(timestampMS int64, granularity string, location *time.Location) int64 {
	if location == nil {
		location = time.UTC
	}
	tm := time.UnixMilli(timestampMS).In(location)
	if granularity == "day" {
		return time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, location).UnixMilli()
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
