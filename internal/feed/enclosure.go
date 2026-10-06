package feed

import (
	"strconv"
	"strings"
)

// mediaDefaults are the values an enclosure takes from the item, or from
// the feed when the item has none.
type mediaDefaults struct {
	title, description string
	credits            []string
	thumbnails         []string
}

func (r *itemReader) mediaText(n *node, name string) (string, bool) {
	if c := n.first(nsMedia, name); c != nil {
		return r.text(c), true
	}
	return "", false
}

func (r *itemReader) mediaCredits(n *node) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range n.all(nsMedia, "credit") {
		if name := r.text(c); !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func (r *itemReader) mediaThumbnails(n *node) []string {
	var out []string
	for _, t := range n.all(nsMedia, "thumbnail") {
		if url, ok := t.attr("url"); ok {
			out = append(out, r.sanitize(url, asIRI, r.ownBase(t)))
		}
	}
	return out
}

func (r *itemReader) mediaDefaults() mediaDefaults {
	if r.channelMedia == nil {
		c := &mediaDefaults{}
		c.title, _ = r.mediaText(r.channel, "title")
		c.description, _ = r.mediaText(r.channel, "description")
		c.credits = r.mediaCredits(r.channel)
		c.thumbnails = r.mediaThumbnails(r.channel)
		r.channelMedia = c
	}
	d := *r.channelMedia
	if v, ok := r.mediaText(r.n, "title"); ok {
		d.title = v
	}
	if v, ok := r.mediaText(r.n, "description"); ok {
		d.description = v
	}
	if len(r.n.all(nsMedia, "credit")) > 0 {
		d.credits = r.mediaCredits(r.n)
	}
	if len(r.n.all(nsMedia, "thumbnail")) > 0 {
		d.thumbnails = r.mediaThumbnails(r.n)
	}
	return d
}

// number reads an attribute PHP would turn into an integer with intval.
func number(n *node, name string) *int {
	v, ok := n.attr(name)
	if !ok {
		return nil
	}
	v = strings.TrimSpace(v)
	end := 0
	for end < len(v) && (v[end] >= '0' && v[end] <= '9' || end == 0 && (v[end] == '-' || v[end] == '+')) {
		end++
	}
	i, _ := strconv.Atoi(v[:end])
	return &i
}

func (r *itemReader) attrText(n *node, name string) string {
	if v, ok := n.attr(name); ok {
		return r.sanitize(v, asText, "")
	}
	return ""
}

// mediaContent reads a media:content element; group is its media:group or nil.
func (r *itemReader) mediaContent(c, group *node, d mediaDefaults) Enclosure {
	e := Enclosure{
		Type:   r.attrText(c, "type"),
		Medium: r.attrText(c, "medium"),
		Length: number(c, "fileSize"),
		Height: number(c, "height"),
		Width:  number(c, "width"),
	}
	if url, ok := c.attr("url"); ok {
		e.URL = r.sanitize(url, asIRI, r.ownBase(c))
	}
	// Each value comes from the nearest level that has it: the content
	// element, its group, the item or the feed.
	levels := []*node{c}
	if group != nil {
		levels = append(levels, group)
	}
	e.Title, e.Description, e.Credit, e.Thumbnails = d.title, d.description, d.credits, d.thumbnails
	for _, level := range levels {
		if v, ok := r.mediaText(level, "title"); ok {
			e.Title = v
			break
		}
	}
	for _, level := range levels {
		if v, ok := r.mediaText(level, "description"); ok {
			e.Description = v
			break
		}
	}
	for _, level := range levels {
		if len(level.all(nsMedia, "credit")) > 0 {
			e.Credit = r.mediaCredits(level)
			break
		}
	}
	for _, level := range levels {
		if len(level.all(nsMedia, "thumbnail")) > 0 {
			e.Thumbnails = unique(r.mediaThumbnails(level))
			break
		}
	}
	return e
}

func unique(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// enclosures returns the media attached to the item, one representation per
// media:group, and the link of the very first enclosure found, which stands
// in for the link of an item that has none.
func (r *itemReader) enclosures() ([]Enclosure, string) {
	d := r.mediaDefaults()
	var all []Enclosure
	alternatives := map[string]bool{}

	for _, group := range r.n.all(nsMedia, "group") {
		contents := group.all(nsMedia, "content")
		var urls []string
		chosen := ""
		for _, c := range contents {
			if _, ok := c.attr("url"); !ok {
				continue
			}
			e := r.mediaContent(c, group, d)
			all = append(all, e)
			if e.URL == "" {
				continue
			}
			urls = append(urls, e.URL)
			if v, _ := c.attr("isDefault"); v == "true" && chosen == "" {
				chosen = e.URL
			}
		}
		if len(contents) < 2 || len(urls) == 0 {
			continue
		}
		if chosen == "" {
			chosen = urls[0]
		}
		for _, u := range urls {
			if u != chosen {
				alternatives[u] = true
			}
		}
	}
	for _, c := range r.n.all(nsMedia, "content") {
		_, hasURL := c.attr("url")
		if hasURL || c.first(nsMedia, "player") != nil {
			all = append(all, r.mediaContent(c, nil, d))
		}
	}
	for _, ns := range []string{nsAtom10, nsAtom03} {
		for _, l := range r.n.all(ns, "link") {
			href, ok := l.attr("href")
			if rel, _ := l.attr("rel"); !ok || rel != "enclosure" {
				continue
			}
			e := Enclosure{
				URL:         r.sanitize(href, asIRI, r.ownBase(l)),
				Type:        r.attrText(l, "type"),
				Length:      number(l, "length"),
				Title:       d.title,
				Description: d.description,
				Credit:      d.credits,
				Thumbnails:  d.thumbnails,
			}
			if ns == nsAtom10 {
				if title := r.attrText(l, "title"); title != "" {
					e.Title = title
				}
			}
			all = append(all, e)
		}
	}
	for _, enc := range r.n.all(nsRSS20, "enclosure") {
		url, ok := enc.attr("url")
		if !ok {
			continue
		}
		all = append(all, Enclosure{
			URL:         r.https.URL(r.sanitize(url, asIRI, r.ownBase(enc))),
			Type:        r.attrText(enc, "type"),
			Length:      number(enc, "length"),
			Title:       d.title,
			Description: d.description,
			Credit:      d.credits,
			Thumbnails:  d.thumbnails,
		})
	}

	first := ""
	if len(all) > 0 {
		first = all[0].URL
	}
	var out []Enclosure
	seen := map[string]bool{}
	for _, e := range all {
		key := strings.Join([]string{e.URL, e.Title, e.Description, e.Type, e.Medium,
			strings.Join(e.Credit, "\x00"), strings.Join(e.Thumbnails, "\x00"),
			intKey(e.Length), intKey(e.Height), intKey(e.Width)}, "\x01")
		if seen[key] || alternatives[e.URL] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, first
}

func intKey(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}
