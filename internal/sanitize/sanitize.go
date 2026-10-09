// Package sanitize cleans HTML that comes from feeds and web pages before it
// is stored: an allowlist of elements and attributes, absolute URLs, no
// scripts. The rules are the ones FreshRSS configures SimplePie with
// (FreshRSS_SimplePieCustom at commit 219eaf58); the HTML is parsed and
// written by golang.org/x/net/html instead of libxml.
package sanitize

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Attributes allowed on every element, besides data-* and aria-*.
var globalAttributes = set("dir", "draggable", "hidden", "lang", "role", "title",
	"displaystyle", "mathsize", "scriptlevel")

// Elements that are kept, with the attributes each of them may carry.
// Everything else is replaced by its content.
var allowedElements = map[string]map[string]bool{
	"a": set("href", "hreflang", "type"), "abbr": nil, "acronym": nil, "address": nil,
	"article": nil, "aside": nil, "audio": set("controlslist", "loop", "muted", "src"),
	"b": nil, "bdi": nil, "bdo": nil, "big": nil, "blink": nil, "blockquote": set("cite"),
	"br": set("clear"), "button": set("disabled"), "canvas": set("width", "height"),
	"caption": set("align"), "center": nil, "cite": nil, "code": nil,
	"col": set("span", "align", "valign", "width"), "colgroup": set("span", "align", "valign", "width"),
	"data": set("value"), "datalist": nil, "dd": nil, "del": set("cite", "datetime"),
	"details": set("open"), "dfn": nil, "dialog": nil, "dir": nil, "div": set("align"),
	"dl": nil, "dt": nil, "em": nil, "fieldset": set("disabled"), "figcaption": nil,
	"figure": nil, "footer": nil, "h1": nil, "h2": nil, "h3": nil, "h4": nil, "h5": nil,
	"h6": nil, "header": nil, "hgroup": nil, "hr": set("align", "noshade", "size", "width"),
	"i":      nil,
	"iframe": set("src", "align", "frameborder", "longdesc", "marginheight", "marginwidth", "scrolling", "allowfullscreen"),
	"image":  set("src", "alt", "width", "height", "align", "border", "hspace", "longdesc", "vspace"),
	"img":    set("src", "alt", "width", "height", "align", "border", "hspace", "longdesc", "vspace"),
	"ins":    set("cite", "datetime"), "kbd": nil, "label": nil, "legend": nil,
	"li": set("value", "type"), "main": nil, "mark": nil,
	"marquee": set("behavior", "direction", "height", "hspace", "loop", "scrollamount", "scrolldelay", "truespeed", "vspace", "width"),
	"menu":    nil, "meter": set("value", "min", "max", "low", "high", "optimum"), "nav": nil,
	"nobr": nil, "noframes": nil, "ol": set("reversed", "start", "type"),
	"optgroup": set("disabled", "label"), "option": set("disabled", "label", "selected", "value"),
	"output": nil, "p": set("align"), "picture": nil, "pre": set("width", "wrap"),
	"progress": set("max", "value"), "q": set("cite"), "rb": nil, "rp": nil, "rt": nil,
	"rtc": nil, "ruby": nil, "s": nil, "samp": nil, "search": nil, "section": nil,
	"select": set("disabled", "multiple", "size"), "small": nil,
	"source": set("type", "src", "media", "height", "width"), "span": nil, "strike": nil,
	"strong": nil, "sub": nil, "summary": nil, "sup": nil,
	"table":    set("align", "border", "cellpadding", "cellspacing", "rules", "summary", "width"),
	"tbody":    set("align", "char", "charoff", "valign"),
	"td":       set("colspan", "headers", "rowspan", "abbr", "align", "height", "scope", "valign", "width"),
	"textarea": set("cols", "disabled", "maxlength", "minlength", "placeholder", "readonly", "rows", "wrap"),
	"tfoot":    set("align", "valign"),
	"th":       set("abbr", "colspan", "rowspan", "scope", "align", "height", "valign", "width"),
	"thead":    set("align", "valign"), "time": set("datetime"), "tr": set("align", "valign"),
	"track": set("default", "kind", "srclang", "label", "src"), "tt": nil, "u": nil,
	"ul": set("type"), "var": nil,
	"video": set("src", "poster", "controlslist", "height", "loop", "muted", "playsinline", "width"),
	"wbr":   nil, "xmp": nil,
	// MathML
	"maction": set("actiontype", "selection"), "math": set("display"), "menclose": set("notation"),
	"merror": nil, "mfenced": set("close", "open", "separators"),
	"mfrac": set("denomalign", "linethickness", "numalign"), "mi": set("mathvariant"),
	"mmultiscripts": set("subscriptshift", "superscriptshift"), "mn": nil,
	"mo":    set("accent", "fence", "form", "largeop", "lspace", "maxsize", "minsize", "movablelimits", "rspace", "separator", "stretchy", "symmetric"),
	"mover": set("accent"), "mpadded": set("depth", "height", "lspace", "voffset", "width"),
	"mphantom": nil, "mprescripts": nil, "mroot": nil, "mrow": nil, "ms": nil,
	"mspace": set("depth", "height", "width"), "msqrt": nil, "msub": nil,
	"msubsup": set("subscriptshift", "superscriptshift"), "msup": set("superscriptshift"),
	"mtable": set("align", "columnalign", "columnlines", "columnspacing", "frame", "framespacing", "rowalign", "rowlines", "rowspacing", "width"),
	"mtd":    set("columnspan", "rowspan", "columnalign", "rowalign"), "mtext": nil,
	"mtr": set("columnalign", "rowalign"), "munder": set("accentunder"),
	"munderover": set("accent", "accentunder"),
}

