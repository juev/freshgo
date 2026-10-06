package refresh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// Kinds of feeds read as RSS or Atom: the plain one, and the one whose
// document is taken for a feed whatever it looks like.
const (
	KindRSS       = 0
	KindRSSForced = 2
)

var (
	// ErrAlreadySubscribed is returned when the user has a feed with the address.
	ErrAlreadySubscribed = errors.New("refresh: already subscribed to the feed")
	// ErrRefused is returned when an extension turned the feed down.
	ErrRefused = errors.New("refresh: the feed was refused by an extension")
)

// AddFeed subscribes the user to the feed at f.URL and fetches its entries.
// The rest of f is what the caller wants the subscription to be; an empty
// name, site and description are taken from the feed. On success f is the
// feed as stored.
//
// A feed read as RSS or Atom has to answer with one before it is added; a
// web page given in its place is searched for the feed it announces. Feeds
// of the other kinds are added as they are and marked as failing when the
// first refresh does not work out.
func (r *Refresher) AddFeed(ctx context.Context, u *store.User, f *store.Feed) error {
	address, ok := r.hooks.CheckURLBeforeAdd.Call(ctx, strings.TrimSpace(f.URL))
	if !ok {
		return ErrRefused
	}
	address, err := checkURL(address)
	if err != nil {
		return err
	}
	f.UserID, f.URL = u.ID, address

	j, err := r.userJob(ctx, u)
	if err != nil {
		return err
	}
	var (
		resp   *fetch.Response
		params fetch.Params
	)
	switch f.Kind {
	case KindRSS, KindRSSForced:
		if params, err = fetch.FeedParams(f.HTTPAuth, f.Attributes); err != nil {
			return err
		}
		var doc *feed.Feed
		if resp, doc, err = r.fetchNew(ctx, j, f, params); err != nil {
			return err
		}
		if f.Name == "" {
			f.Name = doc.Title
		}
		if f.Website == "" {
			f.Website = doc.Link
		}
		if f.Description == "" {
			f.Description = doc.Description
		}
	case scrape.KindHTMLXPath, scrape.KindXMLXPath:
		f.Website = f.URL
	}

	feeds, err := r.db.Feeds(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, other := range feeds {
		if other.URL == f.URL {
			return fmt.Errorf("%w: %s", ErrAlreadySubscribed, withoutCredentials(f.URL))
		}
	}
	added, ok := r.hooks.FeedBeforeInsert.Call(ctx, f)
	if !ok {
		return ErrRefused
	}
	*f = *added
	if err := r.db.CreateFeed(ctx, f); err != nil {
		return err
	}

	now := r.now().Unix()
	if resp != nil {
		_, err = r.storeFetched(ctx, j, f, params, resp, now)
		r.refreshIcon(ctx, f, err)
	} else if _, err = r.refreshFeed(ctx, j, f); err != nil && ctx.Err() == nil {
		// The feed stays, marked as failing, like any feed that fails later.
		r.log.Warn("first refresh of a new feed failed", "user", u.Name, "feed", f.ID,
			"url", withoutCredentials(f.URL), "error", err)
		err = nil
	}
	if err != nil {
		return err
	}
	stored, err := r.db.FeedByID(ctx, u.ID, f.ID)
	if err != nil {
		return err
	}
	*f = *stored
	return nil
}

// RefreshFeed refreshes one feed of the user now, whatever its refresh
// period. An error means the feed is now marked as failing, unless it is the
// database that failed.
func (r *Refresher) RefreshFeed(ctx context.Context, u *store.User, feedID int64) error {
	j, err := r.userJob(ctx, u)
	if err != nil {
		return err
	}
	f, err := r.db.FeedByID(ctx, u.ID, feedID)
	if err != nil {
		return err
	}
	_, err = r.refreshFeed(ctx, j, f)
	return err
}

// userJob prepares a refresh outside a run, for one user.
func (r *Refresher) userJob(ctx context.Context, u *store.User) (*job, error) {
	https, err := r.httpsDomains(ctx)
	if err != nil {
		return nil, err
	}
	return r.newJob(ctx, u, readUserSettings(u.Settings), https)
}

// fetchNew downloads the document of a feed that is being added and reads
// it. When the address answers with a web page, the feed the page announces
// is fetched in its place and becomes the address of the feed. So does the
// address a permanent redirect leads to.
func (r *Refresher) fetchNew(ctx context.Context, j *job, f *store.Feed, params fetch.Params) (*fetch.Response, *feed.Feed, error) {
	for announced := false; ; announced = true {
		req := fetch.Request{URL: strings.TrimSuffix(f.URL, forceFeedSuffix), Accept: fetch.AcceptFeed, Params: params}
		r.hooks.FetchBefore.Call(ctx, hooks.Fetch{Feed: f, Request: &req})
		resp, err := r.client.Fetch(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		if resp.PermanentURL != "" {
			f.URL = withoutCredentials(resp.PermanentURL)
		}
		doc, err := parse(j, f, resp)
		if err == nil {
			return resp, doc, nil
		}
		// Whatever is wrong with the document, it may be a web page: few of
		// them are XML enough to be told from a broken feed.
		if announced {
			return nil, nil, err
		}
		link := announcedFeed(resp.Body, resp.URL)
		if link == "" {
			return nil, nil, err
		}
		f.URL = link
	}
}

// feedTypes are the media types a page announces a feed with.
var feedTypes = map[string]bool{
	"application/rss+xml":    true,
	"application/atom+xml":   true,
	"application/rdf+xml":    true,
	"application/x.atom+xml": true,
	"application/x-atom+xml": true,
	"text/rss+xml":           true,
	"text/atom+xml":          true,
}

// announcedFeed returns the address of the first feed an HTML page links to
// with <link rel="alternate">, or "".
func announcedFeed(page []byte, pageURL string) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok {
		if u, err := base.Parse(strings.TrimSpace(href)); err == nil {
			base = u
		}
	}
	found := ""
	doc.Find("link[href]").EachWithBreak(func(_ int, link *goquery.Selection) bool {
		rels := strings.Fields(strings.ToLower(link.AttrOr("rel", "")))
		mediaType, _, _ := strings.Cut(strings.ToLower(link.AttrOr("type", "")), ";")
		announces := false
		for _, rel := range rels {
			announces = announces || rel == "feed" || rel == "alternate" && feedTypes[strings.TrimSpace(mediaType)]
		}
		if !announces {
			return true
		}
		u, err := base.Parse(strings.TrimSpace(link.AttrOr("href", "")))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return true
		}
		found = u.String()
		return false
	})
	return found
}

// checkURL returns the address of a feed in the form it is stored: with
// https:// in front when it names no scheme. An address that is not an
// http(s) URL gives fetch.ErrBadURL.
func checkURL(address string) (string, error) {
	if address == "" {
		return "", fmt.Errorf("%w: empty address", fetch.ErrBadURL)
	}
	if lower := strings.ToLower(address); !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		address = "https://" + strings.TrimLeft(address, "/")
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %q", fetch.ErrBadURL, address)
	}
	return address, nil
}
