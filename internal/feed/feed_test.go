package feed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const referenceDir = "../../testdata/reference"

// oracleFeed is what FreshRSS makes of one feed document:
// testdata/reference/oracle/generate.sh.
type oracleFeed struct {
	Error           *string
	Title           *string
	Link            *string
	Self            []string
	Hub             []string
	UnicityCriteria *string
	InError         bool
	Entries         []struct {
		GUID       string
		Title      string
		Authors    []string
		Content    string
		Link       string
		Date       int64
		Tags       []string
		Attributes json.RawMessage
	}
}

func loadOracle(t *testing.T) map[string]oracleFeed {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(referenceDir, "oracle", "feeds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var feeds map[string]oracleFeed
	if err := json.Unmarshal(raw, &feeds); err != nil {
		t.Fatal(err)
	}
	return feeds
}

func readFeedFile(t *testing.T, name string) []byte {
	t.Helper()
	for _, dir := range []string{"corpus", "oracle/feeds"} {
		if data, err := os.ReadFile(filepath.Join(referenceDir, dir, name)); err == nil {
			return data
		}
	}
	t.Fatalf("feed %s not found", name)
	return nil
}

// The force-https list of the oracle run.
func oracleHTTPS() *HTTPSDomains {
	return NewHTTPSDomains([]string{"example.net"})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Every feed of the oracle parsed by freshgo gives what FreshRSS stores.
func TestParseAgainstFreshRSS(t *testing.T) {
	oracle := loadOracle(t)
	if len(oracle) < 13 {
		t.Fatalf("only %d feeds in the oracle", len(oracle))
	}
	for name, want := range oracle {
		t.Run(name, func(t *testing.T) {
			if want.Error != nil {
				t.Fatalf("FreshRSS could not read the feed: %s", *want.Error)
			}
			// The oracle parses raw documents: there is no feed address.
			f, err := Parse(readFeedFile(t, name), Options{HTTPS: oracleHTTPS()})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got, want := f.Title, decodeText(tags.ReplaceAllString(deref(want.Title), "")); got != want {
				t.Errorf("feed title %q, want %q", got, want)
			}
			if got, want := f.Link, decodeText(deref(want.Link)); got != want {
				t.Errorf("feed link %q, want %q", got, want)
			}
			if got, want := f.SelfURL, decodeText(first(want.Self)); got != want {
				t.Errorf("self link %q, want %q", got, want)
			}
			if got, want := f.HubURL, decodeText(first(want.Hub)); got != want {
				t.Errorf("hub link %q, want %q", got, want)
			}

			criteria, invalid := f.AssignGUIDs(CriteriaID, false)
			if criteria != deref(want.UnicityCriteria) {
				t.Errorf("criteria %q, want %q", criteria, deref(want.UnicityCriteria))
			}
			if (invalid > 0) != want.InError {
				t.Errorf("%d invalid GUIDs, FreshRSS marks the feed failing: %v", invalid, want.InError)
			}
			if len(f.Items) != len(want.Entries) {
				t.Fatalf("%d items, want %d", len(f.Items), len(want.Entries))
			}
			for i, it := range f.Items {
				w := want.Entries[i]
				// The oracle reports the key before the database cuts it.
				if len(w.GUID) > maxGUIDLength {
					w.GUID = w.GUID[:maxGUIDLength]
				}
				if it.GUID != w.GUID {
					t.Errorf("item %d %q: GUID %q, want %q", i, w.Title, it.GUID, w.GUID)
				}
				// FreshRSS shows the GUID in place of a missing title.
				if it.Title != w.Title && (it.Title != "" || w.Title != w.GUID) {
					t.Errorf("item %d: title %q, want %q", i, it.Title, w.Title)
				}
				if it.Link != w.Link {
					t.Errorf("item %d %q: link %q, want %q", i, w.Title, it.Link, w.Link)
				}
				if it.Published != w.Date {
					t.Errorf("item %d %q: date %d, want %d", i, w.Title, it.Published, w.Date)
				}
				if !reflect.DeepEqual(it.Authors, w.Authors) && (len(it.Authors) > 0 || len(w.Authors) > 0) {
					t.Errorf("item %d %q: authors %q, want %q", i, w.Title, it.Authors, w.Authors)
				}
				if !reflect.DeepEqual(it.Tags, w.Tags) && (len(it.Tags) > 0 || len(w.Tags) > 0) {
					t.Errorf("item %d %q: tags %q, want %q", i, w.Title, it.Tags, w.Tags)
				}
				if !sameJSON(it.Attributes(), w.Attributes) {
					t.Errorf("item %d %q: attributes\n got %s\nwant %s", i, w.Title, it.Attributes(), w.Attributes)
				}
				if it.Content != w.Content {
					t.Errorf("item %d %q: content\n got %q\nwant %q", i, w.Title, it.Content, w.Content)
				}
			}
		})
	}
}

// SimplePie hands such content over as it came; here it is cleaned.
func TestParseSanitizesContentOfAnyType(t *testing.T) {
	const payload = `&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=alert(2)&gt;&lt;a href="javascript:alert(3)"&gt;x&lt;/a&gt;`
	entries := map[string]string{
		"text/html content":       `<content type="text/html">` + payload + `</content>`,
		"application/xml content": `<content type="application/xml">` + payload + `</content>`,
		"svg summary":             `<summary type="image/svg+xml">` + payload + `</summary>`,
		"unknown title type":      `<title type="text/html">` + payload + `</title><content>c</content>`,
		// The same payload, base64-encoded.
		"binary content": `<content type="application/octet-stream">PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0PjxpbWcgc3JjPXggb25lcnJvcj1hbGVydCgyKT48YSBocmVmPSJqYXZhc2NyaXB0OmFsZXJ0KDMpIj54PC9hPg==</content>`,
	}
	for name, entry := range entries {
		for _, ns := range []string{"http://www.w3.org/2005/Atom", "http://purl.org/atom/ns#"} {
			doc := `<feed xmlns="` + ns + `"><title>T</title><entry><id>1</id>` + entry + `</entry></feed>`
			f, err := Parse([]byte(doc), Options{})
			if err != nil || len(f.Items) != 1 {
				t.Fatalf("%s: %v", name, err)
			}
			it := f.Items[0]
			for _, text := range []string{it.Content, it.Title} {
				lower := strings.ToLower(text)
				if strings.Contains(lower, "<script") || strings.Contains(lower, "onerror") || strings.Contains(lower, `href="javascript:`) {
					t.Errorf("%s (%s): unsafe markup kept: %q", name, ns, text)
				}
			}
			// Atom 0.3 marks base64 with mode, not type: there the text stays text.
			binary03 := name == "binary content" && ns != "http://www.w3.org/2005/Atom"
			if strings.Contains(name, "content") && !binary03 && !strings.Contains(it.Content, "<img") {
				t.Errorf("%s (%s): content %q, want the markup cleaned, not dropped", name, ns, it.Content)
			}
		}
	}
}

// Parsing time grows with the size of the document, not with its square.
func TestParseLargeDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"many items":       `<rss><channel><title>T</title>` + strings.Repeat(`<item/>`, 200000) + `</channel></rss>`,
		"many text chunks": `<rss><channel><item><description>` + strings.Repeat(`xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx<b/>`, 200000) + `</description></item></channel></rss>`,
		"many authors and media": `<rss xmlns:media="http://search.yahoo.com/mrss/" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel>` +
			strings.Repeat(`<item><media:content url="http://e.test/a.png"/></item>`, 50000) + `</channel></rss>`,
	} {
		start := time.Now()
		if _, err := Parse([]byte(doc), Options{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: %d bytes took %v", name, len(doc), took)
		}
	}
}

// A date without a zone, in a format SimplePie leaves to PHP, is in the
// user's time zone.
func TestParseDatesInLocation(t *testing.T) {
	const doc = `<rss><channel><item><guid>a</guid><pubDate>2026-10-06 10:00:00</pubDate></item>
<item><guid>b</guid><pubDate>2026-10-06T10:00:00</pubDate></item>
<item><guid>c</guid><pubDate>Tue, 06 Oct 2026 10:00:00 GMT</pubDate></item></channel></rss>`
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip(err)
	}
	const utc = 1791280800
	for name, tt := range map[string]struct {
		location *time.Location
		want     [3]int64 // c, b, a: items come last first
	}{
		"UTC by default": {nil, [3]int64{utc, utc, utc}},
		// Without a zone even the ISO form is left to PHP, which reads it
		// in the user's zone.
		"Moscow": {moscow, [3]int64{utc, utc - 3*3600, utc - 3*3600}},
	} {
		f, err := Parse([]byte(doc), Options{Location: tt.location})
		if err != nil {
			t.Fatal(err)
		}
		for i, it := range f.Items {
			if it.Published != tt.want[i] {
				t.Errorf("%s: item %d: %d, want %d", name, i, it.Published, tt.want[i])
			}
		}
	}
}

func TestParseRejectsOtherDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"html":      `<html><head><title>x</title></head><body></body></html>`,
		"other xml": `<?xml version="1.0"?><catalog><record/></catalog>`,
		"empty":     ``,
	} {
		if _, err := Parse([]byte(doc), Options{}); err == nil {
			t.Errorf("%s: accepted as a feed", name)
		}
	}
	if _, err := Parse([]byte(`<rss><channel><item><title>unclosed`), Options{}); err == nil {
		t.Error("a truncated document was accepted")
	}
}

// FreshRSS refuses these; freshgo reads them.
func TestParseIsLenient(t *testing.T) {
	const item = `<rss version="2.0"><channel><title>T</title><item><title>One</title><guid>g1</guid></item></channel></rss>`
	for name, doc := range map[string]string{
		"whitespace before the declaration": "\n\n  <?xml version=\"1.0\"?>" + item,
		"text before the root":              "garbage " + item,
		"byte order mark":                   "\xEF\xBB\xBF" + item,
		"control characters":                strings.Replace(item, "One", "O\x01ne\x1F", 1),
		"no channel":                        `<rss version="2.0"></rss>`,
	} {
		f, err := Parse([]byte(doc), Options{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name != "no channel" && (len(f.Items) != 1 || f.Items[0].Title != "One") {
			t.Errorf("%s: items %+v", name, f.Items)
		}
	}
}

func TestParseEncodings(t *testing.T) {
	// "Привет" in windows-1251 and in UTF-16LE.
	cp1251 := "\xcf\xf0\xe8\xe2\xe5\xf2"
	body := func(declaration, title string) string {
		return declaration + `<rss version="2.0"><channel><title>T</title><item><title>` + title + `</title></item></channel></rss>`
	}
	utf16 := func(s string) string {
		var b strings.Builder
		b.WriteString("\xFF\xFE")
		for _, r := range s {
			b.WriteByte(byte(r))
			b.WriteByte(byte(r >> 8))
		}
		return b.String()
	}
	for name, tt := range map[string]struct{ doc, contentType string }{
		"declared in XML":          {body(`<?xml version="1.0" encoding="windows-1251"?>`, cp1251), ""},
		"declared by HTTP":         {body(`<?xml version="1.0"?>`, cp1251), "application/rss+xml; charset=windows-1251"},
		"HTTP wins over XML":       {body(`<?xml version="1.0" encoding="utf-8"?>`, cp1251), "text/xml; charset=WINDOWS-1251"},
		"UTF-8":                    {body(`<?xml version="1.0" encoding="UTF-8"?>`, "Привет"), "text/xml"},
		"UTF-16 by byte order":     {utf16(body(`<?xml version="1.0" encoding="UTF-16"?>`, "Привет")), ""},
		"mark wins over HTTP":      {"\xEF\xBB\xBF" + body(`<?xml version="1.0"?>`, "Привет"), "text/xml; charset=windows-1251"},
		"unknown declared charset": {body(`<?xml version="1.0" encoding="x-nothing"?>`, "Привет"), ""},
	} {
		f, err := Parse([]byte(tt.doc), Options{ContentType: tt.contentType})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(f.Items) != 1 || f.Items[0].Title != "Привет" {
			t.Errorf("%s: items %+v", name, f.Items)
		}
	}
	// Nothing declared and not UTF-8: read as windows-1252.
	f, err := Parse([]byte(body("", "caf\xe9")), Options{})
	if err != nil || len(f.Items) != 1 || f.Items[0].Title != "café" {
		t.Errorf("undeclared single-byte text: %v, %+v", err, f)
	}
}

// Relative links are resolved against the feed's link, or its address when
// it has no link; the text of an item against the item's own link.
func TestParseResolvesAgainstFeedURL(t *testing.T) {
	const doc = `<rss version="2.0"><channel><title>T</title>%s
<item><title>A</title><link>/posts/a</link><description>&lt;a href="b"&gt;b&lt;/a&gt;</description></item>
<item><title>B</title><description>&lt;img src="pic.png"&gt;</description></item>
</channel></rss>`
	withLink, err := Parse([]byte(strings.Replace(doc, "%s", "<link>http://site.example.org/blog/</link>", 1)), Options{URL: "http://feeds.example.org/dir/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	// Items come last first.
	b, a := withLink.Items[0], withLink.Items[1]
	if a.Link != "http://site.example.org/posts/a" || a.Content != `<a href="http://site.example.org/posts/b">b</a>` {
		t.Errorf("with a feed link: link %q, content %q", a.Link, a.Content)
	}
	if b.Content != `<img src="http://site.example.org/blog/pic.png">` {
		t.Errorf("with a feed link: content of the item without a link %q", b.Content)
	}

	without, err := Parse([]byte(strings.Replace(doc, "%s", "", 1)), Options{URL: "http://feeds.example.org/dir/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	b, a = without.Items[0], without.Items[1]
	if a.Link != "http://feeds.example.org/posts/a" || b.Content != `<img src="http://feeds.example.org/dir/pic.png">` {
		t.Errorf("without a feed link: link %q, content %q", a.Link, b.Content)
	}
}
