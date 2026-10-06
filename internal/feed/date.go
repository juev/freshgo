package feed

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date formats in the order SimplePie tries them (Parse\Date).
var (
	dateW3C = regexp.MustCompile(`^([0-9]{4})(?:-?([0-9]{2})(?:-?([0-9]{2})(?:[Tt\t ]+([0-9]{2})(?::?([0-9]{2})(?::?([0-9]{2})(?:.([0-9]*))?)?)?(?:(Z)|([+\-])([0-9]{1,2}):?([0-9]{1,2})))?)?)?$`)

	dayPattern   = "(?:" + alternation(dayNames) + ")"
	monthPattern = "(" + alternation(mapKeys(monthNames)) + ")"

	dateRFC2822 = regexp.MustCompile(`(?i)(?:[\t ]*` + dayPattern + `[\t ]*,)?[\t ]*([0-9]{1,2})[\t ]+` + monthPattern +
		`[\t ]+([0-9]{2,4})[\t ]+([0-9]{2})[\t ]*:[\t ]*([0-9]{2})(?:[\t ]*:[\t ]*([0-9]{2}))?[\t ]+(?:([+\-])([0-9]{2})([0-9]{2})|([A-Z]{1,5}))`)
	dateRFC850 = regexp.MustCompile(`(?i)^` + dayPattern + `,[\t ]+([0-9]{1,2})-` + monthPattern +
		`-([0-9]{2})[\t ]+([0-9]{2}):([0-9]{2}):([0-9]{2})[\t ]+([A-Z]{1,5})$`)
	dateAsctime = regexp.MustCompile(`(?i)^` + dayPattern + `[\t ]+` + monthPattern +
		`[\t ]+([0-9]{1,2})[\t ]+([0-9]{2}):([0-9]{2}):([0-9]{2})[\t ]+([0-9]{4})\n?$`)
)

// What PHP strtotime reads and the patterns above do not is approximated by
// these layouts: a date, optionally a time, optionally a zone.
var (
	looseDates = []string{
		"Mon, 2 Jan 2006", "Mon 2 Jan 2006", "Monday, 2 January 2006", "Monday, January 2, 2006",
		"Mon, Jan 2, 2006", "Mon Jan 2 2006", "2 Jan 2006", "2 January 2006",
		"January 2, 2006", "Jan 2, 2006", "January 2 2006", "Jan 2 2006",
		"2006-01-02", "2006/01/02", "2006/1/2", "01/02/2006", "1/2/2006",
		"02.01.2006", "2.1.2006", "02-01-2006",
	}
	looseTimes = []string{
		"", " 15:04:05", " 15:04", "T15:04:05", "T15:04",
		" 3:04:05 PM", " 3:04 PM", " 3:04:05 pm", " 3:04 pm", " 3:04PM", " 3:04pm",
	}
	// A zone at the end of a date that has a time: an offset, alone or
	// after GMT or UTC, or an abbreviation.
	looseOffset = regexp.MustCompile(`(?i)\s*(?:GMT|UTC)?([+\-])([0-9]{1,2})(?::?([0-9]{2}))?$`)
	looseZone   = regexp.MustCompile(`\s([A-Za-z]{1,5})$`)
)

func alternation(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return strings.Join(quoted, "|")
}

func mapKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// asciiLower lowers ASCII letters only, which is how the month table is keyed.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// unix is PHP gmmktime: out-of-range fields roll over.
func unix(year, month, day, hour, minute, second int) int64 {
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC).Unix()
}

// parseDate returns the Unix time a feed date stands for.
func parseDate(date string, location *time.Location) (int64, bool) {
	if t, ok := parseW3C(date); ok {
		return t, true
	}
	if t, ok := parseNamed(date); ok {
		return t, true
	}
	return parseLoose(strings.TrimSpace(date), location)
}

