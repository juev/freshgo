package web

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"

	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
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
	streamFeed     = "feed"
	streamCategory = "category"
	streamLabel    = "label"
	// streamQuery is a view the user has saved.
	streamQuery = "query"
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
	PostsPerPage  int    `json:"posts_per_page"`
	DefaultView   string `json:"default_view"`
	Sort          string `json:"sort"`
	SortOrder     string `json:"sort_order"`
	HideReadFeeds *bool  `json:"hide_read_feeds"`
	ShowFavUnread bool   `json:"show_fav_unread"`
	DisplayPosts  bool   `json:"display_posts"`
	AutoLoadMore  *bool  `json:"auto_load_more"`
	MarkWhen      struct {
		Article *bool `json:"article"`
	} `json:"mark_when"`
	Timezone string          `json:"timezone"`
	Queries  json.RawMessage `json:"queries"`
	// ToplineWebsite and ToplineDate say whether the row of an entry names
	// its feed and its date; Referrers are the hosts whose frames are told
	// where the reader comes from.
	ToplineWebsite string   `json:"topline_website"`
	ToplineDate    *bool    `json:"topline_date"`
	Referrers      []string `json:"send_referrer_allowlist"`
	// Sharing are the services entries can be sent to.
	Sharing json.RawMessage `json:"sharing"`
	// TranslateTo is the language entries are translated into, as a tag;
	// empty for the language of the interface.
	TranslateTo string `json:"translate_to"`
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
	// queries are the views the user has saved.
	queries []savedQuery
}

func (h *Handler) library(ctx context.Context, user *store.User) (*library, error) {
	return h.libraryOf(ctx, user, true)
}

// libraryOf reads what a user has subscribed to, with the counts of unread
// entries when they are asked for: a document handed out to the public has
// no use for them.
func (h *Handler) libraryOf(ctx context.Context, user *store.User, counts bool) (*library, error) {
	userID := user.ID
	lib := &library{feed: map[int64]*store.Feed{}, unread: map[int64]int{}, labelled: map[int64]int{}, queries: readQueries(user)}
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
	feedCounts, labelCounts := map[int64]store.Counts{}, map[int64]store.Counts{}
	if counts {
		if feedCounts, err = h.db.FeedCounts(ctx, userID); err != nil {
			return nil, err
		}
		if labelCounts, err = h.db.LabelCounts(ctx, userID); err != nil {
			return nil, err
		}
	}
	// The categories the user has put in order come first, in that order.
	position := func(c *store.Category) int {
		if p, ok := readAttrs(c.Attributes).number("position"); ok {
			return p
		}
		return math.MaxInt
	}
	slices.SortStableFunc(lib.categories, func(a, b *store.Category) int { return cmp.Compare(position(a), position(b)) })
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
	// base is what a saved query searches for, state the states it asks
	// for, in the bits of FreshRSS; both empty for the other streams.
	base  string
	state int
	sorting
}

// path is the address of the stream.
func (s stream) path() string {
	switch s.kind {
	case streamFeed:
		return "/feeds/" + strconv.FormatInt(s.id, 10)
	case streamCategory:
		return "/categories/" + strconv.FormatInt(s.id, 10)
	case streamLabel:
		return "/labels/" + strconv.FormatInt(s.id, 10)
	case streamQuery:
		return "/queries/" + strconv.FormatInt(s.id, 10)
	}
	return "/"
}

// stream finds the stream of a kind and an identifier; ok is false when the
// user has no such feed, category or label.
func (lib *library) stream(kind string, id int64) (s stream, ok bool) {
	s = stream{kind: kind, id: id}
	switch kind {
	case streamMain:
		minPriority := priorityMain
		s.set.MinPriority = &minPriority
		s.unread = lib.unreadFrom(0, priorityMain)
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
	case streamQuery:
		if id < 0 || id >= int64(len(lib.queries)) {
			return s, false
		}
		return lib.queryStream(int(id), lib.queries[id])
	default:
		return s, false
	}
	return s, true
}

