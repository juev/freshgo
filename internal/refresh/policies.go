package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// goneGrace is how much older than the refresh the last sighting of an entry
// has to be, in seconds, for the entry to count as gone from the feed.
const goneGrace = 10

// archiving is the retention setting of FreshRSS: the "archiving" value of a
// feed, of a category or of a user.
type archiving struct {
	// period is an ISO 8601 duration; entries not listed for longer go.
	// Empty means no such rule.
	period string
	// keepMax is the number of entries past which the older ones go, zero for
	// no such rule; keepMin the number of entries that stay whatever the rules.
	keepMax, keepMin                       int
	keepFavorites, keepLabels, keepUnreads bool
}

// defaultArchiving is what FreshRSS gives a new user.
var defaultArchiving = archiving{period: "P3M", keepMax: 200, keepMin: 50, keepFavorites: true, keepLabels: true}

// readArchiving decodes an "archiving" value; ok is false when there is none,
// in which case the next level decides. FreshRSS stores false for a rule that
// is off, and an empty list for settings that were never filled in: nothing
// is deleted then.
func readArchiving(raw json.RawMessage) (a archiving, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' && raw[0] != '[' {
		return archiving{}, false
	}
	v := readAttributes(raw)
	a.period, _ = get[string](v, "keep_period")
	a.keepMax, _ = get[int](v, "keep_max")
	a.keepMin, _ = get[int](v, "keep_min")
	a.keepFavorites, _ = get[bool](v, "keep_favourites")
	a.keepLabels, _ = get[bool](v, "keep_labels")
	a.keepUnreads, _ = get[bool](v, "keep_unreads")
	return a, true
}

// category is what the feeds of one category share during a run.
type category struct {
	attrs attributes
	// rules are the filter actions of the category.
	rules []search.Rule
	// mu orders the refreshes of the feeds of a category that marks repeated
	// titles or identifiers read: each has to see the entries of the others.
	mu sync.Mutex
}

func (r *Refresher) categories(ctx context.Context, userID int64) (map[int64]*category, error) {
	list, err := r.db.Categories(ctx, userID)
	if err != nil {
		return nil, err
	}
	categories := make(map[int64]*category, len(list))
	for _, c := range list {
		categories[c.ID] = &category{attrs: readAttributes(c.Attributes)}
	}
	return categories, nil
}

// clean deletes the entries of a feed its retention settings give up: those
// of the feed, else of its category, else of the user.
func (r *Refresher) clean(ctx context.Context, tx *store.Store, j *job, f *store.Feed, now int64) (int, error) {
	a, ok := readArchiving(readAttributes(f.Attributes)["archiving"])
	if !ok {
		if c := j.categories[f.CategoryID]; c != nil {
			a, ok = readArchiving(c.attrs["archiving"])
		}
	}
	if !ok {
		a = j.conf.archiving
	}
	keep := store.Retention{
		KeepMax: a.keepMax, KeepMin: a.keepMin,
		KeepFavorites: a.keepFavorites, KeepLabeled: a.keepLabels, KeepUnread: a.keepUnreads,
	}
	if a.period != "" {
		period, err := feed.ParsePeriod(a.period)
		if err != nil {
			r.log.Warn("retention period is not usable, entries are not deleted by age", "user", j.user.Name,
				"feed", f.ID, "url", withoutCredentials(f.URL), "error", err)
		} else {
			keep.SeenBefore = period.Before(time.Unix(now, 0).In(j.conf.location)).Unix()
		}
	}
	n, err := tx.DeleteOldEntries(ctx, f.UserID, f.ID, keep)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		r.log.Debug("old entries deleted", "user", j.user.Name, "feed", f.ID, "url", withoutCredentials(f.URL), "entries", n)
	}
	return n, nil
}

// tidy applies, inside the transaction of a refresh, what follows the entries
// of the feed being brought up to date: old entries are deleted, entries gone
// from the feed and unread entries beyond the allowed number become read.
// changed says whether the refresh added or rewrote entries.
func (r *Refresher) tidy(ctx context.Context, tx *store.Store, j *job, f *store.Feed, now int64, changed bool) error {
	deleted, err := r.clean(ctx, tx, j, f, now)
	if err != nil {
		return err
	}
	attrs := readAttributes(f.Attributes)
	gone := 0
	uponGone, ok := get[bool](attrs, "read_upon_gone")
	if !ok {
		uponGone = j.conf.readUponGone
	}
	if uponGone {
		if gone, err = tx.MarkUnseenEntriesRead(ctx, f.UserID, f.ID, now-goneGrace); err != nil {
			return err
		}
	}
	// As in FreshRSS, the number of unread entries is looked at only when
	// the refresh changed something in the feed.
	if !changed && deleted == 0 && gone == 0 {
		return nil
	}
	maxUnread, ok := get[int](attrs, "keep_max_n_unread")
	if !ok {
		maxUnread = j.conf.maxUnread
	}
	if maxUnread < 0 {
		return nil
	}
	_, err = tx.KeepNewestUnread(ctx, f.UserID, f.ID, maxUnread)
	return err
}

// repeats are the titles and identifiers that make a new entry read on
// arrival. A nil map is a rule that is off.
type repeats struct {
	titles map[string]bool
	guids  map[string]bool
}

