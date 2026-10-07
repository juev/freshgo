package web

import (
	"context"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web/i18n"
)

var configAttribute = regexp.MustCompile(`<body data-config="([^"]*)">`)

// scriptConfigOf reads what a page hands to the script.
func scriptConfigOf(t *testing.T, body string) scriptConfig {
	t.Helper()
	m := configAttribute.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no settings for the script in\n%.400s", body)
	}
	var c scriptConfig
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &c); err != nil {
		t.Fatalf("settings for the script: %v", err)
	}
	return c
}

// part asks for a part of a page the way the script does.
func (s *site) part(method, target, kind string, form url.Values) answer {
	s.t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	return s.send(r, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin", fragmentHeader: kind,
	})
}

// R7: a binding is one key or two, spelled one way.
func TestNormalizeKey(t *testing.T) {
	for binding, want := range map[string]string{
		"": "", "j": "j", " J ": "J", "?": "?", "+": "+", "ctrl++": "Ctrl++", "я": "я",
		"space": "Space", "SPACE": "Space", "f1": "F1", "F12": "F12", "pagedown": "PageDown", "arrowup": "ArrowUp",
		"ctrl+K": "Ctrl+k", "meta+alt+ctrl+x": "Ctrl+Alt+Meta+x", "shift+space": "Shift+Space", "Alt+Shift+Home": "Alt+Shift+Home",
		"g  u": "g u", "Ctrl+x Ctrl+s": "Ctrl+x Ctrl+s",
	} {
		if got, err := normalizeKey(binding); err != nil || got != want {
			t.Errorf("normalizeKey(%q) = %q, %v; want %q", binding, got, err, want)
		}
	}
	for _, binding := range []string{"jk", "g u x", "enter", "Escape", "tab", "f13", "shift+j", "hyper+j", "ctrl+", "ctrl+shift", "\x01"} {
		if got, err := normalizeKey(binding); err == nil {
			t.Errorf("normalizeKey(%q) = %q, want an error", binding, got)
		}
	}
}

// R7: every action has its default key, a user of FreshRSS the keys they
// had there, and an action of freshgo alone whose key is taken has none.
func TestBindings(t *testing.T) {
	defaults := keyboard{}.bindings()
	seen := map[string]string{}
	for _, a := range actions {
		if defaults[a.id] != a.key {
			t.Errorf("the default key of %s is %q, want %q", a.id, defaults[a.id], a.key)
		}
		if normal, err := normalizeKey(a.key); err != nil || normal != a.key {
			t.Errorf("the default key of %s, %q, is not spelled as normalizeKey spells it: %q, %v", a.id, a.key, normal, err)
		}
		if other, taken := seen[a.key]; taken && a.key != "" {
			t.Errorf("%s and %s have the key %q", a.id, other, a.key)
		}
		seen[a.key] = a.id
	}

	imported(t, Options{}, func(t *testing.T, s *site) {
		got := readKeyboard(s.user("alice")).bindings()
		// alice never changed a key in FreshRSS: the keys it gave her by
		// itself are not hers, and she has those of freshgo.
		for _, a := range actions {
			if got[a.id] != a.key {
				t.Errorf("alice of FreshRSS has %q for %s, want %q", got[a.id], a.id, a.key)
			}
		}
		// The keys she did change there are kept, and take the place of
		// the keys of freshgo they are.
		s.setting("alice", "shortcuts", map[string]string{
			"mark_favorite": "f", "go_website": "space", "help": "F1", "actualize": "m", "next_entry": "j", "collapse_entry": "x",
		})
		got = readKeyboard(s.user("alice")).bindings()
		for id, want := range map[string]string{
			"read": "", "refresh": "m", "toggle": "x", "next": "j", "star": "s", "original": "v", "page": "Space", "help": "?", "search": "/",
		} {
			if got[id] != want {
				t.Errorf("alice with keys of her own has %q for %s, want %q", got[id], id, want)
			}
		}
		if len(got) != len(actions) {
			t.Errorf("%d actions have a key or none, want all %d", len(got), len(actions))
		}
	})

	// The first key of two does nothing alone, whoever asked first.
	k := keyboard{Shortcuts: map[string]string{"mark_read": "g", "help": "Enter", "mylabels": "nonsense"}}.bindings()
	if k["read"] != "g" || k["go-unread"] != "" || k["help"] != "?" || k["labels"] != "l" {
		t.Errorf("read %q, go-unread %q, help %q, labels %q", k["read"], k["go-unread"], k["help"], k["labels"])
	}
}

