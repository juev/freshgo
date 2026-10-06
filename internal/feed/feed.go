// Package feed turns an RSS, RDF or Atom document into feed items the way
// FreshRSS does, so that an entry gets the same identifier, link, date and
// text here as it has in a FreshRSS database.
//
// The document is read into a tree of elements, and every value is taken
// from that tree by the rules of SimplePie as shipped with FreshRSS at
// commit 219eaf58 (Item.php, SimplePie.php, Sanitize.php) and of
// FreshRSS_Feed::loadEntries. See docs/specs/guid.md.
package feed

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/sanitize"
)

// ErrNotFeed is returned for a document that is neither RSS, RDF nor Atom.
var ErrNotFeed = errors.New("not an RSS or Atom feed")

// Feed is a parsed feed document.
type Feed struct {
	Title       string
	Link        string
	Description string
	// SelfURL and HubURL are the rel="self" and rel="hub" links, for WebSub.
	SelfURL string
	HubURL  string
	// Items are in the order FreshRSS stores them: from the end of the
	// document to its beginning, which for most feeds is oldest first.
	Items []*Item
}

// Item is one entry of a feed.
type Item struct {
	// GUID is set by Feed.AssignGUIDs.
	GUID    string
	Title   string
	Authors []string
	// Content is sanitized HTML.
	Content string
	Link    string
	// Published is a Unix time, zero when the feed gives no usable date.
	Published int64
	Tags      []string
	// Thumbnail holds the attributes of media:thumbnail, "url" among them.
	Thumbnail  map[string]string
	Enclosures []Enclosure

	// The values identifiers are computed from, in the form SimplePie hands
	// them to FreshRSS: text HTML-encoded, the date as a decimal Unix time.
	id, permalink, date, title, content string
}

