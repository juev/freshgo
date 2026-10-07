package greader

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// These tests cover what the reference installation has no example of. What
// they expect comes from reading the code of FreshRSS, not from running it,
// unless a test says otherwise.

const second = 1_000_000

// world is a database with the user alice and the API over it.
type world struct {
	t        *testing.T
	db       *store.Store
	registry *hooks.Registry
	server   *httptest.Server
	alice    *store.User
	// auth is the Authorization header of alice.
	auth string
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			w := &world{t: t, db: db, registry: &hooks.Registry{}}
			client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			w.server = httptest.NewServer(New(Options{
				DB: db, Refresher: refresh.New(db, client, w.registry, log), Hooks: w.registry, Log: log,
			}))
			t.Cleanup(w.server.Close)
			w.alice = w.user("alice", "")
			w.auth = w.login("alice")
			test(t, w)
		})
	}
}

// user creates a user whose API password is "<name>-password".
func (w *world) user(name, settings string) *store.User {
	w.t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(name+"-password"), bcrypt.MinCost)
	if err != nil {
		w.t.Fatal(err)
	}
	u := &store.User{Name: name, APIPasswordHash: string(hash), Settings: json.RawMessage(settings)}
	if err := w.db.CreateUser(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
	return u
}

func (w *world) login(name string) string {
	w.t.Helper()
	status, body := w.post("", "/accounts/ClientLogin", url.Values{"Email": {name}, "Passwd": {name + "-password"}}.Encode())
	for _, line := range strings.Split(body, "\n") {
		if token, ok := strings.CutPrefix(line, "Auth="); ok {
			return "GoogleLogin auth=" + token
		}
	}
	w.t.Fatalf("login of %s: status %d, body %q", name, status, body)
	return ""
}

func (w *world) do(method, auth, pathAndQuery, body string) (int, string) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.server.URL+pathAndQuery, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func (w *world) get(auth, pathAndQuery string) (int, string) {
	w.t.Helper()
	return w.do(http.MethodGet, auth, pathAndQuery, "")
}

func (w *world) post(auth, pathAndQuery, body string) (int, string) {
	w.t.Helper()
	return w.do(http.MethodPost, auth, pathAndQuery, body)
}

// ok is a POST by alice that has to be answered with OK.
func (w *world) ok(pathAndQuery, body string) {
	w.t.Helper()
	if status, answer := w.post(w.auth, pathAndQuery, body); status != http.StatusOK || answer != "OK" {
		w.t.Fatalf("POST %s %s: status %d, body %q; want OK", pathAndQuery, body, status, answer)
	}
}

