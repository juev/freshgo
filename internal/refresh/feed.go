package refresh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// forceFeedSuffix on a feed address tells FreshRSS to read the document as a
// feed whatever it looks like. It is not part of the address to request.
const forceFeedSuffix = "#force_feed"

// result is what the refresh of one feed stored.
type result struct {
	added, updated int
}

// update is a stored entry the feed has changed, together with the state it
// was in when the change was noticed.
type update struct {
	entry  *store.Entry
	before store.EntryState
}

// refreshFeed fetches one feed and stores what it brought. An error means
// the feed is now marked as failing, unless the context was cancelled.
func (r *Refresher) refreshFeed(ctx context.Context, j *job, f *store.Feed) (result, error) {
	now := r.now().Unix()

	params, err := fetch.FeedParams(f.HTTPAuth, f.Attributes)
	if err != nil {
		return result{}, r.fail(ctx, j, f, now, err)
	}
	req := fetch.Request{
		URL:          strings.TrimSuffix(f.URL, forceFeedSuffix),
		Accept:       accept(f.Kind),
		ETag:         f.HTTPETag,
		LastModified: f.HTTPLastModified,
		Params:       params,
	}
	r.hooks.FetchBefore.Call(ctx, hooks.Fetch{Feed: f, Request: &req})
	resp, err := r.client.Fetch(ctx, req)
	if err != nil {
		return result{}, r.fail(ctx, j, f, now, err)
	}

	if resp.NotModified {
		return result{}, r.storeUnchanged(ctx, j, f, resp, now)
	}

	doc, err := parse(j, f, resp)
	if err != nil {
		return result{}, r.fail(ctx, j, f, now, err)
	}
	attrs := readAttributes(f.Attributes)
	criteria, _ := get[string](attrs, "unicityCriteria")
	if legacy, _ := get[bool](attrs, "hasBadGuids"); legacy {
		criteria = "link"
	}
	forced, _ := get[bool](attrs, "unicityCriteriaForced")
	used, invalid := doc.AssignGUIDs(criteria, forced)
	if invalid > 0 {
		r.log.Warn("feed has entries without a usable identifier", "user", j.user.Name, "feed", f.ID,
			"url", withoutCredentials(f.URL), "entries", invalid, "criteria", used)
	}
	r.hooks.ParseAfter.Call(ctx, hooks.Parsed{Feed: f, Document: doc})

	// Entries that repeat a GUID within the document are dropped: the first
	// one, which is the last in the document, stands.
	var (
		guids   = make([]string, 0, len(doc.Items))
		entries = make([]*store.Entry, 0, len(doc.Items))
		listed  = map[string]bool{}
	)
	for _, it := range doc.Items {
		if listed[it.GUID] {
			continue
		}
		listed[it.GUID] = true
		guids = append(guids, it.GUID)
		entries = append(entries, entryFromItem(it, f, now))
	}

	// What is new and what has changed is decided, and the handlers run,
	// before the transaction: a handler may take its time.
	states, err := r.db.EntryStates(ctx, f.UserID, f.ID, guids)
	if err != nil {
		return result{}, err
	}
	markUnread, ok := get[bool](attrs, "mark_updated_article_unread")
	if !ok {
		markUnread = j.conf.markUpdatedUnread
	}
	uponReception, ok := get[bool](attrs, "read_upon_reception")
	if !ok {
		uponReception = j.conf.readUponReception
	}
	// The titles and identifiers that make a new entry read are looked up
	// only when there is a new entry to compare.
	var known repeats
	if slices.ContainsFunc(guids, func(guid string) bool { _, exists := states[guid]; return !exists }) {
		var lock *sync.Mutex
		if known, lock, err = r.loadRepeats(ctx, j, f, attrs); err != nil {
			return result{}, err
		}
		if lock != nil {
			defer lock.Unlock()
		}
	}
	var (
		added   []*store.Entry
		updates []update
		// hashes are for the entries that have none yet, by entry id.
		hashes = map[int64][]byte{}
	)
	for _, e := range entries {
		st, exists := states[e.GUID]
		switch {
		case !exists:
			if e, ok = r.hooks.EntryBeforeInsert.Call(ctx, e); !ok {
				continue
			}
			r.autoRead(ctx, e, uponReception, known)
			known.add(e)
			if e, ok = r.hooks.EntryBeforeAdd.Call(ctx, e); !ok {
				continue
			}
			added = append(added, e)
		case st.Hash == nil:
			// Imported: there is nothing to compare with, so the entry is
			// taken as unchanged and gets its hash.
			hashes[st.ID] = e.Hash
		case !bytes.Equal(st.Hash, e.Hash):
			e.ID, e.IsRead, e.IsFavorite, e.LastUserModified = st.ID, st.IsRead, st.IsFavorite, st.LastUserModified
			e.LastModified = now
			if markUnread {
				e.IsRead = false
				r.hooks.EntryAutoUnread.Call(ctx, hooks.EntryAuto{Entry: e, Why: hooks.WhyUpdatedArticle})
			}
			if e, ok = r.hooks.EntryBeforeInsert.Call(ctx, e); !ok {
				continue
			}
			// A changed entry is not compared by title: the repeat may be
			// the entry itself.
			r.autoRead(ctx, e, uponReception, repeats{})
			known.add(e)
			if e, ok = r.hooks.EntryBeforeUpdate.Call(ctx, e); !ok {
				continue
			}
			updates = append(updates, update{entry: e, before: st})
		}
	}
	// New entries get identifiers in the order of their dates; entries of
	// one date keep the order of the feed.
	sort.SliceStable(added, func(a, b int) bool { return added[a].Published < added[b].Published })

	var res result
	err = r.db.InTx(ctx, func(tx *store.Store) error {
		res = result{}
		fresh, err := tx.LockFeed(ctx, f.UserID, f.ID)
		if errors.Is(err, store.ErrNotFound) {
			// Unsubscribed while the feed was being fetched.
			return nil
		}
		if err != nil {
			return err
		}
		// Read again under the lock: another refresh of this feed, or the
		// user, may have been here since the first look.
		states, err := tx.EntryStates(ctx, f.UserID, f.ID, guids)
		if err != nil {
			return err
		}
		insert := make([]*store.Entry, 0, len(added))
		for _, e := range added {
			if _, exists := states[e.GUID]; !exists {
				insert = append(insert, e)
			}
		}
		if err := tx.InsertEntries(ctx, f.UserID, insert); err != nil {
			return err
		}
		res.added = len(insert)
		for _, u := range updates {
			st, exists := states[u.entry.GUID]
			if !exists {
				continue
			}
			// What the user changed meanwhile stands, unless this refresh
			// decided the state itself.
			e := *u.entry
			if e.IsRead == u.before.IsRead {
				e.IsRead = st.IsRead
			}
			if e.IsFavorite == u.before.IsFavorite {
				e.IsFavorite = st.IsFavorite
			}
			if e.LastUserModified == u.before.LastUserModified {
				e.LastUserModified = st.LastUserModified
			}
			if err := tx.UpdateEntry(ctx, &e); err != nil {
				return err
			}
			res.updated++
		}
		for id, hash := range hashes {
			if err := tx.SetEntryHash(ctx, f.UserID, id, hash); err != nil {
				return err
			}
		}
		if err := tx.MarkEntriesSeen(ctx, f.UserID, f.ID, guids, now); err != nil {
			return err
		}
		if err := r.tidy(ctx, tx, j, fresh, now, res.added+res.updated > 0); err != nil {
			return err
		}

		succeeded(fresh, f.URL, resp, now)
		if used != criteria {
			freshAttrs := readAttributes(fresh.Attributes)
			delete(freshAttrs, "hasBadGuids")
			freshAttrs.set("unicityCriteria", used)
			fresh.Attributes = freshAttrs.raw()
			r.log.Warn("entry identifiers of the feed are not unique, switched to a weaker key", "user", j.user.Name,
				"feed", f.ID, "url", withoutCredentials(f.URL), "from", criteria, "to", used)
		}
		if fresh.Name == "" {
			fresh.Name = doc.Title
		}
		if (fresh.Website == "" || fresh.Website == f.URL) && doc.Link != fresh.Website {
			fresh.Website = doc.Link
		}
		if strings.TrimSpace(fresh.Description) == "" && doc.Description != "" {
			fresh.Description = doc.Description
		}
		return tx.UpdateFeed(ctx, fresh)
	})
	return res, err
}

