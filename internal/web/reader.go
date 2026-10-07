package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// Priorities of feeds as FreshRSS names them: a feed is shown in the main
// stream, in its category only, or nowhere but on its own page.
const (
	priorityMain     = store.PriorityMain
	priorityCategory = 0
)

// Kinds of streams of entries the reading screen shows.
const (
	streamMain     = "main"
	streamAll      = "all"
	streamStarred  = "starred"
	streamFeed     = "feed"
	streamCategory = "category"
	streamLabel    = "label"
)

// States a listing is narrowed to, as the state parameter spells them.
const (
	stateUnread = "unread"
	stateAll    = "all"
	stateStar   = "starred"
	stateEither = "unread-or-starred"
)

// Orders of a listing, as the sort parameter spells them, and the names
// FreshRSS has for them in the settings it leaves behind.
var (
	orders = map[string]store.Order{
		"added": store.OrderAdded, "published": store.OrderPublished, "title": store.OrderTitle,
		"feed": store.OrderFeed, "random": store.OrderRandom,
	}
	orderNames     = []string{"added", "published", "title", "feed", "random"}
	freshRSSOrders = map[string]string{"id": "added", "date": "published", "title": "title", "f.name": "feed", "rand": "random"}
)

// maxActionForm bounds the body of a request that changes entries, in bytes.
const maxActionForm = 64 << 10

// maxLabelName bounds the name of a label, in characters.
const maxLabelName = 191

// reading are the settings of a user the reading screen goes by, under the
// names FreshRSS gives them.
type reading struct {
	PostsPerPage  int             `json:"posts_per_page"`
	DefaultView   string          `json:"default_view"`
	Sort          string          `json:"sort"`
	SortOrder     string          `json:"sort_order"`
	HideReadFeeds *bool           `json:"hide_read_feeds"`
	ShowFavUnread bool            `json:"show_fav_unread"`
	DisplayPosts  bool            `json:"display_posts"`
	Timezone      string          `json:"timezone"`
	Queries       json.RawMessage `json:"queries"`
}

func readReading(u *store.User) reading {
	var p reading
	// A setting of another type is a setting nobody made.
	_ = json.Unmarshal(u.Settings, &p)
	if p.PostsPerPage < 1 || p.PostsPerPage > 500 {
		p.PostsPerPage = 20
	}
	return p
}

