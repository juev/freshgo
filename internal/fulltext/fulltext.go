// Package fulltext gets the text of an article from its web page, for feeds
// that carry only a summary: the elements a CSS selector picks, or the
// article a readability extraction finds, without the elements another
// selector rules out, cleaned like feed content.
//
// With a selector the behaviour follows FreshRSS_Entry::getContentByParsing of FreshRSS at
// commit 219eaf58. See docs/specs/fulltext.md.
package fulltext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"codeberg.org/readeck/go-readability/v2"
	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/sanitize"
)

// minArticle is how many characters of text an article found without a
// selector has at the least. Of twelve pages looked at, those without an
// article gave 42 to 156 characters, the shortest article 1284.
const minArticle = 250

// maxRefreshes is how many times a page may send on to another one with
// <meta http-equiv="refresh">.
const maxRefreshes = 4

var (
	// ErrSelector is returned for a CSS selector that cannot be read.
	ErrSelector = errors.New("fulltext: invalid CSS selector")
	// ErrNoElements is returned when the selector picks nothing on the page.
	ErrNoElements = errors.New("fulltext: the selector matched no elements")
	// ErrNoArticle is returned when no article is found on the page.
	ErrNoArticle = errors.New("fulltext: no article was found on the page")
	// ErrEmptyPage is returned for a page without a body.
	ErrEmptyPage = errors.New("fulltext: the page is empty")
)

// Request says what to take from which page.
type Request struct {
	// URL is the address of the article.
	URL string
	// Params are the settings of the feed for its requests.
	Params fetch.Params
	// Selector picks the elements that make up the article.
	Selector string
	// Automatic has the article found on the page without a selector, which
	// is then not looked at.
	Automatic bool
	// Filter picks, inside them, the elements to leave out. May be empty.
	Filter string
	// ForceHTTPS, when not nil, gets every URL of the result and returns the
	// one to keep.
	ForceHTTPS func(string) string
	// Read, when not nil, gets the page in place of a request of the client:
	// a browser, for a page that only a browser is let through to. It
	// returns the address the page ends at and its markup. Params are then
	// not used.
	Read func(ctx context.Context, address string) (final, markup string, err error)
}

// selectorPadding is what FreshRSS trims off a list of selectors.
const selectorPadding = ", "

func matcher(selector string) (goquery.Matcher, error) {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %v", ErrSelector, selector, err)
	}
	return compiled, nil
}

// refreshTarget strips what precedes the address in the content of a
// refresh meta element: "5; url=".
var refreshTarget = regexp.MustCompile(`(?i)^[0-9.; ]*\s*(url\s*=)?\s*`)