// storeUnchanged records a refresh that found the feed as it was: the
// entries listed last time are still listed.
func (r *Refresher) storeUnchanged(ctx context.Context, j *job, f *store.Feed, resp *fetch.Response, now int64) error {
	return r.db.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.LockFeed(ctx, f.UserID, f.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.MarkEntriesSeenSince(ctx, f.UserID, f.ID, fresh.LastUpdate, now); err != nil {
			return err
		}
		if err := r.tidy(ctx, tx, j, fresh, now, false); err != nil {
			return err
		}
		succeeded(fresh, f.URL, resp, now)
		return tx.UpdateFeed(ctx, fresh)
	})
}

// succeeded writes into the feed what every successful fetch changes. feedURL
// is the address the feed had when it was fetched.
func succeeded(fresh *store.Feed, feedURL string, resp *fetch.Response, now int64) {
	fresh.LastUpdate = now
	fresh.Error = 0
	// A 304 need not repeat the validators.
	if !resp.NotModified || resp.Header.Get("ETag") != "" {
		fresh.HTTPETag = resp.Header.Get("ETag")
	}
	if !resp.NotModified || resp.Header.Get("Last-Modified") != "" {
		fresh.HTTPLastModified = resp.Header.Get("Last-Modified")
	}
	// The address follows a permanent redirect, unless the user has changed
	// it meanwhile.
	if resp.PermanentURL != "" && fresh.URL == feedURL {
		fresh.URL = withoutCredentials(resp.PermanentURL)
	}
}

