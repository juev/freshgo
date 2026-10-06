// Package hooks is the registry of extension points, modelled on
// Minz_HookType and Minz_ExtensionManager::callHook of FreshRSS at commit
// 219eaf58. See docs/specs/hooks.md.
//
// Handlers are added while the program starts, before the first call. After
// that the registry is only read, so calls need no locking and may come from
// several goroutines at once: a handler has to be safe for concurrent use.
package hooks

import (
	"context"
	"sort"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// Registry holds the handlers of every extension point. The zero value is a
// registry without handlers, with which freshgo behaves as if there were no
// extension points at all. A nil *Registry is not valid.
type Registry struct {
	// CheckURLBeforeAdd may rewrite or refuse the address of a feed that is
	// about to be added.
	CheckURLBeforeAdd Chain[string]
	// FeedBeforeInsert may change or refuse a feed before it is stored.
	FeedBeforeInsert Chain[*store.Feed]
	// FeedsListBeforeActualize may reorder or shorten the feeds of a user a
	// refresh is about to go through.
	FeedsListBeforeActualize Chain[[]*store.Feed]
	// FeedBeforeActualize may change a feed for the refresh that follows, or
	// take it out of the refresh. The change is not stored.
	FeedBeforeActualize Chain[*store.Feed]
	// FetchBefore sees the request for a feed before it is sent and may
	// change it. It stands in for simplepie_before_init.
	FetchBefore Event[Fetch]
	// ParseAfter sees the parsed feed, or the reason there is none, and may
	// change the items. It stands in for simplepie_after_init.
	ParseAfter Event[Parsed]
	// EntryBeforeInsert sees every entry a refresh found new or changed.
	EntryBeforeInsert Chain[*store.Entry]
	// EntryBeforeAdd sees a new entry right before it is stored.
	EntryBeforeAdd Chain[*store.Entry]
	// EntryBeforeUpdate sees a changed entry right before it is stored.
	EntryBeforeUpdate Chain[*store.Entry]
	// EntryBeforeDisplay sees an entry on its way out through the API.
	EntryBeforeDisplay Chain[*store.Entry]
	// EntryAutoRead and EntryAutoUnread report an entry whose read state a
	// rule, not the user, has set.
	EntryAutoRead   Event[EntryAuto]
	EntryAutoUnread Event[EntryAuto]
	// EntriesRead and EntriesFavorite report entries whose state the user
	// has changed.
	EntriesRead     Event[EntriesRead]
	EntriesFavorite Event[EntriesFavorite]
	// Init runs once when a command that works with the data starts.
	Init Signal[struct{}]
	// UserMaintenance runs for a user before each refresh of their feeds.
	UserMaintenance Signal[*store.User]
}

// Fetch is the argument of FetchBefore.
type Fetch struct {
	Feed    *store.Feed
	Request *fetch.Request
}

// Parsed is the argument of ParseAfter. Document is nil and Error says why
// when the feed could not be fetched or read.
type Parsed struct {
	Feed     *store.Feed
	Document *feed.Feed
	Error    string
}

// Reasons given with EntryAutoRead and EntryAutoUnread.
const (
	WhyUpdatedArticle     = "updated_article"
	WhyUponReception      = "upon_reception"
	WhySameTitleInFeed    = "same_title_in_feed"
	WhySameGUIDInCategory = "same_guid_in_category"
)

// EntryAuto is the argument of EntryAutoRead and EntryAutoUnread.
type EntryAuto struct {
	Entry *store.Entry
	// Why names the rule, one of the Why constants.
	Why string
}

// EntriesRead is the argument of the hook of the same name.
type EntriesRead struct {
	UserID int64
	IDs    []int64
	IsRead bool
}

// EntriesFavorite is the argument of the hook of the same name.
type EntriesFavorite struct {
	UserID     int64
	IDs        []int64
	IsFavorite bool
}

// handlers keeps functions in call order: by priority, lower first, and in
// the order they were added within one priority.
type handlers[F any] struct {
	list []prioritized[F]
}

type prioritized[F any] struct {
	priority int
	fn       F
}

func (h *handlers[F]) add(priority int, fn F) {
	i := sort.Search(len(h.list), func(i int) bool { return h.list[i].priority > priority })
	h.list = append(h.list, prioritized[F]{})
	copy(h.list[i+1:], h.list[i:])
	h.list[i] = prioritized[F]{priority: priority, fn: fn}
}

// Chain is an extension point whose handlers transform a value in turn: each
// gets what the previous one returned. A handler that returns false drops the
// value: the rest are not called, and the caller gives up what it was doing
// with it. This is the OneToOne signature of FreshRSS, where the handler
// returns null.
type Chain[T any] struct {
	handlers[func(context.Context, T) (T, bool)]
}

// Add registers a handler; the lower the priority value, the earlier it runs.
func (c *Chain[T]) Add(priority int, fn func(context.Context, T) (T, bool)) {
	c.add(priority, fn)
}

// Call runs the chain. It returns the final value and whether to keep it.
func (c *Chain[T]) Call(ctx context.Context, v T) (T, bool) {
	for _, h := range c.list {
		var ok bool
		if v, ok = h.fn(ctx, v); !ok {
			var zero T
			return zero, false
		}
	}
	return v, true
}

// Event is an extension point whose handlers are told about something. They
// may change what the argument points to. A handler that returns true has
// dealt with the event and the rest are not called. This is the PassArguments
// signature of FreshRSS, where the first result that is not null ends the call.
type Event[A any] struct {
	handlers[func(context.Context, A) bool]
}

// Add registers a handler; the lower the priority value, the earlier it runs.
func (e *Event[A]) Add(priority int, fn func(context.Context, A) bool) {
	e.add(priority, fn)
}

// Call runs the handlers and reports whether one of them dealt with the event.
func (e *Event[A]) Call(ctx context.Context, arg A) bool {
	for _, h := range e.list {
		if h.fn(ctx, arg) {
			return true
		}
	}
	return false
}

// Signal is an extension point all of whose handlers always run. This is the
// NoneToNone signature of FreshRSS; the argument carries what FreshRSS keeps
// in its global context.
type Signal[A any] struct {
	handlers[func(context.Context, A)]
}

// Add registers a handler; the lower the priority value, the earlier it runs.
func (s *Signal[A]) Add(priority int, fn func(context.Context, A)) {
	s.add(priority, fn)
}

// Call runs every handler.
func (s *Signal[A]) Call(ctx context.Context, arg A) {
	for _, h := range s.list {
		h.fn(ctx, arg)
	}
}
