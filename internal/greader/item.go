package greader

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/juev/freshgo/internal/store"
)

const (
	itemPrefix = "tag:google.com,2005:reader/item/"
	// maxContent is the size, in bytes, an entry's text is cut to: some
	// clients fail on more.
	maxContent = 500000
	// maxTitleFromText is how much of the text stands in for a missing title.
	maxTitleFromText = 75
)

type link struct {
	Href string `json:"href"`
}

type origin struct {
	StreamID string `json:"streamId"`
	HTMLURL  string `json:"htmlUrl"`
	Title    string `json:"title"`
}

type content struct {
	Content string `json:"content"`
}

type media struct {
	Href   string `json:"href"`
	Type   string `json:"type"`
	Length int64  `json:"length,omitempty"`
}

// item is an entry as the API shows it.
type item struct {
	ID            string   `json:"id"`
	CrawlTimeMsec string   `json:"crawlTimeMsec"`
	TimestampUsec string   `json:"timestampUsec"`
	Published     int64    `json:"published"`
	Title         string   `json:"title"`
	Canonical     []link   `json:"canonical"`
	Alternate     []link   `json:"alternate"`
	Categories    []string `json:"categories"`
	Origin        origin   `json:"origin"`
	Summary       content  `json:"summary"`
	Enclosure     []media  `json:"enclosure,omitempty"`
	Author        string   `json:"author,omitempty"`
}

// newItem shows an entry of the feed f, which is in the category named
// category; labels are the names of the user's labels on the entry.
func newItem(e *store.Entry, f *store.Feed, category string, labels []string) item {
	attrs := readEntryAttributes(e.Attributes)
	href := entryLink(e)
	it := item{
		ID: itemPrefix + fmt.Sprintf("%016x", e.ID),
		// The identifier is the time the entry was added, in microseconds.
		CrawlTimeMsec: strconv.FormatInt(e.ID/1000, 10),
		TimestampUsec: strconv.FormatInt(e.ID, 10),
		Published:     e.Published,
		Title:         alternative(entryTitle(e), false),
		Canonical:     []link{{href}},
		Alternate:     []link{{href}},
		Categories:    []string{stateReadingList, labelPrefix + category},
		Origin: origin{
			StreamID: feedPrefix + strconv.FormatInt(f.ID, 10),
			HTMLURL:  f.Website,
			Title:    alternative(feedName(f), true),
		},
		Author: alternative(strings.Trim(strings.Join(e.Authors, "; "), "; "), false),
	}
	text := e.Content
	if show, set := feedFlag(f, "display_enclosures"); show || !set {
		text = withEnclosures(text, attrs)
	}
	it.Summary.Content = cutBytes(text, maxContent)

	switch {
	case f.Priority >= priorityImportant:
		it.Categories = append(it.Categories, stateMain, stateImportant)
	case f.Priority >= priorityMain:
		it.Categories = append(it.Categories, stateMain)
	case f.Priority <= priorityHidden:
		it.Categories = append(it.Categories, stateHidden)
	}
	for _, enc := range enclosures(e.Content, attrs) {
		address, _ := enc["url"].(string)
		if address == "" {
			continue
		}
		m := media{Href: address, Length: number(enc["length"])}
		switch mediaType, isType := enc["type"].(string); {
		case isType:
			m.Type = mediaType
		case enc["medium"] != nil:
			m.Type, _ = enc["medium"].(string)
		case isImage(enc):
			m.Type = "image"
		}
		it.Enclosure = append(it.Enclosure, m)
	}
	if e.IsRead {
		it.Categories = append(it.Categories, stateRead)
	}
	if e.IsFavorite {
		it.Categories = append(it.Categories, stateStarred)
	}
	for _, name := range labels {
		it.Categories = append(it.Categories, labelPrefix+name)
	}
	// What the feed filed the entry under goes in as it is.
	it.Categories = append(it.Categories, e.Tags...)
	return it
}

// feedFlag reads a boolean attribute of a feed.
func feedFlag(f *store.Feed, name string) (value, set bool) {
	var attrs map[string]json.RawMessage
	if json.Unmarshal(f.Attributes, &attrs) != nil {
		return false, false
	}
	if json.Unmarshal(attrs[name], &value) != nil {
		return false, false
	}
	return value, true
}

