package refresh

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/store"
)

const referenceDir = "../../testdata/reference"

// curlProxy is the key of CURLOPT_PROXY in feed.attributes.curl_params.
const curlProxy = "10004"

// importReference fills the database from the reference FreshRSS and makes
// its feeds reachable: they live on http://feeds.freshgo.test/, so the test
// server is set as the proxy of every feed and answers with the corpus file
// of the same name, taken from the returned directory. Addresses, and with
// them resolved links and identifiers, stay what FreshRSS saw.
//
// The reference ran on a server in UTC, and the users leave the time zone to
// the server; it is set to UTC here so that the result does not depend on
// the machine the test runs on.
func importReference(t *testing.T, w *world) (corpus string) {
	t.Helper()
	ctx := context.Background()
	if _, err := importer.Run(ctx, w.db, importer.Options{DataDir: filepath.Join(referenceDir, "sqlite", "data")}); err != nil {
		t.Fatalf("import: %v", err)
	}

	corpus = t.TempDir()
	files, err := os.ReadDir(filepath.Join(referenceDir, "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(referenceDir, "corpus", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(corpus, file.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
		w.serve("/"+file.Name(), func(rw http.ResponseWriter, req *http.Request) {
			if req.Host != "feeds.freshgo.test" {
				http.Error(rw, "not asked as a proxy", http.StatusBadRequest)
				return
			}
			// No validators: every refresh gets the document and compares it.
			data, err := os.ReadFile(filepath.Join(corpus, path.Base(req.URL.Path)))
			if err != nil {
				http.Error(rw, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = rw.Write(data)
		})
	}

	proxy := strings.TrimPrefix(w.server.URL, "http://")
	users, err := w.db.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		feeds, err := w.db.Feeds(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range feeds {
			attrs := readAttributes(f.Attributes)
			attrs.set("curl_params", map[string]string{curlProxy: proxy})
			f.Attributes = attrs.raw()
			if err := w.db.UpdateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	return corpus
}

// everyEntry returns the entries of all users, by user name, in id order.
func everyEntry(t *testing.T, db *store.Store) map[string][]*store.Entry {
	t.Helper()
	ctx := context.Background()
	users, err := db.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all := map[string][]*store.Entry{}
	for _, u := range users {
		feeds, err := db.Feeds(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range feeds {
			entries, err := db.EntriesByFeed(ctx, u.ID, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			all[u.Name] = append(all[u.Name], entries...)
		}
	}
	return all
}

// R3: refreshing an imported installation from the same feeds adds nothing
// and changes nothing; a new item in a feed adds exactly one entry.
func TestRefreshOfImportedInstallation(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		corpus := importReference(t, w)
		before := everyEntry(t, w.db)
		if len(before["alice"]) == 0 || len(before["bob"]) == 0 {
			t.Fatalf("the reference has no entries: %d, %d", len(before["alice"]), len(before["bob"]))
		}
		// Feeds of every kind, read and starred entries among them.
		kinds, read, starred := map[int]bool{}, 0, 0
		users, err := w.db.Users(ctx)
		if err != nil {
			t.Fatal(err)
		}
		feedCount := map[string]int{}
		for _, u := range users {
			feeds, err := w.db.Feeds(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			feedCount[u.Name] = len(feeds)
			for _, f := range feeds {
				kinds[f.Kind] = true
			}
		}
		for _, entries := range before {
			for _, e := range entries {
				if e.IsRead {
					read++
				}
				if e.IsFavorite {
					starred++
				}
			}
		}
		if !reflect.DeepEqual(kinds, map[int]bool{0: true, 10: true, 15: true, 25: true, 30: true, 35: true}) || read == 0 || starred == 0 {
			t.Fatalf("the reference covers kinds %v, %d read and %d starred entries", kinds, read, starred)
		}

		// Well after the reference was made: every feed is due.
		w.clock = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, st := range w.run(Options{}) {
			if want := (Stats{User: st.User, Refreshed: feedCount[st.User]}); st != want {
				t.Errorf("first refresh: stats %+v, want %+v", st, want)
			}
		}
		after := everyEntry(t, w.db)
		for user, entries := range after {
			if len(entries) != len(before[user]) {
				t.Fatalf("%s: %d entries after the refresh, %d before", user, len(entries), len(before[user]))
			}
			for i, e := range entries {
				if e.Hash == nil || e.LastSeen != w.clock.Unix() {
					t.Errorf("%s, entry %d %q: hash %x, last seen %d", user, e.ID, e.GUID, e.Hash, e.LastSeen)
				}
				// Only the bookkeeping of the refresh may differ.
				e.Hash, e.LastSeen = nil, before[user][i].LastSeen
				if !reflect.DeepEqual(e, before[user][i]) {
					t.Errorf("%s, entry %d changed:\n got %+v\nwant %+v", user, e.ID, e, before[user][i])
				}
			}
		}

		// Once more, now with hashes to compare: still nothing changes.
		w.clock = w.clock.Add(24 * time.Hour)
		for _, st := range w.run(Options{}) {
			if want := (Stats{User: st.User, Refreshed: feedCount[st.User]}); st != want {
				t.Errorf("second refresh: stats %+v, want %+v", st, want)
			}
		}

		// One more item in rss.xml, which both users are subscribed to.
		file := filepath.Join(corpus, "rss.xml")
		doc, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		const added = `<item><title>Fresh from the oven</title><link>http://feeds.freshgo.test/fresh</link>` +
			`<guid isPermaLink="false">fresh-1</guid><pubDate>Wed, 30 Dec 2026 10:00:00 GMT</pubDate>` +
			`<description>New</description></item></channel>`
		if !strings.Contains(string(doc), "</channel>") {
			t.Fatal("rss.xml has no </channel>")
		}
		if err := os.WriteFile(file, []byte(strings.Replace(string(doc), "</channel>", added, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		w.clock = w.clock.Add(24 * time.Hour)
		for _, st := range w.run(Options{}) {
			if want := (Stats{User: st.User, Refreshed: feedCount[st.User], NewEntries: 1}); st != want {
				t.Errorf("refresh after one new item: stats %+v, want %+v", st, want)
			}
		}
		for user, entries := range everyEntry(t, w.db) {
			var maxBefore int64
			known := map[int64]bool{}
			for _, e := range before[user] {
				known[e.ID] = true
				maxBefore = max(maxBefore, e.ID)
			}
			var fresh []*store.Entry
			for _, e := range entries {
				if !known[e.ID] {
					fresh = append(fresh, e)
				}
			}
			if len(entries) != len(before[user])+1 || len(fresh) != 1 {
				t.Fatalf("%s: %d entries, %d of them new; want %d and 1", user, len(entries), len(fresh), len(before[user])+1)
			}
			if e := fresh[0]; e.GUID != "fresh-1" || e.Title != "Fresh from the oven" || e.ID <= maxBefore || e.IsRead || e.IsFavorite {
				t.Errorf("%s: new entry %+v, greatest id before %d", user, e, maxBefore)
			}
		}
	})
}

// The settings a refresh reads from an imported user are where FreshRSS
// keeps them.
func TestSettingsOfImportedUser(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		importReference(t, w)
		alice, err := w.db.UserByName(context.Background(), "alice")
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(alice.Settings, &raw); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"enabled", "ttl_default", "mark_updated_article_unread", "timezone", "archiving", "mark_when"} {
			if _, ok := raw[key]; !ok {
				t.Errorf("imported settings have no %q", key)
			}
		}
		conf := readUserSettings(alice.Settings)
		if !conf.enabled || conf.ttlDefault != 3600 || conf.markUpdatedUnread || conf.location != time.Local {
			t.Errorf("settings of alice: %+v", conf)
		}
		// What FreshRSS wrote down for a new user is what freshgo assumes for one.
		if conf.archiving != defaultArchiving || conf.readUponGone || conf.readUponReception || conf.maxUnread != -1 || conf.sameTitleInFeed != 0 {
			t.Errorf("retention and auto-read settings of alice: %+v", conf)
		}
		var kept map[string]json.RawMessage
		if err := json.Unmarshal(raw["archiving"], &kept); err != nil || len(kept) != 6 {
			t.Errorf("archiving of alice: %s, %v; want the six keys freshgo reads", raw["archiving"], err)
		}
	})
}