func parseW3C(date string) (int64, bool) {
	if m := dateW3C.FindStringSubmatch(date); m != nil {
		month, day := 1, 1
		if m[2] != "" {
			month = atoi(m[2])
		}
		if m[3] != "" {
			day = atoi(m[3])
		}
		second := atoi(m[6])
		if fraction := m[7]; fraction != "" && fraction[0] >= '5' {
			second++
		}
		offset := atoi(m[10])*3600 + atoi(m[11])*60
		if m[9] == "-" {
			offset = -offset
		}
		return unix(atoi(m[1]), month, day, atoi(m[4]), atoi(m[5]), second) - int64(offset), true
	}
	return 0, false
}

// parseNamed reads the formats that spell the month: RFC 2822, RFC 850
// and asctime.
func parseNamed(date string) (int64, bool) {
	// The month patterns match letters outside ASCII in either case, the
	// table does not: such a date is one SimplePie does not recognize.
	if m := dateRFC2822.FindStringSubmatch(removeComments(date)); m != nil && monthNames[asciiLower(m[2])] != 0 {
		offset := 0
		if m[7] != "" {
			offset = atoi(m[8])*3600 + atoi(m[9])*60
			if m[7] == "-" {
				offset = -offset
			}
		} else {
			offset = zoneOffsets[strings.ToUpper(m[10])]
		}
		year := atoi(m[3])
		if year < 50 {
			year += 2000
		} else if year < 1000 {
			year += 1900
		}
		return unix(year, monthNames[asciiLower(m[2])], atoi(m[1]), atoi(m[4]), atoi(m[5]), atoi(m[6])) - int64(offset), true
	}
	if m := dateRFC850.FindStringSubmatch(date); m != nil && monthNames[asciiLower(m[2])] != 0 {
		year := atoi(m[3])
		if year < 50 {
			year += 2000
		} else {
			year += 1900
		}
		offset := zoneOffsets[strings.ToUpper(m[7])]
		return unix(year, monthNames[asciiLower(m[2])], atoi(m[1]), atoi(m[4]), atoi(m[5]), atoi(m[6])) - int64(offset), true
	}
	if m := dateAsctime.FindStringSubmatch(date); m != nil && monthNames[asciiLower(m[1])] != 0 {
		return unix(atoi(m[6]), monthNames[asciiLower(m[1])], atoi(m[2]), atoi(m[3]), atoi(m[4]), atoi(m[5])), true
	}
	return 0, false
}

// parseLoose reads the dates left to PHP strtotime. A date without a zone
// is in the given location.
func parseLoose(date string, location *time.Location) (int64, bool) {
	if seconds, ok := strings.CutPrefix(date, "@"); ok {
		t, err := strconv.ParseInt(seconds, 10, 64)
		return t, err == nil
	}
	// Surrounding whitespace and letters in lower case stop the strict pattern.
	if t, ok := parseW3C(strings.ToUpper(date)); ok {
		return t, true
	}
	date = strings.Replace(date, "Sept ", "Sep ", 1)
	offset, zoned := 0, false
	if strings.Contains(date, ":") {
		if m := looseOffset.FindStringSubmatch(date); m != nil {
			offset = atoi(m[2])*3600 + atoi(m[3])*60
			if m[1] == "-" {
				offset = -offset
			}
			date, zoned = strings.TrimSuffix(date, m[0]), true
		} else if m := looseZone.FindStringSubmatch(date); m != nil {
			name := strings.ToUpper(m[1])
			if seconds, ok := zoneOffsets[name]; ok || name == "Z" || name == "UTC" || name == "GMT" || name == "UT" {
				offset, zoned = seconds, true
				date = strings.TrimSuffix(date, m[0])
			}
		}
	}
	if zoned {
		location = time.UTC
	}
	for _, d := range looseDates {
		for _, clock := range looseTimes {
			if t, err := time.ParseInLocation(d+clock, date, location); err == nil {
				return t.Unix() - int64(offset), true
			}
		}
	}
	return 0, false
}

// removeComments drops the parenthesized comments RFC 2822 allows in a date.
func removeComments(s string) string {
	if !strings.Contains(s, "(") {
		return s
	}
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		escaped := i > 0 && s[i-1] == '\\'
		switch {
		case c == '(' && !escaped:
			depth++
		case c == ')' && !escaped && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}