func (p reading) location() *time.Location {
	if p.Timezone != "" {
		if loc, err := time.LoadLocation(p.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// sorting are the attributes of a feed or a category that say how its
// entries are sorted unless the reader asks otherwise.
type sorting struct {
	Sort  string `json:"defaultSort"`
	Order string `json:"defaultOrder"`
}

// library is what a user has subscribed to, with the counts of unread entries.
type library struct {
	categories []*store.Category
	feeds      []*store.Feed
	labels     []*store.Tag
	feed       map[int64]*store.Feed
	unread     map[int64]int
	labelled   map[int64]int
	starred    int
}

func (h *Handler) library(ctx context.Context, userID int64) (*library, error) {
	lib := &library{feed: map[int64]*store.Feed{}, unread: map[int64]int{}, labelled: map[int64]int{}}
	var err error
	if lib.categories, err = h.db.Categories(ctx, userID); err != nil {
		return nil, err
	}
	if lib.feeds, err = h.db.Feeds(ctx, userID); err != nil {
		return nil, err
	}
	if lib.labels, err = h.db.Tags(ctx, userID); err != nil {
		return nil, err
	}
	feedCounts, err := h.db.FeedCounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	labelCounts, err := h.db.LabelCounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	if lib.starred, err = h.db.UnreadFavorites(ctx, userID); err != nil {
		return nil, err
	}
	for _, f := range lib.feeds {
		lib.feed[f.ID] = f
		lib.unread[f.ID] = feedCounts[f.ID].Unread
	}
	for id, c := range labelCounts {
		lib.labelled[id] = c.Unread
	}
	return lib, nil
}

// unreadFrom adds up the unread entries of the feeds of a category, or of
// all when it is zero, that are shown at least at the given priority.
func (lib *library) unreadFrom(categoryID int64, minPriority int) int {
	n := 0
	for _, f := range lib.feeds {
		if (categoryID == 0 || f.CategoryID == categoryID) && f.Priority >= minPriority {
			n += lib.unread[f.ID]
		}
	}
	return n
}

// stream is a set of entries the reading screen lists.
type stream struct {
	kind string
	id   int64
	// name is empty for the streams the interface names itself.
	name   string
	unread int
	set    store.EntrySet
	sorting
}

// path is the address of the stream.
func (s stream) path() string {
	switch s.kind {
	case streamAll:
		return "/all"
	case streamStarred:
		return "/starred"
	case streamFeed:
		return "/feeds/" + strconv.FormatInt(s.id, 10)
	case streamCategory:
		return "/categories/" + strconv.FormatInt(s.id, 10)
	case streamLabel:
		return "/labels/" + strconv.FormatInt(s.id, 10)
	}
	return "/"
}

// stream finds the stream of a kind and an identifier; ok is false when the
// user has no such feed, category or label.
func (lib *library) stream(kind string, id int64) (s stream, ok bool) {
	s = stream{kind: kind, id: id}
	switch kind {
	case streamMain, streamAll:
		minPriority := priorityMain
		s.set.MinPriority = &minPriority
		s.unread = lib.unreadFrom(0, priorityMain)
	case streamStarred:
		s.set.OnlyFavorite = true
		s.unread = lib.starred
	case streamFeed:
		f := lib.feed[id]
		if f == nil {
			return s, false
		}
		s.name, s.unread = f.Name, lib.unread[id]
		s.set.FeedID = id
		_ = json.Unmarshal(f.Attributes, &s.sorting)
	case streamCategory:
		for _, c := range lib.categories {
			if c.ID != id {
				continue
			}
			minPriority := priorityCategory
			s.name, s.unread = c.Name, lib.unreadFrom(id, priorityCategory)
			s.set.CategoryID, s.set.MinPriority = id, &minPriority
			_ = json.Unmarshal(c.Attributes, &s.sorting)
			return s, true
		}
		return s, false
	case streamLabel:
		for _, l := range lib.labels {
			if l.ID == id {
				s.name, s.unread = l.Name, lib.labelled[id]
				s.set.LabelID = id
				return s, true
			}
		}
		return s, false
	default:
		return s, false
	}
	return s, true
}

// streamAt finds the stream an address of the interface names.
func (lib *library) streamAt(path string) (stream, bool) {
	for prefix, kind := range map[string]string{"/feeds/": streamFeed, "/categories/": streamCategory, "/labels/": streamLabel} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			id, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return stream{}, false
			}
			return lib.stream(kind, id)
		}
	}
	switch path {
	case "/":
		return lib.stream(streamMain, 0)
	case "/all":
		return lib.stream(streamAll, 0)
	case "/starred":
		return lib.stream(streamStarred, 0)
	}
	return stream{}, false
}

// showing is how a stream is listed: what the reader asked for where they
// did, the settings of the stream and of the user otherwise.
type showing struct {
	state string
	sort  string
	asc   bool
	// seed picks the shuffle when the sort is random.
	seed  int64
	query string
	// asked are the parameters the reader gave, which links carry on.
	asked url.Values
}

