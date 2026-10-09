//go:build e2e

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// The scenarios below drive a headless Chrome over the interface the way a
// reader without a mouse does: the browser is told an address to open and
// keys to press, nothing else. What they look at, they only read.

// browser is a Chrome in front of the interface.
type browser struct {
	t    *testing.T
	s    *site
	ctx  context.Context
	base string
}

// browse opens a browser on the interface of a site.
func browse(t *testing.T, s *site) *browser {
	t.Helper()
	server := httptest.NewServer(s.h)
	t.Cleanup(server.Close)
	options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1280, 900))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), options...)
	t.Cleanup(cancelAllocator)
	ctx, cancel := chromedp.NewContext(allocator)
	t.Cleanup(cancel)
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(cancelTimeout)
	b := &browser{t: t, s: s, ctx: ctx, base: server.URL}
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("Chrome does not start: %v", err)
	}
	return b
}

func (b *browser) run(actions ...chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	if err := chromedp.Do(b.ctx, actions...); err != nil {
		b.t.Fatalf("browser: %v", err)
	}
}

// command sends the browser a command of its protocol.
func command[P, R any](c cdp.Command[P, R], params P) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, c, params)
		return err
	})
}

// read evaluates an expression in the page and returns its value.
func read[T any](b *browser, expression string, options ...chromedp.EvaluateOption) T {
	b.t.Helper()
	value, err := chromedp.Run(b.ctx, chromedp.Evaluate[T](expression, options...))
	if err != nil {
		b.t.Fatalf("browser: %.200s: %v", expression, err)
	}
	return value
}

// loaded is true of a page whose script has run: the script is deferred,
// and a key pressed before it has run is a key nothing listens for.
const loaded = `document.readyState === 'complete'`

// open types an address into the browser and waits for the page.
func (b *browser) open(path string) {
	b.t.Helper()
	b.run(chromedp.Navigate(b.base+path), chromedp.WaitReady("body"))
	b.until("the page "+path, "true")
}

// press presses keys one after the other; a string of several characters
// is typed.
func (b *browser) press(keys ...string) {
	b.t.Helper()
	for _, key := range keys {
		b.run(chromedp.KeyEvent(key))
	}
}

// key presses a key that types nothing: what a browser makes of a key
// whose keydown a page has taken. KeyEvent types the character all the
// same, into the field the key has just moved the focus to.
func (b *browser) key(key, code string) {
	b.t.Helper()
	b.run(
		command(input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: key, Code: code}),
		command(input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key, Code: code}))
}

// chord presses a key with modifiers held.
func (b *browser) chord(key string, modifiers kb.Modifier) {
	b.t.Helper()
	b.run(chromedp.KeyEvent(key, chromedp.KeyModifiers(modifiers)))
}

func (b *browser) text(expression string) string {
	b.t.Helper()
	return read[string](b, `String(`+expression+`)`)
}

// focus says where the focus is: the identifier of the element, else its
// tag and classes.
func (b *browser) focus() string {
	b.t.Helper()
	return b.text(`(e => e ? (e.id ? '#' + e.id : e.tagName.toLowerCase() + (e.className ? '.' + e.className : '')) : '')(document.activeElement)`)
}

// until waits for an expression to become true of a page that has loaded:
// what follows a wait is a key, as a rule, and the page a key has led to
// has to listen for the next one.
func (b *browser) until(what, expression string) {
	b.t.Helper()
	b.eventually(what+" (focus on "+"%s)", func() bool {
		ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](loaded+` && Boolean(`+expression+`)`))
		return err == nil && ok
	})
}

// eventually waits for something to become true.
func (b *browser) eventually(what string, ok func() bool) {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			if strings.Contains(what, "%s") {
				what = fmt.Sprintf(what, b.focus())
			}
			b.t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// tabTo presses Tab until the focus is on an element the selector matches.
func (b *browser) tabTo(selector string) {
	b.t.Helper()
	quoted, _ := json.Marshal(selector)
	for range 300 {
		b.press(kb.Tab)
		if read[bool](b, `Boolean(document.activeElement && document.activeElement.matches(`+string(quoted)+`))`) {
			return
		}
	}
	b.t.Fatalf("Tab never reaches %s; the focus is on %s", selector, b.focus())
}

// login logs a user in through the form.
func (b *browser) login(name string) {
	b.t.Helper()
	b.open("/login")
	b.tabTo("#username")
	b.press(name)
	b.tabTo("#password")
	b.press(name+"-web-password", kb.Enter)
	b.until("the reading screen after the login", `location.pathname === '/' && document.querySelector('.entries')`)
}

var axeSource = func() string {
	source, err := os.ReadFile("testdata/axe.min.js")
	if err != nil {
		panic(err)
	}
	return string(source)
}()

// accessible checks the page with axe-core, in every look and in its light
// and its dark colours, and fails on what it finds. The page is left as it
// was.
func (b *browser) accessible(what string) {
	b.t.Helper()
	if !read[bool](b, `typeof axe !== 'undefined'`) {
		b.run(chromedp.Evaluate[chromedp.Void](axeSource))
	}
	type violation struct {
		ID    string `json:"id"`
		Help  string `json:"help"`
		Nodes []struct {
			HTML string `json:"html"`
		} `json:"nodes"`
	}
	was := read[[]string](b, `[document.documentElement.dataset.look, document.documentElement.dataset.theme]`)
	dress := func(look, theme string) {
		b.run(chromedp.Evaluate[chromedp.Void](`document.documentElement.dataset.look = '` + look + `'; document.documentElement.dataset.theme = '` + theme + `'`))
	}
	for _, look := range looks {
		for _, theme := range []string{"light", "dark"} {
			dress(look, theme)
			violations := read[[]violation](b, `axe.run(document).then(result => result.violations)`, chromedp.EvalAwaitPromise)
			for _, v := range violations {
				for _, node := range v.Nodes {
					b.t.Errorf("%s, %s and %s: axe finds %s (%s) at %.300s", what, look, theme, v.ID, v.Help, node.HTML)
				}
			}
		}
	}
	dress(was[0], was[1])
}

func e(id int64) string { return "#e" + strconv.FormatInt(id, 10) }

// current is true in the page when the entry is the one keys act on, has
// the focus and is open or closed as said.
func current(id int64, open bool) string {
	return fmt.Sprintf(`document.activeElement.id === 'e%d' && document.activeElement.classList.contains('current') && document.querySelector('#e%d details').open === %t`, id, id, open)
}

// R7, R8: entries are read, marked and starred from the keyboard, the
// focus stays on the entry and the tree keeps count.
func TestE2EReadingWithKeys(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")
		b.accessible("the reading screen")
		count := `document.getElementById('stream-unread').textContent`
		if got := b.text(count); got != "18" {
			t.Fatalf("the stream counts %s unread entries, want 18", got)
		}

		// j opens the next entry and makes it read; the tree follows.
		b.press("j")
		b.until("the first entry open", current(ids[0], true))
		b.eventually("the first entry read", func() bool { return s.entry("alice", ids[0]).IsRead })
		b.until("the first entry shown as read", `document.querySelector('`+e(ids[0])+`').classList.contains('read')`)
		b.until("17 unread entries in the tree", count+` === '17'`)
		b.press("j")
		b.until("the second entry open", current(ids[1], true))
		b.until("the first entry closed", `!document.querySelector('`+e(ids[0])+` details').open`)
		b.press("k")
		b.until("the first entry open again", current(ids[0], true))

		// n and p move without opening; o opens and closes.
		b.press("n", "n")
		b.until("the third entry in focus, closed", current(ids[2], false))
		if s.entry("alice", ids[2]).IsRead {
			t.Error("an entry gone to without opening is read")
		}
		b.press("p")
		b.until("the second entry in focus", current(ids[1], false))
		b.press("n", "o")
		b.until("the third entry open", current(ids[2], true))
		b.eventually("the third entry read", func() bool { return s.entry("alice", ids[2]).IsRead })
		b.press("o")
		b.until("the third entry closed", current(ids[2], false))
		// Enter does the same on the entry in focus.
		b.press(kb.Enter)
		b.until("the third entry open by Enter", current(ids[2], true))

		// s stars, m marks unread and read; the focus stays on the entry.
		b.press("s")
		b.eventually("the third entry starred", func() bool { return s.entry("alice", ids[2]).IsFavorite })
		b.until("the star shown", `document.querySelector('`+e(ids[2])+` .entry-state').textContent.includes('Starred')`)
		b.press("m")
		b.eventually("the third entry unread", func() bool { return !s.entry("alice", ids[2]).IsRead })
		b.until("the entry shown as unread, in focus", `!document.activeElement.classList.contains('read') && `+current(ids[2], true))
		b.press("s", "m")
		b.eventually("the third entry read and without a star", func() bool {
			e := s.entry("alice", ids[2])
			return e.IsRead && !e.IsFavorite
		})

		// An entry opened again before the server answered stays read.
		b.press("j", "j", "j", "j", "j", "j", "k", "j", "o", "o", "o", "o")
		b.until("the ninth entry open", current(ids[8], true))
		for _, id := range ids[3:9] {
			b.eventually("the entries gone through read", func() bool { return s.entry("alice", id).IsRead })
		}
		b.until("9 unread entries in the tree", count+` === '9'`)
		for _, id := range ids[3:9] {
			if !s.entry("alice", id).IsRead {
				t.Errorf("entry %d, opened more than once, is unread again", id)
			}
			if _, err := s.db.SetEntriesRead(context.Background(), s.user("alice").ID, []int64{id}, false, 1); err != nil {
				t.Fatal(err)
			}
		}
		b.open("/")

		// h goes to the next unread entry: the first three are read.
		b.press("h")
		b.until("the next unread entry open", current(ids[3], true))

		// v opens the original in a new tab.
		read[bool](b, `window.open = (address, target) => { window.openedAddress = address + ' ' + target; }, true`)
		b.press("v")
		b.until("the original opened in a new tab", `window.openedAddress === document.querySelector('`+e(ids[3])+` a.original').href + ' _blank'`)

		// Space scrolls an entry longer than the window and then goes on.
		for range 40 {
			b.press(" ")
			if read[bool](b, current(ids[4], true)) {
				break
			}
		}
		b.until("the entry after, by the space bar", current(ids[4], true))
		b.accessible("the reading screen with an entry open")
	})
}

