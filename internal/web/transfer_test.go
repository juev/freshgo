package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// archived reads the files of a ZIP archive by name.
func archived(t *testing.T, archive string) map[string]string {
	t.Helper()
	z, err := zip.NewReader(strings.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("the answer is not a ZIP archive: %v", err)
	}
	files := map[string]string{}
	for _, member := range z.File {
		r, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		var content bytes.Buffer
		_, _ = content.ReadFrom(r)
		_ = r.Close()
		files[member.Name] = content.String()
	}
	return files
}

// R10: subscriptions and entries go out as files and come back in an
// account that had none of them.
func TestExportAndImport(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		now := s.clock()
		day := now.Format("2006-01-02")
		s.asAlice()
		page := s.shown("/subscriptions/transfer")
		for _, want := range []string{`action="/subscriptions/import" enctype="multipart/form-data"`, `action="/subscriptions/export"`, `<option value="1">Atom corpus</option>`} {
			if !strings.Contains(page, want) {
				t.Errorf("GET /subscriptions/transfer: no %q", want)
			}
		}
		if a := s.post("/subscriptions/export", url.Values{"feed": {"99"}}); a.status != http.StatusBadRequest || !strings.Contains(a.body, "Choose what to export.") {
			t.Errorf("exporting nothing: status %d", a.status)
		}

		// One file goes out as it is.
		a := s.post("/subscriptions/export", url.Values{"opml": {"1"}})
		if a.status != http.StatusOK || a.header.Get("Content-Disposition") != `attachment; filename="feeds_`+day+`.opml.xml"` ||
			!strings.HasPrefix(a.header.Get("Content-Type"), "application/xml") || !strings.Contains(a.body, `xmlUrl="http://feeds.freshgo.test/atom.xml"`) {
			t.Errorf("export of the subscriptions: status %d, headers %v", a.status, a.header)
		}
		a = s.post("/subscriptions/export", url.Values{"starred": {"1"}})
		var document struct {
			ID    string
			Items []struct{ GUID string }
		}
		if err := json.Unmarshal([]byte(a.body), &document); err != nil || a.header.Get("Content-Disposition") != `attachment; filename="starred_`+day+`.json"` ||
			document.ID != "user/alice/state/org.freshrss/starred" || len(document.Items) != 2 {
			t.Errorf("export of the starred entries: %v, headers %v\n%s", err, a.header, a.body)
		}
		if err := json.Unmarshal([]byte(s.post("/subscriptions/export", url.Values{"starred": {"1"}, "labelled": {"1"}}).body), &document); err != nil || len(document.Items) != 3 {
			t.Errorf("export of the starred and the labelled entries: %v, %d items, want 3", err, len(document.Items))
		}
		if err := json.Unmarshal([]byte(s.post("/subscriptions/export", url.Values{"labelled": {"1"}}).body), &document); err != nil || len(document.Items) != 3 {
			t.Errorf("export of the labelled entries: %v, %d items, want 3", err, len(document.Items))
		}

		// Several files go out as an archive.
		a = s.post("/subscriptions/export", url.Values{"opml": {"1"}, "starred": {"1"}, "labelled": {"1"}, "feed": {"1", "2", "8", "99"}})
		if a.header.Get("Content-Type") != "application/zip" || a.header.Get("Content-Disposition") != `attachment; filename="freshgo_alice_`+day+`_export.zip"` {
			t.Fatalf("export of several files: headers %v", a.header)
		}
		files := archived(t, a.body)
		var names []string
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		want := []string{"feed_" + day + "_1_8.json", "feed_" + day + "_2_1.json", "feed_" + day + "_2_2.json", "feeds_" + day + ".opml.xml", "starred_" + day + ".json"}
		if strings.Join(names, " ") != strings.Join(want, " ") {
			t.Fatalf("files of the archive = %v, want %v", names, want)
		}

		// bob takes the archive in: he has none of these feeds.
		s.cookies = nil
		if a := s.login("bob", "bob-web-password", nil); a.status != http.StatusSeeOther {
			t.Fatalf("login of bob: status %d", a.status)
		}
		had := map[string]bool{}
		for _, f := range s.feeds("bob") {
			had[f.URL] = true
		}
		labelled := func(name string) int {
			labels, err := s.db.Tags(t.Context(), s.user("bob").ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range labels {
				if l.Name == name {
					return len(s.stored("bob", store.Listing{Set: store.EntrySet{LabelID: l.ID}}))
				}
			}
			return -1
		}
		wasLabelled := labelled("later")
		in := s.upload("/subscriptions/import", nil, "file", "freshgo_alice_"+day+"_export.zip", []byte(a.body))
		if in.status != http.StatusSeeOther || in.header.Get("Location") != "/subscriptions/transfer" {
			t.Fatalf("import: status %d\n%s", in.status, in.body)
		}
		if got := notice(s.shown("/subscriptions/transfer")); !strings.HasPrefix(got, "Imported: ") {
			t.Errorf("notice after the import = %q", got)
		}
		feeds := s.feeds("bob")
		got := map[string]*store.Feed{}
		for _, f := range feeds {
			got[f.URL] = f
		}
		added := 0
		for _, f := range s.feeds("alice") {
			mine := got[f.URL]
			if mine == nil {
				t.Errorf("bob lacks the feed %s after the import", f.URL)
				continue
			}
			if had[f.URL] {
				continue
			}
			added++
			if mine.Name != f.Name || mine.Kind != f.Kind || mine.Priority != f.Priority {
				t.Errorf("feed of the import = %+v, want it like %+v", mine, f)
			}
			// The entries of the two feeds whose entries were exported came along.
			if f.ID == 1 || f.ID == 2 || f.ID == 8 {
				theirs := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: f.ID}})
				if n := len(s.stored("bob", store.Listing{Set: store.EntrySet{FeedID: mine.ID}})); n != len(theirs) || n == 0 {
					t.Errorf("feed %s of bob has %d entries, that of alice %d", f.URL, n, len(theirs))
				}
			}
		}
		if added == 0 {
			t.Fatal("bob had every feed of alice already: the test imports nothing new")
		}
		if got := labelled("later"); got <= max(wasLabelled, 0) {
			t.Errorf("entries of bob with the label of alice: %d, before the import %d", got, wasLabelled)
		}

		// What cannot be taken in is said and changes nothing.
		for name, c := range map[string]struct {
			file, content, want string
			status              int
		}{
			"no file":      {"", "", "Choose a file to import.", http.StatusBadRequest},
			"unknown name": {"notes.md", "text", "does not say what it holds", http.StatusBadRequest},
			"not JSON":     {"entries.json", "<html>", "could not be read", http.StatusBadRequest},
			"not OPML":     {"feeds.opml", "{}", "could not be read", http.StatusBadRequest},
		} {
			field := "file"
			if c.file == "" {
				field = ""
			}
			if a := s.upload("/subscriptions/import", nil, field, c.file, []byte(c.content)); a.status != c.status || !strings.Contains(a.body, c.want) {
				t.Errorf("%s: status %d, want %d with %q", name, a.status, c.status, c.want)
			}
		}
		// Past the limits of the installation the import says it left something out.
		s.system(func(system *store.System) { system.Limits.MaxFeeds = len(feeds) })
		s.upload("/subscriptions/import", nil, "file", "more.txt", []byte("https://one.example/feed\n"))
		if got := notice(s.shown("/subscriptions/transfer")); !strings.HasPrefix(got, "Imported in part") || len(s.feeds("bob")) != len(feeds) {
			t.Errorf("import past the limit of feeds: notice %q, %d feeds", got, len(s.feeds("bob")))
		}
	})
}
