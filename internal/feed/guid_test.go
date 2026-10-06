package feed

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHTTPSDomains(t *testing.T) {
	h := NewHTTPSDomains([]string{"example.net", " www.example.org # with a comment", "; a comment", "", "biz", "deep.sub.example.com/path"})
	for u, want := range map[string]string{
		// From the list FreshRSS ships.
		"http://github.com/x":           "https://github.com/x",
		"http://gist.github.com/x":      "https://gist.github.com/x",
		"HTTP://github.com/x":           "HTTPs://github.com/x",
		"http://github.com":             "https://github.com",
		"http://github.com:8080/x":      "https://github.com:8080/x",
		"http://user:pw@github.com/x":   "https://user:pw@github.com/x",
		"http://github.com?x=1":         "https://github.com?x=1",
		"http://notgithub.com/x":        "http://notgithub.com/x",
		"http://github.com.evil.test/x": "http://github.com.evil.test/x",
		"http://GitHub.com/x":           "http://GitHub.com/x",
		"https://github.com/x":          "https://github.com/x",
		"ftp://github.com/x":            "ftp://github.com/x",
		"github.com/x":                  "github.com/x",
		"http://":                       "http://",
		"http://com/":                   "http://com/",
		"tag:github.com,2026:x":         "tag:github.com,2026:x",
		"http://github.com./x":          "https://github.com./x",
		// From the installation's own list.
		"http://example.net/a":           "https://example.net/a",
		"http://a.b.example.net/a":       "https://a.b.example.net/a",
		"http://www.example.org/a":       "https://www.example.org/a",
		"http://example.org/a":           "http://example.org/a",
		"http://other.example.org/a":     "http://other.example.org/a",
		"http://anything.biz/a":          "https://anything.biz/a",
		"http://deep.sub.example.com/a":  "https://deep.sub.example.com/a",
		"http://x.deep.sub.example.com/": "https://x.deep.sub.example.com/",
		"http://sub.example.com/a":       "http://sub.example.com/a",
	} {
		if got := h.URL(u); got != want {
			t.Errorf("URL(%q) = %q, want %q", u, got, want)
		}
	}
	if got := (&HTTPSDomains{}).URL("http://github.com/x"); got != "http://github.com/x" {
		t.Errorf("empty list rewrote the URL to %q", got)
	}
}

// A listed domain covers what is listed below it, whatever the order.
func TestHTTPSDomainsNesting(t *testing.T) {
	for _, list := range [][]string{{"example.com", "www.example.com"}, {"www.example.com", "example.com"}} {
		h := &HTTPSDomains{}
		for _, d := range list {
			h.add(d)
		}
		for _, host := range []string{"example.com", "www.example.com", "a.example.com", "a.www.example.com"} {
			if !h.contains(host) {
				t.Errorf("list %v does not cover %s", list, host)
			}
		}
	}
}

