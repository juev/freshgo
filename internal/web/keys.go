package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/juev/freshgo/internal/store"
)

// action is something the script does on a key.
type action struct {
	// id names the action in the settings, in the script and in the texts
	// ("key.<id>").
	id string
	// key is the key of the action unless the user chose another.
	key string
	// freshRSS is the name of the shortcut of FreshRSS that does the same,
	// empty when FreshRSS has none; freshDefault is the key FreshRSS gives
	// that shortcut (config-user.default.php at commit 219eaf58).
	freshRSS, freshDefault string
}

// actions are the actions in the order the help and the settings list them.
var actions = []action{
	{"next", "j", "next_entry", "j"},
	{"prev", "k", "prev_entry", "k"},
	{"next-unread", "h", "next_unread_entry", "h"},
	{"skip-next", "n", "skip_next_entry", "n"},
	{"skip-prev", "p", "skip_prev_entry", "p"},
	{"toggle", "o", "collapse_entry", "c"},
	{"page", "Space", "", ""},
	{"original", "v", "go_website", "space"},
	{"read", "m", "mark_read", "r"},
	{"star", "s", "mark_favorite", "f"},
	{"labels", "l", "mylabels", "l"},
	{"share", "S", "auto_share", "s"},
	{"mark-all", "A", "", ""},
	{"more", "", "load_more", "m"},
	{"refresh", "r", "actualize", "q"},
	{"search", "/", "focus_search", "a"},
	{"next-node", "J", "", ""},
	{"prev-node", "K", "", ""},
	{"unread-node", "U", "", ""},
	{"tree", "t", "toggle_aside", "t"},
	{"go-unread", "g u", "", ""},
	{"go-all", "g a", "", ""},
	{"go-starred", "g s", "", ""},
	{"go-subscriptions", "g f", "", ""},
	{"go-keys", "g k", "", ""},
	{"add-feed", "+", "", ""},
	{"palette", ":", "", ""},
	{"help", "?", "help", "f1"},
}

// namedKeys are the keys a binding names by a word, as KeyboardEvent.key
// spells them; Space stands for the space bar. Enter, Escape and Tab are
// not among them: they always do what they do.
var namedKeys = func() map[string]string {
	names := []string{
		"Space", "Home", "End", "PageUp", "PageDown", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"Backspace", "Delete", "Insert",
	}
	for i := 1; i <= 12; i++ {
		names = append(names, "F"+strconv.Itoa(i))
	}
	byLower := make(map[string]string, len(names))
	for _, name := range names {
		byLower[strings.ToLower(name)] = name
	}
	return byLower
}()

var errKey = errors.New("web: not a key")

// normalizeKey spells a binding the way the script compares it: one key or
// two pressed one after the other, each a character or a named key, with
// Ctrl, Alt and Meta in that order before it. An empty binding is no key.
func normalizeKey(binding string) (string, error) {
	strokes := strings.Fields(binding)
	if len(strokes) > 2 {
		return "", errKey
	}
	for i, stroke := range strokes {
		// The last plus may be the key itself: "+", "Ctrl++".
		key := stroke
		var modifiers []string
		if at := strings.LastIndex(stroke[:len(stroke)-1], "+"); at >= 0 {
			modifiers, key = strings.Split(stroke[:at], "+"), stroke[at+1:]
		}
		var ctrl, alt, meta, shift bool
		for _, modifier := range modifiers {
			switch strings.ToLower(modifier) {
			case "ctrl":
				ctrl = true
			case "alt":
				alt = true
			case "meta":
				meta = true
			case "shift":
				shift = true
			default:
				return "", errKey
			}
		}
		named, isNamed := namedKeys[strings.ToLower(key)]
		switch {
		case isNamed && utf8.RuneCountInString(key) > 1:
			key = named
		case utf8.RuneCountInString(key) == 1 && !shift:
			// Shift is in the character itself: "J", "?".
			if r, _ := utf8.DecodeRuneInString(key); !unicode.IsGraphic(r) {
				return "", errKey
			}
			if ctrl || alt || meta {
				key = strings.ToLower(key)
			}
		default:
			return "", errKey
		}
		stroke = key
		if shift {
			stroke = "Shift+" + stroke
		}
		if meta {
			stroke = "Meta+" + stroke
		}
		if alt {
			stroke = "Alt+" + stroke
		}
		if ctrl {
			stroke = "Ctrl+" + stroke
		}
		strokes[i] = stroke
	}
	return strings.Join(strokes, " "), nil
}

