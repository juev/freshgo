package fever

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/authguard"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// world is a database with the user alice, the Fever API on, and the handler
// over it.
type world struct {
	t        *testing.T
	db       *store.Store
	registry *hooks.Registry
	handler  *Handler
	server   *httptest.Server
	// now is what the handler takes the time to be, in nanoseconds.
	now   atomic.Int64
	alice *store.User
	// key is the Fever key of alice.
	key string
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
			w.now.Store(time.Unix(1_000_000, 0).UnixNano())
			w.handler = New(Options{DB: db, Hooks: w.registry, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			w.handler.now = func() time.Time { return time.Unix(0, w.now.Load()) }
			w.server = httptest.NewServer(w.handler)
			t.Cleanup(w.server.Close)
			w.system(func(s *store.System) { s.APIEnabled, s.FeverEnabled = true, true })
			w.alice = w.user("alice", "")
			w.key = store.FeverKey("alice", "alice-password")
			test(t, w)
		})
	}
}

func (w *world) system(change func(*store.System)) {
	w.t.Helper()
	ctx := context.Background()
	s, err := w.db.System(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	change(&s)
	if err := w.db.SetSystem(ctx, s); err != nil {
		w.t.Fatal(err)
	}
}

// user creates a user whose API password is "<name>-password".
func (w *world) user(name, settings string) *store.User {
	w.t.Helper()
	u := &store.User{Name: name, FeverKey: store.FeverKey(name, name+"-password"), Settings: json.RawMessage(settings)}
	if err := w.db.CreateUser(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
	return u
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

func (w *world) category(u *store.User, name string) *store.Category {
	w.t.Helper()
	c := &store.Category{UserID: u.ID, Name: name}
	if err := w.db.CreateCategory(context.Background(), c); err != nil {
		w.t.Fatal(err)
	}
	return c
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

// call posts a form with the key of alice to the address, as a client does.
func (w *world) call(query string, form url.Values) (int, string) {
	w.t.Helper()
	return w.callWith(w.key, query, form)
}

func (w *world) callWith(key, query string, form url.Values) (int, string) {
	w.t.Helper()
	body := url.Values{"api_key": {key}}
	for k, v := range form {
		body[k] = v
	}
	resp, err := http.Post(w.server.URL+Path+"?"+query, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
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

// ask is a call that has to be answered with a document of alice.
func (w *world) ask(query string, form url.Values) map[string]json.RawMessage {
	w.t.Helper()
	status, body := w.call(query, form)
	if status != http.StatusOK {
		w.t.Fatalf("%s: status %d, body %q", query, status, body)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		w.t.Fatalf("%s: %v in %s", query, err, body)
	}
	if string(doc["auth"]) != "1" {
		w.t.Fatalf("%s: not logged in: %s", query, body)
	}
	return doc
}

func field[T any](t *testing.T, doc map[string]json.RawMessage, name string) T {
	t.Helper()
	var v T
	if raw, ok := doc[name]; !ok {
		t.Fatalf("no %q in %v", name, doc)
	} else if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%q: %v in %s", name, err, raw)
	}
	return v
}

type item struct {
	ID            int64  `json:"id"`
	FeedID        int64  `json:"feed_id"`
	Title         string `json:"title"`
	Author        string `json:"author"`
	HTML          string `json:"html"`
	URL           string `json:"url"`
	IsSaved       int    `json:"is_saved"`
	IsRead        int    `json:"is_read"`
	CreatedOnTime int64  `json:"created_on_time"`
}

func ids(items []item) []int64 {
	out := []int64{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// entry returns an entry of alice as stored.
func (w *world) entry(id int64) *store.Entry {
	w.t.Helper()
	got, err := w.db.EntriesByIDs(context.Background(), w.alice.ID, []int64{id}, true)
	if err != nil || len(got) != 1 {
		w.t.Fatalf("entry %d: %v, %v", id, got, err)
	}
	return got[0]
}

// A request is let in with the key of its user alone, and only while both
// the API and Fever are switched on.
func TestAccess(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		const unauthorized = `{"api_version":3,"auth":0}` + "\n"
		doc := w.ask("api", nil)
		if got := field[int](t, doc, "api_version"); got != 3 {
			t.Errorf("api_version = %d, want 3", got)
		}
		if _, ok := doc["last_refreshed_on_time"]; !ok {
			t.Error("the answer has no last_refreshed_on_time")
		}

		for name, key := range map[string]string{
			"a wrong key":             strings.Repeat("0", 32),
			"no key":                  "",
			"the key of the name":     store.FeverKey("alice", "alice"),
			"the key of a password":   store.FeverKey("", "alice-password"),
			"an upper-case wrong key": strings.ToUpper(strings.Repeat("a", 32)),
		} {
			if status, body := w.callWith(key, "api&unread_item_ids", nil); status != http.StatusOK || body != unauthorized {
				t.Errorf("%s: status %d, body %q; want the refusal", name, status, body)
			}
		}
		// The key in the address would end up in logs: only the body counts.
		resp, err := http.Get(w.server.URL + Path + "?api&api_key=" + w.key)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != unauthorized {
			t.Errorf("a key in the address: %q, want the refusal", body)
		}
		// The key is read whatever its case.
		if status, body := w.callWith(strings.ToUpper(w.key), "api", nil); status != http.StatusOK || !strings.Contains(body, `"auth":1`) {
			t.Errorf("an upper-case key: status %d, body %q", status, body)
		}
		if status, body := w.callWith("", "", nil); status != http.StatusBadRequest {
			t.Errorf("a request without api: status %d, body %q", status, body)
		}

		// A user whose API password was set before Fever existed has no key.
		old := &store.User{Name: "olga", APIPasswordHash: "$2a$10$hash"}
		if err := w.db.CreateUser(context.Background(), old); err != nil {
			t.Fatal(err)
		}
		if status, body := w.callWith(store.FeverKey("olga", "olga-password"), "api", nil); body != unauthorized {
			t.Errorf("a user without a key: status %d, body %q", status, body)
		}

		// A user who is disabled, or has yet to confirm an address where
		// that is required, gets in nowhere; an administrator does. The
		// failures above are let pass first: they pause the address.
		w.now.Add(int64(time.Hour))
		w.user("carol", `{"enabled":false}`)
		w.user("dave", `{"email_validation_token":"secret"}`)
		w.user("erin", `{"email_validation_token":"secret","is_admin":true}`)
		w.system(func(s *store.System) { s.ForceEmailValidation = true })
		for name, want := range map[string]bool{"carol": false, "dave": false, "erin": true, "alice": true} {
			_, body := w.callWith(store.FeverKey(name, name+"-password"), "api", nil)
			if got := strings.Contains(body, `"auth":1`); got != want {
				t.Errorf("%s: %s, logged in = %v, want %v", name, body, got, want)
			}
		}
		w.system(func(s *store.System) { s.ForceEmailValidation = false })
		if _, body := w.callWith(store.FeverKey("dave", "dave-password"), "api", nil); !strings.Contains(body, `"auth":1`) {
			t.Errorf("an unconfirmed address where confirming is not required: %s", body)
		}

		// With either switch off the API is not there.
		for name, change := range map[string]func(*store.System){
			"Fever off": func(s *store.System) { s.FeverEnabled = false },
			"API off":   func(s *store.System) { s.APIEnabled = false },
		} {
			w.system(change)
			if status, body := w.call("api", nil); status != http.StatusServiceUnavailable || body != "Service Unavailable!" {
				t.Errorf("%s: status %d, body %q", name, status, body)
			}
			w.system(func(s *store.System) { s.APIEnabled, s.FeverEnabled = true, true })
		}
	})
}

// Guesses at a key are counted by address, as the failed logins are, and a
// key that works is not counted.
func TestGuessesAreSlowedDown(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		wrong := strings.Repeat("0", 32)
		for range authguard.FreeFailures - 1 {
			w.callWith(wrong, "api", nil)
		}
		if status, _ := w.call("api", nil); status != http.StatusOK {
			t.Fatalf("a right key after %d failures: status %d", authguard.FreeFailures-1, status)
		}
		for range authguard.FreeFailures {
			w.callWith(wrong, "api", nil)
		}
		resp, err := http.Post(w.server.URL+Path+"?api", "application/x-www-form-urlencoded", strings.NewReader("api_key="+w.key))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
			t.Errorf("after %d failures: status %d, Retry-After %q; want 429 with a wait", authguard.FreeFailures, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
		w.now.Add(int64(time.Hour))
		if status, _ := w.call("api", nil); status != http.StatusOK {
			t.Errorf("after the pause: status %d", status)
		}
	})
}

// A key that works does not clear the guesses made from its address before
// it: a user cannot hide guesses at the key of another among requests with
// their own.
func TestOwnKeyDoesNotHideGuesses(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.now.Add(int64(time.Hour))
		wrong := strings.Repeat("0", 32)
		for i := range authguard.FreeFailures - 1 {
			if status, _ := w.callWith(wrong, "api", nil); status == http.StatusTooManyRequests {
				t.Fatalf("guess %d was paused", i+1)
			}
			if status, _ := w.call("api", nil); status != http.StatusOK {
				t.Fatalf("the own key after guess %d: status %d", i+1, status)
			}
		}
		w.callWith(wrong, "api", nil)
		if status, _ := w.call("api", nil); status != http.StatusTooManyRequests {
			t.Errorf("after %d guesses between own keys: status %d, want 429", authguard.FreeFailures, status)
		}
	})
}

// Categories are groups and feeds are feeds; a hidden feed is in no list.
func TestGroupsAndFeeds(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		news := w.category(w.alice, "News")
		w.category(w.alice, "Empty")
		a := w.feed(w.alice, &store.Feed{Name: "A", URL: "https://a.example/feed", Website: "https://a.example/", CategoryID: news.ID, LastUpdate: 500})
		b := w.feed(w.alice, &store.Feed{Name: "B", URL: "https://b.example/feed", CategoryID: news.ID, LastUpdate: 700})
		c := w.feed(w.alice, &store.Feed{Name: "C", URL: "https://c.example/feed", CategoryID: store.DefaultCategoryID})
		w.feed(w.alice, &store.Feed{Name: "Hidden", URL: "https://h.example/feed", CategoryID: news.ID, Priority: hiddenPriority})
		bob := w.user("bob", "")
		w.feed(bob, &store.Feed{Name: "Bobs", URL: "https://bob.example/feed"})
		w.category(bob, "Secret")

		type group struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		}
		type feedsGroup struct {
			GroupID int64  `json:"group_id"`
			FeedIDs string `json:"feed_ids"`
		}
		doc := w.ask("api&groups", nil)
		titles := map[string]int64{}
		for _, g := range field[[]group](t, doc, "groups") {
			titles[g.Title] = g.ID
		}
		if len(titles) != 3 || titles["News"] != news.ID || titles["Empty"] == 0 || titles[store.DefaultCategoryName] != store.DefaultCategoryID {
			t.Errorf("groups = %v, want the default category, News and Empty of alice only", titles)
		}
		want := map[int64]string{news.ID: joinIDs([]int64{a.ID, b.ID}), store.DefaultCategoryID: joinIDs([]int64{c.ID})}
		got := map[int64]string{}
		for _, g := range field[[]feedsGroup](t, doc, "feeds_groups") {
			got[g.GroupID] = g.FeedIDs
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("feeds_groups = %v, want %v (no empty category, no hidden feed)", got, want)
		}
		if _, ok := doc["feeds"]; ok {
			t.Error("groups answered with feeds that were not asked for")
		}

		doc = w.ask("api&feeds", nil)
		type feed struct {
			ID                int64  `json:"id"`
			FaviconID         int64  `json:"favicon_id"`
			Title             string `json:"title"`
			URL               string `json:"url"`
			SiteURL           string `json:"site_url"`
			IsSpark           int    `json:"is_spark"`
			LastUpdatedOnTime int64  `json:"last_updated_on_time"`
		}
		feeds := field[[]feed](t, doc, "feeds")
		if len(feeds) != 3 {
			t.Fatalf("feeds = %+v, want the three visible ones of alice", feeds)
		}
		byTitle := map[string]feed{}
		for _, f := range feeds {
			byTitle[f.Title] = f
		}
		if f := byTitle["A"]; f.ID != a.ID || f.URL != "https://a.example/feed" || f.SiteURL != "https://a.example/" || f.LastUpdatedOnTime != 500 || f.IsSpark != 0 {
			t.Errorf("feed A = %+v", f)
		}
		if field[[]feedsGroup](t, doc, "feeds_groups") == nil {
			t.Error("feeds came without feeds_groups")
		}
		if got := field[int64](t, doc, "last_refreshed_on_time"); got != 700 {
			t.Errorf("last_refreshed_on_time = %d, want the newest refresh, 700", got)
		}

		doc = w.ask("api&favicons&links", nil)
		if string(doc["favicons"]) != "[]" || string(doc["links"]) != "[]" {
			t.Errorf("favicons = %s, links = %s; want empty lists", doc["favicons"], doc["links"])
		}
	})
}