func TestSafeASCII(t *testing.T) {
	for in, want := range map[string]string{
		"plain":             "plain",
		"a\tb\nc\x00d\x1fe": "abcde",
		"имя=значение":      "=",
		"café":              "caf",
		"keeps \x7f and ~":  "keeps \x7f and ~",
		"id с пробелами":    "id  ",
		"":                  "",
	} {
		if got := safeASCII(in); got != want {
			t.Errorf("safeASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGUIDCriteria(t *testing.T) {
	it := &Item{id: "the-id", permalink: "http://x/?a=1&amp;b=2", date: "1700000000", title: "T &amp; t", content: "<p>c</p>"}
	for criteria, want := range map[string]string{
		"":                                  "the-id",
		"unknown":                           "the-id",
		"link":                              "http://x/?a=1&amp;b=2",
		"sha1:link_published":               sha1Hex("http://x/?a=1&amp;b=2", "1700000000"),
		"sha1:link_published_title":         sha1Hex("http://x/?a=1&amp;b=2", "1700000000", "T &amp; t"),
		"sha1:link_published_title_content": sha1Hex("http://x/?a=1&amp;b=2", "1700000000", "T &amp; t", "<p>c</p>"),
		"sha1:title":                        sha1Hex("T &amp; t"),
		"sha1:title_published":              sha1Hex("T &amp; t", "1700000000"),
		"sha1:title_published_content":      sha1Hex("T &amp; t", "1700000000", "<p>c</p>"),
		"sha1:content":                      sha1Hex("<p>c</p>"),
		"sha1:content_published":            sha1Hex("<p>c</p>", "1700000000"),
		"sha1:published":                    sha1Hex("1700000000"),
	} {
		if got := it.guid(criteria); got != want {
			t.Errorf("criteria %q: %q, want %q", criteria, got, want)
		}
	}
	if got := sha1Hex("http://x/", "1"); got != "0e6c5a3e3ea4c2e1ae2a0a7f1b2aab4dd0dc5d0b" && len(got) != 40 {
		t.Errorf("sha1Hex is not a hex SHA-1: %q", got)
	}
}

// A criteria that gives nothing falls back to what the item has.
func TestGUIDFallback(t *testing.T) {
	tests := []struct {
		name     string
		item     Item
		criteria string
		want     string
	}{
		{"empty link, id present", Item{id: "id"}, "link", "id"},
		{"no title, id present", Item{id: "id"}, "sha1:title", "id"},
		{"no id: link and date", Item{permalink: "l", date: "1"}, "", sha1Hex("l", "1")},
		{"no id, no date: link", Item{permalink: "l"}, "", sha1Hex("l")},
		{"no id, no link: title", Item{title: "t", date: "1"}, "", sha1Hex("1", "t")},
		{"only content", Item{content: "c"}, "", sha1Hex("c")},
		{"nothing at all", Item{}, "", ""},
		{"nothing at all, hashed criteria", Item{}, "sha1:link_published", ""},
		{"non-ASCII id", Item{id: "ид-1"}, "", "-1"},
		{"id of non-ASCII only falls back", Item{id: "ид", permalink: "l"}, "", sha1Hex("l")},
	}
	for _, tt := range tests {
		if got := tt.item.guid(tt.criteria); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	long := Item{id: strings.Repeat("x", 1000)}
	if got := long.guid(""); len(got) != 767 {
		t.Errorf("a long id gives a GUID of %d bytes, want 767", len(got))
	}
}

func items(n int, make func(i int) Item) *Feed {
	f := &Feed{}
	for i := range n {
		it := make(i)
		f.Items = append(f.Items, &it)
	}
	return f
}

func TestAssignGUIDsDegrades(t *testing.T) {
	// Twenty items, n of them sharing an id; links and dates are unique.
	withDuplicates := func(n int) *Feed {
		return items(20, func(i int) Item {
			id := fmt.Sprint("id-", i)
			if i < n {
				id = "same"
			}
			return Item{id: id, permalink: fmt.Sprint("http://x/", i), date: "1", title: fmt.Sprint("t", i)}
		})
	}
	tests := []struct {
		name        string
		feed        *Feed
		criteria    string
		forced      bool
		wantUsed    string
		wantInvalid int
	}{
		{"all unique", withDuplicates(0), "", false, "", 0},
		// round(0.05 * 20) = 1 bad GUID is tolerated.
		{"one repeat tolerated", withDuplicates(2), "", false, "", 1},
		{"two repeats degrade", withDuplicates(3), "", false, "sha1:link_published", 0},
		{"forced criteria stay", withDuplicates(3), "", true, "", 2},
		{"link degrades too", items(3, func(int) Item { return Item{id: "i", permalink: "l", date: "1", title: "t"} }), "link", false, "sha1:link_published_title", 2},
		{"two steps", items(3, func(i int) Item { return Item{permalink: "l", date: "1", title: fmt.Sprint("t", i)} }), "", false, "sha1:link_published_title", 0},
		{"other criteria never change", items(3, func(int) Item { return Item{title: "t"} }), "sha1:title", false, "sha1:title", 2},
		{"empty GUIDs count", items(3, func(i int) Item {
			if i == 0 {
				return Item{}
			}
			return Item{id: fmt.Sprint(i)}
		}), "sha1:title", true, "sha1:title", 1},
		{"no items", &Feed{}, "", false, "", 0},
	}
	for _, tt := range tests {
		used, invalid := tt.feed.AssignGUIDs(tt.criteria, tt.forced)
		if used != tt.wantUsed || invalid != tt.wantInvalid {
			t.Errorf("%s: criteria %q with %d invalid, want %q with %d", tt.name, used, invalid, tt.wantUsed, tt.wantInvalid)
		}
		for _, it := range tt.feed.Items {
			if it.GUID != it.guid(used) {
				t.Errorf("%s: GUID %q was not computed by the returned criteria", tt.name, it.GUID)
			}
		}
	}
}

// The expected values are what SimplePie's date parser returns for these
// strings (PHP in the freshrss/freshrss:1.30.1 image, time zone UTC).
func TestParseDate(t *testing.T) {
	for in, want := range map[string]int64{
		"2024-01-01T10:00:00Z":   1704103200,
		"2024-01-01t10:00:00z":   1704103200,
		"2024-01-01 10:00:00Z":   1704103200,
		"20240101T100000Z":       1704103200,
		"2024-01-01T10:00:00.4Z": 1704103200,
		// One trailing newline does not stop the strict pattern, as in PCRE.
		"2024-01-01T10:00:00.5Z\n":                1704103201,
		" 2024-01-01T10:00:00.5Z":                 1704103200,
		"2024-01-01T10:00:00.5Z":                  1704103201,
		"2024-01-01T10:00Z":                       1704103200,
		"2024-01-01T13:00:00+03:00":               1704103200,
		"2024-01-01T10:00:00+1:00":                1704099600,
		"2024-01-01":                              1704067200,
		"2024-01":                                 1704067200,
		"2024":                                    1704067200,
		"1 Jan 2024 10:00 UT":                     1704103200,
		"Mon, 01 Jan 24 10:00:00 GMT":             1704103200,
		"Mon, 01 Jan 2024 11:00:00 CET":           1704103200,
		"Mon, 01 Jan 2024 05:00:00 (c) -0500 (x)": 1704103200,
		"mon, 01 january 2024 10:00:00 gmt":       1704103200,
		"Mon, 01 Jan 2024 10:00:00 XYZ":           1704103200,
		"Mon, 01 Jan 2024 10:00:00 Z":             1704103200,
		"Mon, 01 Jan 2024 10:00 +0100":            1704099600,
		"Mon, 01 Jan 2024 24:00:00 GMT":           1704153600,
		"Lundi, 01 janvier 2024 10:00:00 GMT":     1704103200,
		"lun, 01 janvier 2024 10:00:00 GMT":       1704103200,
		"Пн, 01 Янв 2024 10:00:00 +0000":          1704103200,
		"Monday, 01-Jan-24 10:00:00 GMT":          1704103200,
		"Monday, 01-Jan-99 10:00:00 GMT":          915184800,
		"Mon Jan  1 10:00:00 2024":                1704103200,
		"2024-01-01 10:00:00":                     1704103200,
		"2024-01-01T10:00:00":                     1704103200,
		"2024-01-01 10:00:00 +0300":               1704092400,
		"2024/01/01 10:00:00":                     1704103200,
		"01/02/2024":                              1704153600,
		"January 1, 2024":                         1704067200,
		"Jan 1, 2024 10:00:00":                    1704103200,
		"1 January 2024":                          1704067200,
		"Mon, 1 Jan 2024 10:00:00":                1704103200,
		"@1704103200":                             1704103200,
		// Not dates for SimplePie either.
		"Ср, 10 янв 2024 10:00:00 +0300": -1,
		"1704103200":                     -1,
		"Mon, 32 Foo 2024":               -1,
		"":                               -1,
		// PHP reads relative dates; freshgo does not.
		"yesterday": -1,
	} {
		got, ok := parseDate(in, time.UTC)
		if want == -1 {
			if ok {
				t.Errorf("parseDate(%q) = %d, want no date", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("parseDate(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
}