func show(params url.Values, prefs reading, s stream) showing {
	v := showing{asked: url.Values{}, query: strings.TrimSpace(params.Get("q"))}
	if v.query != "" {
		v.asked.Set("q", v.query)
	}

	switch state := params.Get("state"); state {
	case stateUnread, stateAll, stateStar, stateEither:
		v.state = state
		v.asked.Set("state", state)
	default:
		switch {
		case s.kind == streamAll, prefs.ShowFavUnread && (s.kind == streamStarred || s.kind == streamLabel):
			v.state = stateAll
		case prefs.DefaultView == "all":
			v.state = stateAll
		case prefs.DefaultView == "unread_or_favorite":
			v.state = stateEither
		case prefs.DefaultView == "unread":
			v.state = stateUnread
		case s.unread > 0: // adaptive, the default: unread entries while there are some
			v.state = stateUnread
		default:
			v.state = stateAll
		}
	}

	if _, known := orders[params.Get("sort")]; known {
		v.sort = params.Get("sort")
		v.asked.Set("sort", v.sort)
	} else {
		// The order of the stream when it has one, even one freshgo lacks.
		name := s.Sort
		if name == "" {
			name = prefs.Sort
		}
		if v.sort = freshRSSOrders[name]; v.sort == "" {
			v.sort = "added"
		}
	}
	if v.sort == "random" {
		// The shuffle is named in every address the page gives out, or coming
		// back to the page after an action would shuffle it again.
		var err error
		if v.seed, err = strconv.ParseInt(params.Get("seed"), 10, 64); err != nil {
			v.seed = rand.Int64()
		}
		v.asked.Set("seed", strconv.FormatInt(v.seed, 10))
	}
	switch order := params.Get("order"); order {
	case "asc", "desc":
		v.asc = order == "asc"
		v.asked.Set("order", order)
	default:
		v.asc = strings.EqualFold(s.Order, "ASC") || s.Order == "" && strings.EqualFold(prefs.SortOrder, "ASC")
	}
	return v
}

// listing is the page of the stream the store is asked for.
func (v showing) listing(s stream, q *search.Query) store.Listing {
	l := store.Listing{Set: s.set, Search: q, Order: orders[v.sort], Ascending: v.asc, Seed: v.seed}
	switch v.state {
	case stateUnread:
		unread := false
		l.Read = &unread
	case stateStar:
		starred := true
		l.Favorite = &starred
	case stateEither:
		l.UnreadOrFavorite = true
	}
	return l
}

// here is the address of the page being shown, for an action to come back
// to: the address asked for, with the shuffle the page picked.
func (v showing) here(asked *url.URL) string {
	if v.sort != "random" {
		return asked.RequestURI()
	}
	here := *asked
	params := here.Query()
	params.Set("seed", strconv.FormatInt(v.seed, 10))
	here.RawQuery = params.Encode()
	return here.RequestURI()
}

// link is the address of a stream listed as asked, with some parameters
// changed; an empty value takes a parameter out.
func (v showing) link(h *Handler, path string, changes ...string) string {
	params := url.Values{}
	for key, values := range v.asked {
		params[key] = values
	}
	for i := 0; i+1 < len(changes); i += 2 {
		if changes[i+1] == "" {
			params.Del(changes[i])
		} else {
			params.Set(changes[i], changes[i+1])
		}
	}
	if len(params) == 0 {
		return h.url(path)
	}
	return h.url(path) + "?" + params.Encode()
}

// parseSearch reads the search of the reader the way the database is
// searched: with labels, and against the saved searches of the user.
func (h *Handler) parseSearch(text string, prefs reading) (*search.Query, error) {
	if text == "" {
		return nil, nil
	}
	return search.Parse(text, search.Options{
		Queries: search.ParseSavedQueries(prefs.Queries), Now: h.now().In(prefs.location()), Labels: true,
	})
}

// searchProblem is the key of the text that says what is wrong with a search.
func searchProblem(err error) string {
	switch {
	case errors.Is(err, search.ErrTooLong):
		return "search.too-long"
	case errors.Is(err, search.ErrTooDeep):
		return "search.too-deep"
	default:
		return "search.regexp"
	}
}

// branch is an entry of the tree of subscriptions.
type branch struct {
	Name    string
	URL     string
	Unread  int
	Current bool
	// Failing marks a feed whose last refresh failed, Muted one that is not
	// refreshed.
	Failing bool
	Muted   bool
	Feeds   []branch
}

// tree is what the reading screen offers to read.
type tree struct {
	Streams    []branch
	Categories []branch
	Labels     []branch
}