// keyboard are the settings of a user about keys.
type keyboard struct {
	// Keys are the keys the user chose, by action; an action that is not
	// there has the key it has by default.
	Keys map[string]string `json:"keys"`
	// SingleKeysOff turns off every key that is a character pressed alone.
	SingleKeysOff bool `json:"keys_single_off"`
	// Shortcuts are the keys the user had in FreshRSS.
	Shortcuts map[string]string `json:"shortcuts"`
}

func readKeyboard(u *store.User) keyboard {
	var k keyboard
	if u != nil {
		// A setting of another type is a setting nobody made.
		_ = json.Unmarshal(u.Settings, &k)
	}
	return k
}

// bindings returns the key of every action: the one the user chose here,
// else the one the user chose for it in FreshRSS, else the default one
// unless another action has taken it, which leaves the action without a
// key. A key FreshRSS gave by itself is nobody's choice: import brings all
// of them, and they would hide the keys of freshgo from everybody who moved.
func (k keyboard) bindings() map[string]string {
	out := make(map[string]string, len(actions))
	// The first key of two cannot do something alone, though it may start
	// several pairs.
	taken, starts := map[string]bool{}, map[string]bool{}
	take := func(id, key string) {
		out[id] = key
		taken[key] = true
		if first, _, pair := strings.Cut(key, " "); pair {
			starts[first] = true
		}
	}
	free := func(key string) bool {
		first, _, pair := strings.Cut(key, " ")
		return !taken[key] && !starts[key] && (!pair || !taken[first])
	}
	if k.Keys != nil {
		for _, a := range actions {
			if key, chosen := k.Keys[a.id]; chosen {
				if key, err := normalizeKey(key); err == nil {
					take(a.id, key)
				}
			}
		}
	} else {
		for _, a := range actions {
			if a.freshRSS == "" || strings.EqualFold(strings.TrimSpace(k.Shortcuts[a.freshRSS]), a.freshDefault) {
				continue
			}
			if key, err := normalizeKey(k.Shortcuts[a.freshRSS]); err == nil && key != "" && free(key) {
				take(a.id, key)
			}
		}
	}
	for _, a := range actions {
		if _, has := out[a.id]; has {
			continue
		}
		if a.key != "" && free(a.key) {
			take(a.id, a.key)
		} else {
			out[a.id] = ""
		}
	}
	return out
}

// scriptTexts are the texts the script shows, by the key it asks for.
var scriptTexts = []string{
	"js.close", "js.confirm", "js.cancel", "js.failed", "js.help", "js.help.key", "js.help.action", "js.help.fixed-enter",
	"js.help.fixed-escape", "js.help.fixed-palette", "js.help.none", "js.mark-all", "js.palette",
	"js.palette.empty", "js.palette.hint", "js.palette.action", "js.labels", "js.no-entry", "js.copied", "js.no-share",
}

// scriptConfig is what a page hands to the script.
type scriptConfig struct {
	// Keys maps a key to the action it does; SingleKeys is false when keys
	// that are a character pressed alone are turned off.
	Keys       map[string]string `json:"keys"`
	SingleKeys bool              `json:"singleKeys"`
	// Actions name the actions for the help and the palette.
	Actions []scriptAction    `json:"actions"`
	Texts   map[string]string `json:"texts"`
	URLs    map[string]string `json:"urls"`
	// MarkOnOpen makes an entry read when it is opened, AutoLoad brings the
	// next page when the end of the list comes near.
	MarkOnOpen bool `json:"markOnOpen"`
	AutoLoad   bool `json:"autoLoad"`
}

