package scrape

import (
	"database/sql"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/juev/freshgo/internal/feed"
)

const referenceDir = "../../testdata/reference"

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// entityDecoder undoes PHP htmlspecialchars: FreshRSS stores text encoded.
var entityDecoder = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'")

// stored reads the ";A; B" and "#a #b" lists of FreshRSS.
func stored(list, separator string) []string {
	var out []string
	for _, v := range strings.Split(list, separator) {
		if v = strings.TrimSpace(strings.TrimLeft(v, ";#")); v != "" {
			out = append(out, entityDecoder.Replace(v))
		}
	}
	return out
}

// The reference installation refreshed one feed of every scraped kind: the
// corpus document, scraped with the settings of the stored feed and parsed
// as a feed, gives the entries FreshRSS stored.
func TestRSSAgainstReferenceDatabase(t *testing.T) {
	kinds := map[int]int{}
	for _, user := range []string{"alice", "bob"} {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(referenceDir, "sqlite", "data", "users", user, "db.sqlite")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()

		type feedRow struct {
			id         int64
			kind       int
			url, name  string
			attributes string
		}
		var feeds []feedRow
		rows, err := db.Query(`SELECT id, kind, url, name, attributes FROM feed WHERE kind <> 0 ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var f feedRow
			if err := rows.Scan(&f.id, &f.kind, &f.url, &f.name, &f.attributes); err != nil {
				t.Fatal(err)
			}
			feeds = append(feeds, f)
		}
		_ = rows.Close()

		for _, f := range feeds {
			body, err := os.ReadFile(filepath.Join(referenceDir, "corpus", path.Base(f.url)))
			if err != nil {
				t.Fatal(err)
			}
			// FreshRSS writes empty attributes as a list.
			if f.attributes == "[]" {
				f.attributes = "{}"
			}
			doc, err := RSS(body, Source{Kind: f.kind, URL: f.url, Name: f.name, Attributes: json.RawMessage(f.attributes)})
			if err != nil {
				t.Errorf("%s (kind %d): %v", f.url, f.kind, err)
				continue
			}
			parsed, err := feed.Parse(doc, feed.Options{})
			if err != nil {
				t.Errorf("%s: the scraped document is not a feed: %v\n%s", f.url, err, doc)
				continue
			}
			if criteria, invalid := parsed.AssignGUIDs(feed.CriteriaID, false); criteria != feed.CriteriaID || invalid != 0 {
				t.Errorf("%s: criteria %q, %d invalid GUIDs", f.url, criteria, invalid)
			}
			byGUID := map[string]*feed.Item{}
			for _, it := range parsed.Items {
				byGUID[it.GUID] = it
			}

			entries, err := db.Query(`SELECT guid, title, author, content, link, date, tags, attributes FROM entry WHERE id_feed = ?`, f.id)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for entries.Next() {
				var guid, title, author, content, link, tags, attributes string
				var date int64
				if err := entries.Scan(&guid, &title, &author, &content, &link, &date, &tags, &attributes); err != nil {
					t.Fatal(err)
				}
				n++
				it := byGUID[guid]
				if it == nil {
					t.Errorf("%s: no item with the stored GUID %q", f.url, guid)
					continue
				}
				if it.Title != entityDecoder.Replace(title) || it.Link != entityDecoder.Replace(link) || it.Published != date {
					t.Errorf("%s %q: title %q, link %q, date %d; stored %q, %q, %d", f.url, guid, it.Title, it.Link, it.Published, title, link, date)
				}
				if it.Content != content {
					t.Errorf("%s %q: content\n got %q\nwant %q", f.url, guid, it.Content, content)
				}
				if want := stored(author, ";"); !reflect.DeepEqual(it.Authors, want) {
					t.Errorf("%s %q: authors %q, want %q", f.url, guid, it.Authors, want)
				}
				if want := stored(tags, " #"); !reflect.DeepEqual(it.Tags, want) {
					t.Errorf("%s %q: tags %q, want %q", f.url, guid, it.Tags, want)
				}
				if !sameJSON(it.Attributes(), []byte(attributes)) {
					t.Errorf("%s %q: attributes %s, want %s", f.url, guid, it.Attributes(), attributes)
				}
			}
			_ = entries.Close()
			if n != len(parsed.Items) || n == 0 {
				t.Errorf("%s: %d items, %d stored entries", f.url, len(parsed.Items), n)
			}
			kinds[f.kind] += n
		}
	}
	for _, kind := range []int{KindHTMLXPath, KindXMLXPath, KindJSONFeed, KindJSONDotNotation, KindHTMLXPathJSON} {
		if kinds[kind] == 0 {
			t.Errorf("no entries of kind %d were compared", kind)
		}
	}
}