// entryAttributes are the attributes of an entry the API shows. Enclosures
// is nil when the entry was stored without any list of them.
type entryAttributes struct {
	Thumbnail  map[string]any
	Enclosures []map[string]any
}

func readEntryAttributes(raw json.RawMessage) entryAttributes {
	var stored struct {
		Thumbnail  json.RawMessage `json:"thumbnail"`
		Enclosures json.RawMessage `json:"enclosures"`
	}
	var attrs entryAttributes
	if json.Unmarshal(raw, &stored) != nil {
		return attrs
	}
	// A value of another shape is no value.
	_ = json.Unmarshal(stored.Thumbnail, &attrs.Thumbnail)
	var list []json.RawMessage
	if json.Unmarshal(stored.Enclosures, &list) == nil && list != nil {
		attrs.Enclosures = []map[string]any{}
		for _, one := range list {
			var enc map[string]any
			if json.Unmarshal(one, &enc) == nil && enc != nil {
				attrs.Enclosures = append(attrs.Enclosures, enc)
			}
		}
	}
	return attrs
}

// number reads an attribute FreshRSS takes for a number: a number, or a
// string of digits.
func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

// specialChars undoes PHP htmlspecialchars.
var specialChars = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'", "&#x27;", "'")

// entryLink is the address of an entry: its link, or its identifier when it
// has no link and the identifier is an address.
func entryLink(e *store.Entry) string {
	if e.Link != "" {
		return e.Link
	}
	guid := specialChars.Replace(e.GUID)
	if u, err := url.Parse(guid); err == nil && u.Scheme != "" && u.Host != "" {
		return guid
	}
	return ""
}

// entryTitle is the title of an entry. One without a title is called by the
// beginning of its text, or by its identifier when it has no text either.
func entryTitle(e *store.Entry) string {
	if e.Title != "" {
		return e.Title
	}
	text := phpTrim(stripTags(e.Content))
	title := text
	if utf8.RuneCountInString(title) > maxTitleFromText {
		title = string([]rune(title)[:maxTitleFromText])
	}
	title = phpTrim(title)
	switch {
	case title == "":
		title = e.GUID
	case len(text) > len(title):
		title += "…"
	}
	// The text and the identifier still carry their HTML encoding.
	return specialChars.Replace(title)
}

// stripTags returns the text of an HTML fragment as written, without tags
// and comments.
func stripTags(fragment string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(fragment))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			b.Write(z.Raw())
		}
	}
}

// cutBytes shortens s to at most limit bytes without splitting a character.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

var remoteURI = regexp.MustCompile(`(?i)^https?://`)

// quoted reports whether the markup already mentions the address in quotes.
func quoted(markup, address string) bool {
	return strings.Contains(markup, `"`+address+`"`) || strings.Contains(markup, `'`+address+`'`)
}

var imageExtension = regexp.MustCompile(`(?i)[.](avif|gif|jpe?g|png|svg|webp)([?#]|$)`)

// isImage reports whether an enclosure is a picture, by what it says about
// itself or by the extension of its address.
func isImage(enc map[string]any) bool {
	address, _ := enc["url"].(string)
	medium, _ := enc["medium"].(string)
	mime, _ := enc["type"].(string)
	return address != "" && medium == "image" || strings.HasPrefix(mime, "image") ||
		mime == "" && number(enc["length"]) == 0 && imageExtension.MatchString(address)
}