func (h *Handler) tree(v *view, lib *library, current stream, state showing, hideRead bool) tree {
	at := func(s stream, name string) branch {
		if name == "" {
			name = s.name
		}
		return branch{
			Name: name, URL: state.link(h, s.path(), "q", ""), Unread: s.unread,
			Current: s.kind == current.kind && s.id == current.id,
		}
	}
	var t tree
	for _, s := range []struct{ kind, name string }{
		{streamMain, "stream.main"}, {streamAll, "stream.all"}, {streamStarred, "stream.starred"},
	} {
		one, _ := lib.stream(s.kind, 0)
		t.Streams = append(t.Streams, at(one, v.T(s.name)))
	}
	for _, c := range lib.categories {
		one, _ := lib.stream(streamCategory, c.ID)
		category := at(one, "")
		for _, f := range lib.feeds {
			if f.CategoryID != c.ID {
				continue
			}
			one, _ := lib.stream(streamFeed, f.ID)
			feed := at(one, "")
			if hideRead && feed.Unread == 0 && !feed.Current {
				continue
			}
			feed.Failing, feed.Muted = f.Error != 0, f.TTL < 0
			category.Feeds = append(category.Feeds, feed)
		}
		t.Categories = append(t.Categories, category)
	}
	for _, l := range lib.labels {
		one, _ := lib.stream(streamLabel, l.ID)
		t.Labels = append(t.Labels, at(one, ""))
	}
	return t
}

// attachment is a file an entry came with.
type attachment struct {
	URL   string
	Title string
	// Kind is "image", "audio", "video" or "file".
	Kind string
	Type string
}

var imageAddress = regexp.MustCompile(`(?i)[.](avif|gif|jpe?g|png|svg|webp)([?#]|$)`)

// attachments reads the enclosures of an entry that the text does not show
// already.
func attachments(e *store.Entry) []attachment {
	var attrs struct {
		Enclosures []struct {
			URL    string `json:"url"`
			Type   string `json:"type"`
			Medium string `json:"medium"`
			Title  string `json:"title"`
		} `json:"enclosures"`
	}
	_ = json.Unmarshal(e.Attributes, &attrs)
	var out []attachment
	for _, enc := range attrs.Enclosures {
		address, err := url.Parse(enc.URL)
		if err != nil || address.Scheme != "http" && address.Scheme != "https" || strings.Contains(e.Content, enc.URL) {
			continue
		}
		a := attachment{URL: enc.URL, Title: enc.Title, Type: enc.Type, Kind: "file"}
		for _, kind := range []string{"image", "audio", "video"} {
			if enc.Medium == kind || strings.HasPrefix(enc.Type, kind+"/") {
				a.Kind = kind
			}
		}
		if a.Kind == "file" && enc.Type == "" && imageAddress.MatchString(enc.URL) {
			a.Kind = "image"
		}
		if a.Title == "" {
			a.Title = address.Host + address.Path
		}
		out = append(out, a)
	}
	return out
}

// article is an entry as a page shows it.
type article struct {
	ID    int64
	Title string
	// URL is the page of the entry, Link the address the feed gave it.
	URL         string
	Link        string
	Feed        string
	FeedURL     string
	Authors     string
	Date        string
	DateTime    string
	Content     template.HTML
	Attachments []attachment
	Tags        []string
	Labels      []string
	Read        bool
	Starred     bool
}

