// Package fever serves the Fever API, the second API older feed readers on
// iOS and macOS speak next to the Google Reader one. It answers the subset
// clients use: groups, feeds, items, unread and saved identifiers, and
// marking. See docs/specs/fever-api.md.
package fever

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/authguard"
	"github.com/juev/freshgo/internal/greader"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
)

// Path is where the API answers, the address FreshRSS serves it at.
const Path = "/api/fever.php"

const (
	apiVersion = 3
	// pageSize is how many items an answer carries at most.
	pageSize = 50
	// maxBody is how much of a request body is read.
	maxBody = 1 << 20
	// second is a Unix second in the microseconds entry identifiers count.
	second = 1_000_000
)

// Priorities of feeds, as the Google Reader API of the installation reads
// them: the reading list starts at readingPriority, and a feed at hiddenPriority
// or below is in no list.
const (
	readingPriority = 0
	hiddenPriority  = -10
)

// Options are what a Handler works with.
type Options struct {
	DB    *store.Store
	Hooks *hooks.Registry
	Log   *slog.Logger
	// Guard counts the failed logins of an address; the server shares it
	// with the other ways in. Nil, and the API has one of its own.
	Guard *authguard.Guard
	// TrustedProxies are the reverse proxies whose word is taken for the
	// address a request comes from.
	TrustedProxies []netip.Prefix
}

// Handler answers the requests of the Fever API.
type Handler struct {
	db      *store.Store
	hooks   *hooks.Registry
	log     *slog.Logger
	guard   *authguard.Guard
	proxies []netip.Prefix
	now     func() time.Time
}

// New returns a Handler.
func New(o Options) *Handler {
	guard := o.Guard
	if guard == nil {
		guard = &authguard.Guard{}
	}
	return &Handler{db: o.DB, hooks: o.Hooks, log: o.Log, guard: guard, proxies: o.TrustedProxies, now: time.Now}
}

// guardKey counts the failures of an address. Fever sends no user name, so
// the count is per address; the key cannot clash with that of a login, as a
// user name has no NUL.
func (h *Handler) guardKey(r *http.Request) string {
	return authguard.Key("\x00fever", authguard.ClientAddr(r, h.proxies))
}

// ServeHTTP answers a request to Path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	system, err := h.db.System(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	if !system.APIEnabled || !system.FeverEnabled {
		text(w, http.StatusServiceUnavailable, "Service Unavailable!")
		return
	}
	if _, ok := r.URL.Query()["api"]; !ok {
		text(w, http.StatusBadRequest, "Bad Request!")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		text(w, http.StatusBadRequest, "Bad Request!")
		return
	}

	user, wait, err := h.authenticate(ctx, r, system)
	switch {
	case err != nil:
		h.fail(w, err)
		return
	case wait > 0:
		w.Header().Set("Retry-After", authguard.RetryAfter(wait))
		text(w, http.StatusTooManyRequests, "Too Many Requests!")
		return
	case user == nil:
		writeJSON(w, map[string]any{"api_version": apiVersion, "auth": 0})
		return
	}

	answer, err := h.answer(ctx, user, r)
	switch {
	case errors.Is(err, errBadRequest):
		text(w, http.StatusBadRequest, "Bad Request!")
	case err != nil:
		if ctx.Err() == nil {
			h.fail(w, err)
		}
	default:
		writeJSON(w, answer)
	}
}

var errBadRequest = errors.New("fever: bad request")

// authenticate finds the user whose key the request carries. A request
// without a key, or with one nobody has, has no user; wait is how long an
// address that has failed too often has to wait.
//
// The key is md5 of a name and a password, which anybody can compute for a
// guess, so the failures are counted like those of a login, before the key
// is looked at.
func (h *Handler) authenticate(ctx context.Context, r *http.Request, system store.System) (user *store.User, wait time.Duration, err error) {
	key := strings.ToLower(strings.TrimSpace(r.PostForm.Get("api_key")))
	if key == "" {
		return nil, 0, nil
	}
	guard := h.guardKey(r)
	if wait = h.guard.Attempt(guard, h.now()); wait > 0 {
		return nil, wait, nil
	}
	user, err = h.db.UserByFeverKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if subtle.ConstantTimeCompare([]byte(user.FeverKey), []byte(key)) != 1 || !greader.Admits(system, user) {
		return nil, 0, nil
	}
	h.guard.Succeeded(guard)
	return user, 0, nil
}

