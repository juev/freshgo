package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/fulltext"
	"github.com/juev/freshgo/internal/opml"
	"github.com/juev/freshgo/internal/store"
)

var (
	// ErrBusy is returned when a refresh is running already.
	ErrBusy = errors.New("refresh: a refresh is running already")
	// ErrTooManyFeeds is returned when the user has as many feeds as the
	// installation allows.
	ErrTooManyFeeds = errors.New("refresh: the user has as many feeds as the installation allows")
	// ErrNoEntries is returned when a feed has no entry to try a selector on.
	ErrNoEntries = errors.New("refresh: the feed has no entries")
	// ErrNoOPML is returned for a category that mirrors no OPML document.
	ErrNoOPML = errors.New("refresh: the category has no OPML address")
)

// defaultOPMLPeriod is how often, in seconds, the OPML document of a
// category is read for a user who has not said otherwise: the
// dynamic_opml_ttl_default of FreshRSS.
const defaultOPMLPeriod = 43200

// KindDynamicOPML is the kind of a category whose feeds are those of an OPML
// document at an address.
const KindDynamicOPML = 2

// RefreshUser refreshes the due feeds of one user now, as a run does for
// every user. While a run is going on it gives ErrBusy instead of waiting.
func (r *Refresher) RefreshUser(ctx context.Context, u *store.User, o Options) (Stats, error) {
	if !r.running.TryLock() {
		return Stats{User: u.Name}, ErrBusy
	}
	defer r.running.Unlock()
	j, err := r.userJob(ctx, u)
	if err != nil {
		return Stats{User: u.Name}, err
	}
	r.refreshOPMLs(ctx, j)
	return r.refreshUser(ctx, j, o)
}

// roomForFeed gives ErrTooManyFeeds when the user may not have one more feed.
func (r *Refresher) roomForFeed(ctx context.Context, userID int64) error {
	system, err := r.db.System(ctx)
	if err != nil {
		return err
	}
	if system.Limits.MaxFeeds <= 0 {
		return nil
	}
	feeds, err := r.db.Feeds(ctx, userID)
	if err != nil {
		return err
	}
	if len(feeds) >= system.Limits.MaxFeeds {
		return ErrTooManyFeeds
	}
	return nil
}