// R7, R8: the palette, the help, the search and the menus are dialogs and
// fields that take the focus and give it back.
func TestE2EDialogs(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")

		// The palette opens on : and on Ctrl+K, finds a feed by a few of its
		// letters and goes there.
		b.press("n")
		b.until("the first entry in focus", current(ids[0], false))
		b.press(":")
		b.until("the palette open", `document.querySelector('dialog[open]') && document.activeElement.id === 'palette-input'`)
		b.accessible("the palette")
		b.press(kb.Tab, kb.Tab, kb.Tab)
		b.until("the focus kept in the palette", `document.activeElement.closest('dialog[open]')`)
		b.press(kb.Escape)
		b.until("the palette closed, the focus back on the entry", `!document.querySelector('dialog') && `+current(ids[0], false))
		b.chord("k", kb.ModifierCtrl)
		b.until("the palette open by Ctrl+K", `document.activeElement.id === 'palette-input'`)
		b.press("atm crps")
		b.until("the feed found", `document.querySelector('[role="option"][aria-selected="true"]').textContent.includes('Atom corpus')`)
		b.press(kb.Enter)
		b.until("the feed open", `location.pathname === '/feeds/1' && document.querySelector('.entries')`)

		// An action from the palette: the help, which lists the keys.
		b.chord("k", kb.ModifierCtrl)
		b.until("the palette open", `document.activeElement.id === 'palette-input'`)
		b.press("help on")
		b.until("the help found", `document.querySelector('[role="option"][aria-selected="true"]').textContent.includes('Help on keys')`)
		b.press(kb.ArrowDown, kb.ArrowUp, kb.Enter)
		b.until("the help open", `document.querySelector('dialog[open] table') && document.querySelector('dialog[open]').textContent.includes('Open the next entry')`)
		b.accessible("the help")
		b.press(kb.Escape)
		b.until("the help closed", `!document.querySelector('dialog')`)
		b.press("?")
		b.until("the help open by its key", `document.querySelector('dialog[open] table')`)
		b.press(kb.Escape)
		b.until("the help closed", `!document.querySelector('dialog')`)

		// / goes to the search; keys typed there are text.
		b.key("/", "Slash")
		b.until("the search in focus", `document.activeElement.id === 'q'`)
		b.press("jk?:")
		if got := b.text(`document.getElementById('q').value`); got != "jk?:" {
			t.Errorf("the search field has %q, want the keys typed", got)
		}
		if got := b.text(`document.querySelectorAll('dialog, .entry.current').length`); got != "0" {
			t.Errorf("keys typed in the search did something: %s dialogs and current entries", got)
		}
		b.press(kb.Escape)
		b.until("the focus out of the search", `document.activeElement.id === 'content'`)
		b.key("/", "Slash")
		b.press("intitle:guid", kb.Enter)
		b.until("the search done", `new URLSearchParams(location.search).get('q') === 'intitle:guid' && document.querySelector('.entries')`)

		// g and a letter go to a section; J and K walk the tree.
		b.press("g", "a")
		b.until("all entries", `location.pathname === '/' && location.search === '?state=all' && document.querySelector('.entries')`)
		b.press("g", "s")
		b.until("starred entries", `location.search === '?state=starred' && document.querySelector('.entries')`)
		b.press("g", "u")
		b.until("unread entries", `location.pathname === '/' && location.search === '' && document.querySelector('.entries')`)
		b.press("J")
		b.until("the first stream of the tree", `location.pathname === '/categories/2' && document.querySelector('.entries')`)
		b.press("K")
		b.until("back at the head of the tree, where everything is", `location.pathname === '/' && document.querySelector('#tree a[aria-current="page"]').textContent === 'All items'`)
		b.press("U")
		b.until("the next stream with unread entries", `location.pathname === '/categories/2'`)
	})
}

// R5, R7, R8: labels are set and everything is marked read from dialogs
// the keyboard opens.
func TestE2ELabelsAndMarkAll(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 3}, Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")
		b.open("/feeds/3")

		b.press("n", "l")
		b.until("the labels of the entry in a dialog", `document.querySelector('dialog[open] form#labels') && document.activeElement.id === 'label-1'`)
		b.accessible("the dialog of labels")
		b.press(" ")
		b.tabTo("#new-label")
		b.press("to read", kb.Enter)
		b.until("the dialog closed, the focus back on the entry", `!document.querySelector('dialog') && `+current(ids[0], false))
		b.eventually("the labels saved", func() bool {
			labels, err := s.db.EntryLabels(context.Background(), s.user("alice").ID, []int64{ids[0]})
			return err == nil && fmt.Sprint(labels[ids[0]]) == "[later to read]"
		})
		b.until("the labels shown and announced", `document.querySelector('`+e(ids[0])+` .entry-labels').textContent.includes('later, to read') && document.getElementById('messages').textContent.includes('Labels saved.')`)

		// A asks before it marks everything read; Escape leaves all as it was.
		b.press("A")
		b.until("the question", `document.querySelector('dialog[open]') && document.activeElement.textContent === 'Mark as read'`)
		b.accessible("the question before marking all")
		b.press(kb.Escape)
		b.until("the question gone", `!document.querySelector('dialog')`)
		if got := len(s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 3}, Read: ptr(false)})); got != len(ids) {
			t.Fatalf("%d unread entries after Escape, want %d", got, len(ids))
		}
		b.press("A")
		b.until("the question again", `document.querySelector('dialog[open]')`)
		b.press(kb.Enter)
		b.eventually("everything in the feed read", func() bool {
			return len(s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 3}, Read: ptr(false)})) == 0
		})
		b.until("the page says how many", `document.getElementById('messages').textContent.includes('marked as read')`)
		if got := len(s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 2}, Read: ptr(false)})); got == 0 {
			t.Error("another feed was marked read too")
		}
	})
}

