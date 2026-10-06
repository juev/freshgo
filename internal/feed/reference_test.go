package feed

import (
	"database/sql"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// The reference installation refreshed the corpus feeds over HTTP: for each
// of them freshgo computes the GUIDs, links and text that FreshRSS stored.
// Unlike the oracle, this covers links resolved against the feed address.
func TestParseAgainstReferenceDatabase(t *testing.T) {
	dataDir := filepath.Join(referenceDir, "sqlite", "data")
	extra, err := os.ReadFile(filepath.Join(dataDir, "force-https.txt"))
	if err != nil {
		t.Fatal(err)
	}
	https := NewHTTPSDomains(strings.Split(string(extra), "\n"))

	entries := 0
	for _, user := range []string{"alice", "bob"} {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "users", user, "db.sqlite")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()

		// Kind 0 is RSS and Atom; the scraped kinds are a later step.
		feeds, err := db.Query(`SELECT id, url FROM feed WHERE kind = 0 ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		type feedRow struct {
			id  int64
			url string
		}
		var list []feedRow
		for feeds.Next() {
			var f feedRow
			if err := feeds.Scan(&f.id, &f.url); err != nil {
				t.Fatal(err)
			}
			list = append(list, f)
		}
		_ = feeds.Close()
		if len(list) == 0 {
			t.Fatalf("%s has no RSS or Atom feeds", user)
		}

		for _, f := range list {
			doc, err := os.ReadFile(filepath.Join(referenceDir, "corpus", path.Base(f.url)))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(doc, Options{URL: f.url, HTTPS: https})
			if err != nil {
				t.Fatalf("%s: %v", f.url, err)
			}
			if criteria, invalid := parsed.AssignGUIDs(CriteriaID, false); criteria != CriteriaID || invalid != 0 {
				t.Errorf("%s: criteria %q, %d invalid GUIDs", f.url, criteria, invalid)
			}
			byGUID := map[string]*Item{}
			for _, it := range parsed.Items {
				byGUID[it.GUID] = it
			}

			rows, err := db.Query(`SELECT guid, title, link, content, date FROM entry WHERE id_feed = ?`, f.id)
			if err != nil {
				t.Fatal(err)
			}
			stored := 0
			for rows.Next() {
				var guid, title, link, content string
				var date int64
				if err := rows.Scan(&guid, &title, &link, &content, &date); err != nil {
					t.Fatal(err)
				}
				stored++
				it := byGUID[guid]
				if it == nil {
					t.Errorf("%s: no item with the stored GUID %q (%s)", f.url, guid, title)
					continue
				}
				// FreshRSS keeps titles and links HTML-encoded.
				if it.Title != decodeText(title) || it.Link != decodeText(link) || it.Published != date {
					t.Errorf("%s %q: title %q, link %q, date %d; stored %q, %q, %d",
						f.url, guid, it.Title, it.Link, it.Published, decodeText(title), decodeText(link), date)
				}
				if it.Content != content {
					t.Errorf("%s %q: content\n got %q\nwant %q", f.url, guid, it.Content, content)
				}
			}
			_ = rows.Close()
			if stored != len(parsed.Items) {
				t.Errorf("%s: %d items, %d stored entries", f.url, len(parsed.Items), stored)
			}
			entries += stored
		}
	}
	if entries < 12 {
		t.Errorf("only %d entries compared", entries)
	}
}
