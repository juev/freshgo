package ttrss_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"github.com/juev/freshgo/internal/transfer"
	"github.com/juev/freshgo/internal/ttrss"
	"github.com/juev/freshgo/internal/ttrss/ttrsstest"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func client(t *testing.T, s *ttrsstest.Server) *fetch.Client {
	t.Helper()
	c, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{s.Host}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func read(t *testing.T, s *ttrsstest.Server) (*transfer.Remote, error) {
	t.Helper()
	return ttrss.Read(context.Background(), client(t, s), s.URL+"/", ttrsstest.User, ttrsstest.Password, nil)
}

func hasWarning(remote *transfer.Remote, part string) bool {
	return slices.ContainsFunc(remote.Warnings, func(w string) bool { return strings.Contains(w, part) })
}

// R1: the account is read in full: every page of articles, the states, the
// labels and the feeds with the settings that have an equivalent; the
// session is closed at the end.
func TestRead(t *testing.T) {
	s := ttrsstest.New(t)
	remote, err := read(t, s)
	if err != nil {
		t.Fatal(err)
	}
	// The categories the user made, nested one and empty one included; the
	// virtual ones of the API are not categories.
	if want := []string{"News", "Empty", "Tech"}; !slices.Equal(remote.Categories, want) {
		t.Errorf("categories = %v, want %v", remote.Categories, want)
	}

	// Pages of two hundred until an empty one, by skip.
	var skips []float64
	for _, r := range s.Requests() {
		if r.Op == "getheadlines" {
			skips = append(skips, r.Params["skip"].(float64))
		}
	}
	if want := []float64{0, 200, 230}; !slices.Equal(skips, want) || len(remote.Entries) != ttrsstest.Entries {
		t.Fatalf("skips %v, %d entries; want %v and %d", skips, len(remote.Entries), want, ttrsstest.Entries)
	}
	if ops := s.Ops(); ops[0] != "login" || ops[len(ops)-1] != "logout" || s.Logouts() != 1 {
		t.Errorf("calls %v, %d logouts; want login first and one logout last", ops, s.Logouts())
	}

	want := []transfer.RemoteFeed{
		{Key: "1", URL: "https://site1.example/feed.xml", Site: "https://site1.example/", Title: "Blog", Category: "News"},
		{Key: "2", URL: "https://site2.example/feed.xml", Site: "https://site2.example/", Title: "Sub", Category: "Tech"},
		{Key: "3", URL: "https://site3.example/feed.xml", Site: "https://site3.example/", Title: "Plain"},
		{Key: "4", URL: "https://site4.example/feed.xml", Site: "https://site4.example/", Title: "Paused", Category: "News", Muted: true},
	}
	if !slices.Equal(remote.Feeds, want) {
		t.Errorf("feeds = %+v\nwant %+v", remote.Feeds, want)
	}

	for _, w := range []string{
		"1 categories were inside others in Tiny Tiny RSS and are made top-level: Tech",
		"refresh period not carried over, set on 1 feeds: Sub",
		"published articles (shared in the public feed of Tiny Tiny RSS) not carried over, set on 1 articles",
		"notes of articles not carried over, set on 1 articles",
		"tags of articles (labels are carried over) not carried over, set on 1 articles",
		"feed logins are not carried over",
		"could not download filters from Tiny Tiny RSS",
	} {
		if !hasWarning(remote, w) {
			t.Errorf("no warning with %q in %q", w, remote.Warnings)
		}
	}
	if len(remote.Warnings) != 7 {
		t.Errorf("warnings = %q, want the seven above", remote.Warnings)
	}

	byTitle := map[string]transfer.RemoteEntry{}
	for _, e := range remote.Entries {
		byTitle[e.Title] = e
	}
	e5 := byTitle["Entry 5"]
	if e5.Read || e5.Starred || !slices.Equal(e5.Labels, []string{"go", "later"}) || e5.Feed != "2" ||
		e5.Author != "Ann" || e5.URL != "https://site2.example/post/5" || e5.Content != "<p>Body 5</p>" ||
		e5.Published != time.Date(2026, 9, 1, 12, 5, 0, 0, time.UTC).Unix() {
		t.Errorf("entry 5 = %+v", e5)
	}
	if e10 := byTitle["Entry 10"]; !e10.Read || !e10.Starred || e10.Labels != nil {
		t.Errorf("entry 10 = %+v, want read and starred", e10)
	}
	if e25 := byTitle["Entry 25"]; !slices.Equal(e25.Labels, []string{"go"}) {
		t.Errorf("entry 25 = %+v, want labelled go", e25)
	}
	// The identifier TT-RSS keeps stands for the link an article lacks.
	if e7 := byTitle["Entry 7"]; e7.URL != "" || !strings.Contains(e7.ID, "SHA1:") {
		t.Errorf("entry 7 = %+v, want one without an address, with the identifier of the server", e7)
	}
}

// R2: the call is what the API takes: a JSON object to /api/ with the
// operation, and the session in every call after the login.
func TestReadCalls(t *testing.T) {
	s := ttrsstest.New(t)
	if _, err := read(t, s); err != nil {
		t.Fatal(err)
	}
	for i, r := range s.Requests() {
		switch r.Op {
		case "login":
			if r.Params["user"] != ttrsstest.User || r.Params["password"] != ttrsstest.Password || i != 0 {
				t.Errorf("login = %v at %d", r.Params, i)
			}
		default:
			if r.Params["sid"] != "0123456789abcdef" {
				t.Errorf("%s without the session: %v", r.Op, r.Params)
			}
		}
	}
}

// R3: a wrong login, the API turned off, a bad address and a failure half
// way are told apart; nothing is returned of a failed read, and the session
// is closed after a failure too.
func TestReadFailures(t *testing.T) {
	s := ttrsstest.New(t)
	ctx := context.Background()
	if _, err := ttrss.Read(ctx, client(t, s), s.URL, ttrsstest.User, "wrong", nil); !errors.Is(err, ttrss.ErrLogin) {
		t.Errorf("wrong password: %v, want ErrLogin", err)
	}
	if _, err := ttrss.Read(ctx, client(t, s), s.URL, "bob", ttrsstest.Password, nil); !errors.Is(err, ttrss.ErrLogin) {
		t.Errorf("wrong login: %v, want ErrLogin", err)
	}
	if s.Logouts() != 0 {
		t.Errorf("%d logouts without a session", s.Logouts())
	}
	for _, address := range []string{"", "ftp://x.example", "not a url", "https://user:pw@x.example"} {
		if _, err := ttrss.Read(ctx, client(t, s), address, ttrsstest.User, ttrsstest.Password, nil); !errors.Is(err, ttrss.ErrAddress) {
			t.Errorf("address %q: %v, want ErrAddress", address, err)
		}
	}

	s.FailHeadlinesFrom(2)
	remote, err := read(t, s)
	var status *fetch.StatusError
	if !errors.As(err, &status) || status.Code != 500 || errors.Is(err, ttrss.ErrLogin) || remote != nil {
		t.Errorf("failure on the second page: %v, %v; want a 500 and no account", remote, err)
	}
	if s.Logouts() != 1 {
		t.Errorf("%d logouts after a failed read, want 1", s.Logouts())
	}

	off := ttrsstest.New(t)
	off.DisableAPI()
	if _, err := read(t, off); !errors.Is(err, ttrss.ErrAPIDisabled) || errors.Is(err, ttrss.ErrLogin) {
		t.Errorf("API disabled: %v, want ErrAPIDisabled", err)
	}

	// The guard of the client holds: this server is not on its allowlist.
	guarded, err := fetch.New(fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ttrss.Read(ctx, guarded, s.URL, ttrsstest.User, ttrsstest.Password, nil); !errors.Is(err, fetch.ErrForbiddenAddress) {
		t.Errorf("server on a private address: %v, want ErrForbiddenAddress", err)
	}
}

type world struct {
	t  *testing.T
	db *store.Store
	u  *store.User
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			u := &store.User{Name: "alice", Settings: json.RawMessage(`{}`)}
			if err := db.CreateUser(context.Background(), u); err != nil {
				t.Fatal(err)
			}
			test(t, &world{t, db, u})
		})
	}
}