// R7, R8: the same reading with Tab, Enter and the space bar alone.
func TestE2ETabOnly(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")

		// The first stop is the link to the content.
		b.open("/")
		b.press(kb.Tab)
		if got := b.focus(); got != "a.skip" {
			t.Fatalf("the first Tab stop is %s, want the link to the content", got)
		}
		b.press(kb.Enter)
		b.until("the focus on the content", `document.activeElement.id === 'content'`)

		// Tab goes through the page in the order it reads.
		var order []string
		for range 400 {
			b.press(kb.Tab)
			at := b.text(`(e => e === document.body ? 'body' : String(Array.prototype.indexOf.call(document.querySelectorAll('*'), e)))(document.activeElement)`)
			if at == "body" {
				break
			}
			order = append(order, at)
		}
		if len(order) < 60 {
			t.Fatalf("Tab stops at %d elements of the reading screen", len(order))
		}
		for i := 1; i < len(order); i++ {
			before, _ := strconv.Atoi(order[i-1])
			after, _ := strconv.Atoi(order[i])
			if after <= before {
				t.Fatalf("Tab stop %d is element %d, before the stop it follows, %d", i, after, before)
			}
		}

		b.open("/")
		first := e(ids[0])
		b.tabTo(first + " summary")
		b.press(kb.Enter)
		b.until("the entry open", `document.querySelector('`+first+` details').open`)
		b.eventually("the entry read", func() bool { return s.entry("alice", ids[0]).IsRead })
		b.tabTo(first + ` form[action$="/star"] button`)
		b.press(" ")
		b.eventually("the entry starred", func() bool { return s.entry("alice", ids[0]).IsFavorite })
		b.until("the focus on the button that takes the star off",
			`document.activeElement.getAttribute('aria-label') === 'Remove star' && document.activeElement.closest('article').id === '`+first[1:]+`'`)
		b.press(kb.Enter)
		b.eventually("the star off", func() bool { return !s.entry("alice", ids[0]).IsFavorite })

		// The menu of "mark as read" opens and closes from the keyboard.
		b.tabTo("details.menu summary")
		b.press(kb.Enter)
		b.until("the menu open", `document.querySelector('details.menu').open`)
		b.press(kb.Tab)
		b.press(kb.Escape)
		b.until("the menu closed, the focus on it", `!document.querySelector('details.menu').open && document.activeElement.matches('details.menu summary')`)
	})
}

// R7, R8: keys are changed and turned off on their page; a user of
// FreshRSS has the keys they chose there.
func TestE2EKeysPage(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		// What alice chose in FreshRSS; the keys it gave her by itself, like
		// j, are not kept, and the help is where freshgo has it.
		s.setting("alice", "shortcuts", map[string]string{"mark_read": "b", "mark_favorite": "z", "help": "f1", "next_entry": "j"})
		b := browse(t, s)
		b.login("alice")

		b.press("n", "b")
		b.eventually("the entry read by the key chosen in FreshRSS", func() bool { return s.entry("alice", ids[0]).IsRead })
		b.press("z")
		b.eventually("the entry starred by the key chosen in FreshRSS", func() bool { return s.entry("alice", ids[0]).IsFavorite })
		b.press("?")
		b.until("the help by ?", `document.querySelector('dialog[open] table')`)
		b.press(kb.Escape)

		b.press("g", "k")
		b.until("the page of keys", `location.pathname === '/settings/keys' && document.getElementById('key-star')`)
		b.accessible("the page of keys")
		b.tabTo("#key-star")
		b.press(kb.End, kb.Backspace, "x", kb.Enter)
		b.until("the keys saved", `document.getElementById('messages').textContent.includes('Keys saved.') && document.getElementById('key-star').value === 'x'`)
		b.press("g", "u")
		b.until("the reading screen", `location.pathname === '/' && document.querySelector('.entries')`)
		b.press("n", "n", "x")
		second := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})[1]
		b.eventually("an entry starred by the new key", func() bool { return s.entry("alice", second).IsFavorite })

		// A key that is taken is refused and the field says so.
		b.press("g", "k")
		b.until("the page of keys", `location.pathname === '/settings/keys' && document.getElementById('key-star')`)
		b.tabTo("#key-star")
		b.press(kb.End, kb.Backspace, "j", kb.Enter)
		b.until("the key refused", `document.getElementById('key-star').getAttribute('aria-invalid') === 'true' && document.querySelector('[role="alert"]')`)
		b.accessible("the page of keys with a key refused")

		// The second key of two may need Shift: Shift going down is no key.
		s.setting("alice", "keys", map[string]string{"go-all": "g A"})
		b.open("/")
		b.key("g", "KeyG")
		b.key("Shift", "ShiftLeft")
		b.press("A")
		b.until("all entries by g and A", `location.search === '?state=all'`)
		s.setting("alice", "keys", map[string]string{})

		// Keys pressed alone are turned off; Ctrl+K stays.
		b.open("/settings/keys")
		b.tabTo("#single")
		b.press(" ")
		b.tabTo(`form.keys button[type="submit"]:not([name])`)
		b.press(kb.Enter)
		b.until("the keys saved", `document.getElementById('messages').textContent.includes('Keys saved.') && !document.getElementById('single').checked`)
		b.open("/")
		b.press("j", "n", "?", "g", "a")
		b.chord("k", kb.ModifierCtrl)
		b.until("the palette by Ctrl+K", `document.activeElement.id === 'palette-input'`)
		if got := b.text(`location.pathname + ' ' + document.querySelectorAll('.entry.current, dialog table').length`); got != "/ 0" {
			t.Errorf("keys pressed alone are off and did something: %s", got)
		}
	})
}

// R4, R7: the list goes on as the reader reaches its end, and a keyboard
// with another layout presses the same keys.
func TestE2EListGoesOn(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "posts_per_page", 5)
		s.setting("alice", "mark_when", map[string]any{"article": false})
		ids := s.stored("alice", store.Listing{Set: mainStream()})
		b := browse(t, s)
		b.login("alice")
		b.open("/?state=all")
		for i, id := range ids {
			if i%2 == 0 {
				b.press("j")
			} else {
				// The key of j on a Russian keyboard.
				b.key("о", "KeyJ")
			}
			b.until(fmt.Sprintf("entry %d of %d open", i+1, len(ids)), current(id, true))
		}
		if got := b.text(`document.querySelectorAll('.entries article.entry').length`); got != strconv.Itoa(len(ids)) {
			t.Errorf("the page lists %s entries, want %d", got, len(ids))
		}
		b.press("j")
		b.until("the end of the list said", `document.getElementById('messages').textContent.includes('No more entries.')`)
		if unread := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)}); len(unread) != 18 {
			t.Errorf("%d unread entries with mark_when.article off, want 18", len(unread))
		}
		b.accessible("the list after it went on")
	})
}