// Enclosure is a media file attached to an item. Field values are kept
// HTML-encoded, as FreshRSS stores them in entry.attributes.
type Enclosure struct {
	URL         string   `json:"url"`
	Title       string   `json:"title,omitempty"`
	Credit      []string `json:"credit,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Medium      string   `json:"medium,omitempty"`
	Length      *int     `json:"length,omitempty"`
	Height      *int     `json:"height,omitempty"`
	Width       *int     `json:"width,omitempty"`
	Thumbnails  []string `json:"thumbnails,omitempty"`
}

// Attributes returns the thumbnail and the enclosures as FreshRSS keeps
// them in entry.attributes.
func (it *Item) Attributes() json.RawMessage {
	enclosures := it.Enclosures
	if enclosures == nil {
		enclosures = []Enclosure{}
	}
	var b strings.Builder
	b.WriteByte('{')
	if it.Thumbnail != nil {
		thumbnail, _ := json.Marshal(it.Thumbnail)
		b.WriteString(`"thumbnail":`)
		b.Write(thumbnail)
		b.WriteByte(',')
	}
	list, _ := json.Marshal(enclosures)
	b.WriteString(`"enclosures":`)
	b.Write(list)
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

// Kinds of text in a feed, SimplePie's CONSTRUCT_* constants.
const (
	asNone = 0
	asText = 1 << iota
	asHTML
	asXHTML
	asBase64
	asIRI
	asMaybeHTML
)

// parser holds what the getters need besides the element they read.
type parser struct {
	https *HTTPSDomains
	// feedURL is the address the document came from.
	feedURL string
	// top is the feed, rss or RDF element; channel is the element holding
	// the feed's own fields.
	top, channel *node
	links        map[string][]string
	linksDone    bool
	// location is the time zone of dates that name none.
	location *time.Location

	// Values of the channel that every item falls back to, read once.
	channelAuthors     []string
	channelAuthorsDone bool
	channelMedia       *mediaDefaults
}

// Options are what Parse needs to know besides the document.
type Options struct {
	// ContentType is the HTTP Content-Type header, used for the encoding.
	ContentType string
	// URL is the address the document was fetched from, the last resort for
	// resolving relative links.
	URL string
	// HTTPS is the force-https list; nil for none.
	HTTPS *HTTPSDomains
	// Location is the time zone of dates written without one in the formats
	// PHP strtotime reads: FreshRSS uses the user's time zone. Nil for UTC.
	Location *time.Location
}

// Parse reads a feed document.
func Parse(data []byte, o Options) (*Feed, error) {
	if o.HTTPS == nil {
		o.HTTPS = &HTTPSDomains{}
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	root, err := parseTree(toUTF8(data, o.ContentType))
	if err != nil {
		return nil, err
	}
	p := &parser{https: o.HTTPS, feedURL: o.URL, location: o.Location}
	var items []*node
	switch {
	case root.first(nsAtom10, "feed") != nil:
		p.top = root.first(nsAtom10, "feed")
		p.channel = p.top
		items = p.top.all(nsAtom10, "entry")
	case root.first(nsAtom03, "feed") != nil:
		p.top = root.first(nsAtom03, "feed")
		p.channel = p.top
		items = p.top.all(nsAtom03, "entry")
	case root.first(nsRDF, "RDF") != nil:
		p.top = root.first(nsRDF, "RDF")
		p.channel = p.top.first(nsRSS10, "channel")
		items = p.top.all(nsRSS10, "item")
		if p.channel == nil {
			p.channel = p.top.first(nsRSS090, "channel")
			items = p.top.all(nsRSS090, "item")
		}
	case root.first(nsRSS20, "rss") != nil:
		p.top = root.first(nsRSS20, "rss")
		p.channel = p.top.first(nsRSS20, "channel")
		items = p.channel.all(nsRSS20, "item")
	default:
		return nil, ErrNotFeed
	}
	if p.channel == nil {
		p.channel = &node{}
	}

	f := &Feed{
		Title:       plainText(p.feedTitle()),
		Link:        decodeText(p.feedLink("alternate")),
		Description: p.feedDescription(),
		SelfURL:     decodeText(p.feedLink("self")),
		HubURL:      decodeText(p.feedLink("hub")),
	}
	for i := len(items) - 1; i >= 0; i-- {
		f.Items = append(f.Items, p.item(items[i]))
	}
	return f, nil
}

var (
	// maybeHTML is SimplePie's test for "this text is HTML": an entity or a
	// closing tag.
	maybeHTML = regexp.MustCompile(`(&(#(x[0-9a-fA-F]+|[0-9]+)|[a-zA-Z0-9]+)|</[A-Za-z][^\x09\x0A\x0B\x0C\x0D\x20\x2F\x3E]*(?:[\x09\x0A\x0B\x0C\x0D\x20\x2F][^>]*)?>)`)
	tags      = regexp.MustCompile(`<[^>]*>`)
)

// entityDecoder undoes PHP htmlspecialchars.
var entityDecoder = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'", "&#39;", "'")

func decodeText(s string) string {
	return entityDecoder.Replace(s)
}

// plainText turns a sanitized title or name into text: no tags, no entities.
func plainText(s string) string {
	return decodeText(tags.ReplaceAllString(s, ""))
}

// phpTrim is PHP trim(): the characters it strips differ from Go's.
func phpTrim(s string) string {
	return strings.Trim(s, " \n\r\t\v\x00")
}

// sanitize is Sanitize::sanitize of SimplePie: the text of an element made
// safe according to its kind. Text and URLs come out HTML-encoded.
func (p *parser) sanitize(data string, kind int, base string) string {
	data = phpTrim(data)
	if data == "" && kind&asIRI == 0 {
		return data
	}
	if kind&asMaybeHTML != 0 {
		if maybeHTML.MatchString(data) {
			kind |= asHTML
		} else {
			kind |= asText
		}
	}
	if kind&asBase64 != 0 {
		decoded, _ := base64.StdEncoding.DecodeString(data)
		data = string(decoded)
	}
	// An Atom construct of a type that is neither text nor markup (text/html,
	// application/xml, a media type sent as base64) is passed through by
	// SimplePie untouched, script and all. Here it is cleaned as HTML.
	if kind&(asText|asHTML|asXHTML|asIRI) == 0 {
		kind |= asHTML
	}
	switch {
	case kind&asXHTML != 0 && kind&asHTML == 0:
		data = sanitize.XHTML(data, base, p.https.URL)
	case kind&(asHTML|asXHTML) != 0:
		data = sanitize.HTML(data, base, p.https.URL)
	}
	if kind&asIRI != 0 {
		if absolute, ok := sanitize.Absolutize(data, base); ok {
			data = absolute
		}
	}
	if kind&(asText|asIRI) != 0 {
		data = compatEscaper.Replace(data)
	}
	return data
}

func (p *parser) text(n *node) string {
	return p.sanitize(n.data, asText, "")
}

// atom10Kind, atom10ContentKind and atom03Kind map the type attribute of an
// Atom construct to the kind of its text.
func atom10Kind(n *node) int {
	t, ok := n.attr("type")
	if !ok {
		return asText
	}
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "text":
		return asText
	case "html":
		return asHTML
	case "xhtml":
		return asXHTML
	}
	return asNone
}

func atom10ContentKind(n *node) int {
	t, ok := n.attr("type")
	if !ok {
		t = n.atomType
	}
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "":
		return asText
	case "text":
		return asText
	case "html":
		return asHTML
	case "xhtml":
		return asXHTML
	}
	if strings.HasSuffix(t, "+xml") || strings.HasSuffix(t, "/xml") || strings.HasPrefix(t, "text/") {
		return asNone
	}
	return asBase64
}

