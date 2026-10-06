package greader

import (
	"cmp"
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/store"
)

// Feed priorities: where the entries of a feed are shown.
const (
	priorityImportant = 20
	priorityMain      = store.PriorityMain
	priorityCategory  = 0
	priorityFeed      = -5
	priorityHidden    = -10
)

// Prefixes and names of streams.
const (
	feedPrefix  = "feed/"
	labelPrefix = "user/-/label/"

	stateReadingList = "user/-/state/com.google/reading-list"
	stateStarred     = "user/-/state/com.google/starred"
	stateRead        = "user/-/state/com.google/read"
	stateUnread      = "user/-/state/com.google/unread"
	stateMain        = "user/-/state/org.freshrss/main"
	stateImportant   = "user/-/state/org.freshrss/important"
	stateHidden      = "user/-/state/org.freshrss/hidden"
)

// library is the subscriptions of a user in the order the API lists them.
type library struct {
	categories []*store.Category
	feeds      map[int64][]*store.Feed // by category
	byID       map[int64]*store.Feed
	category   map[int64]*store.Category
}

func (h *Handler) library(ctx context.Context, u *store.User) (*library, error) {
	categories, err := h.db.Categories(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	feeds, err := h.db.Feeds(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	lib := &library{
		categories: categories,
		feeds:      map[int64][]*store.Feed{},
		byID:       map[int64]*store.Feed{},
		category:   map[int64]*store.Category{},
	}
	// FreshRSS leaves the order of categories to the database, and the
	// reference one compares bytes; feeds it sorts itself, like labels.
	slices.SortStableFunc(lib.categories, func(a, b *store.Category) int { return cmp.Compare(a.Name, b.Name) })
	order := collator(u)
	slices.SortStableFunc(feeds, func(a, b *store.Feed) int { return order.CompareString(feedName(a), feedName(b)) })
	for _, c := range categories {
		lib.category[c.ID] = c
	}
	for _, f := range feeds {
		lib.feeds[f.CategoryID] = append(lib.feeds[f.CategoryID], f)
		lib.byID[f.ID] = f
	}
	return lib, nil
}

func (lib *library) categoryNamed(name string) *store.Category {
	for _, c := range lib.categories {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (lib *library) feedAt(address string) *store.Feed {
	for _, f := range lib.byID {
		if f.URL == address {
			return f
		}
	}
	return nil
}

// collator compares names the way the language of the user orders them,
// numbers by value. A language it does not know is ordered like no
// language in particular.
func collator(u *store.User) *collate.Collator {
	return collate.New(language.Make(readSettings(u.Settings).language), collate.Numeric)
}

// labels returns the labels of a user in the order of their names.
func (h *Handler) labels(ctx context.Context, u *store.User) ([]*store.Tag, error) {
	tags, err := h.db.Tags(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	order := collator(u)
	slices.SortStableFunc(tags, func(a, b *store.Tag) int { return order.CompareString(a.Name, b.Name) })
	return tags, nil
}

func labelNamed(tags []*store.Tag, name string) *store.Tag {
	for _, t := range tags {
		if t.Name == name {
			return t
		}
	}
	return nil
}

var schemePrefix = regexp.MustCompile(`(?i)^https?://(www[.])?`)

// feedName is the name of a feed, or its address without the scheme when it
// has none.
func feedName(f *store.Feed) string {
	if f.Name != "" {
		return f.Name
	}
	return schemePrefix.ReplaceAllString(f.URL, "")
}

// alternative returns the text with the characters some clients read as
// markup replaced by their fullwidth forms, as escapeToUnicodeAlternative
// of FreshRSS does. extended is for names of streams. FreshRSS means to
// replace quotes and the caret there as well, but its list of them is merged
// in a way that drops the first three, and clients see the result.
func alternative(s string, extended bool) string {
	pairs := []string{"&", "＆", "<", "＜", ">", "＞"}
	if extended {
		pairs = append(pairs, "?", "？", `\`, "＼", "/", "／", ",", "，", ";", "；")
	}
	return phpTrim(strings.NewReplacer(pairs...).Replace(s))
}

// phpTrim strips what PHP trim() strips.
func phpTrim(s string) string {
	return strings.Trim(s, " \t\n\r\x00\x0B")
}

func (h *Handler) tagList(ctx context.Context, q *request) error {
	type tag struct {
		ID          string `json:"id"`
		Type        string `json:"type,omitempty"`
		UnreadCount *int   `json:"unread_count,omitempty"`
	}
	tags := []tag{{ID: stateStarred}, {ID: stateReadingList}, {ID: stateMain}, {ID: stateImportant}}

	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}
	for _, c := range lib.categories {
		tags = append(tags, tag{ID: labelPrefix + c.Name, Type: "folder"}) // the type is for Inoreader
	}
	labels, err := h.labels(ctx, q.user)
	if err != nil {
		return err
	}
	counts, err := h.db.LabelCounts(ctx, q.user.ID)
	if err != nil {
		return err
	}
	for _, l := range labels {
		unread := counts[l.ID].Unread
		tags = append(tags, tag{ID: labelPrefix + l.Name, Type: "tag", UnreadCount: &unread})
	}
	return writeJSON(q.w, map[string]any{"tags": tags})
}

// priorityName is how a feed priority is written in the API and in OPML.
func priorityName(priority int) string {
	switch priority {
	case priorityImportant:
		return "important"
	case priorityCategory:
		return "category"
	case priorityFeed:
		return "feed"
	default:
		return "main"
	}
}

func (h *Handler) subscriptionList(ctx context.Context, q *request) error {
	type category struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type subscription struct {
		ID         string     `json:"id"`
		Title      string     `json:"title"`
		Categories []category `json:"categories"`
		URL        string     `json:"url"`
		HTMLURL    string     `json:"htmlUrl"`
		IconURL    string     `json:"iconUrl"`
		Priority   string     `json:"frss:priority"`
	}
	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}
	salt, err := h.db.Salt(ctx)
	if err != nil {
		return err
	}
	icons := h.base(q.r) + favicon.Path
	subscriptions := []subscription{}
	for _, c := range lib.categories {
		for _, f := range lib.feeds[c.ID] {
			if f.Priority <= priorityHidden {
				continue
			}
			subscriptions = append(subscriptions, subscription{
				ID:         feedPrefix + strconv.FormatInt(f.ID, 10),
				Title:      alternative(feedName(f), true),
				Categories: []category{{ID: labelPrefix + c.Name, Label: c.Name}},
				URL:        f.URL,
				HTMLURL:    f.Website,
				IconURL:    icons + favicon.HashOf(salt, f),
				Priority:   priorityName(f.Priority),
			})
		}
	}
	return writeJSON(q.w, map[string]any{"subscriptions": subscriptions})
}

// base is the public address of the server: the configured one, or the one
// the request came to.
func (h *Handler) base(r *http.Request) string {
	if h.baseURL != "" {
		return h.baseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handler) unreadCount(ctx context.Context, q *request) error {
	type count struct {
		ID     string `json:"id"`
		Count  int    `json:"count"`
		Newest string `json:"newestItemTimestampUsec"`
	}
	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}
	feedCounts, err := h.db.FeedCounts(ctx, q.user.ID)
	if err != nil {
		return err
	}
	var (
		counts      = []count{}
		total       int
		totalNewest int64
	)
	for _, c := range lib.categories {
		var (
			unread int
			newest int64
		)
		for _, f := range lib.feeds[c.ID] {
			if f.Priority <= priorityHidden {
				continue
			}
			fc := feedCounts[f.ID]
			counts = append(counts, count{feedPrefix + strconv.FormatInt(f.ID, 10), fc.Unread, strconv.FormatInt(fc.Newest, 10)})
			unread += fc.Unread
			newest = max(newest, fc.Newest)
		}
		counts = append(counts, count{labelPrefix + c.Name, unread, strconv.FormatInt(newest, 10)})
		total += unread
		totalNewest = max(totalNewest, newest)
	}

	labels, err := h.labels(ctx, q.user)
	if err != nil {
		return err
	}
	labelCounts, err := h.db.LabelCounts(ctx, q.user.ID)
	if err != nil {
		return err
	}
	for _, l := range labels {
		lc := labelCounts[l.ID]
		// FreshRSS gives a label without entries no time at all.
		newest := ""
		if lc.Newest != 0 {
			newest = strconv.FormatInt(lc.Newest, 10)
		}
		counts = append(counts, count{labelPrefix + l.Name, lc.Unread, newest})
	}
	counts = append(counts, count{stateReadingList, total, strconv.FormatInt(totalNewest, 10)})
	return writeJSON(q.w, map[string]any{"max": total, "unreadcounts": counts})
}