// json is a GET by alice decoded into v.
func (w *world) json(pathAndQuery string, v any) {
	w.t.Helper()
	status, body := w.get(w.auth, pathAndQuery)
	if status != http.StatusOK {
		w.t.Fatalf("GET %s: status %d, body %q", pathAndQuery, status, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		w.t.Fatalf("GET %s: %v in %s", pathAndQuery, err, body)
	}
}

func (w *world) feed(u *store.User, f *store.Feed) *store.Feed {
	w.t.Helper()
	f.UserID = u.ID
	if f.URL == "" {
		f.URL = "https://example.org/" + strconv.Itoa(int(time.Now().UnixNano()))
	}
	if err := w.db.CreateFeed(context.Background(), f); err != nil {
		w.t.Fatal(err)
	}
	return f
}

func (w *world) entries(u *store.User, entries ...*store.Entry) {
	w.t.Helper()
	for _, e := range entries {
		if e.GUID == "" {
			e.GUID = strconv.FormatInt(e.ID, 10)
		}
	}
	if err := w.db.InsertEntries(context.Background(), u.ID, entries); err != nil {
		w.t.Fatal(err)
	}
}

type shownItem struct {
	ID         string
	Title      string
	Categories []string
	Canonical  []struct{ Href string }
	Summary    struct{ Content string }
	Enclosure  []map[string]any
	Author     string
	Origin     struct{ StreamID, Title, HTMLURL string }
}

// items asks for entries by identifier.
func (w *world) items(ids ...int64) []shownItem {
	w.t.Helper()
	form := url.Values{}
	for _, id := range ids {
		form.Add("i", strconv.FormatInt(id, 10))
	}
	status, body := w.post(w.auth, "/reader/api/0/stream/items/contents", form.Encode())
	if status != http.StatusOK {
		w.t.Fatalf("items: status %d, body %q", status, body)
	}
	var stream struct{ Items []shownItem }
	if err := json.Unmarshal([]byte(body), &stream); err != nil {
		w.t.Fatalf("items: %v in %s", err, body)
	}
	return stream.Items
}

func (w *world) ids(query string) []string {
	w.t.Helper()
	var answer struct {
		ItemRefs []struct{ ID string }
	}
	w.json("/reader/api/0/stream/items/ids?"+query, &answer)
	var ids []string
	for _, ref := range answer.ItemRefs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// A hidden feed is in no list and no stream but its own; its entries say
// that they are hidden.
func TestHiddenFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		shown := w.feed(w.alice, &store.Feed{Name: "Shown", Priority: priorityMain})
		hidden := w.feed(w.alice, &store.Feed{Name: "Hidden", Priority: priorityHidden})
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: shown.ID, Title: "shown"},
			&store.Entry{ID: 2 * second, FeedID: hidden.ID, Title: "hidden", IsFavorite: true})

		var list struct {
			Subscriptions []struct{ ID string }
		}
		w.json("/reader/api/0/subscription/list?output=json", &list)
		if len(list.Subscriptions) != 1 || list.Subscriptions[0].ID != "feed/1" {
			t.Errorf("subscriptions = %+v, want only the feed that is shown", list.Subscriptions)
		}
		var counts struct {
			Max          int
			Unreadcounts []struct {
				ID    string
				Count int
			}
		}
		w.json("/reader/api/0/unread-count?output=json", &counts)
		if counts.Max != 1 || len(counts.Unreadcounts) != 3 {
			t.Errorf("unread counts = %+v, want the shown feed, its category and the total", counts)
		}
		for s, want := range map[string][]string{
			stateReadingList:                        {"1000000"},
			stateStarred:                            nil,
			labelPrefix + store.DefaultCategoryName: {"1000000"},
		} {
			if got := w.ids("n=10&s=" + s); !reflect.DeepEqual(got, want) {
				t.Errorf("ids of %s = %v, want %v", s, got, want)
			}
		}
		if got := w.ids("n=10&s=feed/2"); !reflect.DeepEqual(got, []string{"2000000"}) {
			t.Errorf("ids of the hidden feed itself = %v, want its entry", got)
		}
		items := w.items(2 * second)
		want := []string{stateReadingList, labelPrefix + store.DefaultCategoryName, stateHidden, stateStarred}
		if len(items) != 1 || !reflect.DeepEqual(items[0].Categories, want) {
			t.Errorf("entry of the hidden feed = %+v, want the categories %v", items, want)
		}
		// Marking everything read leaves the hidden feed alone.
		w.ok("/reader/api/0/mark-all-as-read", "s="+stateReadingList)
		if got := w.ids("n=10&xt=" + stateRead + "&s=feed/2"); !reflect.DeepEqual(got, []string{"2000000"}) {
			t.Errorf("unread of the hidden feed after marking the reading list = %v, want it unread", got)
		}
	})
}

