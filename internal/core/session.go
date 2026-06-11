package core

import (
	"sync"
	"time"
)

// Session names align with the framework's kill-zone boundaries by NY hour:
// Asia closes when London opens, NY-AM begins at 08:00, NY-PM at 12:00, and
// the 17:00-19:00 gap is the dead "off" band.
const (
	SessionAsia   = "asia"
	SessionLondon = "london"
	SessionNYAM   = "ny_am"
	SessionNYPM   = "ny_pm"
	SessionOff    = "off"
)

var (
	nyOnce sync.Once
	nyTZ   *time.Location
)

// NyLoc returns the cached America/New_York location. Falls back to a fixed
// -5h zone if tzdata is unavailable (e.g. minimal containers) so the engine
// still runs, even though DST will then be wrong.
func NyLoc() *time.Location {
	nyOnce.Do(func() {
		l, err := time.LoadLocation("America/New_York")
		if err != nil {
			l = time.FixedZone("EST", -5*3600)
		}
		nyTZ = l
	})
	return nyTZ
}

// ToNY converts any time.Time into NY time using NyLoc(). The session
// classifier and every strategy that filters by NY hour go through this.
func ToNY(t time.Time) time.Time { return t.In(NyLoc()) }

// ClassifySession maps a candle's UTC time to the session it belongs to.
func ClassifySession(t time.Time) string {
	h := ToNY(t).Hour()
	switch {
	case h >= 19 || h < 2:
		return SessionAsia
	case h >= 2 && h < 8:
		return SessionLondon
	case h >= 8 && h < 12:
		return SessionNYAM
	case h >= 12 && h < 17:
		return SessionNYPM
	default:
		return SessionOff
	}
}

// YearMonthBucket returns YYYYMM for grouping (e.g. 202503).
func YearMonthBucket(t time.Time) int {
	u := t.UTC()
	return u.Year()*100 + int(u.Month())
}

// YearQuarterBucket returns YYYYQ for grouping (e.g. 20251 for Q1 2025).
func YearQuarterBucket(t time.Time) int {
	u := t.UTC()
	q := (int(u.Month())-1)/3 + 1
	return u.Year()*10 + q
}