// articles prepares entries for a page. Handlers of EntryBeforeDisplay see
// each and may leave it out.
func (h *Handler) articles(ctx context.Context, v *view, lib *library, userID int64, loc *time.Location, entries []*store.Entry) ([]article, error) {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := h.db.EntryLabels(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]article, 0, len(entries))
	for _, e := range entries {
		e, ok := h.hooks.EntryBeforeDisplay.Call(ctx, e)
		if !ok {
			continue
		}
		// An entry without a date of its own has the time it was added.
		date := time.UnixMicro(e.ID).In(loc)
		if e.Published != 0 {
			date = time.Unix(e.Published, 0).In(loc)
		}
		a := article{
			ID: e.ID, Title: e.Title, URL: h.url("/entries/" + strconv.FormatInt(e.ID, 10)),
			Authors: strings.Join(e.Authors, ", "), Date: date.Format("2006-01-02 15:04"), DateTime: date.Format(time.RFC3339),
			// What is stored was cleaned when it was fetched, but not all of it by
			// freshgo: an import brings what FreshRSS let through.
			Content:     template.HTML(sanitize.HTML(e.Content, e.Link, nil)), //nolint:gosec // cleaned on this line
			Attachments: attachments(e), Tags: e.Tags, Labels: labels[e.ID], Read: e.IsRead, Starred: e.IsFavorite,
		}
		if address, err := url.Parse(e.Link); err == nil && (address.Scheme == "http" || address.Scheme == "https") {
			a.Link = e.Link
		}
		if a.Title == "" {
			a.Title = v.T("entry.untitled")
		}
		if f := lib.feed[e.FeedID]; f != nil {
			a.Feed, a.FeedURL = f.Name, h.url("/feeds/"+strconv.FormatInt(f.ID, 10))
		}
		out = append(out, a)
	}
	return out, nil
}

// choice is a link among alternatives, one of which is in effect.
type choice struct {
	Name    string
	URL     string
	Current bool
}

// readerPage is what the reading screen shows.
type readerPage struct {
	Tree     tree
	Entries  []article
	Unread   int
	States   []choice
	Sorts    []choice
	Sort     string
	Asc      bool
	Query    string
	Problem  string
	State    string
	Asked    url.Values
	Path     string
	Here     string
	Next     string
	Expanded bool
	// Before is the moment the page was made, as an entry identifier:
	// "mark all as read" leaves what arrived later alone.
	Before int64
	// CanChange is false for a visitor, who only reads.
	CanChange bool
}

// reader answers with a stream of entries.
func (h *Handler) reader(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, who := r.Context(), state(r).who
		lib, err := h.library(ctx, who.user.ID)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s, ok := lib.stream(kind, id)
		if !ok {
			h.fail(w, r, http.StatusNotFound)
			return
		}
		prefs := readReading(who.user)
		showing := show(r.URL.Query(), prefs, s)

		heading := "stream." + s.kind
		v := h.view(r, "reader", heading)
		if s.name != "" {
			v.Heading = s.name
		}
		v.Wide = true
		page := readerPage{
			Unread: s.unread, Sort: showing.sort, Asc: showing.asc, Query: showing.query, State: showing.state,
			Asked: showing.asked, Path: s.path(), Here: showing.here(r.URL), Expanded: prefs.DisplayPosts,
			Before: h.now().UnixMicro(), CanChange: !who.anonymous,
		}
		for _, state := range []string{stateUnread, stateAll, stateStar} {
			page.States = append(page.States, choice{
				Name: v.T("state." + state), URL: showing.link(h, s.path(), "state", state), Current: state == showing.state,
			})
		}
		for _, name := range orderNames {
			page.Sorts = append(page.Sorts, choice{Name: v.T("sort." + name), URL: name, Current: name == showing.sort})
		}
		hideRead := showing.state == stateUnread && (prefs.HideReadFeeds == nil || *prefs.HideReadFeeds)
		page.Tree = h.tree(v, lib, s, showing, hideRead)

		status := http.StatusOK
		query, err := h.parseSearch(showing.query, prefs)
		if err != nil {
			// A search that cannot be read finds nothing and says why.
			status, page.Problem = http.StatusBadRequest, v.T(searchProblem(err))
		} else {
			listing := showing.listing(s, query)
			listing.After, listing.Limit = r.URL.Query().Get("after"), prefs.PostsPerPage
			entries, next, err := h.db.ListPage(ctx, who.user.ID, listing)
			if errors.Is(err, store.ErrCursor) {
				h.fail(w, r, http.StatusNotFound)
				return
			}
			if err == nil {
				page.Entries, err = h.articles(ctx, v, lib, who.user.ID, prefs.location(), entries)
			}
			if err != nil {
				h.broken(w, r, err)
				return
			}
			if next != "" {
				page.Next = showing.link(h, s.path(), "after", next)
			}
		}
		v.Data = page
		h.render(w, r, status, "reader", v)
	}
}

