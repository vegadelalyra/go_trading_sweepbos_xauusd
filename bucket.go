package main

import "time"

// Session classifies a candle time into a trading session by NY (EST/EDT) hour.
// Boundaries chosen to align with the framework: Asia opens after NY close,
// London opens at NY 02:00, NY open at 08:00, NY PM at 12:00, gap 17–19.
const (
	SessionAsia   = "asia"
	SessionLondon = "london"
	SessionNYAM   = "ny_am"
	SessionNYPM   = "ny_pm"
	SessionOff    = "off"
)

func classifySession(t time.Time) string {
	h := toNY(t).Hour()
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

func yearMonthBucket(t time.Time) int {
	u := t.UTC()
	return u.Year()*100 + int(u.Month())
}

func yearQuarterBucket(t time.Time) int {
	u := t.UTC()
	q := (int(u.Month())-1)/3 + 1
	return u.Year()*10 + q
}