// How an entry is shown when it lacks a title or a link, and how a feed
// without a name is called.
func TestItemFallbacks(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{URL: "https://www.example.org/feed?a=1", Priority: priorityMain})
		long := strings.Repeat("ж", 80)
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: f.ID, GUID: "https://example.org/?a=1&amp;b=2",
				Content: "<p>Short &amp; <b>sweet</b></p>", Authors: []string{"A & B", "C"}},
			&store.Entry{ID: 2 * second, FeedID: f.ID, GUID: "urn:x&lt;y", Content: " <p>" + long + "</p>", Link: "https://example.org/2"},
			&store.Entry{ID: 3 * second, FeedID: f.ID, GUID: "opaque&amp;id", Content: "<img src=\"x.png\">"})

		items := w.items(1*second, 2*second, 3*second)
		if len(items) != 3 {
			t.Fatalf("%d items, want 3", len(items))
		}
		third, second, first := items[0], items[1], items[2]
		// The text stands in for the title; the link falls back to an
		// identifier that is an address.
		if first.Title != "Short ＆ sweet" || first.Canonical[0].Href != "https://example.org/?a=1&b=2" || first.Author != "A ＆ B; C" {
			t.Errorf("first = title %q, link %q, author %q", first.Title, first.Canonical[0].Href, first.Author)
		}
		if want := strings.Repeat("ж", 75) + "…"; second.Title != want || second.Canonical[0].Href != "https://example.org/2" {
			t.Errorf("second = title %q, link %q; want the first 75 characters of the text and its own link", second.Title, second.Canonical[0].Href)
		}
		// No text either: the identifier; and no address to link to.
		if third.Title != "opaque＆id" || third.Canonical[0].Href != "" {
			t.Errorf("third = title %q, link %q; want the identifier and no link", third.Title, third.Canonical[0].Href)
		}
		if first.Origin.Title != "example.org／feed？a=1" {
			t.Errorf("name of a feed without one = %q, want its address without the scheme", first.Origin.Title)
		}
	})
}

// Attachments of an entry are listed and, unless the feed says otherwise,
// written out after its text.
func TestEnclosures(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Priority: priorityMain})
		plain := w.feed(w.alice, &store.Feed{Priority: priorityMain, Attributes: []byte(`{"display_enclosures":false}`)})
		const attributes = `{"thumbnail":{"url":"https://example.org/thumb.jpg"},"enclosures":[
			{"url":"https://example.org/pic","medium":"image","title":"A picture","thumbnails":["https://example.org/small.jpg","ftp://x"]},
			{"url":"https://example.org/clip.mp4","type":"video/mp4","length":"1000","credit":["Ann","Bob"],"description":"one\ntwo"},
			{"url":"https://example.org/doc.pdf","type":"application/pdf","medium":"document","credit":"Carl"},
			{"url":"https://example.org/inline.png"},
			{"url":"/relative.png"},
			{"url":"https://example.org/bare.png","length":0}]}`
		const text = `<p><img src="https://example.org/inline.png"></p>`
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: f.ID, Title: "with", Content: text, Attributes: []byte(attributes)},
			&store.Entry{ID: 2 * second, FeedID: plain.ID, Title: "without", Content: text, Attributes: []byte(attributes)},
			// As FreshRSS before 1.20.1 stored attachments: in the text only.
			&store.Entry{ID: 3 * second, FeedID: f.ID, Title: "legacy", Content: `<p>text</p><div class="enclosure"><p class="enclosure-content">` +
				`<audio src="https://example.org/a.mp3" data-type="audio/mpeg" data-length="5"></audio></p></div>` +
				`<div class="enclosure"><p class="enclosure-content"><img src="https://example.org/b.png"></p></div>`})

		items := w.items(1*second, 2*second, 3*second)
		legacy, without, with := items[0], items[1], items[2]

		wantContent := text +
			"<figure class=\"enclosure\">\n\t<p class=\"enclosure-content\">\n\t\t<img class=\"enclosure-thumbnail\" src=\"https://example.org/thumb.jpg\" alt=\"\" />\n\t</p>\n</figure>" +
			"\n<figure class=\"enclosure\"><p><img class=\"enclosure-thumbnail\" src=\"https://example.org/small.jpg\" alt=\"\" title=\"A picture\" /></p>" +
			"<p class=\"enclosure-content\"><img src=\"https://example.org/pic\" alt=\"\" title=\"A picture\" /></p></figure>\n" +
			"\n<figure class=\"enclosure\"><p class=\"enclosure-content\"><video preload=\"none\" src=\"https://example.org/clip.mp4\" data-length=\"1000\" data-type=\"video/mp4\" controls=\"controls\" title=\"\"></video>" +
			" <a download=\"\" href=\"https://example.org/clip.mp4\">💾</a></p><p class=\"enclosure-credits\">© Ann</p><p class=\"enclosure-credits\">© Bob</p>" +
			"<figcaption class=\"enclosure-description\">one<br />\ntwo</figcaption></figure>\n" +
			"\n<figure class=\"enclosure\"><p class=\"enclosure-content\"><a download=\"\" href=\"https://example.org/doc.pdf\" data-type=\"application/pdf\" data-medium=\"document\" title=\"\">💾</a></p>" +
			"<p class=\"enclosure-credits\">© Carl</p></figure>\n" +
			"\n<figure class=\"enclosure\"><p class=\"enclosure-content\"><img src=\"https://example.org/bare.png\" alt=\"\" title=\"\" /></p></figure>\n"
		if with.Summary.Content != wantContent {
			t.Errorf("text with attachments =\n%q\nwant\n%q", with.Summary.Content, wantContent)
		}
		if without.Summary.Content != text {
			t.Errorf("text of a feed that hides attachments = %q, want the text alone", without.Summary.Content)
		}
		wantList := []map[string]any{
			{"href": "https://example.org/pic", "type": "image"},
			{"href": "https://example.org/clip.mp4", "type": "video/mp4", "length": 1000.0},
			{"href": "https://example.org/doc.pdf", "type": "application/pdf"},
			{"href": "https://example.org/inline.png", "type": "image"},
			{"href": "/relative.png", "type": "image"},
			{"href": "https://example.org/bare.png", "type": "image"},
		}
		if !reflect.DeepEqual(with.Enclosure, wantList) || !reflect.DeepEqual(without.Enclosure, wantList) {
			t.Errorf("attachments = %v and %v, want %v", with.Enclosure, without.Enclosure, wantList)
		}
		wantLegacy := []map[string]any{
			{"href": "https://example.org/a.mp3", "type": "audio/mpeg", "length": 5.0},
			{"href": "https://example.org/b.png", "type": ""},
		}
		if !reflect.DeepEqual(legacy.Enclosure, wantLegacy) {
			t.Errorf("attachments written in the text = %v, want %v", legacy.Enclosure, wantLegacy)
		}
	})
}