func atom03Kind(n *node) int {
	mode := asNone
	if m, _ := n.attr("mode"); strings.ToLower(strings.TrimSpace(m)) == "base64" {
		mode = asBase64
	}
	t, ok := n.attr("type")
	if !ok {
		return asText | mode
	}
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "text", "text/plain":
		return asText | mode
	case "html", "text/html":
		return asHTML | mode
	case "xhtml", "application/xhtml+xml":
		return asXHTML | mode
	}
	return mode
}

// feedBase is the base URL of the feed for an element: its xml:base, the
// feed's own link, the feed's address.
func (p *parser) feedBase(n *node) string {
	if n != nil && n.explicit {
		return n.base
	}
	if link := p.feedLink("alternate"); link != "" {
		return link
	}
	if link := p.feedLink("self"); link != "" {
		return link
	}
	return p.feedURL
}

// feedLink returns the first link of the feed with the given relation.
func (p *parser) feedLink(rel string) string {
	if !p.linksDone {
		// Marked first: resolving a link asks for the base, which asks for
		// the links and has to find none yet.
		p.linksDone = true
		links := map[string][]string{}
		for _, ns := range []string{nsAtom10, nsAtom03} {
			for _, l := range p.channel.all(ns, "link") {
				if href, ok := l.attr("href"); ok {
					r, ok := l.attr("rel")
					if !ok {
						r = "alternate"
					}
					links[r] = append(links[r], p.sanitize(href, asIRI, p.feedBase(l)))
				}
			}
		}
		for _, ns := range []string{nsRSS10, nsRSS090, nsRSS20} {
			if l := p.channel.first(ns, "link"); l != nil {
				links["alternate"] = append(links["alternate"], p.sanitize(l.data, asIRI, p.feedBase(l)))
			}
		}
		p.links = links
	}
	if l := p.links[rel]; len(l) > 0 {
		return l[0]
	}
	return ""
}

func (p *parser) feedTitle() string {
	c := p.channel
	switch {
	case c.first(nsAtom10, "title") != nil:
		n := c.first(nsAtom10, "title")
		return p.sanitize(n.data, atom10Kind(n), p.feedBase(n))
	case c.first(nsAtom03, "title") != nil:
		n := c.first(nsAtom03, "title")
		return p.sanitize(n.data, atom03Kind(n), p.feedBase(n))
	}
	for _, ns := range []string{nsRSS10, nsRSS090, nsRSS20} {
		if n := c.first(ns, "title"); n != nil {
			return p.sanitize(n.data, asMaybeHTML, p.feedBase(n))
		}
	}
	for _, ns := range []string{nsDC11, nsDC10} {
		if n := c.first(ns, "title"); n != nil {
			return p.text(n)
		}
	}
	return ""
}