// withEnclosures returns the text of an entry followed by its thumbnail and
// enclosures as markup, the way FreshRSS_Entry::content() adds them: a
// client that shows only the text still shows the attachments.
func withEnclosures(text string, attrs entryAttributes) string {
	if address, _ := attrs.Thumbnail["url"].(string); remoteURI.MatchString(address) && !quoted(text, address) {
		text += "<figure class=\"enclosure\">\n\t<p class=\"enclosure-content\">\n" +
			"\t\t<img class=\"enclosure-thumbnail\" src=\"" + address + "\" alt=\"\" />\n\t</p>\n</figure>"
	}
	for _, enc := range attrs.Enclosures {
		address, _ := enc["url"].(string)
		if !remoteURI.MatchString(address) || quoted(text, address) {
			continue
		}
		var (
			length    = number(enc["length"])
			medium, _ = enc["medium"].(string)
			mime, _   = enc["type"].(string)
			title, _  = enc["title"].(string)
		)
		var b strings.Builder
		b.WriteString("\n<figure class=\"enclosure\">")
		if thumbnails, _ := enc["thumbnails"].([]any); thumbnails != nil {
			for _, t := range thumbnails {
				if thumbnail, _ := t.(string); remoteURI.MatchString(thumbnail) {
					b.WriteString(`<p><img class="enclosure-thumbnail" src="` + thumbnail + `" alt="" title="` + title + `" /></p>`)
				}
			}
		}
		// Attributes of a player: each ends the one before it.
		player := func(tag string) {
			b.WriteString(`<p class="enclosure-content"><` + tag + ` preload="none" src="` + address)
			if length != 0 {
				b.WriteString(`" data-length="` + strconv.FormatInt(length, 10))
			}
			if mime != "" {
				b.WriteString(`" data-type="` + escapeAttribute(mime))
			}
			b.WriteString(`" controls="controls" title="` + title + `"></` + tag + `> <a download="" href="` + address + `">💾</a></p>`)
		}
		switch {
		case isImage(map[string]any{"url": address, "length": float64(length), "medium": medium, "type": mime}):
			b.WriteString(`<p class="enclosure-content"><img src="` + address + `" alt="" title="` + title + `" /></p>`)
		case medium == "audio" || strings.HasPrefix(mime, "audio"):
			player("audio")
		case medium == "video" || strings.HasPrefix(mime, "video"):
			player("video")
		default: // an application, a text, something unknown
			b.WriteString(`<p class="enclosure-content"><a download="" href="` + address)
			if mime != "" {
				b.WriteString(`" data-type="` + escapeAttribute(mime))
			}
			if medium != "" {
				b.WriteString(`" data-medium="` + escapeAttribute(medium))
			}
			b.WriteString(`" title="` + title + `">💾</a></p>`)
		}
		for _, credit := range stringList(enc["credit"]) {
			b.WriteString(`<p class="enclosure-credits">© ` + credit + `</p>`)
		}
		if description, _ := enc["description"].(string); description != "" {
			b.WriteString(`<figcaption class="enclosure-description">` + lineBreaks.ReplaceAllString(description, "<br />$0") + `</figcaption>`)
		}
		b.WriteString("</figure>\n")
		text += b.String()
	}
	return text
}

// lineBreaks are what PHP nl2br() puts a <br /> in front of.
var lineBreaks = regexp.MustCompile(`\r\n|\n\r|\n|\r`)

// escapeAttribute is PHP htmlspecialchars() with ENT_COMPAT.
func escapeAttribute(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// stringList reads an attribute that is a string or a list of strings.
func stringList(v any) []string {
	switch value := v.(type) {
	case string:
		if value != "" {
			return []string{value}
		}
	case []any:
		var list []string
		for _, one := range value {
			if s, ok := one.(string); ok {
				list = append(list, s)
			}
		}
		return list
	}
	return nil
}

// enclosures lists the attachments of an entry: those stored with it, or,
// for an entry stored by a FreshRSS older than 1.20.1, those written into
// its text.
func enclosures(text string, attrs entryAttributes) []map[string]any {
	if attrs.Enclosures != nil || !strings.Contains(text, `<p class="enclosure-content`) {
		return attrs.Enclosures
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(text))
	if err != nil {
		return nil
	}
	var found []map[string]any
	doc.Find(`div[class="enclosure"] > p[class="enclosure-content"] > [src]`).Each(func(_ int, node *goquery.Selection) {
		enc := map[string]any{
			"url":    escapeAttribute(node.AttrOr("src", "")),
			"type":   escapeAttribute(node.AttrOr("data-type", "")),
			"medium": escapeAttribute(node.AttrOr("data-medium", "")),
		}
		if length, err := strconv.ParseInt(node.AttrOr("data-length", ""), 10, 64); err == nil {
			enc["length"] = float64(length)
		}
		if enc["medium"] == "" {
			switch goquery.NodeName(node) {
			case "img":
				enc["medium"] = "image"
			case "video", "audio":
				enc["medium"] = goquery.NodeName(node)
			}
		}
		found = append(found, enc)
	})
	return found
}