// Items come fifty at a time: the oldest after since_id, the newest before
// max_id, or those named by with_ids.
func TestItemPaging(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Name: "A"})
		hidden := w.feed(w.alice, &store.Feed{Name: "Hidden", Priority: hiddenPriority})
		bob := w.user("bob", "")
		bobs := w.feed(bob, &store.Feed{Name: "Bobs"})
		var all []int64
		for i := int64(1); i <= 120; i++ {
			w.entries(w.alice, &store.Entry{ID: i * second, FeedID: f.ID, Title: "entry " + strconv.FormatInt(i, 10), Published: 1000 + i})
			all = append(all, i*second)
		}
		w.entries(w.alice,
			&store.Entry{ID: 200 * second, FeedID: hidden.ID, Title: "hidden"},
			&store.Entry{ID: 130 * second, FeedID: f.ID, Title: "full", Authors: []string{"Ann", "Bo"}, Content: "<p>text</p>", Link: "https://example.org/full", IsRead: true, IsFavorite: true})
		w.entries(bob, &store.Entry{ID: 300 * second, FeedID: bobs.ID, Title: "not yours"})
		all = append(all, 130*second)

		page := func(query string, form url.Values) ([]item, int) {
			t.Helper()
			doc := w.ask(query, form)
			return field[[]item](t, doc, "items"), field[int](t, doc, "total_items")
		}

		// Without a bound the oldest come, as with since_id=0.
		items, total := page("api&items", nil)
		if total != 121 || !reflect.DeepEqual(ids(items), all[:50]) {
			t.Errorf("items without a bound: total %d, ids %v; want 121 and the oldest fifty", total, ids(items))
		}
		items, _ = page("api&items&since_id=0", nil)
		if !reflect.DeepEqual(ids(items), all[:50]) {
			t.Errorf("since_id=0: %v", ids(items))
		}
		items, _ = page("api&items&since_id="+strconv.FormatInt(50*second, 10), nil)
		if !reflect.DeepEqual(ids(items), all[50:100]) {
			t.Errorf("since_id after the 50th: %v, want the next fifty ascending", ids(items))
		}
		items, _ = page("api&items&since_id="+strconv.FormatInt(100*second, 10), nil)
		if !reflect.DeepEqual(ids(items), all[100:]) {
			t.Errorf("since_id after the 100th: %v, want the last twenty one", ids(items))
		}
		if items, _ = page("api&items&since_id="+strconv.FormatInt(130*second, 10), nil); len(items) != 0 {
			t.Errorf("since_id of the newest: %v, want none", ids(items))
		}

		items, _ = page("api&items&max_id="+strconv.FormatInt(101*second, 10), nil)
		var want []int64
		for i := 100; i > 50; i-- {
			want = append(want, int64(i)*second)
		}
		if !reflect.DeepEqual(ids(items), want) {
			t.Errorf("max_id=101s: %v, want fifty descending from 100s", ids(items))
		}
		items, _ = page("api&items&max_id=0", nil)
		if len(items) != 50 || items[0].ID != 130*second {
			t.Errorf("max_id=0: %d items from %d, want the newest fifty", len(items), items[0].ID)
		}
		if items, _ = page("api&items&max_id=1", nil); len(items) != 0 {
			t.Errorf("max_id=1: %v, want none", ids(items))
		}
		if items, _ = page("api&items&max_id="+strconv.FormatInt(2*second, 10), nil); !reflect.DeepEqual(ids(items), []int64{1 * second}) {
			t.Errorf("max_id=2s: %v", ids(items))
		}

		// with_ids names the items, nothing of another user is found, and
		// no more than fifty are looked at.
		items, _ = page("api&items&with_ids="+strings.Join([]string{strconv.FormatInt(130*second, 10), strconv.FormatInt(3*second, 10), "300000000", "x", "999"}, ","), nil)
		if !reflect.DeepEqual(ids(items), []int64{3 * second, 130 * second}) {
			t.Errorf("with_ids: %v, want the two that exist, oldest first", ids(items))
		}
		var many []string
		for _, id := range all[:60] {
			many = append(many, strconv.FormatInt(id, 10))
		}
		if items, _ = page("api&items", url.Values{"with_ids": {strings.Join(many, ",")}}); len(items) != 50 {
			t.Errorf("sixty with_ids gave %d items, want fifty", len(items))
		}

		// An item says what the entry says.
		items, _ = page("api&items&with_ids="+strconv.FormatInt(130*second, 10), nil)
		wantItem := item{ID: 130 * second, FeedID: f.ID, Title: "full", Author: "Ann, Bo", HTML: "<p>text</p>", URL: "https://example.org/full", IsSaved: 1, IsRead: 1}
		if len(items) != 1 || items[0].CreatedOnTime != 130 || (func() item { it := items[0]; it.CreatedOnTime = 0; return it })() != wantItem {
			t.Errorf("item = %+v, want %+v created at 130", items, wantItem)
		}
		items, _ = page("api&items&with_ids="+strconv.FormatInt(2*second, 10), nil)
		if len(items) != 1 || items[0].CreatedOnTime != 1002 || items[0].IsRead != 0 || items[0].IsSaved != 0 {
			t.Errorf("item 2 = %+v, want unread, not saved, created at 1002", items)
		}

		// An extension may hold an entry back.
		w.registry.EntryBeforeDisplay.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) { return e, e.ID != 2*second })
		items, _ = page("api&items&since_id=0", nil)
		if items[0].ID != 1*second || items[1].ID != 3*second {
			t.Errorf("items with the second held back: %v", ids(items[:3]))
		}
	})
}