// R8: the dark theme and a narrow screen.
func TestE2EDarkAndNarrow(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.run(command(emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}},
		}))
		b.open("/login")
		b.accessible("the login page, dark")
		b.login("alice")
		b.press("j")
		b.until("an entry open", `document.querySelector('.entry.current details').open`)
		b.accessible("the reading screen, dark")
		// U91: the browser has nothing against installing freshgo as an
		// application, and its window takes the colour of the header.
		b.run(chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
			manifest, err := cdp.Call(ctx, target, page.GetAppManifest, page.GetAppManifestParams{})
			if err != nil {
				return err
			}
			if !strings.HasSuffix(manifest.URL, manifestPath) || len(manifest.Errors) > 0 {
				t.Errorf("the manifest of the page is %q, with errors %+v", manifest.URL, manifest.Errors)
			}
			problems, err := cdp.Call(ctx, target, page.GetInstallabilityErrors, cdp.Empty{})
			if err != nil {
				return err
			}
			for _, problem := range problems.InstallabilityErrors {
				t.Errorf("the browser does not install freshgo: %s %+v", problem.ErrorID, problem.ErrorArguments)
			}
			return nil
		}))
		if got := b.text(`document.querySelector('meta[name="theme-color"]').content === getComputedStyle(document.querySelector('.site-header')).backgroundColor`); got != "true" {
			t.Error("the colour of the window is not that of the header")
		}
		b.press("?")
		b.until("the help", `document.querySelector('dialog[open] table')`)
		b.accessible("the help, dark")
		b.press(kb.Escape)

		// On a narrow screen the tree is folded; t unfolds it and goes there.
		// A phone widens its window with a page that is wider than the
		// screen, so the width of a page is held against that of the
		// document, which stays that of the screen.
		b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 360, Height: 740, DeviceScaleFactor: 1, Mobile: true}))
		b.open("/")
		b.until("the tree folded", `!document.querySelector('#tree details').open`)
		b.accessible("the reading screen, narrow")
		// The menu is behind its button.
		if got := b.text(`document.querySelector('.site-nav').getClientRects().length`); got != "0" {
			t.Error("the menu is in sight on a narrow screen before its button is pressed")
		}
		b.tabTo(".menu-toggle")
		b.press(kb.Enter)
		b.until("the menu open", `document.querySelector('.menu-toggle').getAttribute('aria-expanded') === 'true' && document.querySelector('.site-nav a').getClientRects().length`)
		b.accessible("the menu, narrow")
		b.press(kb.Enter)
		b.until("the menu folded", `!document.querySelector('.site-nav').getClientRects().length`)
		// What keys do to entries is at the foot of the screen as buttons.
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b.tabTo(`.reader-bar button[data-run="next"]`)
		b.press(kb.Enter)
		b.until("the first entry open by the button", current(ids[0], true))
		b.eventually("the entry read", func() bool { return s.entry("alice", ids[0]).IsRead })
		b.accessible("an entry open above the bar, narrow")
		// The bar stands before the entries: Shift+Tab goes back to it.
		for range 10 {
			if b.chord(kb.Tab, kb.ModifierShift); read[bool](b, `document.activeElement.dataset.run === 'star'`) {
				break
			}
		}
		if got := b.focus(); got != "button" {
			t.Fatalf("Shift+Tab does not reach the bar from the entry; the focus is on %s", got)
		}
		b.press(kb.Enter)
		b.eventually("the entry starred by the button", func() bool { return s.entry("alice", ids[0]).IsFavorite })
		// The focus stays on the button: the next one follows it.
		b.tabTo(`.reader-bar button[data-run="next"]`)
		b.press(kb.Enter)
		b.until("the second entry open by the button", current(ids[1], true))
		if got := b.text(`document.querySelector('.entry.current').getBoundingClientRect().top < document.querySelector('.reader-bar').getBoundingClientRect().top`); got != "true" {
			t.Error("the entry the button opened is under the bar")
		}
		b.press("t")
		b.until("the tree unfolded, in focus", `document.querySelector('#tree details').open && document.activeElement.matches('#tree summary')`)
		if got := b.text(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`); got != "true" {
			t.Error("the narrow page scrolls sideways")
		}
		// The pages of settings, dark and narrow at once.
		for _, page := range []string{"/subscriptions", "/subscriptions/add", "/subscriptions/feeds/3", "/subscriptions/categories/2", "/subscriptions/labels/1", "/subscriptions/problems", "/subscriptions/transfer",
			"/settings/display", "/settings/queries", "/settings/integrations", "/settings/reading", "/settings/archiving", "/settings/privacy", "/settings/profile", "/log",
			"/stats", "/stats/repartition", "/stats/unread", "/admin/users", "/admin/users/bob", "/admin/system", "/admin/authentication", "/log?all=1", "/reauth",
			"/settings/keys", "/about"} {
			b.open(page)
			b.accessible(page + ", dark and narrow")
			if got := b.text(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`); got != "true" {
				t.Errorf("%s scrolls sideways on a narrow screen", page)
			}
		}
	})
}