// A text too long for some clients is cut without splitting a character.
func TestLongContentIsCut(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Priority: priorityMain})
		w.entries(w.alice, &store.Entry{ID: 1 * second, FeedID: f.ID, Title: "long", Content: "a" + strings.Repeat("ж", maxContent/2)})
		got := w.items(1 * second)[0].Summary.Content
		if len(got) != maxContent-1 || !utf8.ValidString(got) {
			t.Errorf("text is %d bytes (valid UTF-8: %v), want %d whole characters", len(got), utf8.ValidString(got), maxContent-1)
		}
	})
}

// R8: extensions see entries on their way out and are told what the user
// has read and starred.
func TestHooks(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Priority: priorityMain})
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: f.ID, Title: "kept", IsRead: true},
			&store.Entry{ID: 2 * second, FeedID: f.ID, Title: "secret"},
			&store.Entry{ID: 3 * second, FeedID: f.ID, Title: "third"})
		var read []hooks.EntriesRead
		var starred []hooks.EntriesFavorite
		w.registry.EntryBeforeDisplay.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
			shown := *e
			shown.Title = strings.ToUpper(e.Title)
			return &shown, e.Title != "secret"
		})
		w.registry.EntriesRead.Add(0, func(_ context.Context, e hooks.EntriesRead) bool { read = append(read, e); return false })
		w.registry.EntriesFavorite.Add(0, func(_ context.Context, e hooks.EntriesFavorite) bool { starred = append(starred, e); return false })

		var titles []string
		for _, it := range w.items(1*second, 2*second, 3*second) {
			titles = append(titles, it.Title)
		}
		if !reflect.DeepEqual(titles, []string{"THIRD", "KEPT"}) {
			t.Errorf("titles = %v, want the changed ones without the entry held back", titles)
		}
		// The page is cut after the entries are held back: the second page
		// starts with the last entry of the first.
		var page struct {
			Items        []shownItem
			Continuation string
		}
		w.json("/reader/api/0/stream/contents/"+stateReadingList+"?n=1", &page)
		if len(page.Items) != 1 || page.Items[0].Title != "THIRD" || page.Continuation != "3000000" {
			t.Errorf("first page = %+v", page)
		}

		// Entry 1 is read already: nothing changes, nobody is told.
		w.ok("/reader/api/0/edit-tag", "a="+stateRead+"&i=1000000")
		if len(read) != 0 {
			t.Errorf("EntriesRead after reading a read entry = %+v, want no call", read)
		}
		w.ok("/reader/api/0/edit-tag", "a="+stateRead+"&a="+stateStarred+"&i=1000000&i=3000000")
		w.ok("/reader/api/0/edit-tag", "r="+stateStarred+"&i=3000000")
		wantRead := []hooks.EntriesRead{{UserID: w.alice.ID, IDs: []int64{1 * second, 3 * second}, IsRead: true}}
		wantStarred := []hooks.EntriesFavorite{
			{UserID: w.alice.ID, IDs: []int64{1 * second, 3 * second}, IsFavorite: true},
			{UserID: w.alice.ID, IDs: []int64{3 * second}, IsFavorite: false},
		}
		if !reflect.DeepEqual(read, wantRead) || !reflect.DeepEqual(starred, wantStarred) {
			t.Errorf("EntriesRead = %+v, EntriesFavorite = %+v; want %+v and %+v", read, starred, wantRead, wantStarred)
		}
	})
}