// R7, R8: the page of keys changes the key of an action, refuses keys that
// cannot work, turns keys pressed alone off and puts the defaults back.
func TestKeysPage(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		if a := s.get("/settings/keys"); a.status != http.StatusSeeOther {
			t.Errorf("the page of keys without a login: status %d", a.status)
		}
		// Keys alice chose in FreshRSS, among those it gave her by itself.
		s.setting("alice", "shortcuts", map[string]string{"mark_read": "x", "mark_favorite": "b", "help": "f2", "go_website": "space", "next_entry": "j"})
		s.asAlice()
		body := s.page("/settings/keys")
		for _, want := range []string{
			`<h1>Keys</h1>`, `<label for="key-next">Open the next entry</label>`, `id="key-read" name="key-read" value="x"`,
			`id="key-page" name="key-page" value="Space"`, `<input type="checkbox" id="single" name="single" value="1" checked>`,
			`<a href="/settings/keys" aria-current="page">Keys</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /settings/keys: no %q in\n%s", want, body)
			}
		}
		if c := scriptConfigOf(t, body); c.Keys["x"] != "read" || c.Keys["v"] != "original" || !c.SingleKeys || !c.MarkOnOpen || !c.AutoLoad ||
			len(c.Actions) != len(actions) || c.Actions[0].Name != "Open the next entry" || c.Texts["js.close"] != "Close" || c.URLs["palette"] != "/palette" {
			t.Errorf("settings for the script: %+v", c)
		}

		form := func(changes ...string) url.Values {
			v := url.Values{"single": {"1"}}
			for id, key := range readKeyboard(s.user("alice")).bindings() {
				v.Set("key-"+id, key)
			}
			for i := 0; i+1 < len(changes); i += 2 {
				v.Set(changes[i], changes[i+1])
			}
			return v
		}
		a := s.post("/settings/keys", form("key-read", " ctrl+M ", "key-page", "shift+space", "key-help", ""))
		if a.status != http.StatusSeeOther || a.header.Get("Location") != "/settings/keys" {
			t.Fatalf("save keys: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		body = s.page("/settings/keys")
		if !strings.Contains(body, "Keys saved.") || !strings.Contains(body, `name="key-read" value="Ctrl&#43;m"`) {
			t.Errorf("after saving: no notice or no new key in\n%s", body)
		}
		c := scriptConfigOf(t, body)
		if c.Keys["Ctrl+m"] != "read" || c.Keys["Shift+Space"] != "page" || c.Keys["x"] != "" || c.Keys["F2"] != "" || c.Keys["?"] != "" {
			t.Errorf("keys after saving: %v", c.Keys)
		}

		// Keys that cannot work are not saved, and the form says which.
		for what, v := range map[string]url.Values{
			"not a key":           form("key-star", "nonsense"),
			"a key taken":         form("key-star", "j"),
			"the first key taken": form("key-star", "g"),
		} {
			a := s.post("/settings/keys", v)
			if a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) ||
				!strings.Contains(a.body, `aria-invalid="true" aria-describedby="key-star-problem"`) ||
				!strings.Contains(a.body, `name="key-star" value="`+v.Get("key-star")+`"`) {
				t.Errorf("%s: status %d\n%s", what, a.status, a.body)
			}
		}
		if got := readKeyboard(s.user("alice")).bindings()["star"]; got != "b" {
			t.Errorf("star has the key %q after refused forms, want b", got)
		}

		v := form()
		v.Del("single")
		s.post("/settings/keys", v)
		if c := scriptConfigOf(t, s.page("/")); c.SingleKeys || c.Keys["Ctrl+m"] != "read" {
			t.Errorf("keys pressed alone are still on, or the keys are gone: %+v", c)
		}

		s.post("/settings/keys", url.Values{"reset": {"1"}})
		c = scriptConfigOf(t, s.page("/"))
		if !c.SingleKeys || c.Keys["x"] != "read" || c.Keys["F2"] != "help" || c.Keys["Ctrl+m"] != "" {
			t.Errorf("after putting the defaults back: %+v", c.Keys)
		}

		s.setting("alice", "mark_when", map[string]any{"article": false, "max_n_unread": 5})
		s.setting("alice", "auto_load_more", false)
		if c := scriptConfigOf(t, s.page("/")); c.MarkOnOpen || c.AutoLoad {
			t.Errorf("mark_when.article and auto_load_more are off, the script is told %v and %v", c.MarkOnOpen, c.AutoLoad)
		}
	})
}

// R3, R7: a visitor has the keys of freshgo, opens entries without marking
// them, and has no page of keys.
func TestVisitorKeys(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.system(func(system *store.System) { system.AllowAnonymous = true })
		body := s.page("/")
		if c := scriptConfigOf(t, body); c.MarkOnOpen || c.Keys["j"] != "next" {
			t.Errorf("settings of the script for a visitor: %+v", c)
		}
		if strings.Contains(body, `href="/settings/keys"`) {
			t.Error("the menu of a visitor has the page of keys")
		}
		if a := s.post("/settings/keys", url.Values{"reset": {"1"}}); a.status != http.StatusForbidden {
			t.Errorf("POST /settings/keys by a visitor: status %d", a.status)
		}
		var places []place
		if err := json.Unmarshal([]byte(s.page("/palette")), &places); err != nil || slices.ContainsFunc(places, func(p place) bool { return p.URL == "/settings/keys" }) {
			t.Errorf("the palette of a visitor: %v, %v", places, err)
		}
	})
}

// R5, R7: the script gets the entry an action was about, and the tree, as
// parts of the page.
func TestPartsForTheScript(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		s.asAlice()
		id := listed(s.page("/feeds/1"))[0]
		path := "/entries/" + strconv.FormatInt(id, 10)
		article := `<article class="entry read" id="e` + strconv.FormatInt(id, 10) + `"`

		a := s.part(http.MethodPost, path+"/read", "entry", url.Values{"read": {"1"}, "next": {"/feeds/1?state=all"}})
		if a.status != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(a.body), article) || strings.Contains(a.body, "<html") ||
			!strings.Contains(a.body, `<input type="hidden" name="next" value="/feeds/1?state=all">`) ||
			!strings.Contains(a.body, `<input type="hidden" name="read" value="0">`) || !strings.Contains(a.body, ">Mark as unread</button>") {
			t.Errorf("mark read for the script: status %d\n%s", a.status, a.body)
		}
		if e, err := s.db.EntryByID(ctx, alice.ID, id); err != nil || !e.IsRead {
			t.Errorf("the entry is read %v, %v", e.IsRead, err)
		}
		a = s.part(http.MethodPost, path+"/star", "entry", url.Values{"starred": {"1"}, "next": {"/"}})
		if a.status != http.StatusOK || !strings.Contains(a.body, `aria-label="Remove star" title="Remove star" aria-pressed="true">★</button>`) || !strings.Contains(a.body, "Starred</span>") {
			t.Errorf("star for the script: status %d\n%s", a.status, a.body)
		}
		a = s.part(http.MethodPost, path+"/labels", "entry", url.Values{"label": {"1"}, "new": {"Blogs"}, "next": {"/"}})
		notice, _ := url.PathUnescape(a.header.Get(noticeHeader))
		if a.status != http.StatusOK || !strings.Contains(a.body, `<p class="entry-labels">Labels: later</p>`) ||
			!strings.Contains(notice, "A category has this name already") || s.cookies[noticeCookie] != nil {
			t.Errorf("labels for the script: status %d, notice %q, cookie %v\n%s", a.status, notice, s.cookies[noticeCookie], a.body)
		}

		// The tree alone, as the stream asked for shows it.
		a = s.part(http.MethodGet, "/feeds/1?state=all", "tree", nil)
		if a.status != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(a.body), "<details open>") || strings.Contains(a.body, "<article") ||
			!strings.Contains(a.body, `<a href="/feeds/1?state=all" aria-current="page">Atom corpus</a> <span class="count" title="3 unread entries">3</span>`) {
			t.Errorf("the tree for the script: status %d\n%s", a.status, a.body)
		}

		var places []place
		if err := json.Unmarshal([]byte(s.page("/palette")), &places); err != nil {
			t.Fatal(err)
		}
		for _, want := range []place{
			{"All items", "/", "Stream"}, {"Blogs", "/categories/2", "Category"},
			{"No identifiers", "/feeds/8", "Feed"}, {"work & play", "/labels/2", "Label"}, {"Keys", "/settings/keys", "Settings"}, {"Users", "/admin/users", "Administration"},
		} {
			if !slices.Contains(places, want) {
				t.Errorf("the palette lacks %+v among %+v", want, places)
			}
		}
	})
}

var scriptText = regexp.MustCompile(`\bt\('([^']+)'\)`)

// R17: every text the script asks for is handed to it, and exists.
func TestScriptUsesKnownTexts(t *testing.T) {
	texts, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	en := texts.Match(i18n.Fallback)
	script, err := fs.ReadFile(staticFiles, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, m := range scriptText.FindAllStringSubmatch(string(script), -1) {
		if !slices.Contains(asked, m[1]) {
			asked = append(asked, m[1])
		}
	}
	slices.Sort(asked)
	handed := slices.Sorted(slices.Values(scriptTexts))
	if !reflect.DeepEqual(asked, handed) {
		t.Errorf("the script asks for %v\nand is handed %v", asked, handed)
	}
	for _, key := range handed {
		if en.T(key) == key {
			t.Errorf("the text %q of the script does not exist", key)
		}
	}
	for _, a := range actions {
		if en.T("key."+a.id) == "key."+a.id {
			t.Errorf("the action %s has no name", a.id)
		}
	}
}