func (p *parser) feedDescription() string {
	c := p.channel
	switch {
	case c.first(nsAtom10, "subtitle") != nil:
		n := c.first(nsAtom10, "subtitle")
		return p.sanitize(n.data, atom10Kind(n), p.feedBase(n))
	case c.first(nsAtom03, "tagline") != nil:
		n := c.first(nsAtom03, "tagline")
		return p.sanitize(n.data, atom03Kind(n), p.feedBase(n))
	}
	for _, ns := range []string{nsRSS10, nsRSS090} {
		if n := c.first(ns, "description"); n != nil {
			return p.sanitize(n.data, asMaybeHTML, p.feedBase(n))
		}
	}
	if n := c.first(nsRSS20, "description"); n != nil {
		return p.sanitize(n.data, asHTML, p.feedBase(n))
	}
	for _, ns := range []string{nsDC11, nsDC10} {
		if n := c.first(ns, "description"); n != nil {
			return p.text(n)
		}
	}
	for _, name := range []string{"summary", "subtitle"} {
		if n := c.first(nsITunes, name); n != nil {
			return p.sanitize(n.data, asHTML, p.feedBase(n))
		}
	}
	return ""
}

// itemReader reads the fields of one item element.
type itemReader struct {
	*parser
	n         *node
	links     []string
	linksDone bool
	// enclosureLink is the link of the first enclosure, the permalink of an
	// item without a link.
	enclosureLink string
}

// ownBase is the base for the item's links: an xml:base or the feed's base.
func (r *itemReader) ownBase(n *node) string {
	if n.explicit {
		return n.base
	}
	return r.feedBase(nil)
}

// base is the base for the item's text: an xml:base, the item's permalink,
// the feed's base.
func (r *itemReader) base(n *node) string {
	if n.explicit {
		return n.base
	}
	if link := r.permalink(); link != "" {
		return link
	}
	return r.feedBase(n)
}

// alternates returns the links of the item that lead to its page.
func (r *itemReader) alternates() []string {
	if r.linksDone {
		return r.links
	}
	r.linksDone = true
	var links []string
	for _, ns := range []string{nsAtom10, nsAtom03} {
		for _, l := range r.n.all(ns, "link") {
			href, ok := l.attr("href")
			if !ok {
				continue
			}
			rel, ok := l.attr("rel")
			if !ok || rel == "alternate" || rel == "http://www.iana.org/assignments/relation/alternate" {
				links = append(links, r.sanitize(href, asIRI, r.ownBase(l)))
			}
		}
	}
	for _, ns := range []string{nsRSS10, nsRSS090, nsRSS20} {
		if l := r.n.first(ns, "link"); l != nil {
			links = append(links, r.sanitize(l.data, asIRI, r.ownBase(l)))
		}
	}
	if g := r.n.first(nsRSS20, "guid"); g != nil {
		if v, ok := g.attr("isPermaLink"); !ok || strings.ToLower(phpTrim(v)) == "true" {
			links = append(links, r.sanitize(g.data, asIRI, r.ownBase(g)))
		}
	}
	for i, l := range links {
		if !sanitize.AllowedScheme(l) {
			links[i] = "unsafe:" + l
		} else {
			links[i] = r.https.URL(l)
		}
	}
	r.links = links
	return links
}

func (r *itemReader) permalink() string {
	if links := r.alternates(); len(links) > 0 {
		return links[0]
	}
	return r.enclosureLink
}

func (r *itemReader) id() string {
	n := r.n
	var id string
	switch {
	case n.first(nsAtom10, "id") != nil:
		id = r.text(n.first(nsAtom10, "id"))
	case n.first(nsAtom03, "id") != nil:
		id = r.text(n.first(nsAtom03, "id"))
	case n.first(nsRSS20, "guid") != nil:
		id = r.text(n.first(nsRSS20, "guid"))
	case n.first(nsDC11, "identifier") != nil:
		id = r.text(n.first(nsDC11, "identifier"))
	case n.first(nsDC10, "identifier") != nil:
		id = r.text(n.first(nsDC10, "identifier"))
	default:
		id = r.sanitize(n.about, asText, "")
	}
	return r.https.URL(id)
}

func (r *itemReader) title() string {
	n := r.n
	switch {
	case n.first(nsAtom10, "title") != nil:
		t := n.first(nsAtom10, "title")
		return r.sanitize(t.data, atom10Kind(t), r.base(t))
	case n.first(nsAtom03, "title") != nil:
		t := n.first(nsAtom03, "title")
		return r.sanitize(t.data, atom03Kind(t), r.base(t))
	}
	for _, ns := range []string{nsRSS10, nsRSS090, nsRSS20} {
		if t := n.first(ns, "title"); t != nil {
			return r.sanitize(t.data, asMaybeHTML, r.base(t))
		}
	}
	for _, ns := range []string{nsDC11, nsDC10} {
		if t := n.first(ns, "title"); t != nil {
			return r.text(t)
		}
	}
	return ""
}