// U89, U92: on a phone the width of the screen goes to the text of an open
// entry, and text is smaller than a field.
func TestE2EPhoneText(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		words := strings.Repeat("word ", 40)
		quoted := &store.Entry{FeedID: 1, GUID: "quoted", Title: "A title long enough to reach the star of its entry", Link: "https://example.org/quoted",
			Content: "<p>" + words + "</p><blockquote><p>" + words + "</p></blockquote><figure><figcaption>" + words + "</figcaption></figure><ul><li>" + words + "</li></ul>"}
		if err := s.db.InsertEntries(context.Background(), s.user("alice").ID, []*store.Entry{quoted}); err != nil {
			t.Fatal(err)
		}
		b := browse(t, s)
		b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 360, Height: 740, DeviceScaleFactor: 1, Mobile: true}))
		b.login("alice")
		id := strconv.FormatInt(quoted.ID, 10)
		width := func(selector string) float64 {
			return read[float64](b, `document.querySelector('`+selector+`').getBoundingClientRect().width`)
		}
		wide := func(where string) {
			t.Helper()
			for selector, least := range map[string]float64{".entry-content > p": 320, ".entry-content blockquote p": 300, ".entry-content figure": 320, ".entry-content li": 290} {
				if got := width(selector); got < least {
					t.Errorf("%s: %s is %.0f px wide on a screen of 360, want at least %.0f", where, selector, got, least)
				}
			}
			if got := b.text(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`); got != "true" {
				t.Errorf("%s scrolls sideways", where)
			}
		}
		b.open("/feeds/1#e" + id)
		b.until("the entry of the address current", current(quoted.ID, false))
		b.press("o")
		b.until("the entry open", current(quoted.ID, true))
		for _, look := range looks {
			b.run(chromedp.Evaluate[chromedp.Void](`document.documentElement.dataset.look = '` + look + `'`))
			wide("an open entry, " + look)
			// Text is smaller than a field, which keeps the size under
			// which iOS zooms into it.
			if got := read[float64](b, `parseFloat(getComputedStyle(document.querySelector('.entry:not(#e`+id+`) .entry-title')).fontSize)`); got != 15 {
				t.Errorf("%s: the title of a row is %v px on a phone, want 15", look, got)
			}
			if got := read[float64](b, `parseFloat(getComputedStyle(document.querySelector('#e`+id+` .entry-content')).fontSize)`); got < 15.5 || got > 16 {
				t.Errorf("%s: the text of an open entry is %v px on a phone, want about 15.9", look, got)
			}
			if got := b.text(`[...document.querySelectorAll('input:not([type=checkbox], [type=radio], [type=hidden]), select, textarea')].filter(f => parseFloat(getComputedStyle(f).fontSize) < 16).map(f => f.name || f.id).join(' ')`); got != "" {
				t.Errorf("%s: fields with text under 16 px on a phone: %s", look, got)
			}
			// The star stays in the head of the card, on top of it and
			// clear of the title.
			if got := b.text(`(() => {
				const star = document.querySelector('#e` + id + ` button.star').getBoundingClientRect();
				const title = document.querySelector('#e` + id + ` .entry-title').getBoundingClientRect();
				const card = document.querySelector('#e` + id + ` details').getBoundingClientRect();
				const hit = document.elementFromPoint(star.left + star.width / 2, star.top + star.height / 2);
				return hit && hit.matches('#e` + id + ` button.star') && star.left >= title.right && star.right <= card.right && star.top >= card.top && star.bottom <= title.bottom + star.height;
			})()`); got != "true" {
				t.Errorf("%s: the star of an open entry is not in the head of its card, clear of the title", look)
			}
		}
		b.accessible("an entry with a quote open, narrow")
		b.tabTo("#e" + id + " button.star")
		b.press(kb.Enter)
		b.eventually("the open entry starred by its star", func() bool { return s.entry("alice", quoted.ID).IsFavorite })
		b.open("/entries/" + id)
		wide("the page of an entry")
		b.accessible("the page of an entry with a quote, narrow")
	})
}

// U95: the actions of an open entry stand before its text and stay on the
// screen while a long text is read.
func TestE2EActionsInReach(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		long := &store.Entry{FeedID: 1, GUID: "long", Title: "A long entry", Link: "https://example.org/long",
			Content: strings.Repeat("<p>"+strings.Repeat("word ", 60)+"</p>", 40)}
		if err := s.db.InsertEntries(context.Background(), s.user("alice").ID, []*store.Entry{long}); err != nil {
			t.Fatal(err)
		}
		id := strconv.FormatInt(long.ID, 10)
		b := browse(t, s)
		b.login("alice")
		// reach scrolls to the middle of the text and says what is wrong
		// with the actions there.
		reach := func(article string) string {
			return b.text(`(() => {
				const article = document.querySelector('` + article + `');
				const actions = article.querySelector('.entry-actions');
				const content = article.querySelector('.entry-content');
				if (article.querySelectorAll('.entry-actions').length !== 1) return 'not one block of actions';
				if (!(actions.compareDocumentPosition(content) & Node.DOCUMENT_POSITION_FOLLOWING)) return 'the actions follow the text';
				const text = content.getBoundingClientRect();
				if (text.height < innerHeight * 2) return 'the text is too short to tell';
				scrollBy({ top: text.top + text.height / 2, behavior: 'instant' });
				if (content.getBoundingClientRect().top >= 0) return 'the page did not scroll';
				const box = actions.getBoundingClientRect();
				if (box.top < 0 || box.bottom > innerHeight) return 'the actions are off the screen';
				const bar = document.querySelector('.reader-bar');
				if (bar && getComputedStyle(bar).display !== 'none' && box.bottom > bar.getBoundingClientRect().top) return 'the actions are under the toolbar';
				for (const control of actions.querySelectorAll('a, button:not([hidden]), summary')) {
					const at = control.getBoundingClientRect();
					const hit = document.elementFromPoint(at.left + at.width / 2, at.top + at.height / 2);
					if (!hit || !control.contains(hit)) return 'covered: ' + control.textContent.trim();
				}
				return '';
			})()`)
		}
		for _, screen := range []struct {
			name          string
			width, height int64
			mobile        bool
		}{{"a wide screen", 1280, 800, false}, {"a phone", 360, 740, true}} {
			b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: screen.width, Height: screen.height, DeviceScaleFactor: 1, Mobile: screen.mobile}))
			for _, look := range looks {
				b.open("/feeds/1?state=all#e" + id)
				b.until("the entry of the address current", current(long.ID, false))
				b.run(chromedp.Evaluate[chromedp.Void](`document.documentElement.dataset.look = '` + look + `'`))
				b.press("o")
				b.until("the entry open", current(long.ID, true))
				if got := reach("#e" + id); got != "" {
					t.Errorf("%s, %s, in a stream: %s", screen.name, look, got)
				}
				b.accessible("a long entry read halfway, " + screen.name + ", " + look)
				b.open("/entries/" + id)
				b.run(chromedp.Evaluate[chromedp.Void](`document.documentElement.dataset.look = '` + look + `'`))
				if got := reach("article.entry.single"); got != "" {
					t.Errorf("%s, %s, on the page of the entry: %s", screen.name, look, got)
				}
			}
		}
	})
}

// U97: the text of an open entry is taken from its page by a key and put
// back by the button that took its place.
func TestE2EFullTextOfAnEntry(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		paragraph := strings.Repeat("The harbour was quiet that morning, and the boats lay still on the water. ", 4)
		// The feed gives the entry no text at all: its row has no excerpt.
		remote.serve("/feed.xml", "application/rss+xml", strings.Replace(rssOf(remote.URL, "The Blog", "one"), "Summary of one", "", 1))
		remote.serve("/articles/one", "text/html", `<html><head><title>News</title></head><body><nav><a href="/">Home of the gazette</a></nav>`+
			`<article><h1>Boats</h1><p>First. `+paragraph+`</p><p>Second. `+paragraph+`</p><p>Third. `+paragraph+`</p></article></body></html>`)
		s.asAlice()
		s.follow("/subscriptions/feeds", url.Values{"url": {remote.URL + "/feed.xml"}})
		b := browse(t, s)
		b.login("alice")
		b.open("/feeds/9")
		text := `document.querySelector('.entry.current .entry-content').textContent`
		button := `document.querySelector('.entry.current form[action$="/fulltext"] button').textContent`
		b.press("j")
		b.until("the entry open without a text", `document.querySelector('.entry.current details').open && `+text+`.trim() === '' && `+button+` === 'Full text'`)

		b.press("f")
		b.until("the text of the page in the open entry", text+`.includes('Second. The harbour') && !`+text+`.includes('Home of the gazette') && `+button+` === 'Text of the feed'`)
		id := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 9}})[0]
		b.eventually("the text of the page stored", func() bool { return strings.Contains(s.entry("alice", id).Content, "Second. The harbour") })
		// The row of the entry gets the beginning of the new text, in its place.
		b.until("the excerpt in the row", `document.querySelector('.entry.current summary .entry-title + .entry-excerpt').textContent.includes('First. The harbour') && !document.querySelector('.entry.current .entry-body .entry-excerpt')`)
		b.accessible("an entry with the text of its page")

		b.tabTo(`.entry.current form[action$="/fulltext"] button`)
		b.press(kb.Enter)
		b.until("the text of the feed back", text+`.trim() === '' && !document.querySelector('.entry.current .entry-excerpt') && `+button+` === 'Full text'`)
		b.until("the focus on the button that took the place", `document.activeElement.matches('.entry.current form[action$="/fulltext"] button')`)

		// U100: a page that is slow and then has no article. While it is
		// waited for the button shows it; then the entry says, beside its
		// actions, that the text could not be taken.
		slow := make(chan struct{})
		remote.mu.Lock()
		remote.pages["/articles/one"] = func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-slow:
			case <-r.Context().Done():
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><title>Bare</title></head><body><nav></nav></body></html>`))
		}
		remote.mu.Unlock()
		waits := `document.querySelector('.entry.current form[action$="/fulltext"] button')`
		b.press("f")
		b.until("the button waiting for the page", waits+`.getAttribute('aria-busy') === 'true'`)
		close(slow)
		b.until("the entry saying beside its actions that there is no text, the button no longer waiting",
			`document.querySelector('.entry.current .entry-actions .entry-notice').textContent.includes('could not be taken from the page') && `+
				`document.getElementById('messages').textContent.includes('could not be taken from the page') && !`+waits+`.hasAttribute('aria-busy') && `+button+` === 'Full text'`)
		b.accessible("an entry that says what its action came to")
		// The next action takes the words away.
		b.press("m")
		b.until("the notice gone with the next action", `!document.querySelector('.entry.current .entry-notice')`)

		// The help names the action and its key.
		b.press("?")
		b.until("the help with the action", `[...document.querySelectorAll('dialog[open] tr')].some(row => row.textContent.includes('Take the text from the page') && row.textContent.includes('f'))`)
	})
}

