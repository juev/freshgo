package scrape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/juev/freshgo/internal/feed"
)

// Items of the oracle that have no title.
var untitled = map[string]bool{"html-basic/a3": true, "jsonfeed-edge/3": true, "json-list/s2": true, "json-noformat/s2": true}

// Content that knowingly differs from FreshRSS, by case and GUID.
var contentDeviations = map[string]struct{ want, why string }{
	"xml-ms/1": {"Body &lt;b&gt;escaped&lt;/b&gt; text", "FreshRSS encodes the text of a removed element twice"},
}

// Each case of testdata/reference/oracle/scrape-cases.json scraped by
// freshgo gives what FreshRSS 1.30.1 stores for the same page and settings
// (scrape.json, written by oracle/generate.sh).
func TestRSSAgainstFreshRSS(t *testing.T) {
	oracleDir := filepath.Join(referenceDir, "oracle")
	var cases []struct {
		Name, Page, FeedName string
		Kind                 int
		Attributes           json.RawMessage
	}
	var want map[string]struct {
		Failed  bool
		Title   string
		Entries []struct {
			GUID, Title, Content, Link string
			Authors, Tags              []string
			Date                       int64
			Attributes                 json.RawMessage
		}
	}
	for file, into := range map[string]any{"scrape-cases.json": &cases, "scrape.json": &want} {
		raw, err := os.ReadFile(filepath.Join(oracleDir, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	if len(cases) < 20 || len(cases) != len(want) {
		t.Fatalf("%d cases, %d results", len(cases), len(want))
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			w := want[c.Name]
			body, err := os.ReadFile(filepath.Join(oracleDir, "pages", c.Page))
			if err != nil {
				t.Fatal(err)
			}
			// The address the oracle fetched the page from.
			url := "http://127.0.0.1:8080/" + c.Page
			doc, err := RSS(body, Source{Kind: c.Kind, URL: url, Name: c.FeedName, Attributes: c.Attributes})
			if w.Failed {
				if err == nil {
					t.Errorf("FreshRSS gets no feed out of this; freshgo got\n%s", doc)
				}
				return
			}
			if err != nil {
				t.Fatalf("RSS: %v", err)
			}
			f, err := feed.Parse(doc, feed.Options{HTTPS: feed.NewHTTPSDomains([]string{"example.net"})})
			if err != nil {
				t.Fatalf("the scraped document is not a feed: %v\n%s", err, doc)
			}
			f.AssignGUIDs(feed.CriteriaID, false)
			if f.Title != w.Title {
				t.Errorf("feed title %q, want %q", f.Title, w.Title)
			}
			if len(f.Items) != len(w.Entries) {
				t.Fatalf("%d items, want %d\n%s", len(f.Items), len(w.Entries), doc)
			}
			for i, it := range f.Items {
				e := w.Entries[i]
				if it.GUID != e.GUID {
					t.Errorf("item %d %q: GUID %q, want %q", i, e.Title, it.GUID, e.GUID)
				}
				// For an item without a title FreshRSS shows the start of its
				// text; what to store is decided where entries are written.
				if it.Title != e.Title && (it.Title != "" || !untitled[c.Name+"/"+e.GUID]) {
					t.Errorf("item %d: title %q, want %q", i, it.Title, e.Title)
				}
				if it.Link != e.Link {
					t.Errorf("item %d %q: link %q, want %q", i, e.Title, it.Link, e.Link)
				}
				if it.Published != e.Date {
					t.Errorf("item %d %q: date %d, want %d", i, e.Title, it.Published, e.Date)
				}
				if d, ok := contentDeviations[c.Name+"/"+e.GUID]; ok {
					if d.want == e.Content {
						t.Errorf("item %d %q is listed as a deviation (%s) but FreshRSS gives the same", i, e.Title, d.why)
					}
					e.Content = d.want
				}
				if it.Content != e.Content {
					t.Errorf("item %d %q: content\n got %q\nwant %q", i, e.Title, it.Content, e.Content)
				}
				if !reflect.DeepEqual(it.Authors, e.Authors) && len(it.Authors)+len(e.Authors) > 0 {
					t.Errorf("item %d %q: authors %q, want %q", i, e.Title, it.Authors, e.Authors)
				}
				if !reflect.DeepEqual(it.Tags, e.Tags) && len(it.Tags)+len(e.Tags) > 0 {
					t.Errorf("item %d %q: tags %q, want %q", i, e.Title, it.Tags, e.Tags)
				}
				if !sameJSON(it.Attributes(), e.Attributes) {
					t.Errorf("item %d %q: attributes %s, want %s", i, e.Title, it.Attributes(), e.Attributes)
				}
			}
		})
	}
}
