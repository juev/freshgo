package scrape

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The expected values are what PHP DateTime::createFromFormat returns in
// the freshrss/freshrss:1.30.1 image with the time zone UTC; -1 stands for
// false.
func TestParsePHPDate(t *testing.T) {
	tests := []struct {
		format, value string
		want          int64
	}{
		{`Y-m-d H:i:s`, "2026-09-01 11:00:00", 1788260400},
		{`Y-m-d H:i:s`, "2026-9-1 11:00:00", 1788260400},
		{`d.m.Y H:i`, "01.09.2026 10:30", 1788258600},
		{`d.m.Y H:i`, "1.9.2026 10:30", 1788258600},
		{`D, d M Y H:i:s O`, "Tue, 01 Sep 2026 12:00:00 +0200", 1788256800},
		{`Y-m-d\TH:i:sP`, "2026-09-01T12:00:00+02:00", 1788256800},
		{`Y-m-d\TH:i:sP`, "2026-09-01T12:00:00Z", 1788264000},
		{`Y-m-d\TH:i:s.vP`, "2026-09-01T13:00:00.123Z", 1788267600},
		{`Y-m-d\TH:i:s.vP`, "2026-09-01T13:00:00Z", -1},
		{`U`, "1788343200", 1788343200},
		{`U`, "-5", -5},
		{`U`, "abc", -1},
		{`YmdHis`, "20260901120000", 1788264000},
		{`g:i A, M j Y`, "3:05 PM, Sep 1 2026", 1788275100},
		{`h:i a d-m-Y`, "03:05 pm 01-09-2026", 1788275100},
		{`Y-m-d`, "2026-09-01 extra", -1},
		{`!Y-m-d`, "2026-09-01", 1788220800},
		{`Y-m-d|`, "2026-09-01", 1788220800},
		{`Y-m-d H:i:s.u`, "2026-09-01 12:00:00.123456", 1788264000},
		{`Y-m-d G:i`, "2026-09-01 9:05", 1788253500},
		{`c`, "2026-09-01T12:00:00+02:00", -1},
		{`r`, "Tue, 01 Sep 2026 12:00:00 +0200", -1},

		// PHP fills the time from the clock; here it is midnight.
		{`d.m.Y`, "01.09.2026", 1788220800},
		{`Ymd`, "20260901", 1788220800},
		{`m/d/y`, "09/01/26", 1788220800},
		{`j F Y`, "1 September 2026", 1788220800},
		{`l, d F Y`, "Tuesday, 01 September 2026", 1788220800},
		// PHP rolls these over into the next month.
		{`Y-m-d`, "2026-13-01", -1},
		{`Y-m-d`, "2026-02-30", -1},
		// Formats left to reading without a format.
		{`Y-m-d H:i:s T`, "2026-09-01 12:00:00 EST", -1},
		{`Y-m-d H:i:s e`, "2026-09-01 12:00:00 Europe/Moscow", -1},
		{`Y-m-d+`, "2026-09-01 trailing", -1},
		{`Y z`, "2026 100", -1},
	}
	for _, tt := range tests {
		got, ok := parsePHPDate(tt.format, tt.value, time.UTC)
		switch {
		case tt.want == -1 && ok:
			t.Errorf("parsePHPDate(%q, %q) = %d, want no date", tt.format, tt.value, got.Unix())
		case tt.want != -1 && (!ok || got.Unix() != tt.want):
			t.Errorf("parsePHPDate(%q, %q) = %d, %v; want %d", tt.format, tt.value, got.Unix(), ok, tt.want)
		}
	}
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip(err)
	}
	if got, ok := parsePHPDate(`Y-m-d H:i:s`, "2026-09-01 11:00:00", moscow); !ok || got.Unix() != 1788260400-3*3600 {
		t.Errorf("a date without a zone in Moscow: %d, %v", got.Unix(), ok)
	}
	if got, ok := parsePHPDate(`Y-m-d H:i:sP`, "2026-09-01 11:00:00+00:00", moscow); !ok || got.Unix() != 1788260400 {
		t.Errorf("a date with a zone in Moscow: %d, %v", got.Unix(), ok)
	}
}

// A date the format does not fit is read without it; a date that cannot be
// read at all is no date.
func TestTimestamp(t *testing.T) {
	for _, tt := range []struct{ value, format, want string }{
		{"2026-09-01 11:00:00", `Y-m-d H:i:s`, "2026-09-01T11:00:00Z"},
		{"1788343200", `U`, "2026-09-02T10:00:00Z"},
		{"1788343200123", `U`, "2026-09-02T10:00:00Z"},
		{"2026-09-01T10:00:00+02:00", `U`, "2026-09-01T08:00:00Z"},
		{"2026-09-01 12:00:00 EST", `Y-m-d H:i:s T`, "2026-09-01T17:00:00Z"},
		{"Tue, 01 Sep 2026 12:00:00 +0200", ``, "2026-09-01T10:00:00Z"},
		{"2026-09-01T13:00:00Z", `Y-m-d\TH:i:s.vP`, "2026-09-01T13:00:00Z"},
		{"not a date", `d.m.Y H:i`, ""},
		{"", ``, ""},
		{"0", `U`, ""},
		{"1", `U`, ""},
		{"2", `U`, "1970-01-01T00:00:02Z"},
	} {
		if got := timestamp(tt.value, tt.format, time.UTC); got != tt.want {
			t.Errorf("timestamp(%q, %q) = %q, want %q", tt.value, tt.format, got, tt.want)
		}
	}
}

