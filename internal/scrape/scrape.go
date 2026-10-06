// Package scrape turns documents that are not feeds into one: an HTML or XML
// page read with XPath, a JSON Feed, a JSON document read with dot notation,
// JSON embedded in an HTML page. These are the feed kinds 10 to 35 of
// FreshRSS.
//
// As in FreshRSS, the result is an RSS 2.0 document to be handed to
// feed.Parse, so that links, text and identifiers go through the same rules
// as those of a real feed. The behaviour follows FreshRSS_Feed::loadHtmlXpath,
// loadJson and FreshRSS_dotNotation_Util at commit 219eaf58.
package scrape

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // the key format of FreshRSS, not a security measure
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/feed"
)

// Feed kinds, the values of feed.kind.
const (
	KindHTMLXPath       = 10
	KindXMLXPath        = 15
	KindJSONFeed        = 25
	KindJSONDotNotation = 30
	KindHTMLXPathJSON   = 35
)

var (
	// ErrSettings is returned when the feed lacks the settings its kind
	// needs, or they cannot be used: no item path, an XPath that does not
	// compile or that the XPath library cannot evaluate.
	ErrSettings = errors.New("feed scraping settings are unusable")
	// ErrDocument is returned when the document is not of the expected
	// format.
	ErrDocument = errors.New("document cannot be read")
	// ErrNoItems is returned when the document has none of the items the
	// settings describe.
	ErrNoItems = errors.New("no items found")
)

// Source describes the feed a document belongs to.
type Source struct {
	// Kind is one of the Kind constants.
	Kind int
	// URL is the address the document finally came from, after redirects.
	URL string
	// Name is the name of the feed, the title of the result when the
	// settings give none.
	Name string
	// Attributes are the feed's attributes: the keys xpath, json_dotnotation
	// and xPathToJson are read.
	Attributes json.RawMessage
	// ContentType is the HTTP Content-Type of the document, for the
	// character encoding of HTML.
	ContentType string
	// Location is the time zone of dates that name none; nil for UTC.
	Location *time.Location
}

// item is one scraped entry before it is written out. Values are plain text,
// content is HTML.
type item struct {
	title, content, link, author, timestamp, thumbnail, guid string
	tags                                                     []string
}

// RSS returns the document as RSS 2.0.
func RSS(body []byte, src Source) ([]byte, error) {
	if src.Location == nil {
		src.Location = time.UTC
	}
	var settings struct {
		XPath       map[string]string `json:"xpath"`
		DotNotation map[string]string `json:"json_dotnotation"`
		XPathToJSON string            `json:"xPathToJson"`
	}
	if len(src.Attributes) > 0 {
		// Settings of other kinds may be left over in forms that do not
		// fit; only a document that is not an object is an error.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(src.Attributes, &raw); err != nil {
			return nil, fmt.Errorf("%w: attributes: %v", ErrSettings, err)
		}
		_ = json.Unmarshal(raw["xpath"], &settings.XPath)
		_ = json.Unmarshal(raw["json_dotnotation"], &settings.DotNotation)
		_ = json.Unmarshal(raw["xPathToJson"], &settings.XPathToJSON)
	}

	var doc *document
	var err error
	switch src.Kind {
	case KindHTMLXPath, KindXMLXPath:
		doc, err = scrapeXPath(body, src, settings.XPath)
	case KindJSONFeed:
		doc, err = scrapeJSON(body, src, jsonFeedNotation)
	case KindJSONDotNotation:
		doc, err = scrapeJSON(body, src, settings.DotNotation)
	case KindHTMLXPathJSON:
		var extracted []byte
		if extracted, err = extractJSON(body, src, settings.XPathToJSON); err == nil {
			doc, err = scrapeJSON(extracted, src, settings.DotNotation)
		}
	default:
		return nil, fmt.Errorf("%w: feed kind %d is not scraped", ErrSettings, src.Kind)
	}
	if err != nil {
		return nil, err
	}
	return doc.rss(src), nil
}

// document is the scraped content of a page.
type document struct {
	title string
	// base is the href of the page's base element.
	base  string
	items []item
}

