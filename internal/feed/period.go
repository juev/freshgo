package feed

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Period is an ISO 8601 duration such as P3M or PT12H, the form PHP's
// DateInterval reads and FreshRSS keeps in retention settings and accepts in
// date searches.
type Period struct {
	years, months, days int
	clock               time.Duration
}

var periodPattern = regexp.MustCompile(
	`^P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// ParsePeriod reads an ISO 8601 duration made of whole years, months, weeks,
// days, hours, minutes and seconds.
func ParsePeriod(s string) (Period, error) {
	m := periodPattern.FindStringSubmatch(s)
	if m == nil {
		return Period{}, fmt.Errorf("period %q is not an ISO 8601 duration", s)
	}
	var n [8]int
	parts := 0
	for i := 1; i < len(m); i++ {
		if m[i] == "" {
			continue
		}
		v, err := strconv.Atoi(m[i])
		if err != nil {
			return Period{}, fmt.Errorf("period %q: %w", s, err)
		}
		n[i] = v
		parts++
	}
	if parts == 0 {
		return Period{}, fmt.Errorf("period %q is empty", s)
	}
	return Period{
		years: n[1], months: n[2], days: 7*n[3] + n[4],
		clock: time.Duration(n[5])*time.Hour + time.Duration(n[6])*time.Minute + time.Duration(n[7])*time.Second,
	}, nil
}

// Before returns the moment the period before t. Years, months and days are
// counted by the calendar of t's time zone, as PHP does.
func (p Period) Before(t time.Time) time.Time {
	return t.AddDate(-p.years, -p.months, -p.days).Add(-p.clock)
}

// After returns the moment the period after t, counted as in Before.
func (p Period) After(t time.Time) time.Time {
	return t.AddDate(p.years, p.months, p.days).Add(p.clock)
}