func (w *world) entries() map[string]*store.Entry {
	w.t.Helper()
	ctx := context.Background()
	byTitle := map[string]*store.Entry{}
	feeds, err := w.db.Feeds(ctx, w.u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, f := range feeds {
		entries, err := w.db.EntriesByFeed(ctx, w.u.ID, f.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, e := range entries {
			byTitle[e.Title] = e
		}
	}
	return byTitle
}

func TestImportedLegacyAndModernScopes(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		server := ttrsstest.New(t)
		export := `<opml><body>
   <outline text="Blog" xmlUrl="https://site1.example/feed.xml"/>
   <outline text="Sub" xmlUrl="https://site2.example/feed.xml"/>
   <outline text="tt-rss-filters">` +
			scopedFilter("Legacy", `"feed":"Blog"`, `"feed":"Sub"`, "^Different") +
			scopedFilter("Legacy with global rule", `"feed":""`, `"feed":"Sub"`, "^Different") +
			scopedFilter("Modern", `"match":[["Blog",false,false]]`, `"match":[["Sub",false,false]]`, "^Different") +
			`</outline></body></opml>`
		saved, report := w.importFilterExport(server, export)
		if report.Filters != 3 {
			t.Fatalf("import = %+v", report)
		}
		article := filters.Article{FeedID: w.entries()["Entry 4"].FeedID, Title: "Entry one"}
		for _, filter := range saved {
			compiled, err := filters.Compile([]filters.Filter{filter})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(filters.Actions(compiled, article)) > 0, filter.Title != "Modern"; got != want {
				t.Errorf("filter %q matched = %t, want %t", filter.Title, got, want)
			}
		}
	})
}

