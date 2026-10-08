package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/store"
)

// feedSite is a site on this machine that serves the documents of a test.
type feedSite struct {
	*httptest.Server
	mu    sync.Mutex
	pages map[string]http.HandlerFunc
	hits  map[string]int
}

func newFeedSite(t *testing.T) *feedSite {
	t.Helper()
	site := &feedSite{pages: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site.mu.Lock()
		site.hits[r.URL.Path]++
		page := site.pages[r.URL.Path]
		site.mu.Unlock()
		if page == nil {
			http.NotFound(w, r)
			return
		}
		page(w, r)
	}))
	t.Cleanup(site.Close)
	return site
}

func (f *feedSite) serve(path, contentType, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("ETag", `"one"`)
		_, _ = w.Write([]byte(body))
	}
}

func (f *feedSite) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// rssOf is an RSS document with the entries of the given titles, each
// linking to a page of the site named after it.
func rssOf(site, title string, entries ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0"?><rss version="2.0"><channel><title>%s</title><link>%s/</link><description>d</description>`, title, site)
	for _, entry := range entries {
		fmt.Fprintf(&b, `<item><guid>%s</guid><title>%s</title><link>%s/articles/%s</link><description>Summary of %s</description></item>`,
			entry, entry, site, entry, entry)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

// upload sends a form with a file, the way a browser sends the form of the
// settings of a feed.
func (s *site) upload(target string, form url.Values, field, name string, content []byte) answer {
	s.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, values := range form {
		for _, value := range values {
			if err := w.WriteField(key, value); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	if field != "" {
		part, err := w.CreateFormFile(field, name)
		if err != nil {
			s.t.Fatal(err)
		}
		_, _ = part.Write(content)
	}
	if err := w.Close(); err != nil {
		s.t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, target, &body)
	return s.send(r, map[string]string{"Content-Type": w.FormDataContentType(), "Sec-Fetch-Site": "same-origin"})
}

func (s *site) feed(user string, id int64) *store.Feed {
	s.t.Helper()
	f, err := s.db.FeedByID(context.Background(), s.user(user).ID, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return f
}

func (s *site) feeds(user string) []*store.Feed {
	s.t.Helper()
	feeds, err := s.db.Feeds(context.Background(), s.user(user).ID)
	if err != nil {
		s.t.Fatal(err)
	}
	return feeds
}

func (s *site) category(user string, id int64) *store.Category {
	s.t.Helper()
	categories, err := s.db.Categories(context.Background(), s.user(user).ID)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, c := range categories {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *site) label(user string, id int64) *store.Tag {
	s.t.Helper()
	labels, err := s.db.Tags(context.Background(), s.user(user).ID)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, l := range labels {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// decoded reads a JSON object into what its keys hold.
func decoded(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return m
}

// notice is what a page says about the action before it.
func notice(body string) string {
	m := messages.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], ""))
}

// rawKey finds a text that was not found and is shown as its key.
var rawKey = regexp.MustCompile(`[>"](add|category|feed|form|label|notice|palette|period|problems|retention|sub|settings|admin|stats|query|share|transfer|log|reauth|language|stats|integrations|register|tos|validate)\.[a-z0-9:_.-]+[<"]`)

// shown asks for a page, which has to be there with every text it names.
func (s *site) shown(target string) string {
	s.t.Helper()
	body := s.page(target)
	if key := rawKey.FindString(body); key != "" {
		s.t.Errorf("GET %s shows the key of a text in place of the text: %s", target, key)
	}
	return body
}

// follow sends a form and goes where the answer sends the reader.
func (s *site) follow(target string, form url.Values) (location, body string) {
	s.t.Helper()
	a := s.post(target, form)
	if a.status != http.StatusSeeOther {
		s.t.Fatalf("POST %s: status %d, want a redirect\n%s", target, a.status, a.body)
	}
	location = a.header.Get("Location")
	return location, s.shown(location)
}

// R9: the page of subscriptions lists what the user has, and only for the user.
func TestSubscriptionsPage(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		if a := s.get("/subscriptions"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/login?next=%2Fsubscriptions" {
			t.Errorf("GET /subscriptions without a login: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		s.asAlice()
		body := s.shown("/subscriptions")
		for _, want := range []string{
			`<a href="/subscriptions" aria-current="page">Subscriptions</a>`, `<a href="/subscriptions/add">Add a feed</a>`,
			`<h2><a href="/subscriptions/categories/2">Blogs</a></h2>`, `<a href="/subscriptions/feeds/1">Atom corpus</a>`,
			`<span class="feed-address">http://feeds.freshgo.test/atom.xml</span>`, `<a href="/subscriptions/labels/2">work &amp; play</a>`,
			`action="/subscriptions/categories"`, `action="/subscriptions/labels"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /subscriptions: no %q in\n%s", want, body)
			}
		}
		// Categories the user has put in order come in that order, the rest after them.
		blogs, scraped, rest := strings.Index(body, ">Blogs<"), strings.Index(body, ">Scraped &amp; parsed<"), strings.Index(body, ">Uncategorized<")
		if blogs < 0 || blogs > scraped || scraped > rest {
			t.Errorf("order of the categories: Blogs at %d, Scraped at %d, Uncategorized at %d", blogs, scraped, rest)
		}
		// What belongs to bob is not there for alice to see or to change.
		for _, target := range []string{"/subscriptions/feeds/99", "/subscriptions/categories/99", "/subscriptions/labels/99", "/subscriptions/feeds/x"} {
			if a := s.get(target); a.status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
			if a := s.post(target, url.Values{"name": {"x"}, "url": {"http://example.org/"}}); a.status != http.StatusNotFound {
				t.Errorf("POST %s: status %d, want 404", target, a.status)
			}
		}
		for _, target := range []string{"/subscriptions/add", "/subscriptions/problems", "/subscriptions/categories/1", "/subscriptions/labels/1"} {
			s.shown(target)
		}
		for id := int64(1); id <= 8; id++ {
			s.shown(fmt.Sprintf("/subscriptions/feeds/%d", id))
		}
		s.setting("alice", "language", "ru")
		russian := s.get("/subscriptions/feeds/3")
		if !strings.Contains(russian.body, "Адрес фида") || rawKey.MatchString(russian.body) {
			t.Errorf("GET /subscriptions/feeds/3 in Russian:\n%s", russian.body)
		}
	})
}

// R9: a feed is added by its address or by that of its site.
func TestAddFeed(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one", "two"))
		remote.serve("/", "text/html", `<html><head><link rel="alternate" type="application/rss+xml" href="/feed.xml"></head></html>`)
		remote.serve("/other.xml", "application/rss+xml", rssOf(remote.URL, "Other", "three"))
		remote.mu.Lock()
		remote.pages["/secret.xml"] = func(w http.ResponseWriter, r *http.Request) {
			if user, password, ok := r.BasicAuth(); !ok || user != "reader" || password != "pass word" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(rssOf(remote.URL, "Secret", "four")))
		}
		remote.mu.Unlock()
		s.asAlice()

		// By the address of the site, into a category of the user.
		location, body := s.follow("/subscriptions/feeds", url.Values{"url": {" " + remote.URL + "/ "}, "category": {"3"}})
		if location != "/subscriptions/feeds/9" || notice(body) != "Subscribed." {
			t.Fatalf("after adding a feed: at %q, notice %q", location, notice(body))
		}
		added := s.feed("alice", 9)
		if added.URL != remote.URL+"/feed.xml" || added.Name != "The Blog" || added.CategoryID != 3 || added.Priority != store.PriorityMain {
			t.Errorf("added feed = %+v", added)
		}
		if got := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 9}}); len(got) != 2 {
			t.Errorf("the added feed has %d entries, want 2", len(got))
		}
		if !strings.Contains(s.page("/feeds/9"), ">two</h2>") {
			t.Error("the reading screen does not list the entries of the added feed")
		}

		// With credentials.
		location, _ = s.follow("/subscriptions/feeds", url.Values{
			"url": {remote.URL + "/secret.xml"}, "http_user": {"reader"}, "http_password": {"pass word"}, "category": {"77"},
		})
		if secret := s.feed("alice", 10); location != "/subscriptions/feeds/10" || secret.HTTPAuth != "reader:pass word" ||
			secret.Name != "Secret" || secret.CategoryID != store.DefaultCategoryID {
			t.Errorf("feed added with credentials: at %q, %+v", location, secret)
		}

		for name, c := range map[string]struct {
			form url.Values
			want string
		}{
			"twice":       {url.Values{"url": {remote.URL + "/feed.xml"}}, "You are subscribed to this feed already."},
			"no address":  {url.Values{"url": {"  "}}, "This is not an address freshgo can fetch"},
			"not a feed":  {url.Values{"url": {remote.URL + "/missing"}}, "No feed was found at this address."},
			"no password": {url.Values{"url": {remote.URL + "/secret.xml?again"}}, "No feed was found at this address."},
		} {
			a := s.post("/subscriptions/feeds", c.form)
			if a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) || !strings.Contains(a.body, c.want) ||
				!strings.Contains(a.body, `value="`+strings.TrimSpace(c.form.Get("url"))+`"`) {
				t.Errorf("%s: status %d, want 400 with %q and the address kept\n%s", name, a.status, c.want, a.body)
			}
		}
		if n := len(s.feeds("alice")); n != 10 {
			t.Errorf("alice has %d feeds after the refused ones, want 10", n)
		}

		// The installation bounds the number of feeds.
		s.system(func(system *store.System) { system.Limits.MaxFeeds = 10 })
		a := s.post("/subscriptions/feeds", url.Values{"url": {remote.URL + "/other.xml"}})
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, "as many feeds as this installation allows") || remote.hit("/other.xml") != 0 {
			t.Errorf("past the limit: status %d, the feed requested %d times\n%s", a.status, remote.hit("/other.xml"), a.body)
		}
		// The page that adds a feed takes an address to start with.
		if body := s.shown("/subscriptions/add?url=http%3A%2F%2Fexample.org%2Ffeed&category=3"); !strings.Contains(body, `value="http://example.org/feed"`) ||
			!strings.Contains(body, `<option value="3" selected>`) || !strings.Contains(body, " autofocus") {
			t.Errorf("GET /subscriptions/add with an address:\n%s", body)
		}
	})
}

// feedForm is the form of the settings of a feed as its page has it:
// sending it back unchanged changes nothing.
func (s *site) feedForm(id int64) url.Values {
	s.t.Helper()
	body := s.shown(fmt.Sprintf("/subscriptions/feeds/%d", id))
	start := strings.Index(body, `class="form settings"`)
	end := strings.Index(body, `<h2>Actions</h2>`)
	if start < 0 || end < start {
		s.t.Fatalf("no form of settings in\n%s", body)
	}
	return formValues(s.t, body[start:end])
}

var (
	inputTag    = regexp.MustCompile(`<input ([^>]*)>`)
	attribute   = regexp.MustCompile(`([a-z-]+)="([^"]*)"`)
	selectTag   = regexp.MustCompile(`(?s)<select [^>]*name="([^"]+)"[^>]*>(.*?)</select>`)
	selectedTag = regexp.MustCompile(`<option value="([^"]*)" selected>`)
	textareaTag = regexp.MustCompile(`(?s)<textarea [^>]*name="([^"]+)"[^>]*>(.*?)</textarea>`)
)

func unescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&amp;", "&").Replace(s)
}

// formValues reads what a browser would send for the fields of a form.
func formValues(t *testing.T, form string) url.Values {
	t.Helper()
	values := url.Values{}
	for _, m := range inputTag.FindAllStringSubmatch(form, -1) {
		attrs := map[string]string{}
		for _, a := range attribute.FindAllStringSubmatch(m[1], -1) {
			attrs[a[1]] = unescape(a[2])
		}
		switch attrs["type"] {
		case "checkbox":
			if strings.Contains(m[1], " checked") {
				values.Set(attrs["name"], attrs["value"])
			}
		case "file", "submit":
		default:
			values.Set(attrs["name"], attrs["value"])
		}
	}
	for _, m := range selectTag.FindAllStringSubmatch(form, -1) {
		if selected := selectedTag.FindStringSubmatch(m[2]); selected != nil {
			values.Set(m[1], unescape(selected[1]))
		}
	}
	for _, m := range textareaTag.FindAllStringSubmatch(form, -1) {
		values.Set(m[1], unescape(m[2]))
	}
	return values
}

// R9: every setting of a feed can be read and changed on its page.
func TestFeedSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.asAlice()
		// What the form does not know about stays.
		before := s.feed("alice", 4)
		before.HTTPETag, before.HTTPLastModified = `"etag"`, "yesterday"
		kept := readAttrs(before.Attributes)
		kept.set("somethingElse", map[string]any{"a": 1})
		before.Attributes = kept.raw()
		if err := s.db.UpdateFeed(ctx, before); err != nil {
			t.Fatal(err)
		}

		// The form as it is shown stores the feed as it is.
		form := s.feedForm(4)
		if form.Get("xpath-item") != "//record" || form.Get("xpath-itemTimeFormat") != "Y-m-d H:i:s" || form.Get("kind") != "15" ||
			form.Get("url") != "http://feeds.freshgo.test/data.xml" || form.Get("category") != "3" || form.Get("priority") != "10" {
			t.Errorf("form of feed 4 = %v", form)
		}
		if _, body := s.follow("/subscriptions/feeds/4", form); notice(body) != "Saved." {
			t.Errorf("notice after saving = %q", notice(body))
		}
		same := s.feed("alice", 4)
		if !reflect.DeepEqual(decoded(t, same.Attributes), decoded(t, before.Attributes)) || same.HTTPETag != `"etag"` || same.URL != before.URL ||
			same.Name != before.Name || same.TTL != before.TTL || same.Kind != before.Kind {
			t.Errorf("feed saved unchanged = %+v\nattributes %s\nwant      %s", same, same.Attributes, before.Attributes)
		}

		// A change that has nothing to do with how the feed is read keeps
		// the validators; one that has drops them.
		form.Set("name", "Catalog")
		form.Set("category", "2")
		form.Set("priority", "0")
		form.Set("ttl", "7200")
		form.Set("filters_read", "intitle:spam\n\n author:bot ")
		form.Set("default_sort", "title")
		form.Set("default_order", "ASC")
		s.follow("/subscriptions/feeds/4", form)
		got := s.feed("alice", 4)
		attributes := decoded(t, got.Attributes)
		if got.Name != "Catalog" || got.CategoryID != 2 || got.Priority != 0 || got.TTL != 7200 || got.HTTPETag != `"etag"` ||
			got.HTTPLastModified != "yesterday" || attributes["defaultSort"] != "title" || attributes["defaultOrder"] != "ASC" ||
			!reflect.DeepEqual(attributes["filters"], []any{
				map[string]any{"search": "intitle:spam", "actions": []any{"read"}},
				map[string]any{"search": "author:bot", "actions": []any{"read"}},
			}) || !reflect.DeepEqual(attributes["somethingElse"], map[string]any{"a": 1.0}) {
			t.Errorf("feed after a change of its name and place = %+v\n%s", got, got.Attributes)
		}
		// The order of the feed is the one its page lists by.
		if body := s.page("/feeds/4"); !strings.Contains(body, `<option value="title" selected>`) || !strings.Contains(body, `<option value="asc" selected>`) {
			t.Error("the reading screen does not sort the feed the way its settings say")
		}

		form.Set("xpath-item", "//entry")
		s.follow("/subscriptions/feeds/4", form)
		if got := s.feed("alice", 4); got.HTTPETag != "" || got.HTTPLastModified != "" {
			t.Errorf("validators after a change of the XPath = %q, %q; want none", got.HTTPETag, got.HTTPLastModified)
		}

		// Every other setting.
		form = s.feedForm(1)
		for key, value := range map[string]string{
			"url": "example.org/feed", "website": "https://example.org/", "description": " About ", "mute": "1",
			"http_user": "me", "http_password": "secret:1", "path_entries": " article ", "path_entries_filter": ".ad",
			"path_entries_auto": "1", "path_entries_conditions": "intitle:long\r\nauthor:x", "content_action": "append",
			"keep_own": "1", "keep_max_on": "1", "keep_max": "30", "keep_period_on": "1", "keep_period_count": "2",
			"keep_period_unit": "P1W", "keep_min": "5", "keep_favourites": "1", "keep_unreads": "1",
			"read_upon_gone": "1", "read_upon_reception": "0", "mark_updated_article_unread": "1",
			"keep_max_n_unread": "40", "read_when_same_title_in_feed": "0",
			"kind": "30", "json-item": "data", "json-feedTitle": "meta.title", "xpath_to_json": "//script", "xpath-item": "//ignored",
			"proxy_type": "5", "proxy": "proxy.example:1080", "user_agent": "Reader/1", "cookie": "a=b", "cookie_file": "1",
			"headers": "X-One: 1\nRemote-User: root", "method": "POST", "post_fields": `{"q":1}`, "redirects": "7", "timeout": "20",
			"ssl_verify": "0", "unicity": "sha1:link_published", "unicity_forced": "1", "default_sort": "rand",
		} {
			form.Set(key, value)
		}
		s.follow("/subscriptions/feeds/1", form)
		got = s.feed("alice", 1)
		if got.URL != "https://example.org/feed" || got.Website != "https://example.org/" || got.Description != "About" || got.TTL != -3600 ||
			got.HTTPAuth != "me:secret:1" || got.PathEntries != "article" || got.Kind != 30 {
			t.Errorf("feed 1 after the form = %+v", got)
		}
		want := map[string]any{
			"SimplePieHash": "2b6d159abc7f9d9421cd766b769de1e358077bb2", "path_entries_filter": ".ad",
			"path_entries_auto": true, "path_entries_conditions": []any{"intitle:long", "author:x"}, "content_action": "append",
			"archiving": map[string]any{
				"keep_period": "P2W", "keep_max": 30.0, "keep_min": 5.0, "keep_favourites": true, "keep_labels": false, "keep_unreads": true,
			},
			"read_upon_gone": true, "read_upon_reception": false, "mark_updated_article_unread": true,
			"keep_max_n_unread": 40.0, "read_when_same_title_in_feed": false,
			"json_dotnotation": map[string]any{"item": "data", "feedTitle": "meta.title"}, "xPathToJson": "//script",
			"curl_params": map[string]any{
				"101": 5.0, "10004": "proxy.example:1080", "10018": "Reader/1", "10022": "a=b", "10031": "", "47": true,
				"10015": `{"q":1}`, "68": 7.0, "52": true, "10023": []any{"X-One: 1", "Remote-User: root", "Content-Type: application/json"},
			},
			"timeout": 20.0, "ssl_verify": false, "unicityCriteria": "sha1:link_published", "unicityCriteriaForced": true,
			"defaultSort": "rand",
		}
		if attributes := decoded(t, got.Attributes); !reflect.DeepEqual(attributes, want) {
			t.Errorf("attributes of feed 1 after the form:\n got %v\nwant %v", attributes, want)
		}
		// The page shows what was stored, and never the password.
		shown := s.feedForm(1)
		for key, want := range map[string]string{
			"url": "https://example.org/feed", "mute": "1", "ttl": "3600", "http_user": "me", "http_password": "",
			"path_entries_conditions": "intitle:long\nauthor:x", "keep_period_count": "2", "keep_period_unit": "P1W", "keep_max": "30",
			"read_upon_reception": "0", "read_when_same_title_in_feed": "0", "json-feedTitle": "meta.title", "proxy_type": "5",
			"headers": "X-One: 1\nRemote-User: root\nContent-Type: application/json", "method": "POST", "redirects": "7",
			"unicity": "sha1:link_published", "unicity_forced": "1", "default_sort": "rand", "content_action": "append",
			"path_entries_auto": "1",
		} {
			if shown.Get(key) != want {
				t.Errorf("form of feed 1 after the change: %s = %q, want %q", key, shown.Get(key), want)
			}
		}
		// An empty password keeps the one there is; another user name drops it.
		s.follow("/subscriptions/feeds/1", shown)
		if got := s.feed("alice", 1); got.HTTPAuth != "me:secret:1" {
			t.Errorf("credentials after saving with an empty password = %q", got.HTTPAuth)
		}
		shown.Set("http_user", "other")
		s.follow("/subscriptions/feeds/1", shown)
		if got := s.feed("alice", 1); got.HTTPAuth != "" {
			t.Errorf("credentials after a change of the user name without a password = %q", got.HTTPAuth)
		}

		// Back to what the levels above say.
		for _, key := range []string{"keep_own", "mute", "unicity_forced", "cookie_file", "path_entries_auto"} {
			shown.Del(key)
		}
		for key, value := range map[string]string{
			"read_upon_gone": "", "read_upon_reception": "", "mark_updated_article_unread": "", "keep_max_n_unread": "",
			"read_when_same_title_in_feed": "", "proxy": "", "user_agent": "", "cookie": "", "headers": "", "method": "GET",
			"post_fields": "", "redirects": "", "timeout": "", "ssl_verify": "", "unicity": "id", "default_sort": "", "default_order": "",
			"path_entries": "", "path_entries_filter": "", "path_entries_conditions": "", "content_action": "replace",
			"ttl": "0", "json-item": "", "json-feedTitle": "", "xpath_to_json": "",
		} {
			shown.Set(key, value)
		}
		s.follow("/subscriptions/feeds/1", shown)
		got = s.feed("alice", 1)
		if attributes := decoded(t, got.Attributes); len(attributes) != 1 || attributes["SimplePieHash"] == nil || got.TTL != 0 {
			t.Errorf("feed 1 with every setting left to the levels above: ttl %d, kind %d, attributes %s", got.TTL, got.Kind, got.Attributes)
		}

		// An address that is none stores nothing and keeps what was typed.
		form = s.feedForm(2)
		form.Set("url", " ")
		form.Set("name", "Typed")
		a := s.post("/subscriptions/feeds/2", form)
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) || !strings.Contains(a.body, `value="Typed"`) ||
			!strings.Contains(a.body, "not an address freshgo can fetch") {
			t.Errorf("POST with a bad address: status %d\n%s", a.status, a.body)
		}
		if got := s.feed("alice", 2); got.Name != "RSS corpus" || got.URL != "http://feeds.freshgo.test/rss.xml" {
			t.Errorf("feed 2 after a refused form = %+v", got)
		}
		// A category and a priority there is not are passed over.
		form = s.feedForm(2)
		form.Set("category", "99")
		form.Set("priority", "3")
		form.Set("kind", "4")
		s.follow("/subscriptions/feeds/2", form)
		if got := s.feed("alice", 2); got.CategoryID != 2 || got.Priority != 10 || got.Kind != 0 || got.TTL != 7200 {
			t.Errorf("feed 2 after a form with values there are not = %+v", got)
		}
	})
}

// Request settings the form cannot show, or shows otherwise than they are
// stored, survive the form as it is shown.
func TestFeedFormKeepsRequestSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		for stored, want := range map[string]string{
			`{"101":4,"10004":"proxy.example:1080"}`: `{"10004":"proxy.example:1080","101":4}`,
			// A feed kept away from the proxy of the installation stays so.
			`{"101":-1}`:                             `{"101":-1}`,
			`{"101":3,"10004":"proxy.example:1080"}`: `{"101":-1}`,
			`{"52":false}`:                           `{"52":false,"68":0}`,
			`{"68":0}`:                               `{"52":false,"68":0}`,
			`{"68":"3","52":true,"64":false}`:        `{"52":true,"64":false,"68":3}`,
			`[]`:                                     ``,
		} {
			f := s.feed("alice", 1)
			a := readAttrs(f.Attributes)
			a["curl_params"] = json.RawMessage(stored)
			f.Attributes, f.HTTPETag = a.raw(), `"etag"`
			if err := s.db.UpdateFeed(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			s.follow("/subscriptions/feeds/1", s.feedForm(1))
			got := s.feed("alice", 1)
			if curl := string(readAttrs(got.Attributes)["curl_params"]); curl != want {
				t.Errorf("request settings %s after the form as shown = %s, want %s", stored, curl, want)
			}
		}
		// No redirects at all can be asked for and taken back.
		f := s.feed("alice", 1)
		f.Attributes = nil
		if err := s.db.UpdateFeed(t.Context(), f); err != nil {
			t.Fatal(err)
		}
		form := s.feedForm(1)
		form.Set("redirects", "0")
		s.follow("/subscriptions/feeds/1", form)
		if curl := string(readAttrs(s.feed("alice", 1).Attributes)["curl_params"]); curl != `{"52":false,"68":0}` || s.feedForm(1).Get("redirects") != "0" {
			t.Errorf("request settings with no redirects = %s, the form shows %q", curl, s.feedForm(1).Get("redirects"))
		}
	})
}

