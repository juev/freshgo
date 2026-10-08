package refresh

import (
	"context"
	"errors"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/fulltext"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// Markers FreshRSS puts around the text taken from the page of an article.
const (
	fullContentStart = "<!-- FULLCONTENT start //-->"
	fullContentEnd   = "<!-- FULLCONTENT end //-->"
)

// completion is how a feed completes the text of its entries.
type completion struct {
	// selector picks the article on its page.
	selector string
	// automatic has the article found on its page without a selector.
	automatic bool
	// filter picks what to leave out, of the page or else of the feed's text.
	filter string
	// conditions limit the reading of pages to the entries one of them matches.
	conditions []*search.Query
	// action says where the text of the page goes: "replace", "prepend" or "append".
	action string
	params fetch.Params
}

// pages reports that the pages of entries are read.
func (c *completion) pages() bool {
	return c.automatic || c.selector != ""
}

// off reports that the feed's text is stored as it comes.
func (c *completion) off() bool {
	return !c.pages() && c.filter == ""
}

func (r *Refresher) completion(j *job, f *store.Feed, attrs attributes, params fetch.Params) *completion {
	c := &completion{selector: strings.TrimSpace(f.PathEntries), params: params}
	c.filter, _ = get[string](attrs, "path_entries_filter")
	c.filter = strings.TrimSpace(c.filter)
	c.action, _ = get[string](attrs, "content_action")
	c.automatic, _ = get[bool](attrs, "path_entries_auto")
	if !c.pages() {
		return c
	}
	conditions, _ := get[[]string](attrs, "path_entries_conditions")
	for _, condition := range conditions {
		if strings.TrimSpace(condition) == "" {
			continue
		}
		query, err := search.Parse(condition, j.search)
		if err != nil {
			r.log.Warn("condition for reading article pages is not usable and matches nothing", "user", j.user.Name,
				"feed", f.ID, "condition", condition, "error", err)
			// A condition that cannot be met still counts as a condition.
			query = nil
		}
		c.conditions = append(c.conditions, query)
	}
	return c
}

// complete replaces or extends the text of a new or changed entry with the
// article from its page, or cuts the unwanted elements out of the feed's
// text. Whatever fails leaves the text the feed gave.
func (r *Refresher) complete(ctx context.Context, j *job, f *store.Feed, c *completion, e *store.Entry, now int64) {
	if !c.pages() {
		stripped, changed, err := fulltext.Strip(e.Content, c.filter)
		if err != nil {
			r.log.Warn("content filter is not usable", "user", j.user.Name, "feed", f.ID, "error", err)
			return
		}
		if changed {
			setOriginalContent(e, e.Content)
			e.Content = stripped
		}
		return
	}
	if e.Link == "" {
		return
	}
	if len(c.conditions) > 0 {
		subject := searchable(e, f, now)
		met := false
		for _, condition := range c.conditions {
			if condition != nil && condition.Match(subject) {
				met = true
				break
			}
		}
		if !met {
			return
		}
	}
	article, err := fulltext.Article(ctx, r.client, fulltext.Request{
		URL: e.Link, Params: c.params, Selector: c.selector, Automatic: c.automatic, Filter: c.filter, ForceHTTPS: j.https.URL,
	})
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			r.log.Warn("article page was not read, the text of the feed is kept", "user", j.user.Name, "feed", f.ID,
				"url", withoutCredentials(e.Link), "error", err)
		}
		return
	}
	if article == "" {
		return
	}
	article = fullContentStart + article + fullContentEnd
	switch c.action {
	case "prepend":
		e.Content = article + e.Content
	case "append":
		e.Content += article
	default:
		setOriginalContent(e, e.Content)
		e.Content = article
	}
}

// setOriginalContent keeps the text the feed gave in the attributes of the entry.
func setOriginalContent(e *store.Entry, content string) {
	attrs := readAttributes(e.Attributes)
	attrs.set("original_content", content)
	e.Attributes = attrs.raw()
}
