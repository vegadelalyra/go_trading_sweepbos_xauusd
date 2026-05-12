package marketcal

import (
	"testing"
	"time"
)

func tUTC(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestIsXAUUSDTradingDay_WeekendBoundaries(t *testing.T) {
	cases := []struct {
		when string
		want bool
		desc string
	}{
		{"2026-05-08 20:00", true, "Friday before 21 UTC: open"},
		{"2026-05-08 21:00", false, "Friday at 21 UTC: closed (Fri close)"},
		{"2026-05-08 23:30", false, "Friday late: closed"},
		{"2026-05-09 12:00", false, "Saturday noon: closed"},
		{"2026-05-10 12:00", false, "Sunday noon: closed"},
		{"2026-05-10 21:59", false, "Sunday 21:59 UTC: still closed"},
		{"2026-05-10 22:00", true, "Sunday 22 UTC: reopen"},
		{"2026-05-11 09:00", true, "Monday morning: open"},
		{"2026-05-12 14:30", true, "Tuesday afternoon: open"},
	}
	for _, c := range cases {
		got := IsXAUUSDTradingDay(tUTC(c.when))
		if got != c.want {
			t.Errorf("IsXAUUSDTradingDay(%s) = %t, want %t — %s", c.when, got, c.want, c.desc)
		}
	}
}

func TestIsXAUUSDTradingDay_USHolidays2026(t *testing.T) {
	// COMEX gold futures holiday calendar for 2026:
	holidays := []struct {
		date string
		desc string
	}{
		{"2026-01-01", "New Year's Day (Thu)"},
		{"2026-01-19", "MLK Day (3rd Mon Jan)"},
		{"2026-02-16", "Presidents Day (3rd Mon Feb)"},
		{"2026-04-03", "Good Friday"},
		{"2026-05-25", "Memorial Day (last Mon May)"},
		{"2026-06-19", "Juneteenth (Fri)"},
		// 2026-07-04 is Saturday → observed Friday July 3
		{"2026-07-03", "Independence Day observed (Fri)"},
		{"2026-09-07", "Labor Day (1st Mon Sep)"},
		{"2026-11-26", "Thanksgiving (4th Thu Nov)"},
		{"2026-12-25", "Christmas (Fri)"},
	}
	for _, h := range holidays {
		when := tUTC(h.date + " 14:00") // mid-session check
		if IsXAUUSDTradingDay(when) {
			t.Errorf("IsXAUUSDTradingDay(%s) = true, want false — %s", h.date, h.desc)
		}
	}
}

func TestIsXAUUSDTradingDay_USHolidays2025(t *testing.T) {
	// 2025-01-01 = Wed; 2025-07-04 = Fri; 2025-12-25 = Thu (no observed shifts).
	holidays := []string{
		"2025-01-01", "2025-01-20", "2025-02-17", "2025-04-18", "2025-05-26",
		"2025-06-19", "2025-07-04", "2025-09-01", "2025-11-27", "2025-12-25",
	}
	for _, d := range holidays {
		if IsXAUUSDTradingDay(tUTC(d + " 14:00")) {
			t.Errorf("IsXAUUSDTradingDay(%s) = true, want false (2025 US holiday)", d)
		}
	}
}

func TestTradingDaysSince(t *testing.T) {
	cases := []struct {
		prev, now string
		want      int
		desc      string
	}{
		{"2026-05-11 18:00", "2026-05-12 03:00", 1, "Mon evening → Tue 03 UTC: crossed into Tue"},
		{"2026-05-11 23:06", "2026-05-12 13:40", 1, "Mon late evening → Tue mid-day: crossed UTC midnight (the auto-refresh trigger case)"},
		{"2026-05-11 18:00", "2026-05-11 23:00", 0, "Same UTC date (Mon → Mon): no new trading day"},
		{"2026-05-12 09:00", "2026-05-12 14:00", 0, "Same UTC date (Tue → Tue): no new trading day"},
		{"2026-05-08 18:00", "2026-05-09 12:00", 0, "Fri 18 → Sat noon: Sat is not a trading day"},
		{"2026-05-09 12:00", "2026-05-10 23:00", 0, "Sat noon → Sun 23: weekend transitions don't count"},
		{"2026-05-08 18:00", "2026-05-11 23:00", 1, "Fri 18 → Mon 23: weekend skipped, only Mon counts"},
		{"2026-05-11 23:00", "2026-05-11 23:00", 0, "Same instant"},
		{"2026-05-12 03:00", "2026-05-11 18:00", 0, "Reversed → 0"},
		// Across a Monday holiday (MLK 2026-01-19): Mon skipped, only
		// Tue counts.
		{"2026-01-16 18:00", "2026-01-20 03:00", 1, "Fri → post-MLK Tue: only Tue counts"},
		{"2026-01-16 18:00", "2026-01-21 03:00", 2, "Fri → post-MLK Wed: Tue + Wed"},
	}
	for _, c := range cases {
		got := TradingDaysSince(tUTC(c.prev), tUTC(c.now))
		if got != c.want {
			t.Errorf("TradingDaysSince(%s, %s) = %d, want %d — %s", c.prev, c.now, got, c.want, c.desc)
		}
	}
}

func TestNextMarketOpen(t *testing.T) {
	cases := []struct {
		when string
		want string
		desc string
	}{
		{"2026-05-08 21:00", "2026-05-10 22:00", "Friday close → Sunday 22 UTC"},
		{"2026-05-08 23:30", "2026-05-10 22:00", "Friday late → Sunday 22 UTC"},
		{"2026-05-09 00:00", "2026-05-10 22:00", "Saturday midnight → Sunday 22 UTC"},
		{"2026-05-09 18:30", "2026-05-10 22:00", "Saturday afternoon → Sunday 22 UTC"},
		{"2026-05-10 10:00", "2026-05-10 22:00", "Sunday morning → same Sunday 22 UTC"},
		{"2026-05-10 21:59", "2026-05-10 22:00", "Sunday 21:59 → 22 UTC same day"},
		// Already-open instant: defensive identity (callers normally guard
		// with !IsXAUUSDTradingDay first, but the helper must not loop).
		{"2026-05-12 14:30", "2026-05-12 14:30", "Already open → returned unchanged"},
		// MLK Monday 2026-01-19 called during the holiday: jump to Tue 00 UTC.
		{"2026-01-19 12:00", "2026-01-20 00:00", "MLK Monday → Tuesday 00 UTC"},
		// Independence Day observed on Friday 2026-07-03: jumps to Sun 22 UTC.
		{"2026-07-03 12:00", "2026-07-05 22:00", "Holiday-Friday → Sunday 22 UTC (skip Sat 00)"},
		// Christmas 2026 = Friday: caller invoked Fri 12:00 → Sun 22 UTC.
		{"2026-12-25 12:00", "2026-12-27 22:00", "Christmas Friday → Sunday 22 UTC"},
	}
	for _, c := range cases {
		got := NextMarketOpen(tUTC(c.when))
		want := tUTC(c.want)
		if !got.Equal(want) {
			t.Errorf("NextMarketOpen(%s) = %s, want %s — %s", c.when,
				got.Format("2006-01-02 15:04"), c.want, c.desc)
		}
	}
}

func TestNthWeekdayOfMonth(t *testing.T) {
	// 3rd Monday of January 2026 = Jan 19.
	got := nthWeekdayOfMonth(2026, time.January, time.Monday, 3)
	want := tUTC("2026-01-19 00:00")
	if !got.Equal(want) {
		t.Errorf("3rd Mon Jan 2026 = %s, want %s", got, want)
	}
}

func TestLastWeekdayOfMonth(t *testing.T) {
	// Last Monday of May 2026 = May 25.
	got := lastWeekdayOfMonth(2026, time.May, time.Monday)
	want := tUTC("2026-05-25 00:00")
	if !got.Equal(want) {
		t.Errorf("Last Mon May 2026 = %s, want %s", got, want)
	}
}

func TestGoodFriday(t *testing.T) {
	cases := map[int]string{
		2024: "2024-03-29",
		2025: "2025-04-18",
		2026: "2026-04-03",
		2027: "2027-03-26",
	}
	for y, want := range cases {
		got := goodFriday(y)
		if got.Format("2006-01-02") != want {
			t.Errorf("goodFriday(%d) = %s, want %s", y, got.Format("2006-01-02"), want)
		}
	}
}