// fail marks the feed as failing and returns the cause. A feed that is gone
// for good (HTTP 410) is muted as well.
func (r *Refresher) fail(ctx context.Context, j *job, f *store.Feed, now int64, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.hooks.ParseAfter.Call(ctx, hooks.Parsed{Feed: f, Error: cause.Error()})
	var status *fetch.StatusError
	gone := errors.As(cause, &status) && status.Code == 410
	err := r.db.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.LockFeed(ctx, f.UserID, f.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		fresh.Error = now
		if gone && fresh.TTL >= 0 {
			if fresh.TTL == 0 {
				fresh.TTL = j.conf.ttlDefault
			}
			fresh.TTL = -fresh.TTL
		}
		return tx.UpdateFeed(ctx, fresh)
	})
	if gone {
		cause = fmt.Errorf("%w; the feed is muted", cause)
	}
	return errors.Join(cause, err)
}

// accept is the Accept header for the kind of document behind a feed.
func accept(kind int) string {
	switch kind {
	case scrape.KindHTMLXPath, scrape.KindHTMLXPathJSON:
		return fetch.AcceptHTML
	case scrape.KindXMLXPath:
		return fetch.AcceptXML
	case scrape.KindJSONFeed, scrape.KindJSONDotNotation:
		return fetch.AcceptJSON
	default:
		return fetch.AcceptFeed
	}
}

// parse reads the fetched document as a feed. Kinds without a reader of
// their own are read as RSS or Atom, as FreshRSS does.
func parse(j *job, f *store.Feed, resp *fetch.Response) (*feed.Feed, error) {
	contentType := resp.Header.Get("Content-Type")
	switch f.Kind {
	case scrape.KindHTMLXPath, scrape.KindXMLXPath, scrape.KindJSONFeed, scrape.KindJSONDotNotation, scrape.KindHTMLXPathJSON:
		rss, err := scrape.RSS(resp.Body, scrape.Source{
			Kind: f.Kind, URL: resp.URL, Name: f.Name, Attributes: f.Attributes,
			ContentType: contentType, Location: j.conf.location,
		})
		if err != nil {
			return nil, err
		}
		return feed.Parse(rss, feed.Options{HTTPS: j.https, Location: j.conf.location})
	default:
		return feed.Parse(resp.Body, feed.Options{
			ContentType: contentType, URL: resp.URL, HTTPS: j.https, Location: j.conf.location,
		})
	}
}

// entryFromItem turns a feed item into the entry to store at the time now.
func entryFromItem(it *feed.Item, f *store.Feed, now int64) *store.Entry {
	e := &store.Entry{
		UserID: f.UserID, FeedID: f.ID, GUID: it.GUID, Title: it.Title, Authors: it.Authors, Content: it.Content,
		Link: it.Link, Published: it.Published, LastSeen: now, Tags: it.Tags, Attributes: it.Attributes(),
	}
	e.Hash = entryHash(e)
	// An item without a date is dated by the refresh that found it.
	if e.Published <= 1 {
		e.Published = now
	}
	return e
}

// entryHash covers what a feed can change in an entry. The date stays out:
// for an item without one it is the time of the refresh.
func entryHash(e *store.Entry) []byte {
	data, err := json.Marshal(struct {
		Link, Title string
		Authors     []string
		Content     string
		Tags        []string
		Attributes  json.RawMessage
	}{e.Link, e.Title, e.Authors, e.Content, e.Tags, e.Attributes})
	if err != nil {
		// Attributes come from json.Marshal; nothing else can fail.
		panic(err)
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

// withoutCredentials returns the URL without its user and password.
func withoutCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