// Elements that are not allowed and whose content goes with them.
var droppedWithContent = set("script", "style", "svg", "math", "template")

// data-* attributes that are dropped although data-* is allowed.
var strippedAttributes = set("data-auto-leave-validation", "data-leave-validation",
	"data-no-leave-validation", "data-original")

// Attributes set on every element of the kind, replacing what the feed gave.
var addedAttributes = map[string][]html.Attribute{
	"audio": {{Key: "controls", Val: "controls"}, {Key: "preload", Val: "none"}},
	"iframe": {
		{Key: "allow", Val: "accelerometer; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share"},
		{Key: "sandbox", Val: "allow-scripts allow-same-origin"},
		{Key: "allowfullscreen", Val: "allowfullscreen"},
	},
	"video": {{Key: "controls", Val: "controls"}, {Key: "preload", Val: "none"}},
}

// Attributes holding a URL: made absolute and checked for the scheme.
var urlAttributes = map[string][]string{
	"a": {"href"}, "audio": {"src"}, "blockquote": {"cite"}, "del": {"cite"},
	"iframe": {"src"}, "img": {"longdesc", "src"}, "image": {"longdesc", "src"},
	"ins": {"cite"}, "q": {"cite"}, "source": {"src"}, "track": {"src"},
	"video": {"poster", "src"},
}

// Attributes written without a value when it is empty.
var booleanAttributes = set("allowfullscreen", "checked", "default", "disabled", "hidden",
	"loop", "multiple", "muted", "noshade", "open", "playsinline", "readonly", "reversed",
	"selected", "truespeed")

var voidElements = set("area", "base", "br", "col", "embed", "hr", "img", "input", "link",
	"meta", "param", "source", "track", "wbr")

var documentTags = regexp.MustCompile(`(?is)</?(?:html|body)[^>]*?>`)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// HTML returns the fragment reduced to allowed markup. URLs are resolved
// against base; forceHTTPS, when not nil, gets every resulting URL and
// returns the one to store.
func HTML(fragment, base string, forceHTTPS func(string) string) string {
	root := clean(fragment, base, forceHTTPS)
	if root == nil {
		return ""
	}
	return strings.TrimSpace(inner(root))
}

// Keeping is HTML for a text with comments in it that have to stay: each of
// the comments, written exactly so, is left where it stands, and what is
// before, between and after them is cleaned apart.
func Keeping(fragment, base string, comments ...string) string {
	var b strings.Builder
	for {
		at, found := -1, ""
		for _, c := range comments {
			if i := strings.Index(fragment, c); i >= 0 && (at < 0 || i < at) {
				at, found = i, c
			}
		}
		if at < 0 {
			b.WriteString(HTML(fragment, base, nil))
			return b.String()
		}
		b.WriteString(HTML(fragment[:at], base, nil))
		b.WriteString(found)
		fragment = fragment[at+len(found):]
	}
}