func TestRSSErrors(t *testing.T) {
	const page = `<html><body><article><h2>T</h2></article></body></html>`
	tests := []struct {
		name string
		body string
		src  Source
		want error
	}{
		{"attributes are not an object", page, Source{Kind: KindHTMLXPath, Attributes: []byte(`[1]`)}, ErrSettings},
		{"kind that is not scraped", page, Source{Kind: 0}, ErrSettings},
		{"no item path", page, Source{Kind: KindHTMLXPath, Attributes: []byte(`{"xpath":{"itemTitle":"h2"}}`)}, ErrSettings},
		{"no attributes", page, Source{Kind: KindHTMLXPath}, ErrSettings},
		{"XPath that does not compile", page, Source{Kind: KindHTMLXPath, Attributes: []byte(`{"xpath":{"item":"//article["}}`)}, ErrSettings},
		{"field XPath that does not compile", page, Source{Kind: KindHTMLXPath, Attributes: []byte(`{"xpath":{"item":"//article","itemTitle":"h2[["}}`)}, ErrSettings},
		{"no items", page, Source{Kind: KindHTMLXPath, Attributes: []byte(`{"xpath":{"item":"//section"}}`)}, ErrNoItems},
		{"XML that is not XML", `<a><b></a>`, Source{Kind: KindXMLXPath, Attributes: []byte(`{"xpath":{"item":"//b"}}`)}, ErrDocument},
		{"JSON that is not JSON", `{"items":`, Source{Kind: KindJSONFeed}, ErrDocument},
		{"JSON with a tail", `{"items":[]} x`, Source{Kind: KindJSONFeed}, ErrDocument},
		{"JSON scalar", `"text"`, Source{Kind: KindJSONFeed}, ErrDocument},
		{"JSON Feed without items", `{"title":"T"}`, Source{Kind: KindJSONFeed}, ErrNoItems},
		{"JSON Feed with no items", `{"items":[]}`, Source{Kind: KindJSONFeed}, ErrNoItems},
		{"no path to items", `{"a":[]}`, Source{Kind: KindJSONDotNotation, Attributes: []byte(`{"json_dotnotation":{"itemUri":"u"}}`)}, ErrSettings},
		{"no XPath to the JSON", page, Source{Kind: KindHTMLXPathJSON, Attributes: []byte(`{"json_dotnotation":{"item":"a"}}`)}, ErrSettings},
		{"XPath finds no JSON", page, Source{Kind: KindHTMLXPathJSON, Attributes: []byte(`{"xPathToJson":"//script","json_dotnotation":{"item":"a"}}`)}, ErrNoItems},
		{"XPath finds text that is not JSON", page, Source{Kind: KindHTMLXPathJSON, Attributes: []byte(`{"xPathToJson":"string(//h2)","json_dotnotation":{"item":"a"}}`)}, ErrDocument},
	}
	for _, tt := range tests {
		if _, err := RSS([]byte(tt.body), tt.src); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

// Items nested in one another, each with itself for content, would turn a
// small page into gigabytes.
func TestRSSBoundsNestedItems(t *testing.T) {
	settings := []byte(`{"xpath":{"item":"//r","itemTitle":"@n","itemContent":"."}}`)
	nested := strings.Repeat(`<r n="x">`, 8000) + strings.Repeat(`</r>`, 8000)
	for name, src := range map[string]Source{
		"XML":  {Kind: KindXMLXPath, Attributes: settings},
		"HTML": {Kind: KindHTMLXPath, Attributes: []byte(`{"xpath":{"item":"//div","itemTitle":"@n","itemContent":"."}}`)},
	} {
		body := nested
		if name == "HTML" {
			// The HTML parser stops at 512 levels: many blocks just under it.
			body = strings.Repeat(strings.Repeat(`<div n="x">`, 500)+strings.Repeat(`</div>`, 500), 200)
		}
		doc, err := RSS([]byte(body), src)
		if !errors.Is(err, ErrDocument) {
			t.Errorf("%s: %d bytes in, %d bytes out, error %v; want ErrDocument", name, len(body), len(doc), err)
		}
	}
	// A page of ordinary shape is not affected: 2000 flat items of 1 KB.
	flat := "<d>" + strings.Repeat(`<r n="x">`+strings.Repeat("y", 1000)+`</r>`, 2000) + "</d>"
	if _, err := RSS([]byte(flat), Source{Kind: KindXMLXPath, Attributes: settings}); err != nil {
		t.Errorf("flat page: %v", err)
	}
}

// Whatever the XPath library makes of an expression, the result is an
// error or a document: a panic inside it does not get out.
func TestXPathNeverPanics(t *testing.T) {
	const page = `<html><body><article id="a" lang="en"><h2>T</h2><p>x</p></article></body></html>`
	for _, expression := range []string{
		`id('a')`, `//*[lang('en')]`, `//namespace::*`, `//article/namespace::*`, `//@*/..`,
		`//article[position() = last()]`, `//article[1][2][3]`, `(//article)[0]`, `//article[-1]`,
		`//article | //h2`, `//article/following::*/preceding::*`, `//article[substring(., 0, -1)]`,
		`//article[number('x') div 0]`, `//article[concat()]`, `//article[sum(.)]`, `//article[true() and]`,
		`//*[name() = 'article'][string-length() > 99999999999999999999]`, `//article/../../../..`,
		`/`, `.`, `..`, `*`, `@*`, `//text()[1]/text()`, `//comment()`, `//processing-instruction()`,
		`//article[translate(., 'abc', '')]`, `substring-before(//h2, '')`, `//article[contains(., //p)]`,
		`//article[starts-with(@id, 1)]`, `round(//h2)`, `//article[floor(@id) = ceiling(@id)]`,
		`//article[not()]`, `//article[boolean()]`, `count()`, `1 div 0`, `-(-(//h2))`, `//article[1 to 2]`,
		`''`, `"`, `)`, `//`, `///`, `//article[`, `//article]`, `$var`, `//article[@id=$x]`,
	} {
		for _, key := range []string{"item", "itemTitle", "itemContent", "itemCategories", "itemUri", "feedTitle"} {
			settings := map[string]string{"item": "//article", "itemTitle": "h2"}
			settings[key] = expression
			for _, kind := range []int{KindHTMLXPath, KindXMLXPath} {
				// The call returning at all is the check.
				_, _ = scrapeXPath([]byte(page), Source{Kind: kind}, settings)
			}
		}
		_, _ = extractJSON([]byte(page), Source{}, expression)
	}
}

func TestLookup(t *testing.T) {
	root, err := decodeJSON([]byte(`{"a":{"b":"nested","list":[10,20,{"x":"deep"}]},"a.b":"dotted","n":5,"f":1.50,"t":true,"z":null,"e":"","01":"zero-one","big":12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"a.b":               "dotted", // a key that exists as written wins
		"a.list.1":          "20",
		"a.list[1]":         "20",
		"a.list[2].x":       "deep",
		"a.list.2.x":        "deep",
		"a.list.01":         "",
		"a.list.3":          "",
		"a.list.-1":         "",
		"a.missing":         "",
		"a":                 "", // not a scalar
		"n":                 "5",
		"f":                 "1.5",
		"t":                 "1",
		"z":                 "",
		"e":                 "",
		"01":                "zero-one",
		"big":               "1.2345678901235E+19",
		"'literal'":         "literal",
		`"double"`:          "double",
		"'a' & n & 'b'":     "a5b",
		"'x & y' & n":       "x & y5",
		"n & missing & t":   "51",
		" n ":               "5",
		"a.list[0] & a.b":   "10dotted",
		"'unterminated & n": "5",
	} {
		if got := text(root, path); got != want {
			t.Errorf("text(%q) = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"", ".", "$", " . "} {
		if got, ok := lookup(root, path); !ok || got != root {
			t.Errorf("lookup(%q) is not the document itself", path)
		}
	}
	if _, ok := lookup("scalar", "a"); ok {
		t.Error("a path was found inside a scalar")
	}
}

// The members of a JSON object keep the order of the document.
func TestDecodeJSONKeepsOrder(t *testing.T) {
	root, err := decodeJSON([]byte(`{"zeta":1,"alpha":2,"mid":3,"zeta":4}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range elements(root) {
		s, _ := scalar(v)
		got = append(got, s)
	}
	if strings.Join(got, ",") != "4,2,3" {
		t.Errorf("values in order: %v", got)
	}
}

func TestSplitAuthors(t *testing.T) {
	for in, want := range map[string]string{
		"One":             "One",
		"Smith, John":     "Smith|John",
		"A; B":            "A|B",
		"Ann & Bob":       "Ann & Bob",
		"Ann & Bob, Cy":   "Ann & Bob, Cy", // the encoded & brings the semicolon that decides
		"Ann & Bob; Carl": "Ann & Bob|Carl",
		"":                "",
		" ; ;":            "",
	} {
		if got := strings.Join(splitAuthors(in), "|"); got != want {
			t.Errorf("splitAuthors(%q) = %q, want %q", in, got, want)
		}
	}
}