// One user reaches nothing of another through identifiers they share.
func TestUsersAreApart(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		bob := w.user("bob", "")
		bobAuth := w.login("bob")
		mine := w.feed(w.alice, &store.Feed{Name: "Mine", Priority: priorityMain})
		theirs := w.feed(bob, &store.Feed{Name: "Theirs", Priority: priorityMain})
		w.entries(w.alice, &store.Entry{ID: 1 * second, FeedID: mine.ID, Title: "mine"})
		w.entries(bob, &store.Entry{ID: 1 * second, FeedID: theirs.ID, Title: "theirs"}, &store.Entry{ID: 2 * second, FeedID: theirs.ID, Title: "more"})

		if items := w.items(1*second, 2*second); len(items) != 1 || items[0].Title != "mine" {
			t.Errorf("items of alice = %+v, want her entry only", items)
		}
		w.ok("/reader/api/0/edit-tag", "a="+stateRead+"&a="+stateStarred+"&a=user/-/label/x&i=1000000&i=2000000")
		w.ok("/reader/api/0/mark-all-as-read", "s=feed/1")
		w.ok("/reader/api/0/subscription/edit", "ac=edit&s=feed/1&t=Renamed")
		for _, id := range []int64{1 * second, 2 * second} {
			e, err := w.db.EntryByID(context.Background(), bob.ID, id)
			if err != nil || e.IsRead || e.IsFavorite {
				t.Errorf("entry %d of bob = %+v, %v; want it untouched", id, e, err)
			}
		}
		if status, body := w.get(bobAuth, "/reader/api/0/tag/list?output=json"); status != http.StatusOK || strings.Contains(body, "label/x") {
			t.Errorf("tags of bob = %d %s, want none of alice's labels", status, body)
		}
		if f, err := w.db.FeedByID(context.Background(), bob.ID, theirs.ID); err != nil || f.Name != "Theirs" {
			t.Errorf("feed of bob = %+v, %v; want it untouched", f, err)
		}
		// A token is good for its user only.
		if status, _ := w.get("GoogleLogin auth=alice/"+strings.SplitN(bobAuth, "/", 2)[1], "/reader/api/0/token"); status != http.StatusUnauthorized {
			t.Errorf("token of bob under the name of alice: status %d, want 401", status)
		}
	})
}