// ReloadFeed fetches a feed as if it had never been fetched, and reads the
// pages of its newest entries again, limit of them, when the feed takes the
// text of its entries from their pages. An error means the feed is now
// marked as failing, unless it is the database that failed.
func (r *Refresher) ReloadFeed(ctx context.Context, u *store.User, feedID int64, limit int) error {
	j, err := r.userJob(ctx, u)
	if err != nil {
		return err
	}
	var f *store.Feed
	err = r.db.InTx(ctx, func(tx *store.Store) error {
		if f, err = tx.LockFeed(ctx, u.ID, feedID); err != nil {
			return err
		}
		f.LastUpdate, f.HTTPETag, f.HTTPLastModified = 0, "", ""
		return tx.UpdateFeed(ctx, f)
	})
	if err != nil {
		return err
	}
	if _, err := r.refreshFeed(ctx, j, f); err != nil {
		return err
	}
	if f, err = r.db.FeedByID(ctx, u.ID, feedID); err != nil {
		return err
	}
	params, err := fetch.FeedParams(f.HTTPAuth, f.Attributes)
	if err != nil {
		return err
	}
	c := r.completion(j, f, readAttributes(f.Attributes), params)
	if c.selector == "" || limit <= 0 {
		return nil
	}
	entries, _, err := r.db.ListPage(ctx, u.ID, store.Listing{Set: store.EntrySet{FeedID: feedID}, Limit: limit})
	if err != nil {
		return err
	}
	now := r.now().Unix()
	for _, e := range entries {
		before := e.Content
		restoreFeedText(e)
		r.complete(ctx, j, f, c, e, now)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A page that was not read leaves the entry with the text it had:
		// the text of the feed alone would be a loss.
		if e.Content == before || !strings.Contains(e.Content, fullContentStart) {
			continue
		}
		err := r.db.InTx(ctx, func(tx *store.Store) error {
			// What the reader did to the entry meanwhile stands.
			fresh, err := tx.EntryByID(ctx, u.ID, e.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			fresh.Content, fresh.Attributes, fresh.LastModified = e.Content, e.Attributes, now
			return tx.UpdateEntry(ctx, fresh)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// restoreFeedText puts back the text the feed gave an entry whose text was
// completed from its page.
func restoreFeedText(e *store.Entry) {
	attrs := readAttributes(e.Attributes)
	if original, ok := get[string](attrs, "original_content"); ok {
		e.Content = original
		delete(attrs, "original_content")
		e.Attributes = attrs.raw()
		return
	}
	start := strings.Index(e.Content, fullContentStart)
	end := strings.Index(e.Content, fullContentEnd)
	if start >= 0 && end > start {
		e.Content = e.Content[:start] + e.Content[end+len(fullContentEnd):]
	}
}

// PreviewArticle returns what a selector, and a filter if there is one,
// take from the page of the newest entry of a feed: the text the entry
// would get. Nothing is stored.
func (r *Refresher) PreviewArticle(ctx context.Context, u *store.User, feedID int64, selector, filter string) (string, error) {
	f, err := r.db.FeedByID(ctx, u.ID, feedID)
	if err != nil {
		return "", err
	}
	entries, _, err := r.db.ListPage(ctx, u.ID, store.Listing{Set: store.EntrySet{FeedID: feedID}, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 || entries[0].Link == "" {
		return "", ErrNoEntries
	}
	params, err := fetch.FeedParams(f.HTTPAuth, f.Attributes)
	if err != nil {
		return "", err
	}
	https, err := r.httpsDomains(ctx)
	if err != nil {
		return "", err
	}
	return fulltext.Article(ctx, r.client, fulltext.Request{
		URL: entries[0].Link, Params: params, Selector: selector, Filter: filter, ForceHTTPS: https.URL,
	})
}

// RefreshOPML brings the feeds of a category in step with the OPML document
// the category mirrors: a feed the document no longer lists is muted, a new
// one is added, one that is back is unmuted. An error other than ErrNoOPML
// and store.ErrNotFound is recorded with the category.
func (r *Refresher) RefreshOPML(ctx context.Context, u *store.User, categoryID int64) error {
	categories, err := r.db.Categories(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, c := range categories {
		if c.ID == categoryID {
			return r.refreshOPML(ctx, u, readUserSettings(u.Settings), c)
		}
	}
	return fmt.Errorf("refresh: category %d: %w", categoryID, store.ErrNotFound)
}

// refreshOPMLs reads the OPML documents of the categories of a user that
// are due. A document that fails is logged; the feeds stay as they are.
func (r *Refresher) refreshOPMLs(ctx context.Context, j *job) {
	categories, err := r.db.Categories(ctx, j.user.ID)
	if err != nil {
		r.log.Warn("categories were not read, their OPML documents are skipped", "user", j.user.Name, "error", err)
		return
	}
	period := int64(defaultOPMLPeriod)
	if v, ok := get[int64](readAttributes(j.user.Settings), "dynamic_opml_ttl_default"); ok && v > 0 {
		period = v
	}
	now := r.now().Unix()
	for _, c := range categories {
		if c.Kind != KindDynamicOPML || now <= max(c.LastUpdate, c.Error)+period {
			continue
		}
		if err := r.refreshOPML(ctx, j.user, j.conf, c); err != nil && ctx.Err() == nil {
			r.log.Warn("OPML of a category was not read", "user", j.user.Name, "category", c.ID, "error", err)
		}
	}
}

func (r *Refresher) refreshOPML(ctx context.Context, u *store.User, conf userSettings, c *store.Category) error {
	address, _ := get[string](readAttributes(c.Attributes), "opml_url")
	if c.Kind != KindDynamicOPML || address == "" {
		return ErrNoOPML
	}
	err := r.mirrorOPML(ctx, u, conf, c, address)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := r.now().Unix()
	// The category is read again: its settings may have changed meanwhile.
	recorded := r.db.InTx(ctx, func(tx *store.Store) error {
		categories, cerr := tx.Categories(ctx, u.ID)
		if cerr != nil {
			return cerr
		}
		for _, fresh := range categories {
			if fresh.ID != c.ID {
				continue
			}
			if err != nil {
				fresh.Error = now
			} else {
				fresh.LastUpdate, fresh.Error = now, 0
			}
			return tx.UpdateCategory(ctx, fresh)
		}
		return nil
	})
	return errors.Join(err, recorded)
}

func (r *Refresher) mirrorOPML(ctx context.Context, u *store.User, conf userSettings, c *store.Category, address string) error {
	resp, err := r.client.Fetch(ctx, fetch.Request{URL: address, Accept: fetch.AcceptXML})
	if err != nil {
		return err
	}
	listed, err := opml.Feeds(resp.Body, u.ID, c.ID)
	if err != nil {
		return err
	}
	system, err := r.db.System(ctx)
	if err != nil {
		return err
	}
	inDocument := make(map[string]*store.Feed, len(listed))
	for _, f := range listed {
		inDocument[f.URL] = f
	}
	full := false
	err = r.db.InTx(ctx, func(tx *store.Store) error {
		full = false
		feeds, err := tx.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		has := make(map[string]bool, len(feeds))
		for _, f := range feeds {
			has[f.URL] = true
			if f.CategoryID != c.ID {
				continue
			}
			// A feed is known by the address the document gave it, which a
			// redirect may since have replaced.
			listedAs := mirroredFrom(f)
			has[listedAs] = true
			_, stays := inDocument[listedAs]
			if stays == (f.TTL >= 0) {
				continue
			}
			// Under the lock of the feed: a refresh or the user may be
			// writing the rest of it.
			fresh, err := tx.LockFeed(ctx, u.ID, f.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			switch {
			case !stays && fresh.TTL >= 0:
				if fresh.TTL == 0 {
					fresh.TTL = conf.ttlDefault
				}
				fresh.TTL = -fresh.TTL
			case stays && fresh.TTL < 0:
				fresh.TTL = -fresh.TTL
			default:
				continue
			}
			if err := tx.UpdateFeed(ctx, fresh); err != nil {
				return err
			}
		}
		count := len(feeds)
		for _, f := range listed {
			if has[f.URL] {
				continue
			}
			if system.Limits.MaxFeeds > 0 && count >= system.Limits.MaxFeeds {
				full = true
				break
			}
			added, ok := r.hooks.FeedBeforeInsert.Call(ctx, f)
			if !ok {
				continue
			}
			added.CategoryID = c.ID
			attrs := readAttributes(added.Attributes)
			attrs.set(mirroredKey, f.URL)
			added.Attributes = attrs.raw()
			if err := tx.CreateFeed(ctx, added); err != nil {
				return err
			}
			has[f.URL] = true
			count++
		}
		return nil
	})
	if err == nil && full {
		err = ErrTooManyFeeds
	}
	return err
}

// mirroredKey is the attribute of a feed that keeps the address an OPML
// document listed it under when a category that mirrors the document added it.
const mirroredKey = "opml_listed_as"

// mirroredFrom returns the address a mirrored document knows a feed by.
func mirroredFrom(f *store.Feed) string {
	if address, ok := get[string](readAttributes(f.Attributes), mirroredKey); ok && address != "" {
		return address
	}
	return f.URL
}

// OPMLAddress returns the address of the OPML document a category mirrors,
// empty when it mirrors none.
func OPMLAddress(c *store.Category) string {
	var attrs struct {
		Address string `json:"opml_url"`
	}
	if c.Kind != KindDynamicOPML || json.Unmarshal(c.Attributes, &attrs) != nil {
		return ""
	}
	return attrs.Address
}