// R9: a feed gets an icon of the user's own and gives it up.
func TestFeedIcon(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.asAlice()
		picture, err := os.ReadFile("../../testdata/reference/custom-icon.png")
		if err != nil {
			t.Fatal(err)
		}
		salt, err := s.db.Salt(ctx)
		if err != nil {
			t.Fatal(err)
		}
		alice := s.user("alice")
		hash := favicon.CustomHash(salt, alice.ID, 1)
		form := s.feedForm(1)
		if a := s.upload("/subscriptions/feeds/1", form, "icon", "icon.png", picture); a.status != http.StatusSeeOther {
			t.Fatalf("upload: status %d\n%s", a.status, a.body)
		}
		stored, err := s.db.CustomIconByHash(ctx, hash)
		if err != nil || !bytes.Equal(stored.Content, picture) {
			t.Fatalf("stored icon: %v", err)
		}
		if f := s.feed("alice", 1); decoded(t, f.Attributes)["customFavicon"] != true || favicon.HashOf(salt, f) != hash {
			t.Errorf("feed with an icon of its own = %s", f.Attributes)
		}
		body := s.shown("/subscriptions/feeds/1")
		if !strings.Contains(body, `src="/favicon/`+hash+`"`) || !strings.Contains(body, `name="icon_reset"`) {
			t.Errorf("page of the feed with an icon of its own:\n%s", body)
		}

		// What is not a picture, or too large, is refused and nothing is stored.
		form.Set("name", "Renamed")
		for name, content := range map[string][]byte{"text": []byte("not a picture"), "large": append(bytes.Clone(picture), make([]byte, maxIcon)...)} {
			a := s.upload("/subscriptions/feeds/1", form, "icon", "icon.png", content)
			if a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) {
				t.Errorf("%s as an icon: status %d", name, a.status)
			}
		}
		if f := s.feed("alice", 1); f.Name != "Atom corpus" {
			t.Errorf("a refused form renamed the feed to %q", f.Name)
		}

		form = s.feedForm(1)
		form.Set("icon_reset", "1")
		s.follow("/subscriptions/feeds/1", form)
		if _, err := s.db.CustomIconByHash(ctx, hash); err == nil {
			t.Error("the icon is still stored after it was given up")
		}
		if f := s.feed("alice", 1); decoded(t, f.Attributes)["customFavicon"] != nil {
			t.Errorf("feed after giving up its icon = %s", f.Attributes)
		}
	})
}

