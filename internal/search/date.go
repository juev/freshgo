package search

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/feed"
)

// Dates of a query are ISO 8601 intervals, read as parseDateInterval of
// FreshRSS (lib/lib_date.php) reads them: "2014-03", "2014-02/2014-04",
// "2014-02/04", "2014-03/", "/2014-03", "2014-03/P1W", "P1W".

// moment is a point in time that may be absent or unreadable; in both cases
// it bounds nothing.
type moment struct {
	unix int64
	ok   bool
}

var (
	zoneSuffix        = regexp.MustCompile(`([+-]\d{2}:?\d{2}|Z)$`)
	compactZoneSuffix = regexp.MustCompile(`([+-]\d{4}|Z)$`)
	compactTime       = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})(?:(Z)|([+-])(\d{2})(\d{2}))?$`)
)

// interval reads a date interval into its bounds.
func (p *parser) interval(input string) span {
	input = strings.Trim(input, phpSpace)
	input = strings.NewReplacer("--", "/", " ", "T").Replace(input)
	input = strings.ToUpper(input)
	first, second, hasSecond := strings.Cut(input, "/")
	d1 := noDelimit(first)
	d2 := ""
	if hasSecond {
		d2 = relativeDate(d1, second)
	} else if d1 != "" && d1[0] != 'P' {
		d2 = d1
	}

	var low, high moment
	if d1 != "" && d1[0] != 'P' {
		low = p.time(floorDate(d1))
	}
	if d2 != "" {
		switch {
		case d2[0] == 'P':
			if period, err := feed.ParsePeriod(d2); err == nil {
				from := p.opts.Now
				if low.ok {
					from = time.Unix(low.unix, 0).In(p.opts.Now.Location())
				}
				high = moment{period.After(from).Unix() - 1, true}
			}
		case d1 == "" || d1[0] != 'P':
			high = p.time(ceilDate(d2))
		default:
			high = p.time(d2)
		}
	}
	if d1 != "" && d1[0] == 'P' {
		low = moment{}
		if period, err := feed.ParsePeriod(d1); err == nil {
			until := p.opts.Now
			if high.ok {
				until = time.Unix(high.unix, 0).In(p.opts.Now.Location())
			}
			low = moment{period.Before(until).Unix() + 1, true}
		}
	}
	var s span
	if low.ok {
		s.min = low.unix
	}
	if high.ok {
		s.max = high.unix
	}
	return s
}

// noDelimit removes the hyphens and colons of a date, keeping its zone.
func noDelimit(date string) string {
	zone := zoneSuffix.FindString(date)
	date = strings.TrimSuffix(date, zone)
	return strings.NewReplacer("-", "", ":", "").Replace(date) + strings.ReplaceAll(zone, ":", "")
}

// relativeDate completes the second date of an interval from the first:
// "05" after "20140203" is "20140205".
func relativeDate(d1, d2 string) string {
	if d2 != "" && d2[0] != 'P' && d1 != "" && d1[0] != 'P' {
		if year := d2[:min(4, len(d2))]; len(year) < 4 || !digits(year) {
			d2 = noDelimit(d2)
			if len(d2) > len(d1) {
				return d2
			}
			return d1[:len(d1)-len(d2)] + d2
		}
	}
	return noDelimit(d2)
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// padRight is PHP's str_pad: s filled up to the length with the repeated pad.
func padRight(s string, length int, pad string) string {
	for i := 0; len(s) < length; i++ {
		s += pad[i%len(pad) : i%len(pad)+1]
	}
	return s
}

// splitDate cuts a compact date into its day, its time of day and its zone.
func splitDate(date string) (day, clock, zone string) {
	day, clock, _ = strings.Cut(date, "T")
	zone = compactZoneSuffix.FindString(clock)
	return day, strings.TrimSuffix(clock, zone), zone
}

// floorDate fills a partial date with its earliest moment.
func floorDate(date string) string {
	day, clock, zone := splitDate(date)
	return padRight(day, 8, "01") + "T" + padRight(clock, 6, "0") + zone
}

// ceilDate fills a partial date with its latest moment.
func ceilDate(date string) string {
	day, clock, zone := splitDate(date)
	if len(clock) > 1 {
		clock = padRight(clock, 6, "59")
	} else {
		clock = "235959"
	}
	switch len(day) {
	case 4:
		day += "1231"
	case 6:
		if digits(day) {
			year, _ := strconv.Atoi(day[:4])
			month, _ := strconv.Atoi(day[4:])
			if month >= 1 && month <= 12 {
				day += strconv.Itoa(time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day())
			}
		}
	}
	return day + "T" + clock + zone
}

// time reads a compact date, YYYYMMDDTHHMMSS with an optional zone, the way
// PHP's strtotime does: a day past the end of its month runs into the next.
func (p *parser) time(date string) moment {
	m := compactTime.FindStringSubmatch(date)
	if m == nil {
		return moment{}
	}
	var n [7]int
	for i := 1; i <= 6; i++ {
		n[i], _ = strconv.Atoi(m[i])
	}
	if n[2] < 1 || n[2] > 12 || n[3] < 1 || n[3] > 31 || n[4] > 24 || n[5] > 59 || n[6] > 59 {
		return moment{}
	}
	location := p.opts.Now.Location()
	offset := 0
	if m[7] != "" || m[8] != "" {
		location = time.UTC
		if m[8] != "" {
			hours, _ := strconv.Atoi(m[9])
			minutes, _ := strconv.Atoi(m[10])
			offset = hours*3600 + minutes*60
			if m[8] == "-" {
				offset = -offset
			}
		}
	}
	return moment{time.Date(n[1], time.Month(n[2]), n[3], n[4], n[5], n[6], 0, location).Unix() - int64(offset), true}
}