func TestAccess(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		// A user FreshRSS has disabled, and one without an API password.
		w.user("carol", `{"enabled":false}`)
		carol := w.login("carol")
		if status, _ := w.get(carol, "/reader/api/0/token"); status != http.StatusUnauthorized {
			t.Errorf("token of a disabled user: status %d, want 401", status)
		}
		if err := w.db.CreateUser(ctx, &store.User{Name: "dave"}); err != nil {
			t.Fatal(err)
		}
		if status, _ := w.post("", "/accounts/ClientLogin", "Email=dave&Passwd="); status != http.StatusUnauthorized {
			t.Errorf("login without an API password: status %d, want 401", status)
		}

		// Every answer lets a web client of another origin read it and
		// keeps a browser from running it.
		req, err := http.NewRequest(http.MethodGet, w.server.URL+"/reader/api/0/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		for name, want := range map[string]string{
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Headers": "Authorization",
			"X-Content-Type-Options":       "nosniff",
			"Google-Bad-Token":             "true",
		} {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
	})
}

// R13: the list of subscriptions names the icon of every feed at the public
// address of the server, or at the address of the request without one.
func TestIconAddresses(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		site := w.feed(w.alice, &store.Feed{Name: "A", URL: "https://example.org/feed", Website: "https://example.org/", Priority: priorityMain})
		custom := w.feed(w.alice, &store.Feed{Name: "B", Priority: priorityMain, Attributes: []byte(`{"customFavicon":true}`)})
		salt, err := w.db.Salt(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var list struct {
			Subscriptions []struct{ IconURL string }
		}
		w.json("/reader/api/0/subscription/list?output=json", &list)
		want := []string{
			w.server.URL + "/favicon/" + favicon.Hash(salt, "https://example.org/"),
			w.server.URL + "/favicon/" + favicon.CustomHash(salt, w.alice.ID, custom.ID),
		}
		if len(list.Subscriptions) != 2 || list.Subscriptions[0].IconURL != want[0] || list.Subscriptions[1].IconURL != want[1] {
			t.Errorf("icons = %+v, want %v", list.Subscriptions, want)
		}
		if got := favicon.HashOf(salt, site); !strings.HasSuffix(want[0], got) {
			t.Errorf("HashOf = %q, not what the list names", got)
		}

		configured := New(Options{DB: w.db, Hooks: w.registry, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseURL: "https://rss.example.net/sub"})
		req := httptest.NewRequest(http.MethodGet, "/reader/api/0/subscription/list?output=json", nil)
		req.Header.Set("Authorization", w.auth)
		rec := httptest.NewRecorder()
		configured.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), `"iconUrl":"https://rss.example.net/sub/favicon/`) {
			t.Errorf("with a public address configured: %s", rec.Body.String())
		}
	})
}