// answer builds the document for what a request asks. Several things may be
// asked at once, and a change is made before anything is read.
func (h *Handler) answer(ctx context.Context, u *store.User, r *http.Request) (map[string]any, error) {
	form := r.Form
	if form.Has("mark") {
		if err := h.mark(ctx, u, form); err != nil {
			return nil, err
		}
	}
	feeds, err := h.db.Feeds(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var refreshed int64
	for _, f := range feeds {
		refreshed = max(refreshed, f.LastUpdate)
	}
	answer := map[string]any{"api_version": apiVersion, "auth": 1, "last_refreshed_on_time": refreshed}

	// Hidden feeds are in no list.
	feeds = slices.DeleteFunc(feeds, func(f *store.Feed) bool { return f.Priority <= hiddenPriority })
	if form.Has("groups") {
		categories, err := h.db.Categories(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		groups := make([]map[string]any, 0, len(categories))
		for _, c := range categories {
			groups = append(groups, map[string]any{"id": c.ID, "title": c.Name})
		}
		answer["groups"] = groups
	}
	if form.Has("feeds") {
		list := make([]map[string]any, 0, len(feeds))
		for _, f := range feeds {
			title := f.Name
			if title == "" {
				title = f.URL
			}
			list = append(list, map[string]any{
				"id": f.ID, "favicon_id": 0, "title": title, "url": f.URL, "site_url": f.Website,
				"is_spark": 0, "last_updated_on_time": f.LastUpdate,
			})
		}
		answer["feeds"] = list
	}
	if form.Has("groups") || form.Has("feeds") {
		answer["feeds_groups"] = feedsGroups(feeds)
	}
	if form.Has("favicons") {
		// The icons of the installation are served by address, not as the
		// inline images the protocol carries.
		answer["favicons"] = []any{}
	}
	if form.Has("links") {
		answer["links"] = []any{}
	}
	if form.Has("items") {
		total, items, err := h.items(ctx, u, form)
		if err != nil {
			return nil, err
		}
		answer["total_items"], answer["items"] = total, items
	}
	if form.Has("unread_item_ids") {
		ids, err := h.db.ListEntryIDs(ctx, u.ID, store.EntryQuery{Set: readingList(), Read: new(false), Ascending: true})
		if err != nil {
			return nil, err
		}
		answer["unread_item_ids"] = joinIDs(ids)
	}
	if form.Has("saved_item_ids") {
		ids, err := h.db.ListEntryIDs(ctx, u.ID, store.EntryQuery{Set: saved(), Ascending: true})
		if err != nil {
			return nil, err
		}
		answer["saved_item_ids"] = joinIDs(ids)
	}
	return answer, nil
}

// readingList is what the Google Reader API calls the reading list: the
// entries of feeds that are shown in the lists of their category.
func readingList() store.EntrySet {
	return store.EntrySet{MinPriority: new(readingPriority)}
}

// saved is the set of starred entries of feeds that are not hidden.
func saved() store.EntrySet {
	return store.EntrySet{MinPriority: new(hiddenPriority + 1), OnlyFavorite: true}
}

// feedsGroups says which feeds each category has, leaving out the categories
// without a feed.
func feedsGroups(feeds []*store.Feed) []map[string]any {
	byGroup := map[int64][]int64{}
	var order []int64
	for _, f := range feeds {
		if _, ok := byGroup[f.CategoryID]; !ok {
			order = append(order, f.CategoryID)
		}
		byGroup[f.CategoryID] = append(byGroup[f.CategoryID], f.ID)
	}
	groups := make([]map[string]any, 0, len(order))
	for _, id := range order {
		groups = append(groups, map[string]any{"group_id": id, "feed_ids": joinIDs(byGroup[id])})
	}
	return groups
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// items answers the request for items, with the number of all items there
// are. The page is chosen by with_ids, else since_id (the oldest after it),
// else max_id (the newest before it); without any of them the oldest.
func (h *Handler) items(ctx context.Context, u *store.User, form map[string][]string) (int, []map[string]any, error) {
	total, err := h.db.CountSet(ctx, u.ID, readingList())
	if err != nil {
		return 0, nil, err
	}
	param := func(name string) (int64, bool) {
		values, ok := form[name]
		if !ok {
			return 0, false
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(values[len(values)-1]), 10, 64)
		return n, true
	}
	var entries []*store.Entry
	switch withIDs, hasIDs := form["with_ids"]; {
	case hasIDs:
		var ids []int64
		for _, part := range strings.Split(withIDs[len(withIDs)-1], ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > pageSize {
			ids = ids[:pageSize]
		}
		entries, err = h.db.EntriesByIDs(ctx, u.ID, ids, true)
	default:
		query := store.EntryQuery{Set: readingList(), Ascending: true, Limit: pageSize}
		if since, ok := param("since_id"); ok {
			query.From = max(since, 0) + 1
		} else if maxID, ok := param("max_id"); ok {
			if maxID == 1 {
				// Nothing has a smaller identifier, and zero would mean
				// "from the newest" to the query.
				return total, []map[string]any{}, nil
			}
			query.Ascending = false
			if maxID > 1 {
				query.From = maxID - 1
			}
		}
		entries, err = h.db.ListEntries(ctx, u.ID, query)
	}
	if err != nil {
		return 0, nil, err
	}
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		e, ok := h.hooks.EntryBeforeDisplay.Call(ctx, e)
		if !ok {
			continue
		}
		created := e.Published
		if created == 0 {
			created = e.ID / second
		}
		items = append(items, map[string]any{
			"id": e.ID, "feed_id": e.FeedID, "title": e.Title, "author": strings.Join(e.Authors, ", "),
			"html": e.Content, "url": e.Link, "is_saved": flag(e.IsFavorite), "is_read": flag(e.IsRead),
			"created_on_time": created,
		})
	}
	return total, items, nil
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mark changes the state of entries: mark=item with as=read, unread, saved
// or unsaved, and mark=feed or mark=group with as=read, which reads what was
// there before the time in before (everything without it). Group 0 is every
// group.
func (h *Handler) mark(ctx context.Context, u *store.User, form map[string][]string) error {
	get := func(name string) string {
		values := form[name]
		if len(values) == 0 {
			return ""
		}
		return strings.TrimSpace(values[len(values)-1])
	}
	id, err := strconv.ParseInt(get("id"), 10, 64)
	if err != nil {
		return errBadRequest
	}
	now := h.now().Unix()
	as := get("as")
	switch kind := get("mark"); {
	case kind == "item":
		ids := []int64{id}
		switch as {
		case "read", "unread":
			read := as == "read"
			changed, err := h.db.SetEntriesRead(ctx, u.ID, ids, read, now)
			if err == nil && len(changed) > 0 {
				h.hooks.EntriesRead.Call(ctx, hooks.EntriesRead{UserID: u.ID, IDs: ids, IsRead: read})
			}
			return err
		case "saved", "unsaved":
			favorite := as == "saved"
			err := h.db.SetEntriesFavorite(ctx, u.ID, ids, favorite, now)
			if err == nil {
				h.hooks.EntriesFavorite.Call(ctx, hooks.EntriesFavorite{UserID: u.ID, IDs: ids, IsFavorite: favorite})
			}
			return err
		}
	case (kind == "feed" || kind == "group") && as == "read":
		before := int64(math.MaxInt64)
		if t, err := strconv.ParseInt(get("before"), 10, 64); err == nil && t > 0 && t <= math.MaxInt64/second {
			before = t * second
		}
		var set store.EntrySet
		switch {
		case kind == "feed" && id > 0:
			set.FeedID = id
		case kind == "group" && id == 0:
			set = readingList()
		case kind == "group" && id > 0:
			set = readingList()
			set.CategoryID = id
		default:
			return errBadRequest
		}
		_, err := h.db.MarkSetRead(ctx, u.ID, set, before, now)
		return err
	}
	return errBadRequest
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	h.log.Error("Fever API request failed", "error", err)
	text(w, http.StatusInternalServerError, "Internal Server Error!")
}

func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// writeJSON answers with a JSON document.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