func scopedFilter(title, firstScope, secondScope, secondPattern string) string {
	return fmt.Sprintf(`<outline filter-type="2"><![CDATA[
 {"title":%q,"enabled":true,"match_any_rule":false,"rules":[
 {"filter_type":1,"reg_exp":"^Entry",%s},
 {"filter_type":1,"reg_exp":%q,%s}
 ],"actions":[{"action_id":3}]}
 ]]></outline>`, title, firstScope, secondPattern, secondScope)
}

// R4: an account is written into an existing user with its categories, the
// settings freshgo has, the states and the labels; a second run adds nothing
// twice and catches up with the changed states.
func TestImport(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := ttrsstest.New(t)
		remote, err := read(t, s)
		if err != nil {
			t.Fatal(err)
		}
		report, err := transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, remote, transfer.Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if report.Feeds != 4 || report.Entries != ttrsstest.Entries || report.Updated != 0 || report.Incomplete {
			t.Errorf("report = %+v, want 4 feeds and %d entries", report, ttrsstest.Entries)
		}

		categories, err := w.db.Categories(ctx, w.u.ID)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range categories {
			names = append(names, c.Name)
		}
		for _, name := range []string{"News", "Tech", "Empty"} {
			if !slices.Contains(names, name) {
				t.Errorf("categories %v: no %s", names, name)
			}
		}
		feeds, err := w.db.Feeds(ctx, w.u.ID)
		if err != nil {
			t.Fatal(err)
		}
		byURL := map[string]*store.Feed{}
		for _, f := range feeds {
			byURL[f.URL] = f
		}
		blog, paused := byURL["https://site1.example/feed.xml"], byURL["https://site4.example/feed.xml"]
		if blog == nil || paused == nil || len(feeds) != 4 {
			t.Fatalf("%d feeds stored", len(feeds))
		}
		if blog.Name != "Blog" || blog.Website != "https://site1.example/" || blog.TTL != 0 || blog.Priority != store.PriorityMain {
			t.Errorf("Blog = %+v", blog)
		}
		var attrs map[string]any
		_ = json.Unmarshal(blog.Attributes, &attrs)
		if attrs["unicityCriteria"] != "link" || attrs["unicityCriteriaForced"] != true {
			t.Errorf("attributes of Blog = %s, want entries told by their links", blog.Attributes)
		}
		if paused.TTL >= 0 {
			t.Errorf("Paused = %+v, want muted", paused)
		}

		entries := w.entries()
		if len(entries) != ttrsstest.Entries {
			t.Fatalf("%d entries stored, want %d", len(entries), ttrsstest.Entries)
		}
		if e := entries["Entry 10"]; !e.IsRead || !e.IsFavorite || e.GUID != "https://site3.example/post/10" || e.Link != e.GUID || e.Authors[0] != "Ann" {
			t.Errorf("Entry 10 = %+v", e)
		}
		if e := entries["Entry 5"]; e.IsRead || e.IsFavorite {
			t.Errorf("Entry 5 = %+v, want unread and not starred", e)
		}
		if e := entries["Entry 7"]; e.Link != "" || !strings.Contains(e.GUID, "SHA1:") {
			t.Errorf("Entry 7 = %+v, want the identifier of the server for the identifier", e)
		}
		labels, err := w.db.Tags(ctx, w.u.ID)
		if err != nil || len(labels) != 2 {
			t.Fatalf("labels = %v, %v; want go and later", labels, err)
		}
		for _, l := range labels {
			tagged, err := w.db.ListEntries(ctx, w.u.ID, store.EntryQuery{Set: store.EntrySet{LabelID: l.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if want := map[string]int{"go": 10, "later": 1}[l.Name]; len(tagged) != want {
				t.Errorf("label %s is on %d entries, want %d", l.Name, len(tagged), want)
			}
		}

		// Run again after the account changed: nothing is added, the states follow.
		s.Change(5, false, true, "go", "again")
		s.Change(10, true, true)
		remote, err = read(t, s)
		if err != nil {
			t.Fatal(err)
		}
		report, err = transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, remote, transfer.Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if report.Feeds != 0 || report.Entries != 0 || report.Updated != ttrsstest.Entries {
			t.Errorf("second report = %+v, want %d entries updated and none added", report, ttrsstest.Entries)
		}
		entries = w.entries()
		if len(entries) != ttrsstest.Entries {
			t.Errorf("%d entries after the second import, want %d", len(entries), ttrsstest.Entries)
		}
		if e := entries["Entry 5"]; !e.IsRead || !e.IsFavorite {
			t.Errorf("Entry 5 after the change = %+v, want read and starred", e)
		}
		if e := entries["Entry 10"]; e.IsRead {
			t.Errorf("Entry 10 after the change = %+v, want unread", e)
		}
		after, err := w.db.Tags(ctx, w.u.ID)
		if err != nil || !slices.ContainsFunc(after, func(l *store.Tag) bool { return l.Name == "again" }) {
			t.Errorf("labels %v, %v: no again", after, err)
		}
	})
}

// A server that pages without end is read up to a bound: the account is held
// in memory before it is written.
func TestReadIsBounded(t *testing.T) {
	defer func(was int) { *ttrss.MaxBytes = was }(*ttrss.MaxBytes)
	*ttrss.MaxBytes = 1 << 20
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		var content any
		switch in["op"] {
		case "login":
			content = map[string]any{"session_id": "s"}
		case "getCategories", "getFeeds":
			content = []any{}
		case "getHeadlines":
			pages++
			skip, _ := in["skip"].(float64)
			var page []map[string]any
			for i := range 200 {
				page = append(page, map[string]any{"id": int(skip) + i + 1, "feed_id": 1, "content": strings.Repeat("x", 1000)})
			}
			content = page
		default:
			content = map[string]any{"status": "OK"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"seq": 0, "status": 0, "content": content})
	}))
	defer server.Close()
	c, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ttrss.Read(context.Background(), c, server.URL, "u", "p", nil); !errors.Is(err, transfer.ErrRemoteTooLarge) {
		t.Errorf("an account without end: %v, want ErrRemoteTooLarge", err)
	}
	if pages > 7 {
		t.Errorf("%d pages read past the bound", pages)
	}
}