// The identifiers of unread and of saved items follow the reading list:
// nothing of a hidden feed, and saved ones of any feed that is shown.
func TestUnreadAndSavedIDs(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Name: "A"})
		quiet := w.feed(w.alice, &store.Feed{Name: "Quiet", Priority: -5})
		hidden := w.feed(w.alice, &store.Feed{Name: "Hidden", Priority: hiddenPriority})
		bob := w.user("bob", "")
		bobs := w.feed(bob, &store.Feed{})
		w.entries(w.alice,
			&store.Entry{ID: 1 * second, FeedID: f.ID},
			&store.Entry{ID: 2 * second, FeedID: f.ID, IsRead: true},
			&store.Entry{ID: 3 * second, FeedID: f.ID, IsFavorite: true},
			&store.Entry{ID: 4 * second, FeedID: f.ID, IsRead: true, IsFavorite: true},
			&store.Entry{ID: 5 * second, FeedID: quiet.ID, IsFavorite: true},
			&store.Entry{ID: 6 * second, FeedID: quiet.ID},
			&store.Entry{ID: 7 * second, FeedID: hidden.ID, IsFavorite: true},
			&store.Entry{ID: 8 * second, FeedID: hidden.ID})
		w.entries(bob, &store.Entry{ID: 9 * second, FeedID: bobs.ID, IsFavorite: true})

		doc := w.ask("api&unread_item_ids&saved_item_ids", nil)
		if got := field[string](t, doc, "unread_item_ids"); got != "1000000,3000000" {
			t.Errorf("unread_item_ids = %q, want the unread of the feeds in the reading list, oldest first", got)
		}
		if got := field[string](t, doc, "saved_item_ids"); got != "3000000,4000000,5000000" {
			t.Errorf("saved_item_ids = %q, want the saved ones of feeds that are not hidden", got)
		}
		w.ok(url.Values{"mark": {"item"}, "as": {"read"}, "id": {"1000000"}})
		w.ok(url.Values{"mark": {"item"}, "as": {"read"}, "id": {"3000000"}})
		if got := field[string](t, w.ask("api&unread_item_ids", nil), "unread_item_ids"); got != "" {
			t.Errorf("unread_item_ids with everything read = %q, want an empty string", got)
		}
	})
}