// R9: a feed is refreshed, fetched anew, emptied and deleted from its page.
func TestFeedActions(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one"))
		remote.serve("/articles/one", "text/html", `<html><body><article>Text of one</article><p class="ad">Buy</p></body></html>`)
		remote.serve("/articles/two", "text/html", `<html><body><article>Text of two<i class="ad">Buy</i></article></body></html>`)
		s.asAlice()
		s.follow("/subscriptions/feeds", url.Values{"url": {remote.URL + "/feed.xml"}})
		entries := func() []*store.Entry {
			list, err := s.db.EntriesByFeed(context.Background(), s.user("alice").ID, 9)
			if err != nil {
				t.Fatal(err)
			}
			return list
		}

		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one", "two"))
		location, body := s.follow("/subscriptions/feeds/9/refresh", nil)
		if location != "/subscriptions/feeds/9" || notice(body) != "The feed was refreshed." || len(entries()) != 2 {
			t.Errorf("after a refresh: at %q, notice %q, %d entries", location, notice(body), len(entries()))
		}

		// The selector is tried before it is stored.
		form := s.feedForm(9)
		form.Set("path_entries", "article")
		form.Set("path_entries_filter", ".ad")
		form.Set("name", "Not stored")
		a := s.post("/subscriptions/feeds/9/preview", form)
		if a.status != http.StatusOK || !strings.Contains(a.body, `<div class="entry-content"><article>Text of two</article></div>`) ||
			!strings.Contains(a.body, `name="path_entries" value="article"`) || !strings.Contains(a.body, `value="Not stored"`) {
			t.Errorf("preview: status %d\n%s", a.status, a.body)
		}
		// Without a selector the article is looked for when the form says so.
		form.Set("path_entries", "")
		form.Set("path_entries_auto", "1")
		if a := s.post("/subscriptions/feeds/9/preview", form); a.status != http.StatusOK || strings.Contains(a.body, "There is no selector to try") ||
			!strings.Contains(a.body, `name="path_entries_auto" value="1" checked`) {
			t.Errorf("preview of the automatic way: status %d\n%s", a.status, a.body)
		}
		form.Del("path_entries_auto")
		if f := s.feed("alice", 9); f.PathEntries != "" || f.Name != "The Blog" || decoded(t, f.Attributes)["path_entries_auto"] != nil {
			t.Errorf("the preview stored the form: %+v", f)
		}
		for selector, want := range map[string]string{"": "There is no selector to try,", "p[": "could not be read", "section": "could not be read"} {
			form.Set("path_entries", selector)
			if a := s.post("/subscriptions/feeds/9/preview", form); a.status != http.StatusOK || !strings.Contains(a.body, want) {
				t.Errorf("preview with the selector %q: status %d, no %q", selector, a.status, want)
			}
		}

		// Fetched anew, the entries take their text from their pages.
		form = s.feedForm(9)
		form.Set("path_entries", "article")
		s.follow("/subscriptions/feeds/9", form)
		_, body = s.follow("/subscriptions/feeds/9/reload", nil)
		if list := entries(); notice(body) != "The feed was refreshed." || len(list) != 2 || !strings.Contains(list[0].Content, "Text of one") {
			t.Errorf("after fetching anew: notice %q, entries %v", notice(body), list)
		}

		// A feed that stops answering says so and is marked.
		remote.mu.Lock()
		delete(remote.pages, "/feed.xml")
		remote.mu.Unlock()
		_, body = s.follow("/subscriptions/feeds/9/refresh", nil)
		if notice(body) != "The feed could not be refreshed. The log of the server says why." || !strings.Contains(body, "The last refresh failed") {
			t.Errorf("after a failed refresh: notice %q\n%s", notice(body), body)
		}
		if problems := s.shown("/subscriptions/problems"); !strings.Contains(problems, `<a href="/subscriptions/feeds/9">The Blog</a> <span class="mark">failing</span>`) {
			t.Errorf("the failing feed is not among those that need a look:\n%s", problems)
		}
		if body := s.shown("/subscriptions"); !strings.Contains(body, `title="1 feed fails to refresh">1</span>`) {
			t.Error("the page of subscriptions does not count the failing feed")
		}

		_, body = s.follow("/subscriptions/feeds/9/truncate", nil)
		if notice(body) != "2 entries deleted." || len(entries()) != 0 {
			t.Errorf("after emptying: notice %q, %d entries", notice(body), len(entries()))
		}
		location, body = s.follow("/subscriptions/feeds/9/delete", nil)
		if location != "/subscriptions" || notice(body) != "Unsubscribed." || len(s.feeds("alice")) != 8 {
			t.Errorf("after unsubscribing: at %q, notice %q, %d feeds", location, notice(body), len(s.feeds("alice")))
		}
		if a := s.post("/subscriptions/feeds/9/delete", nil); a.status != http.StatusNotFound {
			t.Errorf("unsubscribing twice: status %d", a.status)
		}
	})
}

