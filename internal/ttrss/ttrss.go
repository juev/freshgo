// Package ttrss reads an account from a Tiny Tiny RSS server through its JSON
// API (classes/API.php of tt-rss): the categories, the feeds, and the
// articles with their read and starred states and their labels.
package ttrss

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

// ErrLogin is returned when the server does not take the login and password.
var ErrLogin = errors.New("ttrss: the server does not accept the login and password")

// ErrAPIDisabled is returned when the user has not turned the API on
// (Preferences, Enable API) or the installation has it off.
var ErrAPIDisabled = errors.New("ttrss: the API is not enabled for the user")

// ErrAddress is returned for an address that is not that of a server.
var ErrAddress = errors.New("ttrss: the address of the server is not an http(s) address")

// Fetcher makes the requests to the server. The one the caller passes
// decides which addresses may be reached.
type Fetcher interface {
	Fetch(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// pageSize is how many headlines one request asks for: the most the API
// gives, which is also what it gives when asked for more.
const pageSize = 200

// Read returns the account of the user on the server at address, the
// installation, not its API: https://example.org/tt-rss. Nothing is kept on
// the way: the result is the whole account or an error. The session is closed
// at the end whatever came of the read.
func Read(ctx context.Context, f Fetcher, address, user, password string) (*transfer.Remote, error) {
	base, err := baseURL(address)
	if err != nil {
		return nil, err
	}
	c := &client{fetcher: f, endpoint: base + "/api/"}

	var login struct {
		SessionID string `json:"session_id"`
	}
	if err := c.call(ctx, "login", map[string]any{"user": user, "password": password}, &login); err != nil {
		return nil, err
	}
	if login.SessionID == "" {
		return nil, errors.New("ttrss: login: the answer has no session")
	}
	c.sid = login.SessionID
	defer c.logout(ctx)

	// Every category, nested ones included, and the top level alone: the API
	// does not say which category sits inside which, but what the second
	// list lacks is nested.
	all, err := c.categories(ctx, false)
	if err != nil {
		return nil, err
	}
	top, err := c.categories(ctx, true)
	if err != nil {
		return nil, err
	}
	remote := &transfer.Remote{}
	titles := map[int64]string{}
	var nested []string
	for _, cat := range all {
		titles[cat.ID] = cat.Title
		remote.Categories = append(remote.Categories, cat.Title)
		if !containsCategory(top, cat.ID) {
			nested = append(nested, cat.Title)
		}
	}
	if len(nested) > 0 {
		remote.Warnings = append(remote.Warnings, fmt.Sprintf("%d categories were inside others in Tiny Tiny RSS and are made top-level: %s", len(nested), transfer.Names(nested)))
	}

	// -3 is every real feed, wherever it is; -4 would add the virtual ones.
	var feeds []feed
	if err := c.call(ctx, "getFeeds", map[string]any{"cat_id": -3}, &feeds); err != nil {
		return nil, err
	}
	index := map[string]int{}
	var interval []string
	for i := range feeds {
		f := &feeds[i]
		if f.IsCat || f.URL == "" {
			continue
		}
		index[strconv.FormatInt(f.ID, 10)] = len(remote.Feeds)
		remote.Feeds = append(remote.Feeds, transfer.RemoteFeed{
			Key: strconv.FormatInt(f.ID, 10), URL: f.URL, Title: f.Title, Category: titles[f.CategoryID],
			// A negative interval is the choice "Disable updates".
			Muted: f.Interval < 0,
		})
		if f.Interval > 0 {
			interval = append(interval, f.Title)
		}
	}
	if len(interval) > 0 {
		remote.Warnings = append(remote.Warnings, fmt.Sprintf("refresh period not carried over, set on %d feeds: %s", len(interval), transfer.Names(interval)))
	}

	var published, noted, tagged int
	seen := map[int64]bool{}
	for skip := 0; ; {
		var page []headline
		err := c.call(ctx, "getHeadlines", map[string]any{
			"feed_id": -4, "is_cat": false, "view_mode": "all_articles", "show_content": true,
			// The oldest first: articles that arrive while the account is read
			// land after the pages, so skipping does not shift what is left.
			"order_by": "date_reverse", "limit": pageSize, "skip": skip,
		}, &page)
		if err != nil {
			return nil, err
		}
		// A page is not full when the server caps it below what was asked for,
		// as older versions do: only an empty one is the end.
		if len(page) == 0 {
			break
		}
		fresh := 0
		for i := range page {
			h := &page[i]
			if seen[h.ID] {
				continue
			}
			seen[h.ID] = true
			fresh++
			if h.Published {
				published++
			}
			if strings.TrimSpace(h.Note) != "" {
				noted++
			}
			if len(h.Tags) > 0 {
				tagged++
			}
			if at, ok := index[strconv.FormatInt(int64(h.FeedID), 10)]; ok && remote.Feeds[at].Site == "" {
				remote.Feeds[at].Site = h.SiteURL
			}
			remote.Entries = append(remote.Entries, h.convert())
		}
		if fresh == 0 {
			return nil, fmt.Errorf("ttrss: the server does not page headlines (skip %d gave articles seen before)", skip)
		}
		skip += len(page)
	}
	for _, w := range []struct {
		count int
		what  string
	}{
		{published, "published articles (shared in the public feed of Tiny Tiny RSS)"},
		{noted, "notes of articles"},
		{tagged, "tags of articles (labels are carried over)"},
	} {
		if w.count > 0 {
			remote.Warnings = append(remote.Warnings, fmt.Sprintf("%s not carried over, set on %d articles", w.what, w.count))
		}
	}
	remote.Warnings = append(remote.Warnings, "the filters and the logins of feeds in Tiny Tiny RSS are not given by its API, so none of them are carried over")
	return remote, nil
}

// baseURL is the address of the installation without a trailing slash.
func baseURL(address string) (string, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", ErrAddress
	}
	return address, nil
}

type client struct {
	fetcher  Fetcher
	endpoint string
	sid      string
	// read is how many bytes the answers so far came to.
	read int
}

// maxBytes is transfer.MaxRemoteBytes, smaller in tests.
var maxBytes = transfer.MaxRemoteBytes

// call makes the call op with the arguments and reads its content into into.
// The API answers 200 also to a refusal: the status of the answer is in the
// body, with the reason in content.error.
func (c *client) call(ctx context.Context, op string, args map[string]any, into any) error {
	body := map[string]any{"op": op}
	for k, v := range args {
		body[k] = v
	}
	if c.sid != "" {
		body["sid"] = c.sid
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.fetcher.Fetch(ctx, fetch.Request{
		URL: c.endpoint, Accept: fetch.AcceptJSON,
		Params: fetch.Params{
			Post: true, PostBody: string(payload),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	})
	if err != nil {
		return fmt.Errorf("ttrss: %s: %w", op, err)
	}
	if c.read += len(resp.Body); c.read > maxBytes {
		return fmt.Errorf("ttrss: %w", transfer.ErrRemoteTooLarge)
	}
	var answer struct {
		Status  int             `json:"status"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(resp.Body, &answer); err != nil {
		return fmt.Errorf("ttrss: %s: the answer is not what the API gives: %w", op, err)
	}
	if answer.Status != 0 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(answer.Content, &failure)
		switch failure.Error {
		case "LOGIN_ERROR":
			return ErrLogin
		case "API_DISABLED":
			return ErrAPIDisabled
		}
		return fmt.Errorf("ttrss: %s: the server refused with %q", op, failure.Error)
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(answer.Content, into); err != nil {
		return fmt.Errorf("ttrss: %s: the answer is not what the API gives: %w", op, err)
	}
	return nil
}

// logout closes the session. It is not worth failing a read that is done
// for, and it is made also when the read was cancelled, so the session does
// not stay open on the server.
func (c *client) logout(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = c.call(ctx, "logout", nil, nil)
}

type category struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

func containsCategory(list []category, id int64) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}

// categories returns the categories the user made, also the empty ones. The
// virtual ones the API lists (Special, Labels, Uncategorized) have an id
// of zero or below.
func (c *client) categories(ctx context.Context, topOnly bool) ([]category, error) {
	var list []category
	err := c.call(ctx, "getCategories", map[string]any{"enable_nested": topOnly, "include_empty": true}, &list)
	if err != nil {
		return nil, err
	}
	kept := list[:0]
	for _, cat := range list {
		if cat.ID > 0 {
			kept = append(kept, cat)
		}
	}
	return kept, nil
}

type feed struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"feed_url"`
	CategoryID int64  `json:"cat_id"`
	// Interval is the refresh period in minutes: zero is the default of the
	// user, a negative number disables the updates.
	Interval int  `json:"update_interval"`
	IsCat    bool `json:"is_cat"`
}

type headline struct {
	ID        int64    `json:"id"`
	GUID      string   `json:"guid"`
	Unread    bool     `json:"unread"`
	Marked    bool     `json:"marked"`
	Published bool     `json:"published"`
	Updated   int64    `json:"updated"`
	Title     string   `json:"title"`
	Link      string   `json:"link"`
	FeedID    id       `json:"feed_id"`
	Author    string   `json:"author"`
	Note      string   `json:"note"`
	SiteURL   string   `json:"site_url"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	// Labels are lists of the id, the caption and the two colours.
	Labels [][]any `json:"labels"`
}

// id is a number the API passes on from the database as it comes: a JSON
// number, or a string with some database drivers.
type id int64

func (n *id) UnmarshalJSON(data []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(data), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("ttrss: %s is not an id", data)
	}
	*n = id(v)
	return nil
}

// convert makes an entry of the headline. The identifier TT-RSS keeps is a
// hash of the one the feed gave, with the id of the user inside, so it only
// stands for a link the article has none of.
func (h *headline) convert() transfer.RemoteEntry {
	re := transfer.RemoteEntry{
		Feed: strconv.FormatInt(int64(h.FeedID), 10), ID: h.GUID, Title: h.Title, Author: h.Author, URL: h.Link,
		Content: h.Content, Read: !h.Unread, Starred: h.Marked, Published: h.Updated,
	}
	for _, l := range h.Labels {
		if len(l) < 2 {
			continue
		}
		if name, _ := l[1].(string); strings.TrimSpace(name) != "" {
			re.Labels = append(re.Labels, strings.TrimSpace(name))
		}
	}
	return re
}
