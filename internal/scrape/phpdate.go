package scrape

import (
	"strconv"
	"strings"
	"time"
)

// Format characters of PHP DateTime::createFromFormat and the Go layout
// element each of them stands for.
var phpLayout = map[byte]string{
	'd': "2", 'j': "2", 'D': "Mon", 'l': "Monday",
	'm': "1", 'n': "1", 'M': "Jan", 'F': "January",
	'Y': "2006", 'y': "06",
	'H': "15", 'G': "15", 'h': "3", 'g': "3", 'i': "04", 's': "05",
	'A': "PM", 'a': "pm",
	'v': "000", 'u': "000000",
	'O': "-0700", 'P': "Z07:00", 'p': "Z07:00",
}

// parsePHPDate reads value by a PHP date format. It reports false for a
// value that does not fit the format and for a format it cannot express
// (zone names, day of the year, trailing data); the caller then reads the
// value without the format. A date without a zone is in the given location.
//
// Two things differ from PHP on purpose. Fields the format does not name
// are zero, where PHP takes them from the current time unless the format
// has "!" or "|": a date-only format gives midnight, not the time of the
// refresh. And a date that does not exist (month 13, 30 February) is not a
// date, where PHP rolls it over.
func parsePHPDate(format, value string, location *time.Location) (time.Time, bool) {
	if format == "U" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0).UTC(), true
	}
	var layout strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch {
		case c == '\\' && i+1 < len(format):
			i++
			layout.WriteByte(format[i])
		case c == '!' || c == '|':
			// Reset markers: fields that are not parsed are zero here anyway.
		case phpLayout[c] != "":
			layout.WriteString(phpLayout[c])
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '+' || c == '*' || c == '?':
			return time.Time{}, false
		default:
			layout.WriteByte(c)
		}
	}
	t, err := time.ParseInLocation(layout.String(), value, location)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