// Article downloads the page and returns the HTML of the selected elements,
// or of the article found there.
func Article(ctx context.Context, client *fetch.Client, req Request) (string, error) {
	var pick, drop goquery.Matcher
	var err error
	if !req.Automatic {
		if pick, err = matcher(strings.Trim(req.Selector, selectorPadding)); err != nil {
			return "", err
		}
	}
	if filter := strings.Trim(req.Filter, selectorPadding); filter != "" {
		if drop, err = matcher(filter); err != nil {
			return "", err
		}
	}

	pageURL := req.URL
	var doc *goquery.Document
	for refreshes := 0; ; refreshes++ {
		var r io.Reader
		if req.Read != nil {
			final, markup, err := req.Read(ctx, pageURL)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(markup) == "" {
				return "", ErrEmptyPage
			}
			pageURL, r = final, strings.NewReader(markup)
		} else {
			resp, err := client.Fetch(ctx, fetch.Request{URL: pageURL, Accept: fetch.AcceptHTML, Params: req.Params})
			if err != nil {
				return "", err
			}
			if len(bytes.TrimSpace(resp.Body)) == 0 {
				return "", ErrEmptyPage
			}
			pageURL = resp.URL
			if r, err = charset.NewReader(bytes.NewReader(resp.Body), resp.Header.Get("Content-Type")); err != nil {
				return "", fmt.Errorf("fulltext: %w", err)
			}
		}
		// Without scripting, as libxml reads it: what noscript holds is markup.
		root, err := html.ParseWithOptions(r, html.ParseOptionEnableScripting(false))
		if err != nil {
			return "", fmt.Errorf("fulltext: %w", err)
		}
		doc = goquery.NewDocumentFromNode(root)
		next := ""
		// A browser has followed where the page sent it on already.
		if req.Read == nil && refreshes < maxRefreshes {
			next = refreshedTo(doc, pageURL)
		}
		if next == "" {
			break
		}
		pageURL = next
	}

	base := pageURL
	if href := strings.Join(strings.Fields(doc.Find("base[href]").First().AttrOr("href", "")), " "); href != "" {
		if resolved, ok := resolve(pageURL, href); ok {
			base = resolved
		}
	}

	var elements *goquery.Selection
	if req.Automatic {
		// The search for the article drops classes and identifiers, so
		// the filter looks at the page before it.
		if drop != nil {
			doc.FindMatcher(drop).Remove()
		}
		if elements, err = readable(doc, base); err != nil {
			return "", err
		}
	} else {
		elements = doc.FindMatcher(pick)
	}
	var picked strings.Builder
	found := 0
	elements.Each(func(_ int, s *goquery.Selection) {
		found++
		node := s.Get(0)
		if drop != nil {
			if s.IsMatcher(drop) {
				return
			}
			// Before cleaning, so that the filter sees the page as it is.
			s.FindMatcher(drop).Remove()
		}
		// An element taken out together with an earlier one.
		if node.Parent == nil {
			return
		}
		picked.WriteString(sanitize.Markup(node))
		picked.WriteByte('\n')
	})
	if found == 0 {
		return "", ErrNoElements
	}
	content := sanitize.HTML(picked.String(), base, req.ForceHTTPS)
	if drop != nil {
		// And after it, so that the filter sees the cleaned markup too.
		content, _ = strip(content, drop)
	}
	content = strings.TrimSpace(content)
	// A page that is an application has a few words on it, the name of
	// the thing and what its loading screen says; they are no article.
	if req.Automatic && len([]rune(sanitize.Text(content, minArticle+1))) < minArticle {
		return "", ErrNoArticle
	}
	return content, nil
}

// readable finds the article of a page the way the reader view of a browser
// does.
func readable(doc *goquery.Document, base string) (*goquery.Selection, error) {
	address, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("fulltext: %w", err)
	}
	article, err := readability.FromDocument(doc.Get(0), address)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoArticle, err)
	}
	if article.Node == nil {
		return nil, ErrNoArticle
	}
	return goquery.NewDocumentFromNode(article.Node).Selection, nil
}

// refreshedTo returns the address a refresh meta element of the page sends
// on to, empty when there is none or it is the page itself.
func refreshedTo(doc *goquery.Document, pageURL string) string {
	target := ""
	doc.Find("meta[content]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.ToLower(strings.TrimSpace(s.AttrOr("http-equiv", ""))) != "refresh" {
			return true
		}
		ref := refreshTarget.ReplaceAllString(strings.TrimSpace(s.AttrOr("content", "")), "")
		if resolved, ok := resolve(pageURL, ref); ok && resolved != pageURL {
			target = resolved
			return false
		}
		return true
	})
	return target
}

func resolve(base, ref string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	u, err := b.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.String(), true
}

// Strip removes from an HTML fragment the elements the filter picks. It
// returns the fragment as it was, and false, when there are none.
func Strip(content, filter string) (string, bool, error) {
	filter = strings.Trim(filter, selectorPadding)
	if filter == "" {
		return content, false, nil
	}
	drop, err := matcher(filter)
	if err != nil {
		return content, false, err
	}
	stripped, changed := strip(content, drop)
	return stripped, changed, nil
}

func strip(content string, drop goquery.Matcher) (string, bool) {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragmentWithOptions(strings.NewReader(content), body, html.ParseOptionEnableScripting(false))
	if err != nil {
		return content, false
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	unwanted := goquery.NewDocumentFromNode(body).FindMatcher(drop)
	if unwanted.Length() == 0 {
		return content, false
	}
	unwanted.Remove()
	var b strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(sanitize.Markup(c))
	}
	return b.String(), true
}
