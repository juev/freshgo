package scrape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/juev/freshgo/internal/sanitize"
)

// page is a parsed HTML or XML document that XPath expressions run on.
type page struct {
	root xpath.NodeNavigator
	// markup returns the node as markup.
	markup func(xpath.NodeNavigator) string
}

func parseHTML(body []byte, contentType string) (*page, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDocument, err)
	}
	// Without scripting, as libxml reads it: what noscript holds is markup.
	root, err := html.ParseWithOptions(r, html.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDocument, err)
	}
	return &page{
		root: htmlquery.CreateXPathNavigator(root),
		markup: func(nav xpath.NodeNavigator) string {
			n, ok := nav.(*htmlquery.NodeNavigator)
			if !ok || nav.NodeType() == xpath.AttributeNode {
				return attributeMarkup(nav)
			}
			return sanitize.Markup(n.Current())
		},
	}, nil
}

func parseXML(body []byte) (*page, error) {
	root, err := xmlquery.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDocument, err)
	}
	return &page{
		root: xmlquery.CreateXPathNavigator(root),
		markup: func(nav xpath.NodeNavigator) string {
			n, ok := nav.(*xmlquery.NodeNavigator)
			if !ok || nav.NodeType() == xpath.AttributeNode {
				return attributeMarkup(nav)
			}
			return xmlMarkup(n.Current())
		},
	}, nil
}

// xmlMarkup writes an XML node the way libxml's HTML serializer does: the
// elements HTML knows as empty without a closing tag, every other one with
// it, quotes in text left alone.
func xmlMarkup(n *xmlquery.Node) string {
	var b strings.Builder
	var write func(*xmlquery.Node)
	write = func(n *xmlquery.Node) {
		switch n.Type {
		case xmlquery.TextNode, xmlquery.CharDataNode:
			b.WriteString(markupEscaper.Replace(n.Data))
		case xmlquery.CommentNode:
			b.WriteString("<!--" + n.Data + "-->")
		case xmlquery.ElementNode:
			name := n.Data
			if n.Prefix != "" {
				name = n.Prefix + ":" + name
			}
			b.WriteString("<" + name)
			for _, a := range n.Attr {
				key := a.Name.Local
				if a.Name.Space != "" {
					key = a.Name.Space + ":" + key
				}
				b.WriteString(" " + key + `="` + html.EscapeString(a.Value) + `"`)
			}
			b.WriteByte('>')
			if htmlVoid[name] {
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				write(c)
			}
			b.WriteString("</" + name + ">")
		}
	}
	write(n)
	return b.String()
}

var htmlVoid = map[string]bool{"area": true, "base": true, "basefont": true, "br": true,
	"col": true, "embed": true, "frame": true, "hr": true, "img": true, "input": true,
	"isindex": true, "link": true, "meta": true, "param": true, "source": true,
	"track": true, "wbr": true}

var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// attributeMarkup writes an attribute node the way libxml does.
func attributeMarkup(nav xpath.NodeNavigator) string {
	return " " + nav.LocalName() + `="` + html.EscapeString(nav.Value()) + `"`
}

// evaluate runs an expression at a node. The XPath library panics on some
// expressions and does not implement all of XPath 1.0: both are reported as
// unusable settings instead of taking the process down.
func evaluate(expression string, at xpath.NodeNavigator) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("%w: XPath %q: %v", ErrSettings, expression, r)
		}
	}()
	compiled, err := xpath.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("%w: XPath %q: %v", ErrSettings, expression, err)
	}
	result = compiled.Evaluate(at.Copy())
	// A node set is read to its end here, inside the recover.
	if nodes, ok := result.(*xpath.NodeIterator); ok {
		var list []xpath.NodeNavigator
		for nodes.MoveNext() {
			list = append(list, nodes.Current().Copy())
		}
		return list, nil
	}
	return result, nil
}

// str is the string value of an expression with whitespace normalized,
// empty when the expression is.
func str(expression string, at xpath.NodeNavigator) (string, error) {
	if expression == "" {
		return "", nil
	}
	result, err := evaluate("normalize-space("+expression+")", at)
	if err != nil {
		return "", err
	}
	s, _ := result.(string)
	return s, nil
}

