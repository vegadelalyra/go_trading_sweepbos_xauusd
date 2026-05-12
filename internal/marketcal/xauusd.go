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

// TradingDaysSince counts XAUUSD trading-DAY boundaries that have closed
// between prev and now. A "boundary" is the daily NY 22:00 UTC roll.
// Returns 0 if prev >= now.
//
// Examples (5m bars):
//   - prev = Mon 18:00 UTC, now = Tue 03:00 UTC → 1 (Mon 22:00 roll crossed)
//   - prev = Fri 18:00 UTC, now = Sat 12:00 UTC → 1 (Fri 22:00 roll crossed,
//     even though Sat itself is not a trading day — we count session ENDS)
//   - prev = Sat 12:00 UTC, now = Sun 23:00 UTC → 0 (no session close
//     happened over the weekend; Sun 22:00 is a re-OPEN, not a close)
//   - prev = Fri 18:00 UTC, now = Mon 23:00 UTC → 2 (Fri 22:00 + Mon 22:00)
//
// Holidays where COMEX is closed are NOT counted as trading-day boundaries,
// even if the 22:00 UTC clock tick passes.
func TradingDaysSince(prev, now time.Time) int {
	if !now.After(prev) {
		return 0
	}
	count := 0
	// Walk the daily 22:00 UTC roll cursor from the first roll after prev
	// up to (and including) the last roll at-or-before now.
	cursor := nextDailyRoll(prev.UTC())
	end := now.UTC()
	for !cursor.After(end) {
		// A roll at cursor closes the day that just ended. The day that
		// just ended runs from (cursor - 24h) to cursor. Skip if the day
		// being closed was Sat/Sun UTC (XAUUSD shut Fri 21 UTC → Sun 22 UTC)
		// or a US holiday observed by COMEX.
		dayEnding := cursor.Add(-12 * time.Hour) // any time within the day-being-closed
		wd := dayEnding.Weekday()
		if wd != time.Saturday && wd != time.Sunday && !isUSHoliday(dayEnding) {
			count++
		}
		cursor = cursor.Add(24 * time.Hour)
	}
	return count
}

// nextDailyRoll returns the next 22:00 UTC instant strictly after t.
func nextDailyRoll(t time.Time) time.Time {
	u := t.UTC()
	roll := time.Date(u.Year(), u.Month(), u.Day(), 22, 0, 0, 0, time.UTC)
	if !roll.After(u) {
		roll = roll.Add(24 * time.Hour)
	}
	return roll
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