// U98: an entry is translated by a key, part by part before the eyes of
// the reader, who can stop it, go on, and see the original again.
func TestE2ETranslate(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		in := interpret(t, s)
		in.opening, in.hold = make(chan struct{}), make(chan struct{})
		b := browse(t, s)
		b.login("alice")
		id := strconv.FormatInt(in.entry.ID, 10)
		b.open("/feeds/1?state=all#e" + id)
		b.until("the entry of the address current", current(in.entry.ID, false))
		b.press("o")
		b.until("the entry open", current(in.entry.ID, true))
		translated := `[...document.querySelectorAll('#e` + id + ` .entry-content p')].filter(p => p.textContent.startsWith('Привет')).length`
		button := `document.querySelector('#e` + id + ` form[action$="/translate"] button').textContent`

		// Before the service has said a word, the button and the page say
		// that the translation was asked for.
		b.press("T")
		b.until("the button and the page saying that it has begun, nothing translated yet",
			button+` === 'Stop translating…' && document.querySelector('#e`+id+` form[action$="/translate"] button').getAttribute('aria-busy') === 'true' && `+
				`document.getElementById('messages').textContent === 'Translating…' && `+translated+` === 0`)
		// The first part comes and is shown while the second is waited for.
		close(in.opening)
		b.until("the first paragraph and the title translated, the button offering to stop",
			translated+` === 1 && document.querySelector('#e`+id+` .entry-title').textContent === 'Привет, дорогой читатель' && `+button+` === 'Stop translating (20%)'`)
		b.accessible("an entry partly translated")

		// Stopped, it says so at once, stays where the part under way
		// leaves it and offers to go on.
		b.tabTo("#e" + id + ` form[action$="/translate"] button`)
		b.press(kb.Enter)
		b.until("the button saying that it stops", button+` === 'Stopping…'`)
		close(in.hold)
		b.until("the translation stopped after the part under way", translated+` === 3 && `+button+` === 'Translate on (60%)'`)
		b.eventually("three paragraphs of five stored", func() bool {
			return strings.Count(translate.Of(s.entry("alice", in.entry.ID), "en").Content, "<p>Привет") == 3
		})

		// Asked again, it goes on to the end and offers the original.
		b.press("T")
		b.until("everything translated", translated+` === 5 && `+button+` === 'Show the original'`)
		b.until("the focus still in the entry", `document.querySelector('#e`+id+`').contains(document.activeElement)`)
		b.tabTo("#e" + id + ` form[action$="/translate"] button`)
		b.press(kb.Enter)
		b.until("the original shown", translated+` === 0 && document.querySelector('#e`+id+` .entry-title').textContent === 'Hello, dear reader' && `+button+` === 'Show the translation'`)

		// A service that fails leaves the button as it was, not waiting for ever.
		s.h.translator = translate.New(s.db, &translate.Client{URL: "http://127.0.0.1:1", Key: "k", Model: "m", HTTP: mustFetch(t)})
		other := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})[0]
		b.open("/?state=all#e" + strconv.FormatInt(other, 10))
		b.until("another entry current", current(other, false))
		b.press("o", "T")
		waits := `document.querySelector('` + e(other) + ` form[action$="/translate"] button')`
		b.until("the failure said, the button back to what it offered",
			`document.getElementById('messages').textContent.includes('could not be translated') && `+waits+`.textContent === 'Translate' && !`+waits+`.hasAttribute('aria-busy')`)

		// The help names the action and its key.
		b.press("?")
		b.until("the help with the action", `[...document.querySelectorAll('dialog[open] tr')].some(row => row.textContent.includes('Translate the entry') && row.textContent.includes('T'))`)
	})
}

// U99: the image of a folded entry is fetched when the entry is opened,
// not when the page opens.
func TestE2EImagesWait(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/picture.svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40"/></svg>`)
		hits := func() int {
			remote.mu.Lock()
			defer remote.mu.Unlock()
			return remote.hits["/picture.svg"]
		}
		pictured := &store.Entry{FeedID: 1, GUID: "pictured", Title: "With a picture", Link: "https://example.org/pictured",
			Content: `<p><img src="` + remote.URL + `/picture.svg" alt="A square"></p>`}
		if err := s.db.InsertEntries(context.Background(), s.user("alice").ID, []*store.Entry{pictured}); err != nil {
			t.Fatal(err)
		}
		b := browse(t, s)
		b.login("alice")
		id := strconv.FormatInt(pictured.ID, 10)
		b.open("/feeds/1?state=all#e" + id)
		b.until("the entry of the address current, folded", current(pictured.ID, false))
		b.until("the image set to wait", `document.querySelector('#e`+id+` .entry-content img').loading === 'lazy'`)
		// The page is loaded, and whatever it fetches by itself is fetched.
		time.Sleep(500 * time.Millisecond)
		if n := hits(); n != 0 {
			t.Errorf("the image of a folded entry was requested %d times before the entry was opened", n)
		}
		b.press("o")
		b.until("the entry open", current(pictured.ID, true))
		b.eventually("the image requested once the entry is open", func() bool { return hits() > 0 })
		b.until("the image shown", `document.querySelector('#e`+id+` .entry-content img').naturalWidth === 40`)
	})
}