func TestRepeatFilterImportOrder(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		s := ttrsstest.New(t)
		const begin = `<opml><body><outline text="tt-rss-filters">`
		const end = `</outline></body></opml>`
		record := func(title string, order, action int) string {
			return fmt.Sprintf(`<outline filter-type="2"><![CDATA[{"title":%q,"enabled":true,"order_id":%d,
				"rules":[{"filter_type":1,"reg_exp":".+"}],"actions":[{"action_id":%d}]}]]></outline>`, title, order, action)
		}
		stop, star, read := record("Stop", 20, 8), record("Star", 10, 3), record("Read", 40, 2)

		w.importFilterExport(s, begin+stop+end)
		saved, report := w.importFilterExport(s, begin+stop+star+stop+end)
		if report.Filters != 1 || report.Incomplete {
			t.Fatalf("inserting a filter = %+v", report)
		}
		assertFilterOrder(t, saved, []string{"Star", "Stop"}, filters.ActionStar)

		star = record("Star", 30, 3)
		saved, report = w.importFilterExport(s, begin+stop+star+end)
		if report.Filters != 0 || report.Incomplete {
			t.Fatalf("reordering filters = %+v", report)
		}
		assertFilterOrder(t, saved, []string{"Stop", "Star"})

		w.saveFilters([]filters.Filter{saved[1], saved[0]})
		export := begin + stop + star + read + end
		saved, report = w.importFilterExport(s, export)
		if report.Filters != 1 || !hasOrderConflict(report) {
			t.Fatalf("adding a filter during a local reorder = %+v", report)
		}
		assertFilterOrder(t, saved, []string{"Star", "Stop", "Read"}, filters.ActionStar)

		// The new filter must also participate in local reorder detection.
		w.saveFilters([]filters.Filter{saved[2], saved[1], saved[0]})
		saved, report = w.importFilterExport(s, export)
		if !hasOrderConflict(report) {
			t.Errorf("re-import failed to flag the local reorder: %+v", report)
		}
		assertFilterOrder(t, saved, []string{"Read", "Stop", "Star"}, filters.ActionRead)
	})
}