// streamAt finds the stream an address of the interface names.
func (lib *library) streamAt(path string) (stream, bool) {
	for prefix, kind := range map[string]string{
		"/feeds/": streamFeed, "/categories/": streamCategory, "/labels/": streamLabel, "/queries/": streamQuery,
	} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			id, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return stream{}, false
			}
			return lib.stream(kind, id)
		}
	}
	if path == "/" {
		return lib.stream(streamMain, 0)
	}
	return stream{}, false
}

// showing is how a stream is listed: what the reader asked for where they
// did, the settings of the stream and of the user otherwise.
type showing struct {
	// view is how the entries are listed for this page alone, viewList or
	// viewExpanded, empty when the setting of the user decides.
	view  string
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
		case s.kind == streamQuery:
			// A saved query lists what it says, all when it does not say.
			if v.state = stateOfBits(s.state); v.state == "" {
				v.state = stateAll
			}
		case prefs.ShowFavUnread && s.kind == streamLabel:
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
	switch view := params.Get("view"); view {
	case viewList, viewExpanded:
		v.view = view
		v.asked.Set("view", view)
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
	Name string
	// URL is the stream as the page lists streams now, Path the stream
	// itself, by which the script remembers a category that is folded.
	URL     string
	Path    string
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
	// Root is the stream of everything, where the reader comes back to
	// from a category or a feed.
	Root       branch
	Categories []branch
	Labels     []branch
	Queries    []branch
}

func (h *Handler) tree(v *view, lib *library, current stream, state showing, hideRead bool) tree {
	at := func(s stream, name string) branch {
		if name == "" {
			name = s.name
		}
		return branch{
			Name: name, URL: state.link(h, s.path(), "q", ""), Path: s.path(), Unread: s.unread,
			Current: s.kind == current.kind && s.id == current.id,
		}
	}
	var t tree
	root, _ := lib.stream(streamMain, 0)
	t.Root = at(root, v.T("stream.main"))
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
	for n := range lib.queries {
		// A query whose feed, category or label is gone lists nothing.
		if one, ok := lib.stream(streamQuery, int64(n)); ok && one.name != "" {
			// A query brings its own state and order: those of the page stay behind.
			t.Queries = append(t.Queries, branch{
				Name: one.name, URL: h.url(one.path()), Current: current.kind == streamQuery && current.id == one.id,
			})
		}
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

// excerptLength is how much of the text of an entry its row may show.
const excerptLength = 200

// article is an entry as a page shows it.
type article struct {
	ID    int64
	Title string
	// URL is the page of the entry, Link the address the feed gave it.
	URL      string
	Link     string
	Feed     string
	FeedURL  string
	Authors  string
	Date     string
	DateTime string
	Content  template.HTML
	// Excerpt is the beginning of the text, for the row of the entry.
	Excerpt     string
	Attachments []attachment
	Tags        []string
	Labels      []string
	Read        bool
	Starred     bool
	// Full says that the text is that of the page of the entry.
	Full bool
	// Translation is what the entry offers about its translation; nil
	// when entries are not translated.
	Translation *translation
	// ShowFeed and ShowDate say what the row of the entry in a list names.
	ShowFeed, ShowDate bool
	// Share are the ways the entry can be sent on.
	Share []shareLink
}

// translation is the action an entry offers about its translation.
type translation struct {
	// Do is what the form asks for: "translate" has the next paragraphs
	// translated, "original" and "translation" switch what is shown.
	Do string
	// Label names the action; Percent is how far a translation that was
	// begun has come, 0 when none was.
	Label   string
	Percent int
}

// translateTo is the language the entries of a reader are translated into.
func translateTo(v *view, prefs reading) language.Tag {
	if tag, err := language.Parse(prefs.TranslateTo); err == nil && prefs.TranslateTo != "" {
		return tag
	}
	return language.Make(v.Lang())
}

// offer says what an entry in a state of translation offers.
func offer(v *view, state translate.State) *translation {
	t := &translation{Do: "translate"}
	if state.Total > 0 {
		t.Percent = min(100, state.Done*100/state.Total)
	}
	switch {
	case !state.Exists:
		t.Label, t.Percent = v.T("entry.translate"), 0
	case !state.Shown:
		t.Do, t.Label = "translation", v.T("entry.translation")
	case !state.Complete():
		t.Label = v.T("entry.translate-on", t.Percent)
	default:
		t.Do, t.Label = "original", v.T("entry.untranslated")
	}
	return t
}

// articles prepares entries for a page. Handlers of EntryBeforeDisplay see
// each and may leave it out. With translated set, an entry that has a
// translation into the language of the reader is shown in it.
func (h *Handler) articles(ctx context.Context, v *view, lib *library, userID int64, prefs reading, entries []*store.Entry, translated bool) ([]article, error) {
	loc := prefs.location()
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := h.db.EntryLabels(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	throughServer, err := h.throughServer(ctx, v.mediaProxy, h.prefix)
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
		title, content := e.Title, e.Content
		var offered *translation
		if translated && h.translator != nil {
			state := translate.Of(e, translateTo(v, prefs).String())
			title, content, offered = state.Title, state.Content, offer(v, state)
		}
		a := article{
			ID: e.ID, Title: title, Translation: offered, URL: h.url("/entries/" + strconv.FormatInt(e.ID, 10)),
			Authors: strings.Join(e.Authors, ", "), Date: date.Format("2006-01-02 15:04"), DateTime: date.Format(time.RFC3339),
			// What is stored was cleaned when it was fetched, but not all of it by
			// freshgo: an import brings what FreshRSS let through.
			Content:     template.HTML(throughServer(withReferrers(sanitize.HTML(content, e.Link, nil), prefs.Referrers))), //nolint:gosec // cleaned on this line
			Attachments: attachments(e), Tags: e.Tags, Labels: labels[e.ID], Read: e.IsRead, Starred: e.IsFavorite,
			Full:     refresh.HasPageText(e),
			ShowFeed: prefs.ToplineWebsite != "none", ShowDate: prefs.ToplineDate == nil || *prefs.ToplineDate,
		}
		a.Excerpt = sanitize.Text(string(a.Content), excerptLength)
		if address, err := url.Parse(e.Link); err == nil && (address.Scheme == "http" || address.Scheme == "https") {
			a.Link = e.Link
		}
		a.Share = shareLinks(phpList[sharing](prefs.Sharing), e.ID, e.Title, a.Link)
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

// withReferrers lets the frames of the given hosts know where the reader
// comes from, which some players ask for before they play: everything else
// an entry embeds is told nothing.
func withReferrers(content string, hosts []string) string {
	if len(hosts) == 0 || !strings.Contains(content, "<iframe") {
		return content
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body>" + content + "</body></html>"))
	if err != nil {
		return content
	}
	changed := false
	doc.Find("iframe[src]").Each(func(_ int, frame *goquery.Selection) {
		address, err := url.Parse(frame.AttrOr("src", ""))
		if err == nil && slices.Contains(hosts, strings.ToLower(address.Hostname())) {
			frame.SetAttr("referrerpolicy", "strict-origin-when-cross-origin")
			changed = true
		}
	})
	if !changed {
		return content
	}
	out, err := doc.Find("body").Html()
	if err != nil {
		return content
	}
	return out
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
	// Views are the two ways to list entries, as links, for a visitor, who
	// has no setting to keep the choice in.
	Views []choice
	// Before is the moment the page was made, as an entry identifier:
	// "mark all as read" leaves what arrived later alone.
	Before int64
	// CanChange is false for a visitor, who only reads.
	CanChange bool
	// CanRefresh says whether the reader may have the feeds fetched now.
	CanRefresh bool
	// Settings is the page of the settings of the feed, category or label
	// being read, empty for the other streams.
	Settings string
}

// The two ways to list entries: rows that open in place, or every entry
// open.
const (
	viewList     = "list"
	viewExpanded = "expanded"
)

// saveView keeps the way the user wants entries listed, the setting
// display_posts, and goes back to the stream.
func (h *Handler) saveView(w http.ResponseWriter, r *http.Request) {
	if !h.form(w, r) {
		return
	}
	expanded := r.PostForm.Get("view") == viewExpanded
	err := h.db.UpdateUserSettings(r.Context(), state(r).who.user.ID, func(settings map[string]json.RawMessage) error {
		attrs(settings).set("display_posts", expanded)
		return nil
	})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, h.localTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}

// reader answers with a stream of entries.
func (h *Handler) reader(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, who := r.Context(), state(r).who
		lib, err := h.library(ctx, who.user)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s, ok := lib.stream(kind, id)
		// An address that names a stream by something that is no number
		// names none.
		if !ok || err != nil && r.PathValue("id") != "" {
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
		expanded := prefs.DisplayPosts
		if showing.view != "" {
			expanded = showing.view == viewExpanded
		}
		page := readerPage{
			Unread: s.unread, Sort: showing.sort, Asc: showing.asc, Query: showing.query, State: showing.state,
			Asked: showing.asked, Path: s.path(), Here: showing.here(r.URL), Expanded: expanded,
			Before: h.now().UnixMicro(), CanChange: !who.anonymous,
			CanRefresh: !who.anonymous || state(r).system.AllowAnonymousRefresh,
		}
		if !who.anonymous {
			switch s.kind {
			case streamFeed, streamCategory, streamLabel:
				page.Settings = "/subscriptions" + s.path()
			}
		}
		for _, state := range []string{stateUnread, stateAll, stateStar} {
			page.States = append(page.States, choice{
				Name: v.T("state." + state), URL: showing.link(h, s.path(), "state", state), Current: state == showing.state,
			})
		}
		if who.anonymous {
			for _, view := range []string{viewList, viewExpanded} {
				page.Views = append(page.Views, choice{
					Name: v.T("view." + view), URL: showing.link(h, s.path(), "view", view), Current: expanded == (view == viewExpanded),
				})
			}
		}
		for _, name := range orderNames {
			page.Sorts = append(page.Sorts, choice{Name: v.T("sort." + name), URL: name, Current: name == showing.sort})
		}
		hideRead := showing.state == stateUnread && (prefs.HideReadFeeds == nil || *prefs.HideReadFeeds)
		page.Tree = h.tree(v, lib, s, showing, hideRead)
		if r.Header.Get(fragmentHeader) == "tree" {
			// The script asks for the tree alone, and the count of the
			// stream, to bring the counts up to date.
			v.Data = page
			h.fragment(w, r, "tree-fresh", v)
			return
		}

		status := http.StatusOK
		query, err := h.streamSearch(s, showing.query, prefs)
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
				page.Entries, err = h.articles(ctx, v, lib, who.user.ID, prefs, entries, true)
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
	lib, err := h.library(ctx, who.user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "reader", "entry.untitled")
	shown, err := h.articles(ctx, v, lib, who.user.ID, readReading(who.user), []*store.Entry{e}, true)
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

// fragmentHeader is what the script sends to get a part of a page instead
// of the page: "tree" for the tree of a stream, "entry" for the entry an
// action was about.
const fragmentHeader = "X-Fragment"

// noticeHeader carries what an action has to say with a part of a page,
// encoded as a path segment.
const noticeHeader = "X-Notice"

// back ends an action on an entry. The reader is sent to the page the form
// was on, at the entry, so that reading goes on where it was, and the page
// says what notice names; the script gets the entry as it is now instead.
func (h *Handler) back(w http.ResponseWriter, r *http.Request, entryID int64, notice string) {
	next := r.PostForm.Get("next")
	if r.Header.Get(fragmentHeader) == "entry" {
		h.entryFragment(w, r, entryID, next, notice)
		return
	}
	if notice != "" {
		h.notify(w, r, notice, 0)
	}
	http.Redirect(w, r, h.localTarget(next)+"#e"+strconv.FormatInt(entryID, 10), http.StatusSeeOther)
}

// entryFragment answers with an entry as a stream lists it.
func (h *Handler) entryFragment(w http.ResponseWriter, r *http.Request, entryID int64, here, notice string) {
	ctx, who := r.Context(), state(r).who
	e, err := h.db.EntryByID(ctx, who.user.ID, entryID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	lib, err := h.library(ctx, who.user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "reader", "entry.untitled")
	prefs := readReading(who.user)
	shown, err := h.articles(ctx, v, lib, who.user.ID, prefs, []*store.Entry{e}, true)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if len(shown) == 0 {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	if notice != "" {
		w.Header().Set(noticeHeader, url.PathEscape(noticeText(v, notice, 0)))
	}
	v.Data = dict("Entry", shown[0], "Here", here, "Page", v, "Expanded", prefs.DisplayPosts, "CanChange", true)
	h.fragment(w, r, "article", v.Data)
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
	h.back(w, r, e.ID, "")
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
	h.back(w, r, e.ID, "")
}

// fullTextEntry gives an entry the text of its page, or the text of the
// feed back.
func (h *Handler) fullTextEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	err := h.refresher.CompleteEntry(ctx, user, e.ID, r.PostForm.Get("full") != "0")
	notice := ""
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
		return
	case err != nil:
		h.log.Warn("page of an entry gave no text", "user", user.Name, "entry", e.ID, "error", err)
		notice = "notice.fulltext-failed"
	}
	h.back(w, r, e.ID, notice)
}

// progressHeader tells the script how far the translation of an entry is:
// paragraphs done, a slash, paragraphs in all.
const progressHeader = "X-Progress"

// translateEntry has the text of an entry translated into the language of
// the reader, or switches between the translation and the original. The
// script gets the entry after the next paragraphs and asks again while
// some are left; a plain form waits for all of them.
func (h *Handler) translateEntry(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if h.translator == nil {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	e, ok := h.ownEntry(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	tag := translateTo(h.view(r, "reader", "entry.untitled"), readReading(user))
	var (
		progress translate.State
		err      error
	)
	switch do := r.PostForm.Get("do"); {
	case do == "original" || do == "translation":
		progress, err = h.translator.Show(ctx, user.ID, e.ID, tag, do == "translation")
	case r.Header.Get(fragmentHeader) == "entry":
		progress, err = h.translator.Step(ctx, user.ID, e.ID, tag)
	default:
		progress, err = h.translator.All(ctx, user.ID, e.ID, tag)
	}
	notice := ""
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
		return
	case errors.Is(err, translate.ErrSameLanguage):
		notice = "notice.translate-same"
	case err != nil:
		h.log.Warn("entry was not translated", "user", user.Name, "entry", e.ID, "error", err)
		notice = "notice.translate-failed"
	case progress.Complete() && progress.Failed > 0 && r.PostForm.Get("do") == "translate":
		notice = "notice.translate-partly"
	}
	w.Header().Set(progressHeader, strconv.Itoa(progress.Done)+"/"+strconv.Itoa(progress.Total))
	h.back(w, r, e.ID, notice)
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
	h.back(w, r, e.ID, notice)
}

// markAll makes read the unread entries a page of the reading screen was
// listing: those of its stream, state and search that were there when the
// page was made, or only those older than a day or a week.
func (h *Handler) markAll(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	lib, err := h.library(ctx, user)
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
	query, err := h.streamSearch(s, showing.query, prefs)
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
	http.Redirect(w, r, h.localTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}