// U93, U94: on a phone a reader goes through the entries by their rows, one
// open at a time, and the controls of a stream wait behind a button.
func TestE2EPhoneRows(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")
		shown := func(selector string) bool {
			return read[bool](b, `document.querySelector('`+selector+`').getClientRects().length > 0`)
		}
		// A wide screen has the controls in sight and no button for them.
		if shown(".filters-toggle") || !shown(".toolbar") || !shown(".stream-head .views") {
			t.Error("on a wide screen the controls of a stream are not in sight, or their button is")
		}

		b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 360, Height: 740, DeviceScaleFactor: 1, Mobile: true}))
		b.open("/")
		b.until("the tree folded", `!document.querySelector('#tree details').open`)
		if !shown(".filters-toggle") || shown(".toolbar") || shown(".stream-head .views") {
			t.Error("on a narrow screen the controls of a stream are in sight before their button is pressed")
		}
		if top := read[float64](b, `document.querySelector('article.entry').getBoundingClientRect().top`); top > 370 {
			t.Errorf("the first entry stands %.0f px down a screen of 740, want the upper half", top)
		}
		b.accessible("the reading screen with its controls folded, narrow")

		// A row opens its entry as a key does: the one open before closes,
		// and the title comes to the top.
		row := func(id int64) string { return e(id) + " summary" }
		open := func(id int64, is bool) string {
			return fmt.Sprintf(`document.querySelector('#e%d details').open === %t`, id, is)
		}
		b.tabTo(row(ids[0]))
		b.press(kb.Enter)
		b.until("the first entry open by its row", open(ids[0], true)+` && document.querySelector('#e`+strconv.FormatInt(ids[0], 10)+`').classList.contains('current')`)
		b.eventually("the entry read", func() bool { return s.entry("alice", ids[0]).IsRead })
		if got := b.focus(); !strings.HasPrefix(got, "summary") {
			t.Errorf("the focus left the row for %s", got)
		}
		b.tabTo(row(ids[2]))
		b.press(kb.Enter)
		b.until("the third entry open and the first closed", open(ids[2], true)+" && "+open(ids[0], false))
		b.until("the title of the entry at the top of the screen", `(top => top >= 0 && top < 80)(document.querySelector('#e`+strconv.FormatInt(ids[2], 10)+`').getBoundingClientRect().top)`)
		b.eventually("the third entry read", func() bool { return s.entry("alice", ids[2]).IsRead })
		if s.entry("alice", ids[1]).IsRead {
			t.Error("the entry between the two rows pressed was read")
		}
		// The button of the bar goes on from the entry of the row. A finger
		// presses it: Tab would pass other entries on its way to the bar,
		// and the entry in focus is the current one.
		b.run(chromedp.Evaluate[chromedp.Void](`document.querySelector('.reader-bar button[data-run="next"]').click()`))
		b.until("the fourth entry open by the bar", current(ids[3], true)+" && "+open(ids[2], false))
		// The head of an open entry folds it.
		b.tabTo(row(ids[3]))
		b.press(kb.Enter)
		b.until("the entry folded by its head", open(ids[3], false))

		// The controls come from behind their button and go back.
		b.tabTo(".filters-toggle")
		b.press(kb.Enter)
		b.until("the controls in sight", `document.querySelector('.filters-toggle').getAttribute('aria-expanded') === 'true' && document.querySelector('#q').getClientRects().length`)
		if !shown(".stream-head .views") {
			t.Error("the choice between a list and open entries is not among the controls")
		}
		b.accessible("the controls of a stream open, narrow")
		if got := b.text(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`); got != "true" {
			t.Error("the controls of a stream make the page scroll sideways")
		}
		b.press(kb.Enter)
		b.until("the controls folded", `!document.querySelector('.toolbar').getClientRects().length`)
		// The key of the search finds its field behind the button.
		b.press("/")
		b.until("the search field in focus and in sight", `document.activeElement.id === 'q' && document.activeElement.getClientRects().length`)

		// What is asked of a stream is in sight when the page opens.
		for _, asked := range []string{"/?state=all", "/?q=the", "/?order=asc"} {
			b.open(asked)
			if !shown(".toolbar") {
				t.Errorf("%s: the controls are folded though the stream is asked something", asked)
			}
		}
		// Where every entry is listed open, a row closes nothing.
		b.open("/?view=expanded")
		if n := read[int](b, `document.querySelectorAll('.entries details:not([open])').length`); n != 0 {
			t.Fatalf("%d entries are closed where all are listed open", n)
		}
		rest := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b.tabTo(row(rest[0]))
		b.press(kb.Enter)
		b.until("an entry folded among open ones", open(rest[0], false))
		b.press(kb.Enter)
		b.until("the entry open again and the next one still open", open(rest[0], true)+" && "+open(rest[1], true))
	})
}

// R7, R16: a visitor registers with the keyboard alone.
func TestE2ERegister(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.system(func(system *store.System) { system.Limits.MaxRegistrations, system.TOS = 0, "<p>Be kind.</p>" })
		b := browse(t, s)
		b.open("/login")
		b.tabTo(`a[href$="/register"]`)
		b.press(kb.Enter)
		b.until("the page of registration", `location.pathname === '/register'`)
		b.accessible("the page of registration")
		b.tabTo("#username")
		b.press("carol")
		b.tabTo("#password")
		b.press("carol-password")
		b.tabTo("#again")
		b.press("carol-password")
		b.tabTo("#accept_tos")
		b.press(" ", kb.Enter)
		b.until("logged in as the new user", `location.pathname === '/' && document.querySelector('.account-name').textContent === 'carol'`)
		b.accessible("the reading screen of a user without subscriptions")
		b.open("/tos")
		b.accessible("the terms")
		b.open("/stats")
		b.accessible("the statistics of a user without entries")
	})
}

// R7, R13: an entry is passed on from the keyboard.
func TestE2EShare(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.login("alice")
		b.press("j")
		b.until("an entry open", `document.querySelector('.entry.current details').open`)
		// Nothing is set up yet: the key says so.
		b.press("S")
		b.until("the hint to set sharing up", `document.querySelector('#messages').textContent.includes('Integrations')`)

		// A service is added with Tab and Enter.
		b.open("/settings/integrations")
		b.accessible("the integrations")
		b.tabTo("#add_type")
		b.press("Mastodon")
		b.tabTo("#add_url")
		b.press("https://social.example", kb.Enter)
		b.until("the service stored", `document.querySelector('#messages').textContent.includes('Saved.') && document.querySelector('#name-0')`)
		b.accessible("the integrations with a service")
		s.setting("alice", "sharing", []map[string]any{
			{"type": "mastodon", "name": "Mastodon", "url": "https://social.example"}, {"type": "print", "name": "Print"},
		})

		b.open("/")
		b.press("j")
		b.until("an entry open", `document.querySelector('.entry.current details').open`)
		b.press("S")
		b.until("the menu of sharing open, the focus on its first way",
			`document.querySelector('.entry.current details.share').open && document.activeElement.textContent === 'Mastodon' && document.activeElement.href.startsWith('https://social.example/share?title=')`)
		b.until("the button of the browser's own way shown", `!document.querySelector('.entry.current button[data-share="print"]').hidden`)
		b.accessible("the menu of sharing")
		b.press(kb.Escape)
		b.until("the menu closed", `!document.querySelector('.entry.current details.share').open`)
	})
}

// R7, R12: a view is saved, handed out and read at its public address with
// the keyboard alone.
func TestE2EQueries(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.login("alice")
		b.open("/feeds/1?state=all")
		b.tabTo(`details.menu form[action$="/queries"] input[name="name"], details.menu:not([open]) summary`)
		b.press(kb.Enter)
		b.tabTo("#query-name")
		b.press("Atom, all", kb.Enter)
		b.until("the page of the saved query", `location.pathname === '/settings/queries/0' && document.querySelector('#messages').textContent.includes('Query saved.')`)
		b.accessible("the page of a saved query")
		b.tabTo("#share_rss")
		b.press(" ")
		b.tabTo(`form.settings button[type="submit"]`)
		b.press(kb.Enter)
		b.until("the public addresses", `document.querySelector('a[href$=".rss"]')`)
		b.accessible("the page of a shared query")
		address := b.text(`new URL(document.querySelector('a[href$=".html"]').href).pathname`)

		// The query stands in the tree; K from the stream after it leads there.
		b.open("/settings/queries")
		b.accessible("the saved queries")
		b.open("/queries/0")
		b.until("the saved query in the tree", `document.querySelector('#tree a[aria-current="page"]').textContent === 'Atom, all'`)
		b.accessible("the reading screen of a saved query")
		b.press("j")
		b.until("an entry of the query open", `document.querySelector('.entry.current details').open`)

		// Its public page is read without a login.
		b.tabTo(`form[action$="/logout"] button`)
		b.press(kb.Enter)
		b.until("logged out", `location.pathname === '/login'`)
		b.open(address)
		b.until("the public page", `document.querySelector('h1').textContent === 'Atom, all' && document.querySelectorAll('article').length > 0`)
		b.accessible("the public page of a query")
		b.tabTo("#q")
		b.press("intitle:plain", kb.Enter)
		b.until("the public page narrowed", `document.querySelectorAll('article').length === 1`)
	})
}

// R7, R11, R15: settings are changed and a user is added with the keyboard
// alone.
func TestE2ESettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.login("alice")

		// The palette knows the pages of settings.
		b.chord("k", kb.ModifierCtrl)
		b.until("the palette", `document.activeElement.id === 'palette-input'`)
		b.press("Display")
		b.until("the page found", `document.querySelector('[role="option"][aria-selected="true"]').textContent.includes('Display')`)
		b.press(kb.Enter)
		b.until("the page of the display", `location.pathname === '/settings/display'`)
		b.accessible("the settings of the display")
		// A select is changed by typing into it.
		b.tabTo("#look")
		b.press("Reader 2")
		b.tabTo("#darkMode")
		b.press("D")
		b.tabTo(`form[action$="/settings/display"] button[type="submit"]`)
		b.press(kb.Enter)
		b.until("the other look in dark colours",
			`document.documentElement.dataset.look === 'modern' && document.documentElement.dataset.theme === 'dark' && document.querySelector('#messages').textContent.includes('Saved.')`)
		if got := s.settings("alice"); got["darkMode"] != "dark" || got["look"] != "modern" {
			t.Errorf("after the form darkMode = %v, look = %v", got["darkMode"], got["look"])
		}

		// The menu of the section is a Tab away; so is every page of it.
		b.tabTo(`nav.tabs a[href$="/settings/reading"]`)
		b.press(kb.Enter)
		b.until("the page of reading", `location.pathname === '/settings/reading'`)
		b.accessible("the settings of reading, dark")
		b.tabTo("#display_posts")
		b.press(" ")
		b.tabTo(`form[action$="/settings/reading"] button[type="submit"]`)
		b.press(kb.Enter)
		b.until("the settings saved", `document.querySelector('#messages').textContent.includes('Saved.')`)
		if got := s.settings("alice")["display_posts"]; got != true {
			t.Errorf("display_posts after the form = %v", got)
		}

		// An administrator adds a user.
		b.open("/admin/users")
		b.accessible("the users")
		b.tabTo("#name")
		b.press("carol")
		b.tabTo("#new")
		b.press("carol-password")
		b.tabTo("#again")
		b.press("carol-password", kb.Enter)
		b.until("the user added", `document.querySelector('#messages').textContent.includes('User added.')`)
		if s.user("carol").Name != "carol" {
			t.Error("the user was not added")
		}
		b.tabTo(`a[href$="/admin/users/carol"]`)
		b.press(kb.Enter)
		b.until("the page of the user", `location.pathname === '/admin/users/carol'`)
		b.accessible("the page of a user")
	})
}

// R7, R9: a feed is added, set up, refreshed and given up with the
// keyboard alone.
func TestE2ESubscriptions(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		remote := newFeedSite(t)
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one"))
		remote.serve("/articles/one", "text/html", `<html><body><article>Text of one</article></body></html>`)
		b := browse(t, s)
		b.login("alice")

		// + leads to the page that adds a feed, with the focus in its field.
		b.press("+")
		b.until("the page that adds a feed, the focus in the address", `location.pathname === '/subscriptions/add' && document.activeElement.id === 'url'`)
		b.accessible("the page that adds a feed")
		b.press(remote.URL+"/feed.xml", kb.Enter)
		b.until("the settings of the new feed", `location.pathname === '/subscriptions/feeds/9' && document.querySelector('#messages').textContent.includes('Subscribed.')`)
		b.accessible("the settings of a feed")

		// The selector is tried and stored with Tab and Enter.
		b.tabTo("#path_entries")
		b.press("article")
		b.tabTo(`button[formaction$="/preview"]`)
		b.press(kb.Enter)
		b.until("the preview of the selector", `document.querySelector('.preview .entry-content').textContent.includes('Text of one')`)
		b.accessible("the settings of a feed with a preview")
		b.tabTo(`form.settings button[type="submit"]:not([formaction])`)
		b.press(kb.Enter)
		// The entry there is takes its text from its page at once.
		b.until("the settings saved", `document.querySelector('#messages').textContent.includes('Saved. 1 unread entry got another text.')`)
		if f := s.feed("alice", 9); f.PathEntries != "article" {
			t.Errorf("selector of the feed after the form = %q", f.PathEntries)
		}

		// g f goes to the subscriptions, r on the page of a feed refreshes it.
		b.press("g", "f")
		b.until("the page of subscriptions", `location.pathname === '/subscriptions'`)
		b.accessible("the page of subscriptions")
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one", "two"))
		b.open("/feeds/9")
		b.press("r")
		b.until("the feed refreshed", `document.querySelector('#messages').textContent.includes('The feed was refreshed.') && document.querySelectorAll('.entries > article').length === 2`)

		// The settings of what is being read are a Tab away; unsubscribing
		// asks first.
		b.tabTo("a.stream-settings")
		b.press(kb.Enter)
		b.until("the settings of the feed", `location.pathname === '/subscriptions/feeds/9'`)
		b.tabTo(`details.danger form[action$="/delete"] button, details.danger:last-of-type summary`)
		if b.focus() != "summary" {
			t.Fatalf("the button that unsubscribes can be reached before its question is open: focus on %s", b.focus())
		}
		b.press(kb.Enter)
		b.tabTo(`form[action$="/delete"] button`)
		b.accessible("the question before unsubscribing")
		b.press(kb.Enter)
		b.until("the feed gone", `location.pathname === '/subscriptions' && document.querySelector('#messages').textContent.includes('Unsubscribed.')`)
		if n := len(s.feeds("alice")); n != 8 {
			t.Errorf("alice has %d feeds after unsubscribing, want 8", n)
		}
	})
}

// R7, R8: the row above the entries: an order applies as it is chosen, an
// open entry is folded by its button, and the focus stays with it.
func TestE2EToolbar(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")
		b.accessible("the reading screen with its row of controls")

		// An entry the reader opened by its row, not by a key, closes like
		// any other when a key moves on.
		b.tabTo(e(ids[0]) + " summary")
		b.press(kb.Enter)
		b.until("the first entry open by its row", `document.querySelector('`+e(ids[0])+` details').open`)
		b.press("j")
		b.until("the second entry open, the first closed", current(ids[1], true)+` && !document.querySelector('`+e(ids[0])+` details').open`)
		b.press("k")
		b.until("the first entry open", current(ids[0], true))
		b.tabTo(e(ids[0]) + " button[data-fold]")
		b.press(kb.Enter)
		b.until("the entry folded, the focus on its title",
			`!document.querySelector('`+e(ids[0])+` details').open && document.activeElement.matches('`+e(ids[0])+` summary')`)

		b.tabTo("#sort")
		b.press("T")
		b.until("the entries by title", `new URLSearchParams(location.search).get('sort') === 'title' && document.getElementById('sort').value === 'title'`)
		b.until("no button where the script sorts", `document.querySelector('form.sorting button').hidden`)

		// The switch of the view opens every entry and keeps the choice.
		b.tabTo(`form.views button[value="expanded"]`)
		b.press(kb.Enter)
		b.until("every entry open", `document.querySelectorAll('.entries details:not([open])').length === 0 && document.querySelector('form.views button[value="expanded"]').getAttribute('aria-pressed') === 'true'`)
		b.accessible("the reading screen with every entry open")
		// Where every entry is open, moving on closes none.
		b.press("j", "j")
		b.until("still every entry open", `document.querySelector('.entry.current') && document.querySelectorAll('.entries details:not([open])').length === 0`)
		if s.settings("alice")["display_posts"] != true {
			t.Errorf("display_posts after the switch = %v", s.settings("alice")["display_posts"])
		}
		b.tabTo(`form.views button[value="list"]`)
		b.press(kb.Enter)
		b.until("rows again", `document.querySelectorAll('.entries details[open]').length === 0`)

		// A category is folded from the keyboard and stays folded on the
		// next page; K and J pass over its feeds.
		b.tabTo("#tree details.branch summary")
		b.press(kb.Enter)
		b.until("the category folded", `!document.querySelector('#tree details.branch').open`)
		b.open("/?state=all")
		b.until("the category still folded", `!document.querySelector('#tree details.branch').open && document.querySelectorAll('#tree details.branch')[1].open`)
		b.accessible("the tree with a category folded")
		b.open("/categories/2")
		b.press("J")
		b.until("the next category, past the feeds of the folded one", `location.pathname === '/categories/3'`)

		// A feed of the folded category shows where the reader is, and
		// the tree, brought up to date after an entry is read, comes with
		// its categories open: neither undoes what the reader folded.
		b.open("/feeds/1")
		b.until("the category of the feed being read open", `document.querySelector('#tree details.branch').open`)
		unread := b.text(`document.getElementById('stream-unread').textContent`)
		b.press("j")
		b.until("the tree brought up to date", `document.getElementById('stream-unread').textContent !== '`+unread+`'`)
		b.open("/?state=all")
		b.until("the category folded as the reader left it", `!document.querySelector('#tree details.branch').open && JSON.parse(localStorage.getItem('freshgo.folded')).length === 1`)
	})
}
