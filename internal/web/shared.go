package web

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/opml"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/transfer"
)

// sharedFormats are the forms a saved query is handed out in. Those marked
// opml are opened by the switch FreshRSS calls shareOpml, the others by
// shareRss.
var sharedFormats = []struct {
	name string
	opml bool
}{{"rss", false}, {"atom", false}, {"html", false}, {"opml", true}, {"json", true}}

var alnum = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// shared is a stream handed out to whoever has its address.
type shared struct {
	owner  *store.User
	lib    *library
	stream stream
	// kind names the list in the identifier of a JSON document.
	kind string
	// title, description and image are what a feed says of itself; html and
	// self its addresses as a page and as a feed.
	title, description, image string
	html, self                string
	// labels puts the labels of the owner into the feed, with prefix before
	// each; noTags leaves the tags of the entries out.
	labels bool
	prefix string
	noTags bool
	// form is where the search field of the page sends the visitor, hidden
	// what goes with it, field the name the search has there.
	form   string
	hidden [][2]string
	field  string
	// opml says the stream is one OPML can describe: everything, a
	// category or a feed.
	opml opml.Part
	safe bool
}

// plain answers a request for a public address that cannot be served.
func plain(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}

// sharedQuery finds the saved query of a token that is handed out in a
// format. With a user name only the queries of that user are looked at.
func (h *Handler) sharedQuery(r *http.Request, userName, token, format string) (*shared, error) {
	if !alnum.MatchString(token) {
		return nil, nil
	}
	ctx := r.Context()
	var users []*store.User
	if userName != "" {
		u, err := h.userNamed(ctx, userName)
		if err != nil || u == nil {
			return nil, err
		}
		users = []*store.User{u}
	} else {
		var err error
		if users, err = h.db.Users(ctx); err != nil {
			return nil, err
		}
	}
	wantsOPML := format == "opml" || format == "json"
	for _, u := range users {
		if !readPreferences(u).enabled() {
			continue
		}
		for n, q := range readQueries(u) {
			if q.Token == "" || subtle.ConstantTimeCompare([]byte(q.Token), []byte(token)) != 1 ||
				wantsOPML && !q.ShareOpml || !wantsOPML && !q.ShareRss {
				continue
			}
			lib, err := h.libraryOf(ctx, u, false)
			if err != nil {
				return nil, err
			}
			s, ok := lib.queryStream(n, q)
			if !ok {
				return nil, nil
			}
			sh := &shared{
				owner: u, lib: lib, stream: s, kind: "query/" + q.Token, title: q.Name, description: q.Description, image: q.ImageURL,
				html: h.public("/shared/" + q.Token + ".html"), self: h.public("/shared/" + q.Token + ".rss"),
				labels: q.IncludeUserLabels, prefix: q.UserLabelPrefix, noTags: q.ExcludeArticleTags,
			}
			sh.opml, sh.safe = opmlPart(q.Get)
			return sh, nil
		}
	}
	return nil, nil
}

// opmlPart is the part of the subscriptions a stream stands for, when it is
// one OPML can describe.
func opmlPart(get string) (opml.Part, bool) {
	m := getPattern.FindStringSubmatch(get)
	if m == nil {
		return opml.Part{Public: true}, true
	}
	id, _ := strconv.ParseInt(m[2], 10, 64)
	switch m[1] {
	case "a":
		return opml.Part{Public: true}, true
	case "c":
		return opml.Part{Public: true, CategoryID: id}, true
	case "f":
		return opml.Part{Public: true, FeedID: id}, true
	}
	return opml.Part{}, false
}

// sharedFile hands out a saved query at its address: /shared/<token>.<format>.
func (h *Handler) sharedFile(w http.ResponseWriter, r *http.Request) {
	token, format, _ := strings.Cut(r.PathValue("file"), ".")
	h.serveQuery(w, r, "", token, format, r.URL.Query().Get("q"), func(sh *shared) {
		sh.form, sh.field = r.URL.Path, "q"
	})
}

