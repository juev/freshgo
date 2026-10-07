package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// feedItems reads the identifiers of the entries an RSS document lists.
func feedItems(t *testing.T, body string) []int64 {
	t.Helper()
	var doc struct {
		Channel struct {
			Items []struct {
				GUID string `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the feed is not XML: %v\n%s", err, body)
	}
	var ids []int64
	for _, item := range doc.Channel.Items {
		id, _ := strconv.ParseInt(item.GUID, 10, 64)
		ids = append(ids, id)
	}
	return ids
}

func (s *site) queries(user string) []map[string]any {
	s.t.Helper()
	var list []map[string]any
	raw, _ := json.Marshal(s.settings(user)["queries"])
	_ = json.Unmarshal(raw, &list)
	return list
}

// R12: a view of the reading screen is saved under a name, stands in the
// tree and in the palette, and is changed, moved and deleted.
func TestSavedQueries(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		if body := s.page("/feeds/1?state=all"); !strings.Contains(body, `<form method="post" action="/queries">`) {
			t.Fatal("the reading screen has no form that saves the view")
		}
		location, body := s.follow("/queries", url.Values{
			"name": {" Atom by title "}, "stream": {"/feeds/1"}, "state": {"all"}, "sort": {"title"}, "order": {"asc"}, "q": {"intitle:a"}, "next": {"/feeds/1"},
		})
		if location != "/settings/queries/0" || notice(body) != "Query saved." {
			t.Fatalf("after saving a view: at %q, notice %q", location, notice(body))
		}
		want := map[string]any{"name": "Atom by title", "get": "f_1", "state": 3.0, "search": "intitle:a", "sort": "title", "order": "ASC"}
		if got := s.queries("alice"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("queries after saving = %v, want %v", got, want)
		}
		query, err := search.Parse("intitle:a", search.Options{})
		if err != nil {
			t.Fatal(err)
		}
		listing := store.Listing{Set: store.EntrySet{FeedID: 1}, Search: query, Order: store.OrderTitle, Ascending: true}
		page := s.shown("/queries/0")
		if got, want := listed(page), s.stored("alice", listing); !reflect.DeepEqual(got, want) || len(want) == 0 {
			t.Errorf("GET /queries/0 lists %v, want %v", got, want)
		}
		for _, want := range []string{
			`<h1 id="stream-heading">Atom by title</h1>`, `<a href="/queries/0" aria-current="page">Atom by title</a>`, `<h2>Saved queries</h2>`,
			`<option value="title" selected>`, `<a href="/queries/0?state=all" aria-current="true">All</a>`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("GET /queries/0: no %q", want)
			}
		}
		// What the reader types narrows the query further.
		narrowed, _ := search.Parse("intitle:a intitle:e", search.Options{})
		listing.Search = narrowed
		if got, want := listed(s.page("/queries/0?q=intitle%3Ae")), s.stored("alice", listing); !reflect.DeepEqual(got, want) {
			t.Errorf("GET /queries/0 with a search lists %v, want %v", got, want)
		}
		var places []place
		_ = json.Unmarshal([]byte(s.page("/palette")), &places)
		found := false
		for _, p := range places {
			found = found || p == place{"Atom by title", "/queries/0", "Saved query"}
		}
		if !found {
			t.Error("the palette does not offer the saved query")
		}
		// The saved search can be referred to by name.
		if got := listed(s.page("/feeds/1?state=all&sort=title&order=asc&q=" + url.QueryEscape(`search:"Atom by title"`))); !reflect.DeepEqual(got, listed(page)) {
			t.Errorf("a search that refers to the saved query lists %v", got)
		}
		for name, form := range map[string]url.Values{
			"no name": {"stream": {"/"}}, "bad search": {"name": {"x"}, "stream": {"/"}, "q": {`/(?=a)/`}}, "no stream": {"name": {"x"}, "stream": {"/feeds/99"}},
		} {
			s.post("/queries", form)
			if n := len(s.queries("alice")); n != 1 {
				t.Errorf("saving a view, %s: %d queries, want 1", name, n)
			}
		}

		// A second one, a view of the first narrowed.
		s.follow("/queries", url.Values{"name": {"Second"}, "stream": {"/queries/0"}, "q": {"intitle:e"}, "state": {"unread"}})
		if got := s.queries("alice")[1]; got["get"] != "f_1" || got["search"] != "(intitle:a) (intitle:e)" || got["state"] != 2.0 {
			t.Errorf("a saved view of a saved query = %v", got)
		}
		// The saved view lists what its page listed, also when one of the
		// two searches has an OR, which binds weaker than what joins them.
		s.setting("alice", "queries", []map[string]any{{"name": "Either", "get": "f_1", "state": 3, "search": "intitle:a OR intitle:e"}})
		shownPage := listed(s.page("/queries/0?q=intitle%3Azzzz"))
		s.follow("/queries", url.Values{"name": {"Narrowed"}, "stream": {"/queries/0"}, "q": {"intitle:zzzz"}, "state": {"all"}})
		if saved := listed(s.page("/queries/1")); len(shownPage) != 0 || len(saved) != 0 || len(listed(s.page("/queries/0"))) == 0 {
			t.Errorf("the page of a narrowed query lists %v, the view saved from it %v (%v)", shownPage, saved, s.queries("alice")[1]["search"])
		}
		s.setting("alice", "queries", []map[string]any{
			{"name": "Atom by title", "get": "f_1", "state": 3, "search": "intitle:a", "sort": "title", "order": "ASC"},
			{"name": "Second", "get": "f_1", "state": 2, "search": "intitle:a intitle:e"},
		})

		// The page of a query stores what its form says.
		form := s.formAt("/settings/queries/1", "/settings/queries/1")
		if form.Get("name") != "Second" || form.Get("get") != "f_1" || form.Get("state") != "unread" || form.Get("search") != "intitle:a intitle:e" {
			t.Errorf("form of the second query = %v", form)
		}
		for key, value := range map[string]string{"name": "Starred news", "get": "s", "state": "", "search": "", "default_sort": "date", "default_order": "DESC", "description": "My stars"} {
			form.Set(key, value)
		}
		s.follow("/settings/queries/1", form)
		got := s.queries("alice")[1]
		if got["name"] != "Starred news" || got["get"] != "s" || got["state"] != nil || got["sort"] != "date" || got["order"] != "DESC" || got["description"] != "My stars" || got["token"] != nil {
			t.Errorf("the second query after its form = %v", got)
		}
		if got, want := listed(s.page("/queries/1")), s.stored("alice", store.Listing{Set: store.EntrySet{OnlyFavorite: true}, Order: store.OrderPublished}); !reflect.DeepEqual(got, want) || len(want) != 2 {
			t.Errorf("GET /queries/1 lists %v, want the starred entries %v", got, want)
		}
		form.Set("search", "/(?=a)/")
		form.Set("name", "Typed")
		if a := s.post("/settings/queries/1", form); a.status != http.StatusBadRequest || !strings.Contains(a.body, `value="Typed"`) || s.queries("alice")[1]["name"] != "Starred news" {
			t.Errorf("a search that cannot be read: status %d", a.status)
		}

		// Moving, and deleting.
		s.follow("/settings/queries/1", url.Values{"action": {"up"}})
		if list := s.queries("alice"); list[0]["name"] != "Starred news" || list[1]["name"] != "Atom by title" {
			t.Errorf("queries after moving the second up = %v", list)
		}
		s.follow("/settings/queries/0", url.Values{"action": {"up"}})
		s.follow("/settings/queries/1", url.Values{"action": {"down"}})
		if list := s.queries("alice"); len(list) != 2 || list[0]["name"] != "Starred news" {
			t.Errorf("queries after moves that lead nowhere = %v", list)
		}
		if body := s.shown("/settings/queries"); strings.Index(body, "Starred news") > strings.Index(body, "Atom by title") {
			t.Error("the page of queries does not list them in their order")
		}
		location, body = s.follow("/settings/queries/0", url.Values{"action": {"delete"}})
		if list := s.queries("alice"); location != "/settings/queries" || notice(body) != "Query deleted." || len(list) != 1 || list[0]["name"] != "Atom by title" {
			t.Errorf("after deleting a query: at %q, notice %q, %v", location, notice(body), list)
		}
		for _, target := range []string{"/queries/1", "/queries/x", "/settings/queries/1", "/queries/-1"} {
			if a := s.get(target); a.status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
		}

		// A query whose feed is gone lists nothing and is marked.
		if err := s.db.DeleteFeed(context.Background(), s.user("alice").ID, 1); err != nil {
			t.Fatal(err)
		}
		if a := s.get("/queries/0"); a.status != http.StatusNotFound {
			t.Errorf("a query whose feed is gone: status %d", a.status)
		}
		if body := s.shown("/settings/queries"); !strings.Contains(body, "its feed, category or label is gone") || strings.Contains(s.page("/?state=all"), "Saved queries") {
			t.Error("a query whose feed is gone is not marked, or is still in the tree")
		}
	})
}

// Queries FreshRSS saved are read as it wrote them.
func TestImportedQueries(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "queries", []any{
			map[string]any{"name": "Unread or starred", "get": "a", "state": 66, "order": "ASC", "search": "", "url": "./?get=a"},
			map[string]any{"name": "Labelled", "get": "T", "state": "3"},
			map[string]any{"name": "Everything", "get": "Z", "state": 3},
			map[string]any{"name": "Read stars", "get": "A", "state": 5},
			map[string]any{"get": "c_3", "state": 2, "name": "Scraped, unread", "shareRss": "1", "token": "abc123", "publishLabelsInsteadOfTags": true},
			"not a query",
		})
		s.asAlice()
		yes := true
		for n, want := range map[int]store.Listing{
			0: {Set: mainStream(), UnreadOrFavorite: true, Ascending: true},
			1: {Set: store.EntrySet{Labeled: true}},
			2: {},
			3: {Set: store.EntrySet{MinPriority: ptr(-5)}, Favorite: &yes},
			4: {Set: store.EntrySet{CategoryID: 3, MinPriority: ptr(0)}, Read: ptr(false)},
		} {
			target := "/queries/" + strconv.Itoa(n) + "?nb=1"
			if got, want := listed(s.page(target)), s.stored("alice", want); !reflect.DeepEqual(got, want[:min(len(want), 20)]) || len(want) == 0 {
				t.Errorf("GET %s lists %v, want %v", target, got, want)
			}
		}
		// A read state the reading screen has no name for is listed exactly
		// in the feed of the query.
		s.setting("alice", "token", "main")
		a := s.get("/rss?user=alice&token=main&get=A&state=5&nb=100")
		if got, want := feedItems(t, a.body), s.stored("alice", store.Listing{Set: store.EntrySet{MinPriority: ptr(-5)}, Favorite: &yes, Read: &yes}); !reflect.DeepEqual(got, want) {
			t.Errorf("read starred entries by token = %v, want %v", got, want)
		}
		if queries := readQueries(s.user("alice")); len(queries) != 6 || !queries[4].ShareRss || !queries[4].IncludeUserLabels || !queries[4].ExcludeArticleTags || queries[5].Name != "" {
			t.Errorf("queries as read = %+v", queries)
		}
		// The form of an imported query stores it as it was where it shows no change.
		s.follow("/settings/queries/0", s.formAt("/settings/queries/0", "/settings/queries/0"))
		if got := s.queries("alice")[0]; got["state"] != 66.0 || got["url"] != "./?get=a" || got["order"] != "ASC" {
			t.Errorf("an imported query after its form as shown = %v", got)
		}
	})
}

var sharedLink = regexp.MustCompile(`<a href="(/shared/[0-9a-f]+)\.rss">`)

// R12: a query that is handed out answers without a login at its secret
// address, in every format, and stops when the secret changes.
func TestSharedQuery(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.clock()
		s.asAlice()
		s.follow("/queries", url.Values{"name": {"Blogs & more"}, "stream": {"/categories/2"}, "state": {"all"}})
		form := s.formAt("/settings/queries/0", "/settings/queries/0")
		form.Set("share_rss", "1")
		form.Set("description", "What I read")
		form.Set("image_url", "https://example.org/logo.png")
		_, body := s.follow("/settings/queries/0", form)
		link := sharedLink.FindStringSubmatch(body)
		token, _ := s.queries("alice")[0]["token"].(string)
		if link == nil || len(token) != 32 || link[1] != "/shared/"+token || strings.Contains(body, token+".opml") {
			t.Fatalf("the page of a shared query links to %v, the token is %q", link, token)
		}
		base := link[1]
		want := s.stored("alice", store.Listing{Set: store.EntrySet{CategoryID: 2, MinPriority: ptr(0)}})

		// Nobody is logged in from here on.
		s.cookies = nil
		rss := s.get(base + ".rss")
		if rss.status != http.StatusOK || !strings.HasPrefix(rss.header.Get("Content-Type"), "application/rss+xml") ||
			rss.header.Get("Access-Control-Allow-Origin") != "*" || rss.header.Get("Cache-Control") != "public, max-age=60" ||
			!strings.Contains(rss.header.Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("GET %s.rss: status %d, headers %v", base, rss.status, rss.header)
		}
		if got := feedItems(t, rss.body); !reflect.DeepEqual(got, want) || len(want) == 0 {
			t.Errorf("the feed lists %v, want %v", got, want)
		}
		for _, text := range []string{
			"<title>Blogs &amp; more</title>", "<description>What I read</description>", "<url>https://example.org/logo.png</url>",
			`<atom:link href="` + base + `.rss" rel="self"`, "<link>" + base + ".html</link>", "<description><![CDATA[", "<pubDate>",
		} {
			if !strings.Contains(rss.body, text) {
				t.Errorf("the feed lacks %q:\n%.1500s", text, rss.body)
			}
		}
		if strings.Contains(rss.body, "<category>later</category>") {
			t.Error("the feed names the labels of the user without being told to")
		}

		atom := s.get(base + ".atom")
		var feed struct {
			Title   string `xml:"title"`
			Entries []struct {
				ID string `xml:"id"`
			} `xml:"entry"`
		}
		if err := xml.Unmarshal([]byte(atom.body), &feed); err != nil || feed.Title != "Blogs & more" || len(feed.Entries) != len(want) ||
			!strings.HasPrefix(atom.header.Get("Content-Type"), "application/atom+xml") {
			t.Errorf("GET %s.atom: %v, %d entries, title %q", base, err, len(feed.Entries), feed.Title)
		}
		page := s.get(base + ".html")
		// The page is rendered for whoever asks: it is not for a cache to
		// hand to somebody else, as the documents are.
		if page.header.Get("Cache-Control") != "no-store" || !strings.Contains(page.body, `action="`+base+`.html"`) || !strings.Contains(page.body, `name="q"`) {
			t.Errorf("GET %s.html: Cache-Control %q, or no form of its own", base, page.header.Get("Cache-Control"))
		}
		if page.status != http.StatusOK || !strings.Contains(page.body, "<h1>Blogs &amp; more</h1>") || strings.Count(page.body, `<article class="entry single"`) != len(want) ||
			strings.Contains(page.body, `href="/feeds/`) || strings.Contains(page.body, `action="/entries/`) {
			t.Errorf("GET %s.html: status %d\n%.800s", base, page.status, page.body)
		}

		// A visitor narrows the query, and changes its order and its length,
		// but does not get at the saved searches of the owner.
		words := `intitle:"` + s.entry("alice", want[0]).Title + `"`
		narrowed, _ := search.Parse(words, search.Options{})
		found := s.stored("alice", store.Listing{Set: store.EntrySet{CategoryID: 2, MinPriority: ptr(0)}, Search: narrowed})
		if got := feedItems(t, s.get(base+".rss?q="+url.QueryEscape(words)).body); !reflect.DeepEqual(got, found) || len(found) == 0 || len(found) == len(want) {
			t.Errorf("the feed narrowed by a visitor lists %v, want %v of %d", got, found, len(want))
		}
		if got := feedItems(t, s.get(base+".rss?order=ASC&nb=3").body); len(got) != 3 || got[0] != want[len(want)-1] {
			t.Errorf("the feed, oldest first and three long, lists %v", got)
		}
		if got := feedItems(t, s.get(base+".rss?q="+url.QueryEscape(`search:"Blogs & more" S:0`)).body); len(got) != len(want) {
			t.Errorf("a visitor referring to the saved searches of the owner gets %d entries, want all %d", len(got), len(want))
		}
		if a := s.get(base + ".rss?q=" + url.QueryEscape("/(?=a)/")); a.status != http.StatusBadRequest {
			t.Errorf("a visitor's search that cannot be read: status %d", a.status)
		}

		// What is not handed out is not there.
		for _, target := range []string{base + ".json", base + ".opml", base + ".txt", base, "/shared/" + strings.Repeat("0", 32) + ".rss", "/shared/..rss", "/shared/a%00b.rss"} {
			if a := s.get(target); a.status != http.StatusNotFound && a.status != http.StatusBadRequest {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
		}

		// The address FreshRSS gave the query answers alike.
		legacy := s.get("/api/query.php?user=alice&t=" + token + "&f=rss")
		if !reflect.DeepEqual(feedItems(t, legacy.body), want) {
			t.Errorf("the address of FreshRSS lists %v", feedItems(t, legacy.body))
		}
		if a := s.get("/api/query.php?user=alice&t=" + token + "&f=atom&search=" + url.QueryEscape(words)); !reflect.DeepEqual(feedItems(t, a.body), found) {
			t.Errorf("the address of FreshRSS with a search: status %d, lists %v", a.status, feedItems(t, a.body))
		}
		// Its page searches where FreshRSS has the search.
		legacyPage := s.get("/api/query.php?user=alice&t=" + token + "&f=html")
		for _, field := range []string{`action="/api/query.php"`, `name="user" value="alice"`, `name="t" value="` + token + `"`, `name="f" value="html"`, `name="search"`} {
			if !strings.Contains(legacyPage.body, field) {
				t.Errorf("the page at the address of FreshRSS lacks %s in its form", field)
			}
		}
		if a := s.get("/api/query.php?user=alice&t=" + token + "&f=html&search=" + url.QueryEscape(words)); strings.Count(a.body, `<article class="entry single"`) != len(found) {
			t.Errorf("the page at the address of FreshRSS with a search: status %d", a.status)
		}
		for target, status := range map[string]int{
			"/api/query.php?user=bob&t=" + token + "&f=rss": http.StatusNotFound, "/api/query.php?user=alice&t=" + token + "&f=pdf": http.StatusUnprocessableEntity,
			"/api/query.php?user=alice&t=no-token&f=rss": http.StatusUnprocessableEntity, "/api/query.php?user=a%20b&t=" + token + "&f=rss": http.StatusUnprocessableEntity,
			"/api/query.php?user=alice&t=" + token + "&f=greader": http.StatusNotFound,
		} {
			if a := s.get(target); a.status != status {
				t.Errorf("GET %s: status %d, want %d", target, a.status, status)
			}
		}

		// OPML and JSON are opened by their own switch; labels and tags
		// follow the settings of the query.
		s.asAlice()
		form = s.formAt("/settings/queries/0", "/settings/queries/0")
		form.Set("share_opml", "1")
		form.Set("include_labels", "1")
		form.Set("label_prefix", "label: ")
		form.Set("exclude_tags", "1")
		s.follow("/settings/queries/0", form)
		if s.queries("alice")[0]["token"] != token {
			t.Error("sharing in one more format changed the secret")
		}
		s.cookies = nil
		var document struct {
			ID    string
			Items []struct {
				ID         string `json:"frss:id"`
				Categories []string
			}
		}
		a := s.get(base + ".json")
		if err := json.Unmarshal([]byte(a.body), &document); err != nil || document.ID != "user/alice/state/org.freshrss/query/"+token || len(document.Items) != len(want) || document.Items[0].ID != strconv.FormatInt(want[0], 10) {
			t.Errorf("GET %s.json: %v\n%.600s", base, err, a.body)
		}
		for _, item := range document.Items {
			for _, category := range item.Categories {
				if strings.HasPrefix(category, "user/-/label/") {
					t.Errorf("the JSON of a shared query names a label of the user: %s", category)
				}
			}
		}
		outlines := s.get(base + ".opml")
		if outlines.status != http.StatusOK || !strings.Contains(outlines.body, `<outline text="Blogs">`) || !strings.Contains(outlines.body, `xmlUrl="http://feeds.freshgo.test/atom.xml"`) ||
			strings.Contains(outlines.body, "Scraped") || strings.Contains(outlines.body, "frss:CURLOPT") {
			t.Errorf("GET %s.opml: status %d\n%s", base, outlines.status, outlines.body)
		}
		if body := s.get(base + ".rss").body; !strings.Contains(body, "<category>label: later</category>") || strings.Contains(body, "<category>go</category>") {
			t.Errorf("the feed with labels in place of tags:\n%.1500s", body)
		}
		if greader := s.get("/api/query.php?user=alice&t=" + token + "&f=greader"); greader.status != http.StatusOK || !strings.HasPrefix(greader.body, `{"id":"user/alice/`) {
			t.Errorf("the address of FreshRSS in the format greader: status %d", greader.status)
		}

		// With the API off nothing is handed out; a disabled owner hands out nothing.
		s.system(func(system *store.System) { system.APIEnabled = false })
		if a := s.get(base + ".rss"); a.status != http.StatusServiceUnavailable {
			t.Errorf("a shared query with the API off: status %d", a.status)
		}
		s.system(func(system *store.System) { system.APIEnabled = true })
		s.setting("alice", "enabled", false)
		if a := s.get(base + ".rss"); a.status != http.StatusNotFound {
			t.Errorf("the shared query of a disabled user: status %d", a.status)
		}
		s.setting("alice", "enabled", true)

		// Another secret closes the old address; no sharing closes both.
		s.asAlice()
		_, body = s.follow("/settings/queries/0", url.Values{"action": {"token"}})
		fresh, _ := s.queries("alice")[0]["token"].(string)
		if fresh == token || len(fresh) != 32 || !strings.Contains(notice(body), "old ones no longer work") {
			t.Fatalf("after changing the secret: %q, notice %q", fresh, notice(body))
		}
		form = s.formAt("/settings/queries/0", "/settings/queries/0")
		s.cookies = nil
		if old, now := s.get(base+".rss"), s.get("/shared/"+fresh+".rss"); old.status != http.StatusNotFound || now.status != http.StatusOK {
			t.Errorf("after changing the secret the old address answers %d, the new one %d", old.status, now.status)
		}
		s.asAlice()
		form.Del("share_rss")
		form.Del("share_opml")
		s.follow("/settings/queries/0", form)
		s.cookies = nil
		for _, format := range []string{"rss", "json", "html"} {
			if a := s.get("/shared/" + fresh + "." + format); a.status != http.StatusNotFound {
				t.Errorf("a query no longer shared, as %s: status %d", format, a.status)
			}
		}
	})
}

// R12: the token of a user opens their entries as a feed and their
// subscriptions as OPML, also at the addresses FreshRSS had for them.
func TestTokenFeeds(t *testing.T) {
	imported(t, Options{BaseURL: "https://reader.example"}, func(t *testing.T, s *site) {
		s.clock()
		s.setting("alice", "token", "s3cret")
		want := s.stored("alice", store.Listing{Set: mainStream(), Limit: 20})
		for _, target := range []string{"/rss?user=alice&token=s3cret&state=3", "/i/?a=rss&user=alice&token=s3cret&state=3"} {
			a := s.get(target)
			if got := feedItems(t, a.body); a.status != http.StatusOK || !reflect.DeepEqual(got, want) {
				t.Errorf("GET %s: status %d, lists %v, want %v", target, a.status, got, want)
			}
			if !strings.Contains(a.body, `<atom:link href="https://reader.example/rss?token=s3cret&amp;user=alice"`) {
				t.Errorf("GET %s: the feed does not give its public address:\n%.600s", target, a.body)
			}
		}
		if got, want := feedItems(t, s.get("/rss?user=alice&token=s3cret&get=f_2&state=3&order=ASC&nb=2").body), s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 2}, Ascending: true})[:2]; !reflect.DeepEqual(got, want) {
			t.Errorf("the feed of one feed by token lists %v, want %v", got, want)
		}
		for _, target := range []string{"/opml?user=alice&token=s3cret", "/i/?a=opml&user=alice&token=s3cret"} {
			if a := s.get(target); a.status != http.StatusOK || !strings.Contains(a.body, `xmlUrl="http://feeds.freshgo.test/rss.xml"`) || !strings.Contains(a.body, "<opml") {
				t.Errorf("GET %s: status %d\n%.300s", target, a.status, a.body)
			}
		}
		for _, target := range []string{
			"/rss?user=alice&token=wrong", "/rss?user=alice", "/rss?user=bob&token=s3cret", "/rss?user=bob&token=", "/rss?token=s3cret", "/opml?user=alice&token=S3CRET",
			"/i/?a=rss&user=alice&token=nope",
		} {
			if a := s.get(target); a.status != http.StatusForbidden {
				t.Errorf("GET %s: status %d, want 403", target, a.status)
			}
		}
		if a := s.get("/rss?user=alice&token=s3cret&get=f_99"); a.status != http.StatusNotFound {
			t.Errorf("the feed of a feed there is not: status %d", a.status)
		}
		// Everything else FreshRSS had under /i/ leads to the start page.
		for _, target := range []string{"/i/", "/i/?c=subscription", "/i/index.php?a=normal&get=c_2"} {
			if a := s.get(target); a.status != http.StatusSeeOther || a.header.Get("Location") != "/" {
				t.Errorf("GET %s: status %d, Location %q", target, a.status, a.header.Get("Location"))
			}
		}
		s.asAlice()
		if body := s.shown("/settings/profile"); !strings.Contains(body, `href="https://reader.example/rss?token=s3cret&amp;user=alice"`) {
			t.Errorf("the profile does not give the address of the feed:\n%s", body)
		}
	})
}

