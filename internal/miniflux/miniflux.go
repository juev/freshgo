// Package miniflux reads an account from a Miniflux server through its REST
// API (https://miniflux.app/docs/api.html): the categories, the feeds with
// the settings freshgo has an equivalent for, and the entries with their read
// and starred states and their tags.
package miniflux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/transfer"
)

// ErrToken is returned when the server does not take the API token.
var ErrToken = errors.New("miniflux: the server does not accept the API token")

// ErrAddress is returned for an address that is not that of a server.
var ErrAddress = errors.New("miniflux: the address of the server is not an http(s) address")

// Fetcher makes the requests to the server. The one the caller passes
// decides which addresses may be reached.
type Fetcher interface {
	Fetch(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// pageSize is how many entries one request asks for.
const pageSize = 100

// Read returns the account the token belongs to on the server at address.
// Nothing is kept on the way: the result is the whole account or an error.
func Read(ctx context.Context, f Fetcher, address, token string) (*transfer.Remote, error) {
	base, err := baseURL(address)
	if err != nil {
		return nil, err
	}
	c := &client{fetcher: f, base: base, token: token}

	// The account first: a wrong token or address fails before the long part.
	if err := c.get(ctx, "/v1/me", nil, &struct{}{}); err != nil {
		return nil, err
	}
	var categories []category
	if err := c.get(ctx, "/v1/categories", nil, &categories); err != nil {
		return nil, err
	}
	var feeds []feed
	if err := c.get(ctx, "/v1/feeds", nil, &feeds); err != nil {
		return nil, err
	}
	remote := &transfer.Remote{}
	for _, cat := range categories {
		remote.Categories = append(remote.Categories, cat.Title)
	}
	convertFeeds(remote, feeds)

	last := int64(0)
	for {
		query := url.Values{
			"status": {"read", "unread"}, "order": {"id"}, "direction": {"asc"},
			"limit": {strconv.Itoa(pageSize)}, "after_entry_id": {strconv.FormatInt(last, 10)},
		}
		var page struct {
			Entries []entry `json:"entries"`
		}
		if err := c.get(ctx, "/v1/entries", query, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			if e.ID <= last {
				return nil, fmt.Errorf("miniflux: the server does not page entries in order (entry %d after %d)", e.ID, last)
			}
			last = e.ID
			remote.Entries = append(remote.Entries, e.convert())
		}
		if len(page.Entries) < pageSize {
			return remote, nil
		}
	}
}

// baseURL is the address of the server without a trailing slash.
func baseURL(address string) (string, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", ErrAddress
	}
	return address, nil
}

type client struct {
	fetcher Fetcher
	base    string
	token   string
	// read is how many bytes the answers so far came to.
	read int
}

// maxBytes is transfer.MaxRemoteBytes, smaller in tests.
var maxBytes = transfer.MaxRemoteBytes

func (c *client) get(ctx context.Context, path string, query url.Values, into any) error {
	address := c.base + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	resp, err := c.fetcher.Fetch(ctx, fetch.Request{
		URL: address, Accept: fetch.AcceptJSON,
		Params: fetch.Params{Header: http.Header{"X-Auth-Token": {c.token}}},
	})
	var status *fetch.StatusError
	switch {
	case errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden):
		return ErrToken
	case err != nil:
		return fmt.Errorf("miniflux: %s: %w", path, err)
	}
	if c.read += len(resp.Body); c.read > maxBytes {
		return fmt.Errorf("miniflux: %w", transfer.ErrRemoteTooLarge)
	}
	if err := json.Unmarshal(resp.Body, into); err != nil {
		return fmt.Errorf("miniflux: %s: the answer is not what the API gives: %w", path, err)
	}
	return nil
}

type category struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// HideGlobally keeps the entries of the category out of the global lists.
	HideGlobally bool `json:"hide_globally"`
}