// R9: categories are created, set up, emptied and deleted.
func TestCategories(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		location, body := s.follow("/subscriptions/categories", url.Values{"name": {" News "}})
		created := s.category("alice", 4)
		if location != "/subscriptions/categories/4" || notice(body) != "Category added." || created == nil || created.Name != "News" ||
			decoded(t, created.Attributes)["position"] != 2.0 {
			t.Fatalf("after creating a category: at %q, notice %q, %+v", location, notice(body), created)
		}
		for name, want := range map[string]string{
			"": "A name is needed.", "later": "A category or a label has this name already.", "News": "A category or a label has this name already.",
			strings.Repeat("я", 192): "The name is too long.",
		} {
			if location, body := s.follow("/subscriptions/categories", url.Values{"name": {name}}); location != "/subscriptions" || notice(body) != want {
				t.Errorf("creating a category named %.20q: at %q, notice %q, want %q", name, location, notice(body), want)
			}
		}
		s.system(func(system *store.System) { system.Limits.MaxCategories = 4 })
		if _, body := s.follow("/subscriptions/categories", url.Values{"name": {"One more"}}); !strings.Contains(notice(body), "as many categories") {
			t.Errorf("creating a category past the limit: notice %q", notice(body))
		}

		form := formValues(t, s.shown("/subscriptions/categories/2"))
		if form.Get("name") != "Blogs" || form.Get("position") != "0" {
			t.Errorf("form of category 2 = %v", form)
		}
		for key, value := range map[string]string{
			"name": "Journals", "position": "5", "filters_read": "intitle:ad", "keep_own": "1", "keep_period_on": "1",
			"keep_period_count": "10", "keep_period_unit": "P1D", "keep_min": "3", "keep_labels": "1",
			"read_when_same_title_in_category_on": "1", "read_when_same_title_in_category": "25",
			"read_when_same_guid_in_category_on": "1", "read_when_same_guid_in_category": "0",
			"default_sort": "f.name", "default_order": "DESC",
		} {
			form.Set(key, value)
		}
		form.Del("keep_max_on")
		s.follow("/subscriptions/categories/2", form)
		got := s.category("alice", 2)
		want := map[string]any{
			"position": 5.0, "filters": []any{map[string]any{"search": "intitle:ad", "actions": []any{"read"}}},
			"archiving": map[string]any{
				"keep_period": "P10D", "keep_max": false, "keep_min": 3.0, "keep_favourites": false, "keep_labels": true, "keep_unreads": false,
			},
			"read_when_same_title_in_category": 25.0, "read_when_same_guid_in_category": 0.0, "defaultSort": "f.name", "defaultOrder": "DESC",
		}
		if got.Name != "Journals" || !reflect.DeepEqual(decoded(t, got.Attributes), want) {
			t.Errorf("category 2 after the form: %q %s", got.Name, got.Attributes)
		}
		shown := formValues(t, s.shown("/subscriptions/categories/2"))
		if shown.Get("read_when_same_title_in_category") != "25" || shown.Get("read_when_same_guid_in_category_on") != "1" ||
			shown.Get("keep_period_count") != "10" || shown.Get("keep_max_on") != "" || shown.Get("default_sort") != "f.name" {
			t.Errorf("form of category 2 after the change = %v", shown)
		}
		// The tree follows the places: News (2), then Journals (5).
		if tree := s.page("/?state=all"); strings.Index(tree, ">News<") > strings.Index(tree, ">Journals<") || !strings.Contains(tree, ">News<") {
			t.Error("the tree does not list the categories by their places")
		}

		// A name that is taken, or none, stores nothing.
		shown.Set("name", "work & play")
		shown.Set("position", "9")
		if a := s.post("/subscriptions/categories/2", shown); a.status != http.StatusBadRequest || !strings.Contains(a.body, "has this name already") ||
			!strings.Contains(a.body, `value="work &amp; play"`) {
			t.Errorf("renaming a category to the name of a label: status %d\n%s", a.status, a.body)
		}
		if got := s.category("alice", 2); got.Name != "Journals" || decoded(t, got.Attributes)["position"] != 5.0 {
			t.Errorf("category 2 after a refused form = %+v", got)
		}

		// The category of feeds without one keeps its name and stays.
		form = formValues(t, s.shown("/subscriptions/categories/1"))
		form.Set("name", "Mine")
		form.Set("position", "1")
		s.follow("/subscriptions/categories/1", form)
		if got := s.category("alice", 1); got.Name != store.DefaultCategoryName || decoded(t, got.Attributes)["position"] != 1.0 {
			t.Errorf("default category after the form = %+v", got)
		}
		if _, body := s.follow("/subscriptions/categories/1/delete", nil); !strings.Contains(notice(body), "cannot be deleted") || s.category("alice", 1) == nil {
			t.Errorf("deleting the default category: notice %q", notice(body))
		}

		// Deleted, a category leaves its feeds to the default one.
		location, body = s.follow("/subscriptions/categories/2/delete", nil)
		if location != "/subscriptions" || notice(body) != "Category deleted." || s.category("alice", 2) != nil || s.feed("alice", 1).CategoryID != 1 {
			t.Errorf("after deleting a category: at %q, notice %q, feed 1 in %d", location, notice(body), s.feed("alice", 1).CategoryID)
		}
		_, body = s.follow("/subscriptions/categories/3/empty", nil)
		if notice(body) != "5 feeds deleted." || len(s.feeds("alice")) != 3 {
			t.Errorf("after emptying a category: notice %q, %d feeds", notice(body), len(s.feeds("alice")))
		}
	})
}