// entryPage is what the page of one entry shows.
type entryPage struct {
	Entry     article
	Labels    []labelChoice
	Here      string
	CanChange bool
}

// labelChoice is a label the entry has or could get.
type labelChoice struct {
	ID   int64
	Name string
	On   bool
}

// ownEntry finds the entry a request names among those of the user.
func (h *Handler) ownEntry(w http.ResponseWriter, r *http.Request) (*store.Entry, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	e, err := h.db.EntryByID(r.Context(), state(r).who.user.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	case err != nil:
		h.broken(w, r, err)
		return nil, false
	}
	return e, true
}

// entry answers with the page of one entry.
func (h *Handler) entry(w http.ResponseWriter, r *http.Request) {
	ctx, who := r.Context(), state(r).who
	e, ok := h.ownEntry(w, r)
	if !ok {
		return
	}
	lib, err := h.library(ctx, who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "reader", "entry.untitled")
	shown, err := h.articles(ctx, v, lib, who.user.ID, readReading(who.user).location(), []*store.Entry{e})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if len(shown) == 0 {
		// A handler of EntryBeforeDisplay keeps the entry from being shown.
		h.fail(w, r, http.StatusNotFound)
		return
	}
	page := entryPage{Entry: shown[0], Here: r.URL.RequestURI(), CanChange: !who.anonymous}
	on, err := h.db.EntryTagIDs(ctx, who.user.ID, e.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	for _, l := range lib.labels {
		choice := labelChoice{ID: l.ID, Name: l.Name}
		for _, id := range on {
			choice.On = choice.On || id == l.ID
		}
		page.Labels = append(page.Labels, choice)
	}
	v.Heading, v.Data = page.Entry.Title, page
	h.render(w, r, http.StatusOK, "entry", v)
}

// form reads the body of a request that changes something; ok is false when
// it cannot be read, and the answer is given.
func (h *Handler) form(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxActionForm)
	if err := r.ParseForm(); err != nil || !isText(r.PostForm) {
		h.fail(w, r, http.StatusBadRequest)
		return false
	}
	return true
}

// isText reports whether every value is text a database keeps: UTF-8
// without a NUL, which PostgreSQL refuses.
func isText(values url.Values) bool {
	for _, list := range values {
		for _, value := range list {
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return false
			}
		}
	}
	return true
}

// back sends the reader to the page the form was on, at the entry when
// there is one, so that reading goes on where it was.
func (h *Handler) back(w http.ResponseWriter, r *http.Request, entryID int64) {
	target := h.localTarget(r.PostForm.Get("next"))
	if entryID != 0 {
		target += "#e" + strconv.FormatInt(entryID, 10)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// markEntry makes an entry read or unread.
func (h *Handler) markEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	read := r.PostForm.Get("read") != "0"
	n, err := h.db.SetEntriesRead(ctx, user.ID, []int64{e.ID}, read, h.now().Unix())
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if n > 0 {
		h.hooks.EntriesRead.Call(ctx, hooks.EntriesRead{UserID: user.ID, IDs: []int64{e.ID}, IsRead: read})
	}
	h.back(w, r, e.ID)
}

// starEntry stars an entry or takes the star off.
func (h *Handler) starEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	starred := r.PostForm.Get("starred") != "0"
	if err := h.db.SetEntriesFavorite(ctx, user.ID, []int64{e.ID}, starred, h.now().Unix()); err != nil {
		h.broken(w, r, err)
		return
	}
	h.hooks.EntriesFavorite.Call(ctx, hooks.EntriesFavorite{UserID: user.ID, IDs: []int64{e.ID}, IsFavorite: starred})
	h.back(w, r, e.ID)
}