// legacyQuery hands out a saved query at the address FreshRSS gave it.
func (h *Handler) legacyQuery(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	token, format, user := params.Get("t"), params.Get("f"), params.Get("user")
	switch {
	case !alnum.MatchString(token):
		plain(w, http.StatusUnprocessableEntity, "Invalid token `t`!")
		return
	case !store.ValidUserName(user):
		plain(w, http.StatusUnprocessableEntity, "Invalid user!")
		return
	}
	switch format {
	case "atom":
		// What FreshRSS calls Atom there is the RSS feed.
		format = "rss"
	case "greader":
		format = "json"
	case "rss", "html", "opml", "json":
	default:
		plain(w, http.StatusUnprocessableEntity, "Invalid format `f`!")
		return
	}
	h.serveQuery(w, r, user, token, format, params.Get("search"), func(sh *shared) {
		sh.form, sh.field = r.URL.Path, "search"
		sh.hidden = [][2]string{{"user", user}, {"t", token}, {"f", "html"}}
	})
}

func (h *Handler) serveQuery(w http.ResponseWriter, r *http.Request, user, token, format, visitorSearch string, place func(*shared)) {
	if !state(r).system.APIEnabled {
		plain(w, http.StatusServiceUnavailable, "Service Unavailable!")
		return
	}
	known := false
	for _, f := range sharedFormats {
		known = known || f.name == format
	}
	if !known {
		plain(w, http.StatusNotFound, "User query not found!")
		return
	}
	sh, err := h.sharedQuery(r, user, token, format)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if sh == nil {
		plain(w, http.StatusNotFound, "User query not found!")
		return
	}
	place(sh)
	h.serveShared(w, r, sh, format, visitorSearch)
}

// userFeed hands out the entries, or the subscriptions, of a user to
// whoever has the token of the user.
func (h *Handler) userFeed(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if !state(r).system.APIEnabled {
			plain(w, http.StatusServiceUnavailable, "Service Unavailable!")
			return
		}
		name, token := params.Get("user"), params.Get("token")
		var owner *store.User
		if store.ValidUserName(name) && token != "" {
			var err error
			if owner, err = h.userNamed(r.Context(), name); err != nil {
				h.broken(w, r, err)
				return
			}
		}
		if owner == nil || !readPreferences(owner).enabled() {
			plain(w, http.StatusForbidden, "Forbidden!")
			return
		}
		if own := readAttrs(owner.Settings).text("token"); own == "" || subtle.ConstantTimeCompare([]byte(own), []byte(token)) != 1 {
			plain(w, http.StatusForbidden, "Forbidden!")
			return
		}
		lib, err := h.libraryOf(r.Context(), owner, false)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		bits, _ := strconv.Atoi(params.Get("state"))
		q := savedQuery{Name: state(r).system.Title, Get: params.Get("get"), Search: params.Get("search"), State: bits, Order: params.Get("order")}
		s, ok := lib.queryStream(0, q)
		if !ok {
			plain(w, http.StatusNotFound, "Not found!")
			return
		}
		address := url.Values{"user": {name}, "token": {token}}
		sh := &shared{
			owner: owner, lib: lib, stream: s, kind: "feed", title: q.Name,
			html: h.public("/"), self: h.public("/rss") + "?" + address.Encode(),
		}
		sh.opml, sh.safe = opmlPart(q.Get)
		// The owner's own document: nothing of it is held back but what a
		// feed is requested with.
		h.serveShared(w, r, sh, format, "")
	}
}

// legacyIndex answers the addresses of the interface of FreshRSS: the feed
// and the OPML a token opens, and for everything else the start page.
func (h *Handler) legacyIndex(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("a") {
	case "rss":
		h.userFeed("rss")(w, r)
	case "opml":
		h.userFeed("opml")(w, r)
	default:
		http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
	}
}