// Marks FreshRSS puts around the text taken from the page of an article.
// "Remove the full text" looks for them, so they stay in a cleaned text.
const (
	FullContentStart = "<!-- FULLCONTENT start //-->"
	FullContentEnd   = "<!-- FULLCONTENT end //-->"
)

// Stored cleans the texts of an entry that were stored without being
// cleaned here: its text, and in its attributes the text the feed gave,
// which is kept there while a full text stands in its place. Relative
// addresses are resolved against base. It returns the two as they are to
// be stored and whether either changed.
//
// A text without markup is left as it is written: it holds nothing to
// clean, and parsing it would only write its character references anew.
func Stored(content string, attributes []byte, base string) (string, []byte, bool) {
	one := func(text string) string {
		if !strings.Contains(text, "<") {
			return text
		}
		return Keeping(text, base, FullContentStart, FullContentEnd)
	}
	changed := false
	if cleaned := one(content); cleaned != content {
		content, changed = cleaned, true
	}
	const key = "original_content"
	var attrs map[string]json.RawMessage
	var original string
	if json.Unmarshal(attributes, &attrs) == nil && json.Unmarshal(attrs[key], &original) == nil {
		if cleaned := one(original); cleaned != original {
			// A string and a map of raw values cannot fail to be written.
			attrs[key], _ = json.Marshal(cleaned)
			attributes, _ = json.Marshal(attrs)
			changed = true
		}
	}
	return content, attributes, changed
}

// XHTML is HTML for an Atom construct of type xhtml: its content is one div
// element, which is not part of the text. As in SimplePie, anything after
// the first node is ignored.
func XHTML(fragment, base string, forceHTTPS func(string) string) string {
	root := clean(fragment, base, forceHTTPS)
	if root == nil || root.FirstChild == nil {
		return ""
	}
	first := root.FirstChild
	if first.Type == html.ElementNode && first.Data == "div" {
		return strings.TrimSpace(inner(first))
	}
	var b strings.Builder
	write(&b, first)
	return strings.TrimSpace(b.String())
}

func inner(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		write(&b, c)
	}
	return b.String()
}

func clean(fragment, base string, forceHTTPS func(string) string) *html.Node {
	fragment = documentTags.ReplaceAllString(fragment, "")
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragmentWithOptions(strings.NewReader(fragment), body, html.ParseOptionEnableScripting(false))
	if err != nil {
		return nil
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	s := sanitizer{base: base, forceHTTPS: forceHTTPS}
	s.children(root)
	return root
}

type sanitizer struct {
	base       string
	forceHTTPS func(string) string
}

func (s *sanitizer) children(parent *html.Node) {
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling
		switch c.Type {
		case html.TextNode:
		case html.ElementNode:
			if first := s.element(parent, c); first != nil {
				next = first
			}
		default:
			parent.RemoveChild(c)
		}
		c = next
	}
}

// element cleans one element. When the element is replaced by its children,
// the first of them is returned: the walk continues from there.
func (s *sanitizer) element(parent, n *html.Node) *html.Node {
	tag := n.Data
	allowed, ok := allowedElements[tag]
	if !ok {
		if droppedWithContent[tag] {
			parent.RemoveChild(n)
			return nil
		}
		first := n.FirstChild
		for c := n.FirstChild; c != nil; c = n.FirstChild {
			n.RemoveChild(c)
			parent.InsertBefore(c, n)
		}
		next := n.NextSibling
		parent.RemoveChild(n)
		if first == nil {
			return next
		}
		return first
	}

	var id, class *string
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Namespace != "" {
			continue
		}
		switch {
		case a.Key == "id":
			id = &a.Val
		case a.Key == "class":
			class = &a.Val
		case strippedAttributes[a.Key]:
		case allowed[a.Key], globalAttributes[a.Key],
			strings.HasPrefix(a.Key, "data-"), strings.HasPrefix(a.Key, "aria-"):
			kept = append(kept, a)
		}
	}
	n.Attr = kept
	// id and class would clash with the page that shows the article.
	if id != nil {
		n.Attr = append(n.Attr, html.Attribute{Key: "data-sanitized-id", Val: *id})
	}
	if class != nil {
		n.Attr = append(n.Attr, html.Attribute{Key: "data-sanitized-class", Val: *class})
	}
	for _, added := range addedAttributes[tag] {
		setAttribute(n, added.Key, added.Val)
	}
	for _, name := range urlAttributes[tag] {
		for i := range n.Attr {
			if n.Attr[i].Key == name {
				n.Attr[i].Val = s.url(n.Attr[i].Val)
			}
		}
	}

	if tag == "iframe" {
		// What is inside an iframe element is fallback text, never shown.
		for c := n.FirstChild; c != nil; c = n.FirstChild {
			n.RemoveChild(c)
		}
		return nil
	}
	s.children(n)
	return nil
}