var errLabelLong = errors.New("web: the name of the label is too long")

// labelNamed finds the label of a name and makes it when there is none.
// A name a category has is store.ErrConflict.
func (h *Handler) labelNamed(ctx context.Context, userID int64, name string) (int64, error) {
	find := func() (int64, error) {
		labels, err := h.db.Tags(ctx, userID)
		for _, l := range labels {
			if l.Name == name {
				return l.ID, err
			}
		}
		return 0, err
	}
	if id, err := find(); id != 0 || err != nil {
		return id, err
	}
	if utf8.RuneCountInString(name) > maxLabelName {
		return 0, errLabelLong
	}
	created := &store.Tag{UserID: userID, Name: name}
	err := h.db.CreateTag(ctx, created)
	if errors.Is(err, store.ErrConflict) {
		// A category has the name, or another request made the label meanwhile.
		if id, found := find(); id != 0 || found != nil {
			return id, found
		}
	}
	return created.ID, err
}

// labelEntry gives an entry the labels ticked in the form, and a new one
// when the form names it, and takes the others off.
func (h *Handler) labelEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	wanted := map[int64]bool{}
	for _, value := range r.PostForm["label"] {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			wanted[id] = true
		}
	}
	notice := "notice.labels"
	if name := strings.TrimSpace(r.PostForm.Get("new")); name != "" {
		id, err := h.labelNamed(ctx, user.ID, name)
		switch {
		case errors.Is(err, errLabelLong):
			notice = "notice.label-long"
		case errors.Is(err, store.ErrConflict):
			notice = "notice.label-taken"
		case err != nil:
			h.broken(w, r, err)
			return
		default:
			wanted[id] = true
		}
	}
	err := h.db.InTx(ctx, func(tx *store.Store) error {
		labels, err := tx.Tags(ctx, user.ID)
		if err != nil {
			return err
		}
		for _, l := range labels {
			if wanted[l.ID] {
				err = tx.TagEntries(ctx, user.ID, l.ID, []int64{e.ID})
			} else {
				err = tx.UntagEntries(ctx, user.ID, l.ID, []int64{e.ID})
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, notice, 0)
	h.back(w, r, e.ID)
}

// markAll makes read the unread entries a page of the reading screen was
// listing: those of its stream, state and search that were there when the
// page was made, or only those older than a day or a week.
func (h *Handler) markAll(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	lib, err := h.library(ctx, user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	s, ok := lib.streamAt(r.PostForm.Get("stream"))
	if !ok {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	now := h.now()
	before, err := strconv.ParseInt(r.PostForm.Get("before"), 10, 64)
	if err != nil || before <= 0 || before > now.UnixMicro() {
		before = now.UnixMicro()
	}
	switch r.PostForm.Get("older") {
	case "day":
		before = min(before, now.Add(-24*time.Hour).UnixMicro())
	case "week":
		before = min(before, now.Add(-7*24*time.Hour).UnixMicro())
	}
	prefs := readReading(user)
	showing := show(r.PostForm, prefs, s)
	query, err := h.parseSearch(showing.query, prefs)
	if err != nil {
		h.fail(w, r, http.StatusBadRequest)
		return
	}

	var n int
	set := s.set
	set.OnlyFavorite = set.OnlyFavorite || showing.state == stateStar
	if query == nil {
		n, err = h.db.MarkSetRead(ctx, user.ID, set, before, now.Unix())
	} else {
		// The entries a search picks are known only one by one.
		unread := false
		var entries []*store.Entry
		entries, _, err = h.db.ListPage(ctx, user.ID, store.Listing{Set: set, Read: &unread, Search: query})
		var ids []int64
		for _, e := range entries {
			if e.ID <= before {
				ids = append(ids, e.ID)
			}
		}
		if err == nil {
			n, err = h.db.SetEntriesRead(ctx, user.ID, ids, true, now.Unix())
		}
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.marked", n)
	h.back(w, r, 0)
}
