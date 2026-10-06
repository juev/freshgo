package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"

	"github.com/juev/freshgo/internal/sanitize"
)

// XML namespaces of the feed formats and their extensions.
const (
	nsAtom10  = "http://www.w3.org/2005/Atom"
	nsAtom03  = "http://purl.org/atom/ns#"
	nsRDF     = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	nsRSS090  = "http://my.netscape.com/rdf/simple/0.9/"
	nsRSS10   = "http://purl.org/rss/1.0/"
	nsRSS20   = ""
	nsContent = "http://purl.org/rss/1.0/modules/content/"
	nsDC10    = "http://purl.org/dc/elements/1.0/"
	nsDC11    = "http://purl.org/dc/elements/1.1/"
	nsMedia   = "http://search.yahoo.com/mrss/"
	nsITunes  = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	nsXHTML   = "http://www.w3.org/1999/xhtml"
	nsXML     = "http://www.w3.org/XML/1998/namespace"
)

// Misspellings of the Media RSS namespace found in feeds.
var mediaNamespaces = map[string]bool{
	"http://search.yahoo.com/mrss":        true,
	"http://video.search.yahoo.com/mrss":  true,
	"http://video.search.yahoo.com/mrss/": true,
	"http://www.rssboard.org/media-rss":   true,
	"http://www.rssboard.org/media-rss/":  true,
}

// node is an element of the feed document, in the shape SimplePie keeps it.
type node struct {
	space, name string
	// attrs holds the attributes without a namespace.
	attrs map[string]string
	// atomType is the type attribute in the Atom namespace, which some feeds
	// write instead of the plain one; about is rdf:about.
	atomType string
	about    string
	children []*node
	// data is the text of the element. For an XHTML construct it is the
	// markup of what the element contains. text collects it while the
	// element is being read.
	data string
	text []byte
	// base is the base URL in effect; explicit tells whether an xml:base on
	// this element or above set it.
	base     string
	explicit bool
}

func (n *node) all(space, name string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, c := range n.children {
		if c.space == space && c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *node) first(space, name string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.space == space && c.name == name {
			return c
		}
	}
	return nil
}

func (n *node) attr(name string) (string, bool) {
	v, ok := n.attrs[name]
	return v, ok
}

func normalizeSpace(space string) string {
	switch {
	case space == "xml":
		return nsXML
	case mediaNamespaces[space]:
		return nsMedia
	case strings.EqualFold(space, nsITunes):
		return nsITunes
	}
	return space
}

// xhtmlConstruct tells whether the content of the element is markup to be
// kept as text: Atom constructs of type xhtml and the title of RSS.
func xhtmlConstruct(n *node) bool {
	switch n.space {
	case nsAtom03:
		switch n.name {
		case "title", "tagline", "copyright", "info", "summary", "content":
			return n.attrs["mode"] == "xml"
		}
	case nsAtom10:
		switch n.name {
		case "rights", "subtitle", "summary", "info", "title", "content":
			return n.attrs["type"] == "xhtml"
		}
	case nsRSS20, nsRSS090, nsRSS10:
		return n.name == "title"
	}
	return false
}

var xhtmlVoid = map[string]bool{"area": true, "base": true, "basefont": true, "br": true,
	"col": true, "frame": true, "hr": true, "img": true, "input": true, "isindex": true,
	"link": true, "meta": true, "param": true}

var (
	quoteEscaper  = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#039;")
	compatEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

// parseTree reads the document into a tree. Text that is not valid XML in a
// harmless way (HTML entities, unknown prefixes) is accepted.
func parseTree(data []byte) (*node, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	// The input is UTF-8 by now, whatever its XML declaration says.
	d.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	root := &node{}
	stack := []*node{root}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("feed is not well-formed XML: %w", err)
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := startNode(t, top)
			top.children = append(top.children, n)
			if xhtmlConstruct(n) {
				if err := readMarkup(d, n); err != nil {
					return nil, fmt.Errorf("feed is not well-formed XML: %w", err)
				}
				continue
			}
			stack = append(stack, n)
		case xml.EndElement:
			top.data, top.text = string(top.text), nil
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			top.text = append(top.text, t...)
		}
	}
	return root, nil
}

