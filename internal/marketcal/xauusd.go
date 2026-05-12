// Package marketcal decides when XAUUSD spot is open or closed and counts
// elapsed trading days. Used by paper-live to gate its background
// auto-refresh: rederiving weights on a weekend or US holiday wastes
// compute and creates a misleading canonical run.
//
// Coverage:
//   - Weekly close: Friday 21:00 UTC → Sunday 22:00 UTC (Admirals/most ECN
//     brokers; gold spot follows the COMEX session calendar with a small
//     liquidity tail into FX hours).
//   - US holidays: COMEX-observed closures only. London/Asian flow keeps
//     XAUUSD moving on UK/EU holidays, so we don't gate on those.
package marketcal

import "time"

// IsXAUUSDTradingDay reports whether a wall-clock instant falls inside the
// XAUUSD trading week. "Trading day" here is the *session* sense: returns
// true any time the market is open, not just Monday-Friday.
func IsXAUUSDTradingDay(t time.Time) bool {
	u := t.UTC()
	switch u.Weekday() {
	case time.Saturday:
		return false
	case time.Friday:
		// Open until 21:00 UTC.
		if u.Hour() >= 21 {
			return false
		}
	case time.Sunday:
		// Reopens at 22:00 UTC.
		if u.Hour() < 22 {
			return false
		}
	}
	if isUSHoliday(u) {
		return false
	}
	return true
}

// TradingDaysSince counts XAUUSD trading days that have BEGUN strictly
// after prev's UTC calendar date and at-or-before now's UTC calendar
// date. A "trading day" is a UTC date that is Monday-Friday and not a
// COMEX-observed US holiday.
//
// The semantic answers the operational question "have I touched a new
// trading day since the last refresh?". Two restarts on the same UTC
// date return 0; restarting after a UTC midnight rollover into a
// weekday returns 1; restarting Monday morning after a Friday refresh
// returns 1 (the weekend dates are skipped, Monday counts).
//
// Returns 0 if prev >= now.
func TradingDaysSince(prev, now time.Time) int {
	if !now.After(prev) {
		return 0
	}
	pYY, pMM, pDD := prev.UTC().Date()
	nYY, nMM, nDD := now.UTC().Date()
	prevDate := time.Date(pYY, pMM, pDD, 0, 0, 0, 0, time.UTC)
	nowDate := time.Date(nYY, nMM, nDD, 0, 0, 0, 0, time.UTC)
	count := 0
	for d := prevDate.AddDate(0, 0, 1); !d.After(nowDate); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue
		}
		if isUSHoliday(d) {
			continue
		}
		count++
	}
	return count
}

// isUSHoliday returns true on COMEX-observed closures. Calls a per-year
// computation so we work past 2030 without lookup tables.
func isUSHoliday(t time.Time) bool {
	y, m, d := t.UTC().Date()
	for _, h := range usHolidays(y) {
		hy, hm, hd := h.Date()
		if hy == y && hm == m && hd == d {
			return true
		}
	}
	return false
}

// usHolidays returns COMEX-observed federal holidays for a given year,
// computed structurally (Nth-weekday or fixed date with weekend rollover).
//
// COMEX closure list (gold futures):
//   - New Year's Day (Jan 1, Sat→Fri prior, Sun→Mon after)
//   - MLK Day (3rd Monday January)
//   - Presidents Day (3rd Monday February)
//   - Good Friday (Friday before Easter)
//   - Memorial Day (last Monday May)
//   - Juneteenth (Jun 19, weekend roll)
//   - Independence Day (Jul 4, weekend roll)
//   - Labor Day (1st Monday September)
//   - Thanksgiving (4th Thursday November)
//   - Christmas Day (Dec 25, weekend roll)
func usHolidays(year int) []time.Time {
	return []time.Time{
		observed(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)),
		nthWeekdayOfMonth(year, time.January, time.Monday, 3),
		nthWeekdayOfMonth(year, time.February, time.Monday, 3),
		goodFriday(year),
		lastWeekdayOfMonth(year, time.May, time.Monday),
		observed(time.Date(year, time.June, 19, 0, 0, 0, 0, time.UTC)),
		observed(time.Date(year, time.July, 4, 0, 0, 0, 0, time.UTC)),
		nthWeekdayOfMonth(year, time.September, time.Monday, 1),
		nthWeekdayOfMonth(year, time.November, time.Thursday, 4),
		observed(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC)),
	}
}

// observed shifts a fixed-date federal holiday onto the nearest weekday:
// Saturday → Friday prior, Sunday → Monday after, weekday unchanged.
func observed(t time.Time) time.Time {
	switch t.Weekday() {
	case time.Saturday:
		return t.AddDate(0, 0, -1)
	case time.Sunday:
		return t.AddDate(0, 0, 1)
	}
	return t
}

// nthWeekdayOfMonth returns e.g. the 3rd Monday of January.
func nthWeekdayOfMonth(year int, month time.Month, wd time.Weekday, n int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(wd) - int(first.Weekday()) + 7) % 7
	day := 1 + offset + 7*(n-1)
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// lastWeekdayOfMonth returns e.g. the last Monday of May.
func lastWeekdayOfMonth(year int, month time.Month, wd time.Weekday) time.Time {
	// Start at the 1st of the NEXT month, step back to the wanted weekday.
	first := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 0, -1) // last day of `month`
	offset := (int(last.Weekday()) - int(wd) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

// goodFriday returns the Friday before Easter Sunday. Uses the Meeus/Jones/
// Butcher Gregorian algorithm for Easter — verified against the standard
// ecclesiastical table for 1900-2099.
func goodFriday(year int) time.Time {
	y := year
	a := y % 19
	b := y / 100
	c := y % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := ((h + l - 7*m + 114) % 31) + 1
	easter := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return easter.AddDate(0, 0, -2)
}