// sharedPage is what the page of a shared stream shows.
type sharedPage struct {
	Description string
	Query       string
	Problem     string
	Entries     []article
	Labels      bool
	Tags        bool
	// Form is where the search goes, Hidden what goes with it, Field the
	// name the search has there.
	Form   string
	Hidden [][2]string
	Field  string
	Feed   string
}

// serveShared answers with a stream in one of the formats.
func (h *Handler) serveShared(w http.ResponseWriter, r *http.Request, sh *shared, format, visitorSearch string) {
	ctx, params := r.Context(), r.URL.Query()
	prefs := readReading(sh.owner)
	header := w.Header()
	header.Set("Access-Control-Allow-Methods", "GET")
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Max-Age", "600")
	if format != "html" {
		// A document of somebody's entries is not a page of ours.
		header.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	}
	// A document is the same for everybody and may be kept for a minute.
	// The page is not: it has the name and the menu of whoever is logged
	// in, and an error is nobody's to keep.
	document := func(contentType string, body []byte) {
		header.Set("Content-Type", contentType)
		header.Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write(body)
	}

	if format == "opml" {
		if !sh.safe {
			plain(w, http.StatusNotFound, "OPML not allowed for this user query!")
			return
		}
		exported, err := opml.ExportPart(ctx, h.db, sh.owner, h.now(), sh.opml)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		document("application/xml; charset=utf-8", exported)
		return
	}

	// What the stream searches for, and within it what the visitor does.
	// The visitor's search is read without the saved searches of the owner.
	query, err := h.parseSearch(sh.stream.base, prefs)
	if err != nil {
		plain(w, http.StatusBadRequest, "Bad user query!")
		return
	}
	visitorSearch = strings.TrimSpace(visitorSearch)
	if visitorSearch != "" {
		visitor, err := search.Parse(visitorSearch, search.Options{Now: h.now().In(prefs.location()), Labels: false})
		if err != nil {
			plain(w, http.StatusBadRequest, "Bad search!")
			return
		}
		query = search.And(query, visitor)
	}
	showing := show(url.Values{"order": {strings.ToLower(params.Get("order"))}}, prefs, sh.stream)
	listing := store.Listing{Set: sh.stream.set, Search: query, Order: orders[showing.sort], Ascending: showing.asc, Seed: showing.seed}
	applyBits(&listing, sh.stream.state)
	limit, err := strconv.Atoi(params.Get("nb"))
	if err != nil || limit < 1 || limit > 500 {
		limit = prefs.PostsPerPage
	}

	if format == "json" {
		var out bytes.Buffer
		err := transfer.Write(ctx, &out, h.db, h.hooks, sh.owner, transfer.Document{
			Kind: sh.kind, Title: sh.title, Listing: &listing, Limit: limit, NoLabels: true,
		})
		if err != nil {
			h.broken(w, r, err)
			return
		}
		document("application/json; charset=utf-8", out.Bytes())
		return
	}

	listing.Limit = limit
	entries, _, err := h.db.ListPage(ctx, sh.owner.ID, listing)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "", "shared.heading")
	shown, err := h.articles(ctx, v, sh.lib, sh.owner.ID, prefs, entries)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	description := sh.description
	if description == "" {
		description = v.T("shared.feed-of", sh.title)
	}
	switch format {
	case "rss":
		document("application/rss+xml; charset=utf-8", rssDocument(sh, description, shown, entries, h.now()))
	case "atom":
		document("application/atom+xml; charset=utf-8", atomDocument(sh, description, shown, entries, h.now()))
	default:
		v.Heading = sh.title
		v.Data = sharedPage{
			Description: description, Query: visitorSearch, Entries: shown, Labels: sh.labels, Tags: !sh.noTags,
			Form: sh.form, Hidden: sh.hidden, Field: sh.field, Feed: sh.self,
		}
		h.render(w, r, http.StatusOK, "shared", v)
	}
}