// content prefers the full text of the item over its summary.
func (r *itemReader) content() string {
	n := r.n
	if c := n.first(nsAtom10, "content"); c != nil {
		if s := r.sanitize(c.data, atom10ContentKind(c), r.base(c)); s != "" {
			return s
		}
	}
	if c := n.first(nsAtom03, "content"); c != nil {
		if s := r.sanitize(c.data, atom03Kind(c), r.base(c)); s != "" {
			return s
		}
	}
	if c := n.first(nsContent, "encoded"); c != nil {
		if s := r.sanitize(c.data, asHTML, r.base(c)); s != "" {
			return s
		}
	}
	return r.description()
}

func (r *itemReader) description() string {
	n := r.n
	type source struct {
		space, name string
		kind        func(*node) int
		based       bool
	}
	fixed := func(kind int) func(*node) int { return func(*node) int { return kind } }
	for _, s := range []source{
		{nsAtom10, "summary", atom10Kind, true},
		{nsAtom03, "summary", atom03Kind, true},
		{nsRSS10, "description", fixed(asMaybeHTML), true},
		{nsRSS20, "description", fixed(asHTML), true},
		{nsDC11, "description", fixed(asText), false},
		{nsDC10, "description", fixed(asText), false},
		{nsITunes, "summary", fixed(asHTML), true},
		{nsITunes, "subtitle", fixed(asText), false},
		{nsRSS090, "description", fixed(asHTML), false},
	} {
		d := n.first(s.space, s.name)
		if d == nil {
			continue
		}
		base := ""
		if s.based {
			base = r.base(d)
		}
		if out := r.sanitize(d.data, s.kind(d), base); out != "" {
			return out
		}
	}
	return ""
}

// date returns the raw date of the item: when it was published, else when
// it was last changed.
func (r *itemReader) date() string {
	for _, s := range [][2]string{
		{nsAtom10, "published"}, {nsRSS20, "pubDate"}, {nsDC11, "date"}, {nsDC10, "date"},
		{nsAtom10, "updated"}, {nsAtom03, "issued"}, {nsAtom03, "created"}, {nsAtom03, "modified"},
	} {
		if d := r.n.first(s[0], s[1]); d != nil {
			return d.data
		}
	}
	return ""
}

// authors returns the names of the item's authors, HTML-encoded: the item's
// own, else the feed's.
func (r *itemReader) authors() []string {
	names := personNames(r.parser, r.n)
	if len(names) == 0 {
		if source := r.n.first(nsAtom10, "source"); source != nil {
			names = personNames(r.parser, source)
		}
	}
	if len(names) == 0 {
		if !r.channelAuthorsDone {
			r.channelAuthors, r.channelAuthorsDone = personNames(r.parser, r.channel), true
		}
		names = r.channelAuthors
	}
	return names
}