// titleKey is what titles are compared by. An entry without a title stands
// under its identifier, as in FreshRSS, so that untitled entries of a feed
// are not taken for repeats of one another.
func titleKey(guid, title string) string {
	if title == "" {
		return guid
	}
	return title
}

func (p *repeats) add(e *store.Entry) {
	if p.titles != nil {
		p.titles[titleKey(e.GUID, e.Title)] = true
	}
	if p.guids != nil {
		p.guids[e.GUID] = true
	}
}

// sameTitleInFeed is the number of latest entries of the feed among which a
// repeated title makes a new entry read; zero is off.
func sameTitleInFeed(attrs attributes, conf userSettings) int {
	raw, present := attrs["read_when_same_title_in_feed"]
	if !present || string(raw) == "null" {
		return conf.sameTitleInFeed
	}
	n, _ := get[int](attrs, "read_when_same_title_in_feed")
	return max(n, 0)
}

// loadRepeats reads what the feed's and its category's rules compare new
// entries with. It returns the lock that keeps other feeds of the category
// from adding entries meanwhile, nil when the category has no such rule; the
// caller holds it until its own entries are stored.
func (r *Refresher) loadRepeats(ctx context.Context, j *job, f *store.Feed, attrs attributes) (repeats, *sync.Mutex, error) {
	var (
		known repeats
		lock  *sync.Mutex
	)
	titles := func(keys []store.EntryKey) {
		if known.titles == nil {
			known.titles = make(map[string]bool, len(keys))
		}
		for _, k := range keys {
			known.titles[titleKey(k.GUID, k.Title)] = true
		}
	}
	if n := sameTitleInFeed(attrs, j.conf); n > 0 {
		keys, err := r.db.LatestFeedEntries(ctx, f.UserID, f.ID, n)
		if err != nil {
			return repeats{}, nil, err
		}
		titles(keys)
	}
	c := j.categories[f.CategoryID]
	if c == nil {
		return known, nil, nil
	}
	// A limit that is not a positive number means every entry of the category.
	limit := func(key string) (n int, on bool) {
		raw, present := c.attrs[key]
		if !present || string(raw) == "null" {
			return 0, false
		}
		n, _ = get[int](c.attrs, key)
		return max(n, 0), true
	}
	titleLimit, byTitle := limit("read_when_same_title_in_category")
	guidLimit, byGUID := limit("read_when_same_guid_in_category")
	if !byTitle && !byGUID {
		return known, nil, nil
	}
	lock = &c.mu
	lock.Lock()
	if byTitle {
		keys, err := r.db.LatestCategoryEntries(ctx, f.UserID, f.CategoryID, titleLimit)
		if err != nil {
			lock.Unlock()
			return repeats{}, nil, err
		}
		titles(keys)
	}
	if byGUID {
		keys, err := r.db.LatestCategoryEntries(ctx, f.UserID, f.CategoryID, guidLimit)
		if err != nil {
			lock.Unlock()
			return repeats{}, nil, err
		}
		known.guids = make(map[string]bool, len(keys))
		for _, k := range keys {
			known.guids[k.GUID] = true
		}
	}
	return known, lock, nil
}

// autoRead marks an unread entry read when a rule says so: every entry on
// arrival, or one whose title or identifier is among the known ones.
func (r *Refresher) autoRead(ctx context.Context, e *store.Entry, uponReception bool, known repeats) {
	if e.IsRead {
		return
	}
	mark := func(why string) {
		e.IsRead = true
		r.hooks.EntryAutoRead.Call(ctx, hooks.EntryAuto{Entry: e, Why: why})
	}
	if uponReception {
		mark(hooks.WhyUponReception)
	}
	if known.titles[titleKey(e.GUID, e.Title)] {
		mark(hooks.WhySameTitleInFeed)
	}
	if known.guids[e.GUID] {
		mark(hooks.WhySameGUIDInCategory)
	}
}

// PurgeStats is the outcome of a purge for one user.
type PurgeStats struct {
	User    string
	Deleted int
}

// Purge deletes, for every user, the entries the retention settings give up,
// as a refresh does for the feeds it handles.
func (r *Refresher) Purge(ctx context.Context) ([]PurgeStats, error) {
	users, err := r.db.Users(ctx)
	if err != nil {
		return nil, fmt.Errorf("purge: %w", err)
	}
	var all []PurgeStats
	for _, u := range users {
		st, err := r.purgeUser(ctx, u)
		all = append(all, st)
		if err != nil {
			return all, fmt.Errorf("purge: user %s: %w", u.Name, err)
		}
	}
	return all, nil
}

func (r *Refresher) purgeUser(ctx context.Context, u *store.User) (PurgeStats, error) {
	st := PurgeStats{User: u.Name}
	j := &job{user: u, conf: readUserSettings(u.Settings)}
	var err error
	if j.categories, err = r.categories(ctx, u.ID); err != nil {
		return st, err
	}
	feeds, err := r.db.Feeds(ctx, u.ID)
	if err != nil {
		return st, err
	}
	now := r.now().Unix()
	for _, f := range feeds {
		err := r.db.InTx(ctx, func(tx *store.Store) error {
			fresh, err := tx.LockFeed(ctx, f.UserID, f.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			n, err := r.clean(ctx, tx, j, fresh, now)
			st.Deleted += n
			return err
		})
		if err != nil {
			return st, err
		}
	}
	return st, nil
}