// escaped writes text into an XML document.
func escaped(b *bytes.Buffer, text string) {
	_ = xml.EscapeText(b, []byte(text))
}

// cdata writes markup into an XML document as it is.
func cdata(b *bytes.Buffer, markup string) {
	b.WriteString("<![CDATA[")
	// The end of a CDATA section cannot stand inside one.
	b.WriteString(strings.ReplaceAll(markup, "]]>", "]]]]><![CDATA[>"))
	b.WriteString("]]>")
}

// enclosure is a file of an entry as a feed names it.
type enclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type"`
	Medium string `json:"medium"`
	Length any    `json:"length"`
	Title  string `json:"title"`
}

func enclosuresOf(e *store.Entry) (thumbnail string, list []enclosure) {
	var attrs struct {
		Thumbnail  enclosure   `json:"thumbnail"`
		Enclosures []enclosure `json:"enclosures"`
	}
	// Attributes of another shape hold no enclosures.
	_ = json.Unmarshal(e.Attributes, &attrs)
	seen := map[string]bool{}
	if attrs.Thumbnail.URL != "" {
		thumbnail = attrs.Thumbnail.URL
		if attrs.Thumbnail.Medium == "" {
			attrs.Thumbnail.Medium = "image"
		}
		attrs.Enclosures = append([]enclosure{attrs.Thumbnail}, attrs.Enclosures...)
	}
	for _, enc := range attrs.Enclosures {
		if enc.URL == "" || seen[enc.URL] {
			continue
		}
		seen[enc.URL] = true
		list = append(list, enc)
	}
	return thumbnail, list
}

// categoriesOf are the labels and the tags a shared feed gives an entry.
func categoriesOf(sh *shared, a article) []string {
	var out []string
	if sh.labels {
		for _, label := range a.Labels {
			out = append(out, sh.prefix+label)
		}
	}
	if !sh.noTags {
		out = append(out, a.Tags...)
	}
	return out
}