// personNames collects the authors named by the children of n. An author is
// known by name, or by e-mail when there is no name.
func personNames(p *parser, n *node) []string {
	type person struct{ name, email string }
	var people []person
	for _, a := range n.all(nsAtom10, "author") {
		var who person
		if c := a.first(nsAtom10, "name"); c != nil {
			who.name = p.text(c)
		}
		if c := a.first(nsAtom10, "email"); c != nil {
			who.email = p.text(c)
		}
		if a.first(nsAtom10, "name") != nil || a.first(nsAtom10, "email") != nil || a.first(nsAtom10, "uri") != nil {
			people = append(people, who)
		}
	}
	if a := n.first(nsAtom03, "author"); a != nil {
		var who person
		if c := a.first(nsAtom03, "name"); c != nil {
			who.name = p.text(c)
		}
		if c := a.first(nsAtom03, "email"); c != nil {
			who.email = p.text(c)
		}
		if a.first(nsAtom03, "name") != nil || a.first(nsAtom03, "email") != nil || a.first(nsAtom03, "url") != nil {
			people = append(people, who)
		}
	}
	if a := n.first(nsRSS20, "author"); a != nil {
		people = append(people, person{email: p.text(a)})
	}
	for _, s := range [][2]string{{nsDC11, "creator"}, {nsDC10, "creator"}, {nsITunes, "author"}} {
		for _, a := range n.all(s[0], s[1]) {
			people = append(people, person{name: p.text(a)})
		}
	}
	var names []string
	seen := map[person]bool{}
	for _, who := range people {
		if seen[who] {
			continue
		}
		seen[who] = true
		name := who.name
		if name == "" {
			name = who.email
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// categories returns the labels of the item's categories, HTML-encoded.
func (r *itemReader) categories() []string {
	type category struct{ term, scheme, label string }
	var list []category
	for _, c := range r.n.all(nsAtom10, "category") {
		var cat category
		if v, ok := c.attr("term"); ok {
			cat.term = r.sanitize(v, asText, "")
		}
		if v, ok := c.attr("scheme"); ok {
			cat.scheme = r.sanitize(v, asText, "")
		}
		if v, ok := c.attr("label"); ok {
			cat.label = r.sanitize(v, asText, "")
		}
		list = append(list, cat)
	}
	for _, c := range r.n.all(nsRSS20, "category") {
		cat := category{term: r.text(c)}
		if v, ok := c.attr("domain"); ok {
			cat.scheme = r.sanitize(v, asText, "")
		}
		list = append(list, cat)
	}
	for _, ns := range []string{nsDC11, nsDC10} {
		for _, c := range r.n.all(ns, "subject") {
			list = append(list, category{term: r.text(c)})
		}
	}
	var labels []string
	seen := map[category]bool{}
	for _, cat := range list {
		if seen[cat] {
			continue
		}
		seen[cat] = true
		label := cat.label
		if label == "" {
			label = cat.term
		}
		labels = append(labels, label)
	}
	return labels
}

// item reads one item the way FreshRSS_Feed::loadEntries does.
func (p *parser) item(n *node) *Item {
	r := &itemReader{parser: p, n: n}
	enclosures, first := r.enclosures()
	r.enclosureLink = first

	it := &Item{
		id:        r.id(),
		permalink: r.permalink(),
		title:     r.title(),
		content:   r.content(),
	}
	if raw := r.date(); raw != "" {
		// A date that reads as the epoch is no date for SimplePie.
		if t, ok := parseDate(raw, p.location); ok && t != 0 {
			it.Published = t
			it.date = strconv.FormatInt(t, 10)
		}
	}
	it.Title = plainText(it.title)
	it.Content = it.content
	it.Link = decodeText(it.permalink)
	for _, name := range r.authors() {
		if name = plainText(name); name != "" {
			it.Authors = append(it.Authors, name)
		}
	}
	// Some feeds put several comma-separated tags into one category.
	seen := map[string]bool{}
	for _, label := range r.categories() {
		for _, tag := range strings.Split(label, ",") {
			if tag = decodeText(strings.TrimSpace(tag)); tag != "" && !seen[tag] {
				seen[tag] = true
				it.Tags = append(it.Tags, tag)
			}
		}
	}
	it.Thumbnail = r.thumbnail()
	thumbnailURL := it.Thumbnail["url"]
	for _, e := range enclosures {
		if !remote(e.URL) {
			continue
		}
		e.Type = strings.ToLower(e.Type)
		e.Medium = strings.ToLower(e.Medium)
		kept := e.Thumbnails[:0:0]
		for _, t := range e.Thumbnails {
			if remote(t) && t != thumbnailURL {
				kept = append(kept, t)
			}
		}
		e.Thumbnails = kept
		it.Enclosures = append(it.Enclosures, e)
	}
	return it
}

var remoteURL = regexp.MustCompile(`(?i)^https?://`)

func remote(u string) bool {
	return remoteURL.MatchString(u)
}

// thumbnail returns the attributes of the item's first media:thumbnail,
// when it has a URL that can be fetched.
func (r *itemReader) thumbnail() map[string]string {
	t := r.n.first(nsMedia, "thumbnail")
	if t == nil || t.attrs["url"] == "" {
		return nil
	}
	url := r.sanitize(t.attrs["url"], asIRI, r.base(t))
	if !remote(url) {
		return nil
	}
	out := maps.Clone(t.attrs)
	out["url"] = url
	return out
}
