package usage

import "time"

// deepSeekPeakMultiplier is the published DeepSeek peak multiplier for the Go
// catalog. Peak windows are 01:00-04:00 and 06:00-10:00 UTC, Monday-Friday.
const deepSeekPeakMultiplier = 2.0

// IsDeepSeekPeak reports whether at falls in a DeepSeek peak window. The
// windows are UTC and apply Monday through Friday; weekends are off-peak.
func IsDeepSeekPeak(at time.Time) bool {
	utc := at.UTC()
	if wd := utc.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	mins := utc.Hour()*60 + utc.Minute()
	return (mins >= 1*60 && mins < 4*60) || (mins >= 6*60 && mins < 10*60)
}

// PeakMultiplier returns the price multiplier for a model at a point in time.
// The upstream reports the authoritative list cost; this is used only for
// local fallback estimates.
func PeakMultiplier(modelID string, at time.Time) float64 {
	switch modelID {
	case "deepseek-v4.1-flash", "deepseek-v4-pro":
		if IsDeepSeekPeak(at) {
			return deepSeekPeakMultiplier
		}
	}
	return 1
}