// Where freshgo does not follow FreshRSS 1.30.1, on purpose; see Decisions
// in docs/specs/greader-api.md. What FreshRSS does was observed on the
// reference installation.
func TestDepartures(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		f := w.feed(w.alice, &store.Feed{Name: "Feed", Priority: priorityMain})
		w.entries(w.alice, &store.Entry{ID: 1 * second, FeedID: f.ID, Title: "entry"})
		if err := w.db.CreateTag(ctx, &store.Tag{UserID: w.alice.ID, Name: "taken"}); err != nil {
			t.Fatal(err)
		}

		// FreshRSS creates the label "padded" and leaves the entry without it.
		w.ok("/reader/api/0/edit-tag", "a=user/-/label/%20padded%20&i=1000000")
		if got := w.items(1 * second)[0].Categories; !reflect.DeepEqual(got[len(got)-1:], []string{"user/-/label/padded"}) {
			t.Errorf("categories after adding a padded label = %v, want the label on the entry", got)
		}

		// FreshRSS keeps showing the default category under its own name.
		w.ok("/reader/api/0/rename-tag", "s=user/-/label/"+store.DefaultCategoryName+"&dest=user/-/label/Inbox")
		if status, body := w.get(w.auth, "/reader/api/0/tag/list?output=json"); status != http.StatusOK || !strings.Contains(body, `"user/-/label/Inbox"`) {
			t.Errorf("tags after renaming the default category = %s", body)
		}
		// It cannot be deleted, renamed or not.
		w.ok("/reader/api/0/disable-tag", "s=user/-/label/Inbox")
		if got := w.ids("n=10&s=user/-/label/Inbox"); !reflect.DeepEqual(got, []string{"1000000"}) {
			t.Errorf("default category after deleting it = %v, want it and its feed in place", got)
		}

		// FreshRSS answers 500 and moves nothing; the name belongs to a label.
		other := &store.Category{UserID: w.alice.ID, Name: "Other"}
		if err := w.db.CreateCategory(ctx, other); err != nil {
			t.Fatal(err)
		}
		w.ok("/reader/api/0/subscription/edit", "ac=edit&s=feed/1&a=user/-/label/Other")
		w.ok("/reader/api/0/subscription/edit", "ac=edit&s=feed/1&a=user/-/label/taken")
		if got, err := w.db.FeedByID(ctx, w.alice.ID, f.ID); err != nil || got.CategoryID != store.DefaultCategoryID {
			t.Errorf("feed moved to a category named like a label = %+v, %v; want it in the default category", got, err)
		}
	})
}

// R15: an administrator turns the API off, and the limits of the
// installation hold for what clients add.
func TestSwitchAndLimits(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		system := func(change func(*store.System)) {
			t.Helper()
			s, err := w.db.System(ctx)
			if err != nil {
				t.Fatal(err)
			}
			change(&s)
			if err := w.db.SetSystem(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		system(func(s *store.System) { s.APIEnabled = false })
		for _, path := range []string{"/reader/api/0/tag/list?output=json", "/accounts/ClientLogin?Email=alice&Passwd=alice-password", "/api/greader.php/reader/api/0/token"} {
			if status, body := w.get(w.auth, path); status != http.StatusServiceUnavailable || body != "Service Unavailable!" {
				t.Errorf("GET %s with the API off: status %d, body %q", path, status, body)
			}
		}
		system(func(s *store.System) { s.APIEnabled, s.Limits.MaxCategories = true, 2 })
		if status, _ := w.get(w.auth, "/reader/api/0/tag/list?output=json"); status != http.StatusOK {
			t.Fatalf("the API switched on again: status %d", status)
		}

		// One category beside the default one is all alice may have: a feed
		// moved to one more goes to the default category.
		one := w.feed(w.alice, &store.Feed{URL: "http://one.example/feed", Name: "One"})
		two := w.feed(w.alice, &store.Feed{URL: "http://two.example/feed", Name: "Two"})
		_, token := w.get(w.auth, "/reader/api/0/token")
		move := func(f *store.Feed, category string) {
			t.Helper()
			form := url.Values{"ac": {"edit"}, "s": {"feed/" + strconv.FormatInt(f.ID, 10)}, "a": {"user/-/label/" + category}, "T": {strings.TrimSpace(token)}}
			if status, body := w.post(w.auth, "/reader/api/0/subscription/edit", form.Encode()); status != http.StatusOK {
				t.Fatalf("moving a feed: status %d, body %q", status, body)
			}
		}
		move(one, "First")
		move(two, "Second")
		categories, err := w.db.Categories(ctx, w.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		moved, _ := w.db.FeedByID(ctx, w.alice.ID, one.ID)
		kept, _ := w.db.FeedByID(ctx, w.alice.ID, two.ID)
		if len(categories) != 2 || moved.CategoryID == store.DefaultCategoryID || kept.CategoryID != store.DefaultCategoryID {
			t.Errorf("%d categories; the first feed is in %d, the second in %d", len(categories), moved.CategoryID, kept.CategoryID)
		}
	})
}