// R9: a category takes its feeds from an OPML document.
func TestCategoryOPML(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/a.xml", "application/rss+xml", rssOf(remote.URL, "A", "one"))
		remote.serve("/list.opml", "text/x-opml", `<opml version="2.0"><body><outline text="A" type="rss" xmlUrl="`+remote.URL+`/a.xml"/></body></opml>`)
		s.asAlice()
		form := formValues(t, s.shown("/subscriptions/categories/2"))
		form.Set("opml_url", "not an address ")
		if a := s.post("/subscriptions/categories/2", form); a.status != http.StatusBadRequest {
			t.Errorf("an OPML address that is none: status %d", a.status)
		}
		form.Set("opml_url", remote.URL+"/list.opml")
		s.follow("/subscriptions/categories/2", form)
		if got := s.category("alice", 2); got.Kind != 2 || decoded(t, got.Attributes)["opml_url"] != remote.URL+"/list.opml" {
			t.Fatalf("category with an OPML address = %+v", got)
		}
		location, body := s.follow("/subscriptions/categories/2/opml", nil)
		if location != "/subscriptions/categories/2" || notice(body) != "The OPML document was read." {
			t.Errorf("after reading the document: at %q, notice %q", location, notice(body))
		}
		// The feed of the document is there; those the category had are muted.
		added := s.feed("alice", 9)
		if added.URL != remote.URL+"/a.xml" || added.CategoryID != 2 || s.feed("alice", 1).TTL >= 0 || s.feed("alice", 3).TTL != 0 {
			t.Errorf("feeds after reading the document: added %+v, ttl of feed 1 %d", added, s.feed("alice", 1).TTL)
		}
		if body := s.shown("/subscriptions"); !strings.Contains(body, `<span class="muted">from an OPML document</span>`) {
			t.Error("the page of subscriptions does not mark the category that mirrors a document")
		}
		remote.serve("/list.opml", "text/html", "gone")
		if _, body := s.follow("/subscriptions/categories/2/opml", nil); !strings.Contains(notice(body), "could not be read") ||
			!strings.Contains(body, "The last attempt to read the document failed.") {
			t.Errorf("after a document that is not OPML: notice %q", notice(body))
		}
		if _, body := s.follow("/subscriptions/categories/3/opml", nil); notice(body) != "The category has no OPML document." {
			t.Errorf("reading the document of a plain category: notice %q", notice(body))
		}
		// Without an address the category is a plain one again.
		form.Set("opml_url", "")
		s.follow("/subscriptions/categories/2", form)
		if got := s.category("alice", 2); got.Kind != 0 || decoded(t, got.Attributes)["opml_url"] != nil {
			t.Errorf("category after its OPML address was taken out = %+v", got)
		}
	})
}

