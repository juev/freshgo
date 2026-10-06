package feed

import (
	"testing"
	"time"
)

func TestPeriod(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, paris)
	cases := []struct {
		period        string
		before, after time.Time
	}{
		// 31 February and 31 June, as PHP counts them.
		{"P3M", time.Date(2026, 3, 3, 12, 0, 0, 0, paris), time.Date(2026, 8, 31, 12, 0, 0, 0, paris)},
		{"P1M", time.Date(2026, 5, 1, 12, 0, 0, 0, paris), time.Date(2026, 7, 1, 12, 0, 0, 0, paris)},
		{"P1Y", time.Date(2025, 5, 31, 12, 0, 0, 0, paris), time.Date(2027, 5, 31, 12, 0, 0, 0, paris)},
		{"P2W", time.Date(2026, 5, 17, 12, 0, 0, 0, paris), time.Date(2026, 6, 14, 12, 0, 0, 0, paris)},
		{"P10D", time.Date(2026, 5, 21, 12, 0, 0, 0, paris), time.Date(2026, 6, 10, 12, 0, 0, 0, paris)},
		{"PT36H", time.Date(2026, 5, 30, 0, 0, 0, 0, paris), time.Date(2026, 6, 2, 0, 0, 0, 0, paris)},
		{"P1Y2M3DT4H5M6S", time.Date(2025, 3, 28, 7, 54, 54, 0, paris), time.Date(2027, 8, 3, 16, 5, 6, 0, paris)},
	}
	for _, c := range cases {
		p, err := ParsePeriod(c.period)
		if err != nil {
			t.Errorf("ParsePeriod(%q): %v", c.period, err)
			continue
		}
		if got := p.Before(now); !got.Equal(c.before) {
			t.Errorf("%s before: %v, want %v", c.period, got, c.before)
		}
		if got := p.After(now); !got.Equal(c.after) {
			t.Errorf("%s after: %v, want %v", c.period, got, c.after)
		}
	}
	for _, period := range []string{"", "P", "PT", "3M", "P1.5M", "P-1D", "P1H"} {
		if got, err := ParsePeriod(period); err == nil {
			t.Errorf("ParsePeriod(%q) = %+v, want an error", period, got)
		}
	}
}