func startNode(t xml.StartElement, parent *node) *node {
	n := &node{
		space:    normalizeSpace(t.Name.Space),
		name:     t.Name.Local,
		attrs:    map[string]string{},
		base:     parent.base,
		explicit: parent.explicit,
	}
	for _, a := range t.Attr {
		switch space := normalizeSpace(a.Name.Space); {
		case space == "":
			n.attrs[a.Name.Local] = a.Value
		case space == nsXML && a.Name.Local == "base":
			if base, ok := sanitize.Absolutize(a.Value, parent.base); ok {
				n.base, n.explicit = base, true
			}
		case space == nsAtom10 && a.Name.Local == "type":
			n.atomType = a.Value
		case space == nsRDF && a.Name.Local == "about":
			n.about = a.Value
		}
	}
	return n
}

// readMarkup consumes the content of an XHTML construct and stores it as
// markup: elements of the XHTML namespace as tags, everything else as text.
func readMarkup(d *xml.Decoder, n *node) error {
	var b strings.Builder
	var open []xml.Name
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			open = append(open, t.Name)
			if t.Name.Space != nsXHTML {
				continue
			}
			b.WriteByte('<')
			b.WriteString(t.Name.Local)
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local != "xmlns" {
					b.WriteString(" " + a.Name.Local + `="` + compatEscaper.Replace(a.Value) + `"`)
				}
			}
			b.WriteByte('>')
		case xml.EndElement:
			if len(open) == 0 {
				n.data = b.String()
				return nil
			}
			open = open[:len(open)-1]
			if t.Name.Space == nsXHTML && !xhtmlVoid[t.Name.Local] {
				b.WriteString("</" + t.Name.Local + ">")
			}
		case xml.CharData:
			b.WriteString(quoteEscaper.Replace(string(t)))
		}
	}
	n.data = b.String()
	return nil
}

var (
	xmlDeclaration = regexp.MustCompile(`^<\?xml[^>]*?encoding\s*=\s*["']([A-Za-z0-9._:-]+)["']`)
	// Characters XML forbids; feeds carry them now and then.
	controlChars = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`)
)

// toUTF8 returns the document as UTF-8 without what precedes its first tag.
// The encoding is taken from the byte order mark, then from the HTTP
// Content-Type, then from the XML declaration; a document that claims
// nothing and is not UTF-8 is read as windows-1252.
func toUTF8(data []byte, contentType string) []byte {
	declared := ""
	switch {
	case bytes.HasPrefix(data, []byte("\xEF\xBB\xBF")):
		data = data[3:]
		declared = "utf-8"
	case bytes.HasPrefix(data, []byte("\xFF\xFE")):
		declared = "utf-16le"
	case bytes.HasPrefix(data, []byte("\xFE\xFF")):
		declared = "utf-16be"
	}
	if declared == "" {
		if _, params, err := mime.ParseMediaType(contentType); err == nil {
			declared = params["charset"]
		}
	}
	if declared == "" {
		if m := xmlDeclaration.FindSubmatch(bytes.TrimLeft(data, " \t\r\n")); m != nil {
			declared = string(m[1])
		}
	}
	if declared == "" && !utf8.Valid(data) {
		declared = "windows-1252"
	}
	if enc, name := charset.Lookup(declared); enc != nil && name != "utf-8" {
		if decoded, err := enc.NewDecoder().Bytes(data); err == nil {
			data = decoded
		}
	}
	data = bytes.ToValidUTF8(data, []byte("\uFFFD"))
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if i := bytes.IndexByte(data, '<'); i > 0 {
		data = data[i:]
	}
	return controlChars.ReplaceAll(data, nil)
}