// R9: labels are created, renamed, given filters and deleted.
func TestLabels(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		location, body := s.follow("/subscriptions/labels", url.Values{"name": {"urgent"}})
		if location != "/subscriptions/labels/3" || notice(body) != "Label added." || s.label("alice", 3).Name != "urgent" {
			t.Fatalf("after creating a label: at %q, notice %q", location, notice(body))
		}
		for name, want := range map[string]string{"": "A name is needed.", "Blogs": "has this name already", "urgent": "has this name already"} {
			if _, body := s.follow("/subscriptions/labels", url.Values{"name": {name}}); !strings.Contains(notice(body), want) {
				t.Errorf("creating a label named %q: notice %q, want %q", name, notice(body), want)
			}
		}
		s.follow("/subscriptions/labels/3", url.Values{"name": {"now"}, "filters_label": {"intitle:urgent\nauthor:boss"}})
		got := s.label("alice", 3)
		if got.Name != "now" || !reflect.DeepEqual(decoded(t, got.Attributes)["filters"], []any{
			map[string]any{"search": "intitle:urgent", "actions": []any{"label"}},
			map[string]any{"search": "author:boss", "actions": []any{"label"}},
		}) {
			t.Errorf("label after the form = %q %s", got.Name, got.Attributes)
		}
		if form := formValues(t, s.shown("/subscriptions/labels/3")); form.Get("filters_label") != "intitle:urgent\nauthor:boss" {
			t.Errorf("form of the label = %v", form)
		}
		// The same name with other filters is no conflict.
		s.follow("/subscriptions/labels/3", url.Values{"name": {"now"}, "filters_label": {""}})
		if got := s.label("alice", 3); decoded(t, got.Attributes)["filters"] != nil {
			t.Errorf("label after its filters were taken out = %s", got.Attributes)
		}
		if a := s.post("/subscriptions/labels/3", url.Values{"name": {"Blogs"}}); a.status != http.StatusBadRequest || !strings.Contains(a.body, "has this name already") {
			t.Errorf("renaming a label to the name of a category: status %d", a.status)
		}
		// Deleting a label takes it off its entries.
		labelled := s.stored("alice", store.Listing{Set: store.EntrySet{LabelID: 1}})
		location, body = s.follow("/subscriptions/labels/1/delete", nil)
		if location != "/subscriptions" || notice(body) != "Label deleted." || s.label("alice", 1) != nil || len(labelled) == 0 {
			t.Errorf("after deleting a label: at %q, notice %q", location, notice(body))
		}
		if _, err := s.db.EntryByID(context.Background(), s.user("alice").ID, labelled[0]); err != nil {
			t.Errorf("an entry went with its label: %v", err)
		}
	})
}