type feed struct {
	ID           int64    `json:"id"`
	FeedURL      string   `json:"feed_url"`
	SiteURL      string   `json:"site_url"`
	Title        string   `json:"title"`
	Category     category `json:"category"`
	Username     string   `json:"username"`
	Password     string   `json:"password"`
	UserAgent    string   `json:"user_agent"`
	Cookie       string   `json:"cookie"`
	Crawler      bool     `json:"crawler"`
	ScraperRules string   `json:"scraper_rules"`
	Disabled     bool     `json:"disabled"`
	HideGlobally bool     `json:"hide_globally"`

	RewriteRules    string `json:"rewrite_rules"`
	URLRewriteRules string `json:"urlrewrite_rules"`
	BlocklistRules  string `json:"blocklist_rules"`
	KeeplistRules   string `json:"keeplist_rules"`
	FetchViaProxy   bool   `json:"fetch_via_proxy"`
	SelfSigned      bool   `json:"allow_self_signed_certificates"`
}

// convertFeeds adds the feeds to the account. What has no equivalent in
// freshgo is told in the warnings, one line a setting with the feeds it is
// set on.
func convertFeeds(remote *transfer.Remote, feeds []feed) {
	unsupported := []struct {
		what string
		has  func(*feed) bool
	}{
		{"rewrite rules", func(f *feed) bool { return strings.TrimSpace(f.RewriteRules) != "" }},
		{"URL rewrite rules", func(f *feed) bool { return strings.TrimSpace(f.URLRewriteRules) != "" }},
		{"block rules", func(f *feed) bool { return strings.TrimSpace(f.BlocklistRules) != "" }},
		{"keep rules", func(f *feed) bool { return strings.TrimSpace(f.KeeplistRules) != "" }},
		{"fetching through the proxy of Miniflux", func(f *feed) bool { return f.FetchViaProxy }},
		{"accepting self-signed certificates", func(f *feed) bool { return f.SelfSigned }},
	}
	named := make([][]string, len(unsupported))
	for i := range feeds {
		f := &feeds[i]
		rf := transfer.RemoteFeed{
			Key: strconv.FormatInt(f.ID, 10), URL: f.FeedURL, Site: f.SiteURL, Title: f.Title, Category: f.Category.Title,
			Muted: f.Disabled, CategoryOnly: f.HideGlobally || f.Category.HideGlobally,
			UserAgent: f.UserAgent, Cookie: f.Cookie,
		}
		if f.Username != "" || f.Password != "" {
			rf.HTTPAuth = f.Username + ":" + f.Password
		}
		// Miniflux applies its scraper rules only when the crawler is on.
		if f.Crawler {
			if selector := strings.TrimSpace(f.ScraperRules); selector != "" {
				rf.Selector = selector
			} else {
				rf.Automatic = true
			}
		}
		remote.Feeds = append(remote.Feeds, rf)
		for j, u := range unsupported {
			if u.has(f) {
				named[j] = append(named[j], f.Title)
			}
		}
	}
	for j, u := range unsupported {
		if len(named[j]) > 0 {
			remote.Warnings = append(remote.Warnings, fmt.Sprintf("%s not carried over, set on %d feeds: %s", u.what, len(named[j]), transfer.Names(named[j])))
		}
	}
}

type entry struct {
	ID          int64    `json:"id"`
	FeedID      int64    `json:"feed_id"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Author      string   `json:"author"`
	Content     string   `json:"content"`
	Hash        string   `json:"hash"`
	PublishedAt string   `json:"published_at"`
	Status      string   `json:"status"`
	Starred     bool     `json:"starred"`
	Tags        []string `json:"tags"`
}

func (e *entry) convert() transfer.RemoteEntry {
	re := transfer.RemoteEntry{
		Feed: strconv.FormatInt(e.FeedID, 10), ID: e.Hash, Title: e.Title, Author: e.Author, URL: e.URL,
		Content: e.Content, Read: e.Status == "read", Starred: e.Starred,
	}
	if t, err := time.Parse(time.RFC3339, e.PublishedAt); err == nil {
		re.Published = t.Unix()
	}
	for _, tag := range e.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			re.Labels = append(re.Labels, tag)
		}
	}
	return re
}
