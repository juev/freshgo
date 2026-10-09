// Package browser has a page read by a browser that runs already, for the
// pages a plain request does not get: those behind a check that wants
// JavaScript to run. The browser is one that speaks the DevTools protocol
// over a websocket, such as Lightpanda or the headless shell of Chrome.
//
// See docs/specs/fulltext.md.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

var (
	// ErrChallenge is returned for a page that stays behind a check of
	// its site: the browser was not let through in the time it had.
	ErrChallenge = errors.New("browser: the page stays behind a check of the site")
	// ErrTooLarge is returned for a page larger than a page may be.
	ErrTooLarge = errors.New("browser: the page is too large")
)

const (
	// pageTimeout is how long one page may take, the check of its site
	// included.
	pageTimeout = 30 * time.Second
	// pollInterval is how often a page is asked how far it is.
	pollInterval = 500 * time.Millisecond
	// maxPage is how many characters the markup of a page may have.
	maxPage = 16 << 20
)

// state has a page say how far it is. A page of the check of Cloudflare
// defines _cf_chl_opt; the page it lets through does not.
const state = `JSON.stringify({
	ready: document.readyState === 'complete',
	check: '_cf_chl_opt' in window,
	url: location.href,
	size: document.documentElement ? document.documentElement.outerHTML.length : 0,
})`

// Browser reads pages with a browser that runs elsewhere.
type Browser struct {
	address *url.URL
	// turn keeps the browser to one page at a time.
	turn chan struct{}
	http *http.Client
	// timeout is how long one page may take, limit how many characters
	// its markup may have.
	timeout time.Duration
	limit   int
}

// New returns a Browser for the one at address, the websocket of its
// DevTools protocol: ws://host:port, or the whole address of the socket
// for a service that gives one out.
func New(address string) (*Browser, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, fmt.Errorf("browser URL %q: want a ws or wss address", address)
	}
	return &Browser{
		address: u,
		turn:    make(chan struct{}, 1),
		// The browser is a neighbour of the server: no proxy leads to it.
		http:    &http.Client{Transport: &http.Transport{Proxy: nil}},
		timeout: pageTimeout,
		limit:   maxPage,
	}, nil
}

// Page opens address in a tab of its own, which knows nothing of the pages
// read before, and returns the markup the page ends with and the address it
// ends at. The browser goes its own way to the
// network: nothing of the request settings of freshgo applies to it.
func (b *Browser) Page(ctx context.Context, address string) (final, markup string, err error) {
	select {
	case b.turn <- struct{}{}:
		defer func() { <-b.turn }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	socket, err := b.socket(ctx)
	if err != nil {
		return "", "", err
	}
	browser, disconnect := remote.NewAllocator(ctx, socket, remote.NoModifyURL)
	defer disconnect()
	// A tab with nothing of the pages before it, no cookie among them: a
	// Chrome that a check had let through once was not let through again
	// with what the first visit had left it.
	tab, closeTab := chromedp.NewContext(browser, chromedp.WithNewBrowserContext())
	defer closeTab()
	if err := chromedp.Do(tab, chromedp.Navigate(address)); err != nil {
		return "", "", fmt.Errorf("browser: %w", err)
	}
	var page struct {
		Ready, Check bool
		URL          string
		Size         int
	}
	for {
		// A page that moves on meanwhile, as the check does when it is
		// passed, fails the question; the next one is put to the new page.
		if answer, err := chromedp.Run(tab, chromedp.Evaluate[string](state)); err == nil && json.Unmarshal([]byte(answer), &page) == nil && page.Ready && !page.Check {
			if page.Size > b.limit {
				return "", "", ErrTooLarge
			}
			if markup, err := chromedp.Run(tab, chromedp.Evaluate[string](`document.documentElement.outerHTML`)); err == nil {
				return page.URL, markup, nil
			}
		}
		select {
		case <-tab.Done():
			if page.Check {
				return "", "", ErrChallenge
			}
			return "", "", fmt.Errorf("browser: the page was not read: %w", context.Cause(tab))
		case <-time.After(pollInterval):
		}
	}
}

// socket returns the address of the websocket to talk to the browser at.
// An address without a path is that of the browser, which is asked for its
// socket: Chrome has a new one every time it starts.
func (b *Browser) socket(ctx context.Context) (string, error) {
	if (b.address.Path != "" && b.address.Path != "/") || b.address.RawQuery != "" {
		return b.address.String(), nil
	}
	// Chrome answers a request only when it names it by address, not by
	// name; and a name may have addresses the browser does not listen at.
	addresses, err := net.DefaultResolver.LookupHost(ctx, b.address.Hostname())
	if err != nil {
		return "", fmt.Errorf("browser: %w", err)
	}
	for _, host := range addresses {
		u := *b.address
		switch {
		case u.Port() != "":
			u.Host = net.JoinHostPort(host, u.Port())
		case strings.Contains(host, ":"):
			u.Host = "[" + host + "]"
		default:
			u.Host = host
		}
		var path *url.URL
		if path, err = b.version(ctx, u); err == nil {
			u.Path, u.RawQuery = path.Path, path.RawQuery
			return u.String(), nil
		}
	}
	return "", err
}

// version asks the browser at u for its socket. The browser names it by
// the address it knows itself by, which is not always the one it is reached
// at: only the path is its to say.
func (b *Browser) version(ctx context.Context, u url.URL) (*url.URL, error) {
	u.Scheme = map[string]string{"ws": "http", "wss": "https"}[u.Scheme]
	u.Path = "/json/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browser: %s answers %s", u.String(), resp.Status)
	}
	var said struct {
		Socket string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&said); err != nil {
		return nil, fmt.Errorf("browser: %s: %w", u.String(), err)
	}
	socket, err := url.Parse(said.Socket)
	if err != nil {
		return nil, fmt.Errorf("browser: %s: %w", u.String(), err)
	}
	return socket, nil
}