// ok is a change that has to be accepted.
func (w *world) ok(form url.Values) {
	w.t.Helper()
	w.ask("api", form)
}

// Marking an item changes the entry, and tells the extensions only of a
// change that happened.
func TestMarkItem(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		f := w.feed(w.alice, &store.Feed{Name: "A"})
		bob := w.user("bob", "")
		bobs := w.feed(bob, &store.Feed{})
		w.entries(w.alice, &store.Entry{ID: 1 * second, FeedID: f.ID}, &store.Entry{ID: 2 * second, FeedID: f.ID, IsRead: true})
		w.entries(bob, &store.Entry{ID: 1 * second, FeedID: bobs.ID})
		var read []hooks.EntriesRead
		var starred []hooks.EntriesFavorite
		w.registry.EntriesRead.Add(0, func(_ context.Context, e hooks.EntriesRead) bool { read = append(read, e); return false })
		w.registry.EntriesFavorite.Add(0, func(_ context.Context, e hooks.EntriesFavorite) bool { starred = append(starred, e); return false })

		mark := func(as string, id int64) {
			t.Helper()
			doc := w.ask("api", url.Values{"mark": {"item"}, "as": {as}, "id": {strconv.FormatInt(id, 10)}})
			if _, ok := doc["last_refreshed_on_time"]; !ok {
				t.Errorf("mark %s: no base answer in %v", as, doc)
			}
		}
		mark("read", 1*second)
		if e := w.entry(1 * second); !e.IsRead || e.LastUserModified != 1_000_000 {
			t.Errorf("after read: read %v, changed by the user at %d; want read and 1000000", e.IsRead, e.LastUserModified)
		}
		mark("read", 2*second) // read already
		mark("unread", 1*second)
		if w.entry(1 * second).IsRead {
			t.Error("after unread: the entry is read")
		}
		mark("saved", 1*second)
		if !w.entry(1 * second).IsFavorite {
			t.Error("after saved: the entry is not starred")
		}
		mark("unsaved", 1*second)
		if w.entry(1 * second).IsFavorite {
			t.Error("after unsaved: the entry is starred")
		}
		wantRead := []hooks.EntriesRead{
			{UserID: w.alice.ID, IDs: []int64{1 * second}, IsRead: true},
			{UserID: w.alice.ID, IDs: []int64{1 * second}, IsRead: false},
		}
		wantStarred := []hooks.EntriesFavorite{
			{UserID: w.alice.ID, IDs: []int64{1 * second}, IsFavorite: true},
			{UserID: w.alice.ID, IDs: []int64{1 * second}, IsFavorite: false},
		}
		if !reflect.DeepEqual(read, wantRead) || !reflect.DeepEqual(starred, wantStarred) {
			t.Errorf("EntriesRead = %+v, EntriesFavorite = %+v; want %+v and %+v", read, starred, wantRead, wantStarred)
		}

		// The entry of another user with the same identifier stays.
		mark("read", 1*second)
		got, err := w.db.EntriesByIDs(context.Background(), bob.ID, []int64{1 * second}, true)
		if err != nil || len(got) != 1 || got[0].IsRead {
			t.Errorf("the entry of bob after alice read hers: %+v, %v", got, err)
		}

		for name, form := range map[string]url.Values{
			"an unknown state":        {"mark": {"item"}, "as": {"deleted"}, "id": {"1000000"}},
			"no id":                   {"mark": {"item"}, "as": {"read"}},
			"an id that is no number": {"mark": {"item"}, "as": {"read"}, "id": {"x"}},
			"an unknown target":       {"mark": {"link"}, "as": {"read"}, "id": {"1"}},
		} {
			if status, body := w.call("api", form); status != http.StatusBadRequest {
				t.Errorf("%s: status %d, body %q; want 400", name, status, body)
			}
		}
	})
}