func assertFilterOrder(t *testing.T, saved []filters.Filter, want []string, wantActions ...string) {
	t.Helper()
	var titles []string
	for _, filter := range saved {
		titles = append(titles, filter.Title)
	}
	if !slices.Equal(titles, want) {
		t.Fatalf("filter order = %v, want %v", titles, want)
	}
	compiled, err := filters.Compile(saved)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, action := range filters.Actions(compiled, filters.Article{Title: "Entry"}) {
		actions = append(actions, action.Type)
	}
	if !slices.Equal(actions, wantActions) {
		t.Errorf("actions = %v, want %v", actions, wantActions)
	}
}

func hasOrderConflict(report transfer.Report) bool {
	return report.Incomplete && slices.ContainsFunc(report.Warnings, func(warning string) bool {
		return strings.Contains(warning, "reordered in FreshGo")
	})
}

func (w *world) saveFilters(saved []filters.Filter) {
	w.t.Helper()
	if err := w.db.UpdateUserSettings(context.Background(), w.u.ID, func(settings map[string]json.RawMessage) error {
		return filters.Save(settings, saved)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) importFilterExport(server *ttrsstest.Server, opml string) ([]filters.Filter, transfer.Report) {
	w.t.Helper()
	server.SetExport(opml)
	account, err := read(w.t, server)
	if err != nil {
		w.t.Fatal(err)
	}
	ctx := context.Background()
	report, err := transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, account, transfer.Options{Now: now})
	if err != nil {
		w.t.Fatal(err)
	}
	user, err := w.db.UserByName(ctx, w.u.Name)
	if err != nil {
		w.t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(user.Settings, &settings); err != nil {
		w.t.Fatal(err)
	}
	saved, err := filters.Decode(settings[filters.SettingsKey])
	if err != nil {
		w.t.Fatal(err)
	}
	return saved, report
}