// rssDocument writes a stream as RSS 2.0, the way FreshRSS does.
func rssDocument(sh *shared, description string, shown []article, entries []*store.Entry, now time.Time) []byte {
	byID := make(map[int64]*store.Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:media="http://search.yahoo.com/mrss/">` + "\n<channel>\n<title>")
	escaped(&b, sh.title)
	b.WriteString("</title>\n<link>")
	escaped(&b, sh.html)
	b.WriteString("</link>\n<description>")
	escaped(&b, description)
	b.WriteString("</description>\n<pubDate>" + now.Format(time.RFC1123Z) + "</pubDate>\n<lastBuildDate>" + now.UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT</lastBuildDate>\n")
	b.WriteString(`<atom:link href="`)
	escaped(&b, sh.self)
	b.WriteString(`" rel="self" type="application/rss+xml"/>` + "\n")
	if sh.image != "" {
		b.WriteString("<image>\n<url>")
		escaped(&b, sh.image)
		b.WriteString("</url>\n<title>")
		escaped(&b, sh.title)
		b.WriteString("</title>\n<link>")
		escaped(&b, sh.html)
		b.WriteString("</link>\n</image>\n")
	}
	for _, a := range shown {
		e := byID[a.ID]
		b.WriteString("<item>\n<title>")
		escaped(&b, e.Title)
		b.WriteString("</title>\n<link>")
		escaped(&b, a.Link)
		b.WriteString("</link>\n")
		for _, author := range e.Authors {
			b.WriteString("<dc:creator>")
			escaped(&b, author)
			b.WriteString("</dc:creator>\n")
		}
		for _, category := range categoriesOf(sh, a) {
			b.WriteString("<category>")
			escaped(&b, category)
			b.WriteString("</category>\n")
		}
		thumbnail, files := enclosuresOf(e)
		if thumbnail != "" {
			b.WriteString(`<media:thumbnail url="`)
			escaped(&b, thumbnail)
			b.WriteString(`"/>` + "\n")
		}
		for _, enc := range files {
			b.WriteString(`<media:content url="`)
			escaped(&b, enc.URL)
			for _, attribute := range [][2]string{{"medium", enc.Medium}, {"type", enc.Type}} {
				if attribute[1] != "" {
					b.WriteString(`" ` + attribute[0] + `="`)
					escaped(&b, attribute[1])
				}
			}
			if length := fmt.Sprint(enc.Length); enc.Length != nil && length != "" && length != "0" {
				b.WriteString(`" length="`)
				escaped(&b, length)
			}
			b.WriteString(`">`)
			if enc.Title != "" {
				b.WriteString(`<media:title type="html">`)
				escaped(&b, enc.Title)
				b.WriteString(`</media:title>`)
			}
			b.WriteString("</media:content>\n")
		}
		// The text as it is stored, as in the JSON: whoever reads a feed
		// cleans it for themselves.
		b.WriteString("<description>")
		cdata(&b, e.Content)
		b.WriteString("</description>\n<pubDate>")
		published, _ := time.Parse(time.RFC3339, a.DateTime)
		b.WriteString(published.Format(time.RFC1123Z))
		b.WriteString("</pubDate>\n" + `<guid isPermaLink="false">` + strconv.FormatInt(a.ID, 10) + "</guid>\n</item>\n")
	}
	b.WriteString("</channel>\n</rss>\n")
	return b.Bytes()
}

// atomDocument writes a stream as an Atom feed.
func atomDocument(sh *shared, description string, shown []article, entries []*store.Entry, now time.Time) []byte {
	byID := make(map[int64]*store.Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n<title>")
	escaped(&b, sh.title)
	b.WriteString("</title>\n<subtitle>")
	escaped(&b, description)
	b.WriteString("</subtitle>\n<id>")
	escaped(&b, sh.html)
	b.WriteString("</id>\n<updated>" + now.UTC().Format(time.RFC3339) + "</updated>\n" + `<link rel="alternate" type="text/html" href="`)
	escaped(&b, sh.html)
	b.WriteString(`"/>` + "\n")
	if sh.image != "" {
		b.WriteString("<logo>")
		escaped(&b, sh.image)
		b.WriteString("</logo>\n")
	}
	for _, a := range shown {
		e := byID[a.ID]
		b.WriteString("<entry>\n<id>")
		escaped(&b, sh.html+"#"+strconv.FormatInt(a.ID, 10))
		b.WriteString("</id>\n<title>")
		escaped(&b, e.Title)
		b.WriteString("</title>\n<updated>" + a.DateTime + "</updated>\n")
		if a.Link != "" {
			b.WriteString(`<link rel="alternate" type="text/html" href="`)
			escaped(&b, a.Link)
			b.WriteString(`"/>` + "\n")
		}
		for _, author := range e.Authors {
			b.WriteString("<author><name>")
			escaped(&b, author)
			b.WriteString("</name></author>\n")
		}
		if len(e.Authors) == 0 {
			// Atom wants an author of every entry or of the feed.
			b.WriteString("<author><name>")
			escaped(&b, a.Feed)
			b.WriteString("</name></author>\n")
		}
		for _, category := range categoriesOf(sh, a) {
			b.WriteString(`<category term="`)
			escaped(&b, category)
			b.WriteString(`"/>` + "\n")
		}
		_, files := enclosuresOf(e)
		for _, enc := range files {
			b.WriteString(`<link rel="enclosure" href="`)
			escaped(&b, enc.URL)
			if enc.Type != "" {
				b.WriteString(`" type="`)
				escaped(&b, enc.Type)
			}
			b.WriteString(`"/>` + "\n")
		}
		b.WriteString(`<content type="html">`)
		escaped(&b, e.Content)
		b.WriteString("</content>\n</entry>\n")
	}
	b.WriteString("</feed>\n")
	return b.Bytes()
}