func (s *sanitizer) url(value string) string {
	absolute, ok := Absolutize(value, s.base)
	if !ok {
		// Browsers drop the control characters that made the URL unparsable,
		// so the scheme check must not be skipped.
		absolute = value
	}
	if !AllowedScheme(absolute) {
		return "unsafe:" + absolute
	}
	if s.forceHTTPS != nil {
		return s.forceHTTPS(absolute)
	}
	return absolute
}

func setAttribute(n *html.Node, key, value string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: value})
}

var (
	textEscaper      = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attributeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

// Markup writes a node as HTML the way libxml does, as far as that is known:
// void elements without a closing tag, quotes in text left alone, an
// attribute value with a double quote in single quotes. Scraped content is
// hashed in this form.
func Markup(n *html.Node) string {
	var b strings.Builder
	write(&b, n)
	return b.String()
}

func write(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.CommentNode:
		b.WriteString("<!--" + n.Data + "-->")
		return
	case html.DoctypeNode:
		return
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			write(b, c)
		}
		return
	}
	if n.Type == html.TextNode {
		// The text of script and style is not markup.
		if p := n.Parent; p != nil && p.Type == html.ElementNode && (p.Data == "script" || p.Data == "style") {
			b.WriteString(n.Data)
			return
		}
		b.WriteString(textEscaper.Replace(n.Data))
		return
	}
	b.WriteByte('<')
	b.WriteString(n.Data)
	for _, a := range n.Attr {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		if a.Val == "" && booleanAttributes[a.Key] {
			continue
		}
		// As libxml writes it: single quotes around a value with double ones.
		if strings.Contains(a.Val, `"`) && !strings.Contains(a.Val, "'") {
			b.WriteString("='")
			b.WriteString(textEscaper.Replace(a.Val))
			b.WriteByte('\'')
			continue
		}
		b.WriteString(`="`)
		b.WriteString(attributeEscaper.Replace(a.Val))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	if voidElements[n.Data] {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		write(b, c)
	}
	b.WriteString("</")
	b.WriteString(n.Data)
	b.WriteByte('>')
}

// Text returns the beginning of the text of an HTML fragment for a line that
// tells what the fragment is about: no markup, runs of white space as one
// space, at most limit characters, the last of them an ellipsis when the text
// goes on.
func Text(fragment string, limit int) string {
	var text []rune
	space, skipped := false, 0
	tokens := html.NewTokenizer(strings.NewReader(fragment))
	// One character more than fits tells whether the text goes on.
	for len(text) <= limit {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.TextToken {
			// Markup between two words parts them, as it does on a page.
			space = true
			name, _ := tokens.TagName()
			if tag := atom.Lookup(name); tag == atom.Script || tag == atom.Style {
				if kind == html.StartTagToken {
					skipped++
				} else if kind == html.EndTagToken && skipped > 0 {
					skipped--
				}
			}
			continue
		}
		if skipped > 0 {
			continue
		}
		for _, r := range html.UnescapeString(string(tokens.Raw())) {
			if unicode.IsSpace(r) {
				space = true
				continue
			}
			if space && len(text) > 0 {
				text = append(text, ' ')
			}
			space = false
			text = append(text, r)
		}
	}
	if len(text) <= limit {
		return string(text)
	}
	return strings.TrimRight(string(text[:limit-1]), " ") + "…"
}