// scrapeXPath is FreshRSS_Feed::loadHtmlXpath.
func scrapeXPath(body []byte, src Source, settings map[string]string) (*document, error) {
	if settings["item"] == "" {
		return nil, fmt.Errorf("%w: no XPath to the items", ErrSettings)
	}
	var p *page
	var err error
	if src.Kind == KindXMLXPath {
		p, err = parseXML(body)
	} else {
		p, err = parseHTML(body, src.ContentType)
	}
	if err != nil {
		return nil, err
	}

	doc := &document{title: src.Name}
	if settings["feedTitle"] != "" {
		if doc.title, err = str(settings["feedTitle"], p.root); err != nil {
			return nil, err
		}
	}
	if doc.base, err = str("//base/@href", p.root); err != nil {
		return nil, err
	}
	doc.base = strings.TrimSpace(doc.base)
	// FreshRSS gives an HTML page without a base element one with its own
	// address: relative URLs in the content are resolved against the page.
	if doc.base == "" && src.Kind == KindHTMLXPath {
		doc.base = src.URL
	}
	// Items nested in one another with content that is the item itself
	// multiply the page; the output is held to a multiple of the input.
	budget := 16*len(body) + 1<<20

	found, err := evaluate(settings["item"], p.root)
	if err != nil {
		return nil, err
	}
	nodes, _ := found.([]xpath.NodeNavigator)
	if len(nodes) == 0 {
		return nil, ErrNoItems
	}
	for _, node := range nodes {
		var it item
		fields := []struct {
			to      *string
			setting string
		}{
			{&it.title, "itemTitle"}, {&it.link, "itemUri"}, {&it.author, "itemAuthor"},
			{&it.timestamp, "itemTimestamp"}, {&it.thumbnail, "itemThumbnail"}, {&it.guid, "itemUid"},
		}
		for _, f := range fields {
			if *f.to, err = str(settings[f.setting], node); err != nil {
				return nil, err
			}
		}
		it.timestamp = timestamp(it.timestamp, settings["itemTimeFormat"], src.Location)

		if expression := settings["itemContent"]; expression != "" {
			result, err := evaluate(expression, node)
			if err != nil {
				return nil, err
			}
			switch c := result.(type) {
			case []xpath.NodeNavigator:
				// A list of nodes is kept as markup.
				var content strings.Builder
				for _, child := range c {
					content.WriteString(p.markup(child) + "\n")
				}
				it.content = content.String()
			case string:
				it.content = c
			case bool:
				if c {
					it.content = "1"
				}
			}
		}
		if expression := settings["itemCategories"]; expression != "" {
			result, err := evaluate(expression, node)
			if err != nil {
				return nil, err
			}
			switch c := result.(type) {
			case string:
				if c != "" {
					it.tags = []string{c}
				}
			case []xpath.NodeNavigator:
				for _, tag := range c {
					it.tags = append(it.tags, tag.Value())
				}
			}
		}
		if budget -= len(it.content); budget < 0 {
			return nil, fmt.Errorf("%w: the items repeat the page too many times over", ErrDocument)
		}
		if it.finish() {
			doc.items = append(doc.items, it)
		}
	}
	return doc, nil
}

// extractJSON is FreshRSS_Feed::extractJsonFromHtml: the JSON an XPath
// expression finds in an HTML page. A string result is the JSON text; a list
// of nodes gives a JSON list of what each node holds.
func extractJSON(body []byte, src Source, expression string) ([]byte, error) {
	if expression == "" {
		return nil, fmt.Errorf("%w: no XPath to the JSON", ErrSettings)
	}
	p, err := parseHTML(body, src.ContentType)
	if err != nil {
		return nil, err
	}
	result, err := evaluate(expression, p.root)
	if err != nil {
		return nil, err
	}
	switch r := result.(type) {
	case string:
		return []byte(r), nil
	case []xpath.NodeNavigator:
		var fragments []json.RawMessage
		for _, node := range r {
			fragment := strings.TrimSpace(node.Value())
			// Only objects and lists count, as for PHP json_decode into an array.
			if json.Valid([]byte(fragment)) && (strings.HasPrefix(fragment, "{") || strings.HasPrefix(fragment, "[")) {
				fragments = append(fragments, json.RawMessage(fragment))
			}
		}
		if len(r) == 0 {
			return nil, ErrNoItems
		}
		if fragments == nil {
			fragments = []json.RawMessage{}
		}
		return json.Marshal(fragments)
	}
	return nil, ErrNoItems
}