// Marking a feed or a group reads what is in it up to a time; group 0 is
// every group.
func TestMarkFeedAndGroup(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		news := w.category(w.alice, "News")
		a := w.feed(w.alice, &store.Feed{Name: "A", CategoryID: news.ID})
		b := w.feed(w.alice, &store.Feed{Name: "B", CategoryID: news.ID})
		c := w.feed(w.alice, &store.Feed{Name: "C", CategoryID: store.DefaultCategoryID})
		hidden := w.feed(w.alice, &store.Feed{Name: "Hidden", CategoryID: store.DefaultCategoryID, Priority: hiddenPriority})
		bob := w.user("bob", "")
		bobs := w.feed(bob, &store.Feed{})
		fill := func() {
			t.Helper()
			ctx := context.Background()
			for _, f := range []*store.Feed{a, b, c, hidden} {
				ids := []int64{}
				for _, i := range []int64{10, 20, 30} {
					ids = append(ids, i*second+f.ID)
				}
				if _, err := w.db.SetEntriesRead(ctx, w.alice.ID, ids, false, 0); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, f := range []*store.Feed{a, b, c, hidden} {
			for _, i := range []int64{10, 20, 30} {
				w.entries(w.alice, &store.Entry{ID: i*second + f.ID, FeedID: f.ID})
			}
		}
		w.entries(bob, &store.Entry{ID: 10*second + a.ID, FeedID: bobs.ID})
		unread := func() []int64 {
			t.Helper()
			ids, err := w.db.ListEntryIDs(context.Background(), w.alice.ID, store.EntryQuery{Read: new(false), Ascending: true})
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}
		at := func(i, feed int64) int64 { return i*second + feed }
		mark := func(kind string, id int64, before int64) {
			t.Helper()
			form := url.Values{"mark": {kind}, "as": {"read"}, "id": {strconv.FormatInt(id, 10)}}
			if before != 0 {
				form.Set("before", strconv.FormatInt(before, 10))
			}
			w.ask("api", form)
		}

		// A feed up to a time (in seconds): the entries of 10 and 20 go.
		mark("feed", a.ID, 25)
		got := unread()
		for _, i := range []int64{10, 20} {
			for _, id := range got {
				if id == at(i, a.ID) {
					t.Errorf("entry %d of feed A is unread after marking it before 25", i)
				}
			}
		}
		if len(got) != 12-2 {
			t.Errorf("%d unread after marking feed A before 25, want 10", len(got))
		}
		// A feed without a time: all of it, and no other feed.
		mark("feed", a.ID, 0)
		if len(unread()) != 12-3 {
			t.Errorf("%d unread after marking feed A, want 9", len(unread()))
		}
		fill()

		// A group: its feeds, not those of another group, hidden ones nor
		// another user's entries.
		mark("group", news.ID, 0)
		got = unread()
		if len(got) != 6 {
			t.Errorf("unread after marking group News = %v, want the six of C and Hidden", got)
		}
		fill()
		// Group 0 is the reading list: the hidden feed stays unread.
		mark("group", 0, 0)
		got = unread()
		if len(got) != 3 || got[0] != at(10, hidden.ID) {
			t.Errorf("unread after marking every group = %v, want only the three of the hidden feed", got)
		}
		fill()
		mark("group", 0, 15)
		if got := unread(); len(got) != 12-3 {
			t.Errorf("%d unread after marking every group before 15, want 9: the oldest of each of the three feeds of the reading list read", len(got))
		}
		bobsEntries, err := w.db.EntriesByIDs(context.Background(), bob.ID, []int64{at(10, a.ID)}, true)
		if err != nil || len(bobsEntries) != 1 || bobsEntries[0].IsRead {
			t.Errorf("the entry of bob after alice marked groups: %+v, %v", bobsEntries, err)
		}

		for name, form := range map[string]url.Values{
			"a feed that is no feed": {"mark": {"feed"}, "as": {"read"}, "id": {"0"}},
			"unread a feed":          {"mark": {"feed"}, "as": {"unread"}, "id": {"1"}},
			"save a group":           {"mark": {"group"}, "as": {"saved"}, "id": {"1"}},
			"no id":                  {"mark": {"group"}, "as": {"read"}},
		} {
			if status, body := w.call("api", form); status != http.StatusBadRequest {
				t.Errorf("%s: status %d, body %q; want 400", name, status, body)
			}
		}
	})
}