type scriptAction struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// config prepares what the script needs to know on a page.
func (h *Handler) config(v *view, who *identity) string {
	c := scriptConfig{
		Keys: map[string]string{}, SingleKeys: true, Texts: map[string]string{}, MarkOnOpen: true, AutoLoad: true,
		URLs: map[string]string{
			"palette": h.url("/palette"), "unread": h.url("/"), "all": h.url("/all"), "starred": h.url("/starred"),
			"keys": h.url("/settings/keys"), "subscriptions": h.url("/subscriptions"), "add": h.url("/subscriptions/add"),
		},
	}
	var k keyboard
	if who != nil {
		k = readKeyboard(who.user)
		prefs := readReading(who.user)
		c.MarkOnOpen = !who.anonymous && (prefs.MarkWhen.Article == nil || *prefs.MarkWhen.Article)
		c.AutoLoad = prefs.AutoLoadMore == nil || *prefs.AutoLoadMore
	}
	c.SingleKeys = !k.SingleKeysOff
	keys := k.bindings()
	for _, a := range actions {
		c.Actions = append(c.Actions, scriptAction{ID: a.id, Name: v.T("key." + a.id), Key: keys[a.id]})
		if keys[a.id] != "" {
			c.Keys[keys[a.id]] = a.id
		}
	}
	for _, key := range scriptTexts {
		c.Texts[key] = v.T(key)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// keyRow is an action on the page of keys.
type keyRow struct {
	ID, Name, Key, Default string
	// Problem says what is wrong with the key the form gave, if anything.
	Problem string
}

// keysPage is what the page of keys shows.
type keysPage struct {
	Rows       []keyRow
	SingleKeys bool
	Failed     bool
}

func (h *Handler) keysPage(w http.ResponseWriter, r *http.Request) {
	k := readKeyboard(state(r).who.user)
	h.showKeys(w, r, http.StatusOK, k.bindings(), !k.SingleKeysOff, nil)
}

func (h *Handler) showKeys(w http.ResponseWriter, r *http.Request, status int, keys map[string]string, single bool, problems map[string]string) {
	v := h.settingsView(r, "keys")
	page := keysPage{SingleKeys: single, Failed: len(problems) > 0}
	for _, a := range actions {
		page.Rows = append(page.Rows, keyRow{ID: a.id, Name: v.T("key." + a.id), Key: keys[a.id], Default: a.key, Problem: problems[a.id]})
	}
	v.Data = page
	h.render(w, r, status, "keys", v)
}

// saveKeys stores the keys the form of the page of keys gives, or puts the
// default ones back.
func (h *Handler) saveKeys(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	single := r.PostForm.Get("single") != ""
	var chosen map[string]string
	if r.PostForm.Get("reset") == "" {
		chosen = make(map[string]string, len(actions))
		problems := map[string]string{}
		given := map[string]string{}
		v := h.view(r, "keys", "keys.heading")
		for _, a := range actions {
			given[a.id] = strings.TrimSpace(r.PostForm.Get("key-" + a.id))
			key, err := normalizeKey(given[a.id])
			if err != nil {
				problems[a.id] = v.T("keys.unknown")
				continue
			}
			chosen[a.id] = key
		}
		// A key does one thing, and the first of two keys nothing alone.
		for _, a := range actions {
			for _, b := range actions {
				first, _, _ := strings.Cut(chosen[b.id], " ")
				if a.id != b.id && chosen[a.id] != "" && (chosen[a.id] == chosen[b.id] || chosen[a.id] == first) && problems[a.id] == "" {
					problems[a.id] = v.T("keys.taken", v.T("key."+b.id))
				}
			}
		}
		if len(problems) > 0 {
			h.showKeys(w, r, http.StatusBadRequest, given, single, problems)
			return
		}
	}
	err := h.db.UpdateUserSettings(ctx, user.ID, func(settings map[string]json.RawMessage) error {
		delete(settings, "keys")
		delete(settings, "keys_single_off")
		if chosen != nil {
			// The keys the user chose from now on; those of FreshRSS stay
			// as they were imported and are not looked at any more.
			raw, err := json.Marshal(chosen)
			if err != nil {
				return err
			}
			settings["keys"] = raw
			if !single {
				settings["keys_single_off"] = json.RawMessage("true")
			}
		}
		return nil
	})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.keys", 0)
	http.Redirect(w, r, h.url("/settings/keys"), http.StatusSeeOther)
}

// place is somewhere the palette can take the reader.
type place struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Kind says what the place is, in the language of the reader.
	Kind string `json:"kind"`
}

// palette lists the places the palette of commands offers: streams,
// categories, feeds, labels and the pages of the interface.
func (h *Handler) palette(w http.ResponseWriter, r *http.Request) {
	who := state(r).who
	lib, err := h.library(r.Context(), who.user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "", "palette.page")
	places := []place{
		{v.T("stream.main"), h.url("/"), v.T("palette.stream")},
		{v.T("stream.all"), h.url("/all"), v.T("palette.stream")},
		{v.T("stream.starred"), h.url("/starred"), v.T("palette.stream")},
	}
	for _, c := range lib.categories {
		places = append(places, place{c.Name, h.url("/categories/" + strconv.FormatInt(c.ID, 10)), v.T("stream.category")})
	}
	for _, f := range lib.feeds {
		places = append(places, place{f.Name, h.url("/feeds/" + strconv.FormatInt(f.ID, 10)), v.T("stream.feed")})
	}
	for _, l := range lib.labels {
		places = append(places, place{l.Name, h.url("/labels/" + strconv.FormatInt(l.ID, 10)), v.T("stream.label")})
	}
	for n, q := range lib.queries {
		if _, ok := lib.queryStream(n, q); ok && q.Name != "" {
			places = append(places, place{q.Name, h.url("/queries/" + strconv.Itoa(n)), v.T("palette.query")})
		}
	}
	if !who.anonymous {
		for _, c := range lib.categories {
			places = append(places, place{c.Name, h.url("/subscriptions/categories/" + strconv.FormatInt(c.ID, 10)), v.T("palette.category-settings")})
		}
		for _, f := range lib.feeds {
			places = append(places, place{f.Name, h.url("/subscriptions/feeds/" + strconv.FormatInt(f.ID, 10)), v.T("palette.feed-settings")})
		}
		for _, l := range lib.labels {
			places = append(places, place{l.Name, h.url("/subscriptions/labels/" + strconv.FormatInt(l.ID, 10)), v.T("palette.label-settings")})
		}
		places = append(places,
			place{v.T("sub.heading"), h.url("/subscriptions"), v.T("palette.page")},
			place{v.T("add.heading"), h.url("/subscriptions/add"), v.T("palette.page")},
			place{v.T("transfer.heading"), h.url("/subscriptions/transfer"), v.T("palette.page")},
			place{v.T("problems.heading"), h.url("/subscriptions/problems"), v.T("palette.page")})
		for _, tab := range settingsTabs {
			places = append(places, place{v.T("settings." + tab.name + ".heading"), h.url(tab.path), v.T("palette.settings")})
		}
		for _, tab := range statsTabs[:3] {
			places = append(places, place{v.T("stats." + tab.name + ".heading"), h.url(tab.path), v.T("palette.stats")})
		}
		if who.admin {
			for _, tab := range adminTabs {
				places = append(places, place{v.T("admin." + tab.name + ".heading"), h.url(tab.path), v.T("palette.admin")})
			}
		}
	}
	for _, l := range v.Menu {
		places = append(places, place{l.Name, l.URL, v.T("palette.extension")})
	}
	places = append(places, place{v.T("about.heading"), h.url("/about"), v.T("palette.page")})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(places)
}
