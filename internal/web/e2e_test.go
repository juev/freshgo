//go:build e2e

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/juev/freshgo/internal/store"
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

// open types an address into the browser.
func (b *browser) open(path string) {
	b.t.Helper()
	b.run(chromedp.Navigate(b.base+path), chromedp.WaitReady("body"))
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

// until waits for an expression of the page to become true.
func (b *browser) until(what, expression string) {
	b.t.Helper()
	b.eventually(what+" (focus on "+"%s)", func() bool {
		ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`Boolean(`+expression+`)`))
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
	for range 120 {
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

// accessible checks the page with axe-core and fails on what it finds.
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
	violations := read[[]violation](b, `axe.run(document).then(result => result.violations)`, chromedp.EvalAwaitPromise)
	for _, v := range violations {
		for _, node := range v.Nodes {
			b.t.Errorf("%s: axe finds %s (%s) at %.300s", what, v.ID, v.Help, node.HTML)
		}
	}
}

// freshgoKeys gives alice the keys of freshgo instead of those she had in
// FreshRSS.
func freshgoKeys(s *site) {
	s.setting("alice", "keys", map[string]string{})
}

func (s *site) entry(name string, id int64) *store.Entry {
	s.t.Helper()
	e, err := s.db.EntryByID(context.Background(), s.user(name).ID, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return e
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
		freshgoKeys(s)
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")
		b.accessible("the reading screen")
		count := `document.querySelector('#tree a[aria-current="page"] + .count').textContent`
		if got := b.text(count); got != "18" {
			t.Fatalf("the tree counts %s unread entries, want 18", got)
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
		freshgoKeys(s)
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
		b.until("all entries", `location.pathname === '/all'`)
		b.press("g", "s")
		b.until("starred entries", `location.pathname === '/starred'`)
		b.press("g", "u")
		b.until("unread entries", `location.pathname === '/' && document.querySelector('.entries')`)
		b.press("J")
		b.until("the next stream of the tree", `location.pathname === '/all'`)
		b.press("K")
		b.until("the stream before", `location.pathname === '/' && document.querySelector('.entries')`)
		b.press("U")
		b.until("the next stream with unread entries", `location.pathname === '/all'`)
	})
}

// R5, R7, R8: labels are set and everything is marked read from dialogs
// the keyboard opens.
func TestE2ELabelsAndMarkAll(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		freshgoKeys(s)
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
		freshgoKeys(s)
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
			`document.activeElement.textContent === 'Remove star' && document.activeElement.closest('article').id === '`+first[1:]+`'`)
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
// FreshRSS has the keys they had.
func TestE2EKeysPage(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ids := s.stored("alice", store.Listing{Set: mainStream(), Read: ptr(false)})
		b := browse(t, s)
		b.login("alice")

		// The keys of FreshRSS: r marks read, f stars, F1 is the help.
		b.press("n", "r")
		b.eventually("the entry read by the key of FreshRSS", func() bool { return s.entry("alice", ids[0]).IsRead })
		b.press("f")
		b.eventually("the entry starred by the key of FreshRSS", func() bool { return s.entry("alice", ids[0]).IsFavorite })
		b.press(kb.F1)
		b.until("the help by F1", `document.querySelector('dialog[open] table')`)
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
		b.until("all entries by g and A", `location.pathname === '/all'`)
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
		freshgoKeys(s)
		s.setting("alice", "posts_per_page", 5)
		s.setting("alice", "mark_when", map[string]any{"article": false})
		ids := s.stored("alice", store.Listing{Set: mainStream()})
		b := browse(t, s)
		b.login("alice")
		b.open("/all")
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
		freshgoKeys(s)
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
		b.press("?")
		b.until("the help", `document.querySelector('dialog[open] table')`)
		b.accessible("the help, dark")
		b.press(kb.Escape)

		// On a narrow screen the tree is folded; t unfolds it and goes there.
		b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 400, Height: 800, DeviceScaleFactor: 1, Mobile: true}))
		b.open("/")
		b.until("the tree folded", `!document.querySelector('#tree details').open`)
		b.accessible("the reading screen, narrow")
		b.press("t")
		b.until("the tree unfolded, in focus", `document.querySelector('#tree details').open && document.activeElement.matches('#tree summary')`)
		if got := b.text(`document.documentElement.scrollWidth <= window.innerWidth`); got != "true" {
			t.Error("the narrow page scrolls sideways")
		}
		// The pages of settings, dark and narrow at once.
		for _, page := range []string{"/subscriptions", "/subscriptions/add", "/subscriptions/feeds/3", "/subscriptions/categories/2", "/subscriptions/labels/1", "/subscriptions/problems"} {
			b.open(page)
			b.accessible(page + ", dark and narrow")
			if got := b.text(`document.documentElement.scrollWidth <= window.innerWidth`); got != "true" {
				t.Errorf("%s scrolls sideways on a narrow screen", page)
			}
		}
	})
}

// R7, R9: a feed is added, set up, refreshed and given up with the
// keyboard alone.
func TestE2ESubscriptions(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		freshgoKeys(s)
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
		b.until("the settings saved", `document.querySelector('#messages').textContent.includes('Saved.')`)
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
		b.until("the feed refreshed", `document.querySelector('#messages').textContent.includes('The feed was refreshed.') && document.querySelectorAll('.entries article').length === 2`)

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