// finish gives the item its key and tells whether it is worth keeping:
// FreshRSS drops an item with no title, no content and no link.
func (it *item) finish() bool {
	if it.guid == "" || it.guid == "0" {
		sum := sha1.Sum([]byte(it.title + it.content + it.link)) //nolint:gosec // see the import
		it.guid = "urn:sha1:" + hex.EncodeToString(sum[:])
	}
	return it.title != "" || it.content != "" || it.link != ""
}

var (
	authorSemicolon = regexp.MustCompile(`\s*;\s*`)
	authorComma     = regexp.MustCompile(`\s*,\s*`)
)

// splitAuthors is FreshRSS_Entry::_authors for a string that has been
// HTML-encoded: an encoded character brings a semicolon of its own, which
// decides how the string is split.
func splitAuthors(author string) []string {
	separator := authorComma
	if strings.Contains(author, ";") || strings.ContainsAny(author, `&<>"`) {
		separator = authorSemicolon
	}
	var out []string
	for _, name := range separator.Split(author, -1) {
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func phpTrim(s string) string {
	return strings.Trim(s, " \n\r\t\v\x00")
}

func escape(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

// rss writes the document the way the index/rss view of FreshRSS renders
// scraped entries.
func (d *document) rss(src Source) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:media="http://search.yahoo.com/mrss/"`)
	if d.base != "" {
		b.WriteString(` xml:base="`)
		escape(&b, d.base)
		b.WriteByte('"')
	}
	b.WriteString(">\n<channel>\n<title>")
	escape(&b, d.title)
	b.WriteString("</title>\n<link>")
	escape(&b, src.URL)
	b.WriteString("</link>\n<atom:link href=\"")
	escape(&b, src.URL)
	b.WriteString("\" rel=\"self\" type=\"application/rss+xml\"/>\n")
	for _, it := range d.items {
		guid := phpTrim(it.guid)
		b.WriteString("<item>\n<title>")
		escape(&b, phpTrim(it.title))
		b.WriteString("</title>\n<link>")
		escape(&b, phpTrim(it.link))
		b.WriteString("</link>\n")
		for _, author := range splitAuthors(it.author) {
			b.WriteString("<dc:creator>")
			escape(&b, author)
			b.WriteString("</dc:creator>\n")
		}
		for _, tag := range it.tags {
			b.WriteString("<category>")
			escape(&b, tag)
			b.WriteString("</category>\n")
		}
		if it.thumbnail != "" {
			// The thumbnail is listed as an attached image as well.
			b.WriteString(`<media:thumbnail url="`)
			escape(&b, it.thumbnail)
			b.WriteString("\"/>\n<media:content url=\"")
			escape(&b, it.thumbnail)
			b.WriteString("\" medium=\"image\"></media:content>\n")
		}
		b.WriteString("<description>")
		escape(&b, it.content)
		b.WriteString("</description>\n")
		if it.timestamp != "" {
			b.WriteString("<pubDate>")
			escape(&b, it.timestamp)
			b.WriteString("</pubDate>\n")
		}
		b.WriteString(`<guid isPermaLink="false">`)
		escape(&b, guid)
		b.WriteString("</guid>\n</item>\n")
	}
	b.WriteString("</channel>\n</rss>\n")
	return b.Bytes()
}

// timestamp reads a scraped date: by the feed's time format when there is
// one and the date fits it, else the way PHP strtotime would. The result is
// in RFC 3339, empty when the date cannot be read.
func timestamp(value, format string, location *time.Location) string {
	if format != "" {
		// A Unix time in milliseconds.
		if format == "U" && len(value) > 10 {
			value = value[:len(value)-3]
		}
		if t, ok := parsePHPDate(format, value, location); ok {
			return rfc3339(t.Unix())
		}
	}
	if seconds, ok := feed.Strtotime(value, location); ok {
		return rfc3339(seconds)
	}
	return ""
}

// rfc3339 formats a Unix time; FreshRSS takes 0 and 1 for "no date".
func rfc3339(seconds int64) string {
	if seconds <= 1 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}