// Filters of one action leave those of the others alone.
func TestFiltersByAction(t *testing.T) {
	a := readAttrs(json.RawMessage(`{"filters":[{"search":"a","actions":["read","star"]},{"search":"b","actions":["read"]},{"search":"c","actions":["star"]}]}`))
	if got := a.filtersFor("read"); got != "a\nb" {
		t.Errorf("filtersFor(read) = %q", got)
	}
	a.setFilters("read", []string{"c", "d", "d"})
	want := `[{"search":"a","actions":["star"]},{"search":"c","actions":["star","read"]},{"search":"d","actions":["read"]}]`
	if got := string(a["filters"]); got != want {
		t.Errorf("filters = %s\n     want %s", got, want)
	}
	a.setFilters("star", nil)
	a.setFilters("read", nil)
	if _, has := a["filters"]; has {
		t.Errorf("filters after all were taken out = %s", a["filters"])
	}
}

// R9: the feeds are refreshed from the reading screen; a visitor may ask
// for that only where the installation says so.
func TestRefreshNow(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one"))
		s.asAlice()
		if body := s.page("/"); !strings.Contains(body, `<form method="post" action="/refresh" class="refresh">`) ||
			strings.Contains(body, `class="stream-settings"`) {
			t.Errorf("the reading screen has no form to refresh the feeds, or links to settings the stream has not:\n%s", body)
		}
		for target, want := range map[string]string{"/feeds/2": "/subscriptions/feeds/2", "/categories/2": "/subscriptions/categories/2", "/labels/1": "/subscriptions/labels/1"} {
			if body := s.page(target); !strings.Contains(body, `<a class="stream-settings" href="`+want+`">Settings</a>`) {
				t.Errorf("GET %s does not link to %s", target, want)
			}
		}
		// The feeds of the reference live on a host there is not: only
		// what a test serves can be fetched. Mute them.
		for _, f := range s.feeds("alice") {
			f.TTL = -3600
			if err := s.db.UpdateFeed(context.Background(), f); err != nil {
				t.Fatal(err)
			}
		}
		s.follow("/subscriptions/feeds", url.Values{"url": {remote.URL + "/feed.xml"}})
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one", "two", "three"))

		// The stream of a feed refreshes that feed, whatever its period.
		location, body := s.follow("/refresh", url.Values{"stream": {"/feeds/9"}, "next": {"/feeds/9?state=all"}})
		if location != "/feeds/9?state=all" || notice(body) != "The feed was refreshed." || len(listed(body)) != 3 {
			t.Errorf("after refreshing a feed: at %q, notice %q, %d entries", location, notice(body), len(listed(body)))
		}
		if a := s.post("/refresh", url.Values{"stream": {"/feeds/99"}}); a.status != http.StatusNotFound {
			t.Errorf("refreshing a feed there is not: status %d", a.status)
		}
		// Any other stream refreshes the feeds that are due.
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one", "two", "three", "four"))
		if _, body := s.follow("/refresh", url.Values{"stream": {"/"}, "next": {"/?state=all"}}); notice(body) != "Feeds refreshed: 0 new entries." {
			t.Errorf("refreshing before anything is due: notice %q", notice(body))
		}
		f := s.feed("alice", 9)
		f.LastUpdate -= 7200
		if err := s.db.UpdateFeed(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		if location, body := s.follow("/refresh", url.Values{"stream": {"/"}, "next": {"//elsewhere.example/"}}); location != "/" ||
			notice(body) != "Feeds refreshed: 1 new entry." {
			t.Errorf("refreshing the due feeds: at %q, notice %q", location, notice(body))
		}

		// A visitor.
		s.cookies = nil
		s.system(func(system *store.System) { system.AllowAnonymous = true })
		if body := s.page("/"); strings.Contains(body, `action="/refresh"`) {
			t.Error("a visitor is offered to refresh the feeds")
		}
		if a := s.post("/refresh", url.Values{"stream": {"/"}}); a.status != http.StatusForbidden {
			t.Errorf("a visitor refreshing the feeds: status %d, want 403", a.status)
		}
		s.system(func(system *store.System) { system.AllowAnonymousRefresh = true })
		if body := s.page("/"); !strings.Contains(body, `action="/refresh"`) {
			t.Error("a visitor who may refresh the feeds is not offered to")
		}
		if a := s.post("/refresh", url.Values{"stream": {"/"}, "next": {"/"}}); a.status != http.StatusSeeOther {
			t.Errorf("a visitor who may refresh the feeds: status %d", a.status)
		}
		for _, target := range []string{"/subscriptions", "/subscriptions/feeds/1"} {
			if a := s.get(target); a.status != http.StatusSeeOther {
				t.Errorf("a visitor asking for %s: status %d", target, a.status)
			}
		}
		if a := s.post("/subscriptions/feeds/1/delete", nil); a.status != http.StatusForbidden || len(s.feeds("alice")) != 9 {
			t.Errorf("a visitor deleting a feed: status %d", a.status)
		}
	})
}

// The palette leads to the settings of everything the user is subscribed to.
func TestPaletteHasSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		var places []place
		if err := json.Unmarshal([]byte(s.page("/palette")), &places); err != nil {
			t.Fatal(err)
		}
		want := map[place]bool{
			{"Atom corpus", "/subscriptions/feeds/1", "Settings of the feed"}: false, {"Blogs", "/subscriptions/categories/2", "Settings of the category"}: false,
			{"later", "/subscriptions/labels/1", "Settings of the label"}: false, {"Subscriptions", "/subscriptions", "Page"}: false,
			{"Add a feed", "/subscriptions/add", "Page"}: false, {"Feeds that need a look", "/subscriptions/problems", "Page"}: false,
		}
		for _, p := range places {
			if _, wanted := want[p]; wanted {
				want[p] = true
			}
		}
		for p, found := range want {
			if !found {
				t.Errorf("the palette does not offer %+v", p)
			}
		}
	})
}