// rssItem is an entry of an RSS document, in what a reader of the feed gets.
type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Creators    []string `xml:"creator"`
	Categories  []string `xml:"category"`
	Description string   `xml:"description"`
	PubDate     string   `xml:"pubDate"`
	GUID        string   `xml:"guid"`
	Thumbnail   []struct {
		URL string `xml:"url,attr"`
	} `xml:"thumbnail"`
	Content []struct {
		URL    string `xml:"url,attr"`
		Medium string `xml:"medium,attr"`
		Type   string `xml:"type,attr"`
		Length string `xml:"length,attr"`
	} `xml:"content"`
}

type rssChannel struct {
	Title       string    `xml:"channel>title"`
	Description string    `xml:"channel>description"`
	Items       []rssItem `xml:"channel>item"`
}

// R12: the feed and the JSON of a shared query are those FreshRSS 1.30.1
// answers with for the same query over the same installation: the entries,
// their order and everything a reader gets of each.
func TestSharedMatchesFreshRSS(t *testing.T) {
	const reference = "../../testdata/reference/queries/"
	var queries []map[string]any
	raw, err := os.ReadFile(reference + "queries.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &queries); err != nil {
		t.Fatal(err)
	}
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "queries", queries)
		// The reference was made on a server in UTC.
		s.setting("alice", "timezone", "UTC")
		compare := func(target, file string) {
			t.Helper()
			want, err := os.ReadFile(reference + file)
			if err != nil {
				t.Fatal(err)
			}
			a := s.get(target)
			if a.status != http.StatusOK {
				t.Fatalf("GET %s: status %d\n%s", target, a.status, a.body)
			}
			if strings.HasSuffix(file, ".rss") {
				var ours, theirs rssChannel
				if err := xml.Unmarshal([]byte(a.body), &ours); err != nil {
					t.Fatalf("GET %s: %v", target, err)
				}
				if err := xml.Unmarshal(want, &theirs); err != nil {
					t.Fatal(err)
				}
				if len(theirs.Items) == 0 && !strings.Contains(file, "search") {
					t.Fatalf("%s lists nothing: the reference proves nothing", file)
				}
				for i := range min(len(ours.Items), len(theirs.Items)) {
					if !reflect.DeepEqual(ours.Items[i], theirs.Items[i]) {
						t.Errorf("GET %s, item %d:\n got %+v\nwant %+v", target, i, ours.Items[i], theirs.Items[i])
						return
					}
				}
				if len(ours.Items) != len(theirs.Items) || ours.Title != theirs.Title || ours.Description != theirs.Description {
					t.Errorf("GET %s differs from %s: %d items, want %d; %q %q, want %q %q", target, file, len(ours.Items), len(theirs.Items),
						ours.Title, ours.Description, theirs.Title, theirs.Description)
				}
				return
			}
			var ours, theirs map[string]any
			if err := json.Unmarshal([]byte(a.body), &ours); err != nil {
				t.Fatalf("GET %s: %v", target, err)
			}
			if err := json.Unmarshal(want, &theirs); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ours, theirs) {
				gotItems, _ := ours["items"].([]any)
				wantItems, _ := theirs["items"].([]any)
				for i := range min(len(gotItems), len(wantItems)) {
					if !reflect.DeepEqual(gotItems[i], wantItems[i]) {
						t.Errorf("GET %s, item %d:\n got %v\nwant %v", target, i, gotItems[i], wantItems[i])
						return
					}
				}
				t.Errorf("GET %s differs from %s: %d items, want %d; head %v, want %v", target, file, len(gotItems), len(wantItems), ours["title"], theirs["title"])
			}
		}
		for _, q := range queries {
			token := q["token"].(string)
			compare("/api/query.php?user=alice&t="+token+"&f=rss&nb=100", token+".rss")
			compare("/api/query.php?user=alice&t="+token+"&f=greader&nb=100", token+".greader")
		}
		compare("/api/query.php?user=alice&t=tokenblogs1&f=rss&nb=100&search=intitle%3ARSS", "tokenblogs1.search.rss")
	})
}
