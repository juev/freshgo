// Package opml writes the subscriptions of a user as OPML and reads them
// back, with the settings of feeds in the frss: attributes of FreshRSS.
//
// The format follows app/views/helpers/export/opml.phtml and
// FreshRSS_Import_Service of FreshRSS at commit 219eaf58. See
// docs/specs/greader-api.md.
package opml

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// namespace is where the frss: attributes are defined.
const namespace = "https://freshrss.org/opml"

// kindDynamicOPML is the kind of a category that mirrors a remote OPML.
const kindDynamicOPML = 2

// Values of the type attribute by kind of feed. Feeds read as RSS or Atom
// are "rss".
var kindTypes = map[int]string{
	scrape.KindHTMLXPath:       "HTML+XPath",
	scrape.KindXMLXPath:        "XML+XPath",
	scrape.KindJSONFeed:        "JSONFeed",
	scrape.KindJSONDotNotation: "JSON+DotNotation",
	scrape.KindHTMLXPathJSON:   "HTML+XPath+JSON+DotNotation",
}

// Values of frss:priority; the main stream is the default and is not written.
var priorityNames = map[int]string{20: "important", 0: "category", -5: "feed", -10: "hidden"}

// scrapeFields are the settings of scraped feeds: the key in the attributes
// of the feed, which is also the end of the attribute name after frss:xPath
// or frss:json, with the first letter in upper case.
var scrapeFields = []string{
	"item", "itemTitle", "itemContent", "itemUri", "itemAuthor", "itemTimestamp", "itemTimeFormat",
	"itemThumbnail", "itemCategories", "itemUid",
}

// Keys of feed.attributes.curl_params, the numeric values of CURLOPT_*, by
// the name of their attribute, in the order they are written.
var curlFields = []struct{ name, key string }{
	{"CURLOPT_COOKIE", "10022"},
	{"CURLOPT_COOKIEFILE", "10031"},
	{"CURLOPT_FOLLOWLOCATION", "52"},
	{"CURLOPT_MAXREDIRS", "68"},
	{"CURLOPT_POST", "47"},
	{"CURLOPT_POSTFIELDS", "10015"},
	{"CURLOPT_PROXY", "10004"},
	{"CURLOPT_PROXYTYPE", "101"},
	{"CURLOPT_USERAGENT", "10018"},
}

const curlHTTPHeader = "10023"

// attribute is one attribute of an outline.
type attribute struct{ name, value string }

type attributes map[string]json.RawMessage

func readAttributes(raw json.RawMessage) attributes {
	var a attributes
	if json.Unmarshal(raw, &a) != nil || a == nil {
		return attributes{}
	}
	return a
}

func (a attributes) text(key string) string {
	var s string
	_ = json.Unmarshal(a[key], &s)
	return s
}

// scalar writes a string, a number or a boolean the way an OPML attribute
// holds it; anything else is "".
func scalar(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch value := v.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return ""
}

// lines joins a list of strings into the lines of one attribute.
func lines(raw json.RawMessage) string {
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var schemePrefix = regexp.MustCompile(`(?i)^https?://(www[.])?`)

// displayName is the name of a feed, or its address without the scheme when
// it has none.
func displayName(f *store.Feed) string {
	if f.Name != "" {
		return f.Name
	}
	return schemePrefix.ReplaceAllString(f.URL, "")
}

// outline returns the attributes of the outline of a feed, in the order
// FreshRSS writes them. Empty ones are left out.
func outline(f *store.Feed) []attribute {
	attrs := readAttributes(f.Attributes)
	kind, known := kindTypes[f.Kind]
	if !known {
		kind = "rss"
	}
	out := []attribute{
		{"text", displayName(f)}, {"type", kind}, {"xmlUrl", f.URL}, {"htmlUrl", f.Website}, {"description", f.Description},
		{"frss:priority", priorityNames[f.Priority]},
	}
	if criteria := attrs.text("unicityCriteria"); criteria != "id" {
		out = append(out, attribute{"frss:unicityCriteria", criteria})
	}
	if scalar(attrs["unicityCriteriaForced"]) == "true" {
		out = append(out, attribute{"frss:unicityCriteriaForced", "true"})
	}
	if f.TTL != 0 {
		// Signed: a negative period is a muted feed.
		out = append(out, attribute{"frss:ttl", strconv.Itoa(f.TTL)})
	}
	scraped := func(prefix, key string) {
		settings := readAttributes(attrs[key])
		for _, field := range scrapeFields {
			out = append(out, attribute{prefix + strings.ToUpper(field[:1]) + field[1:], settings.text(field)})
		}
	}
	switch f.Kind {
	case scrape.KindHTMLXPath, scrape.KindXMLXPath:
		scraped("frss:xPath", "xpath")
	case scrape.KindJSONDotNotation, scrape.KindHTMLXPathJSON:
		scraped("frss:json", "json_dotnotation")
		if f.Kind == scrape.KindHTMLXPathJSON {
			out = append(out, attribute{"frss:xPathToJson", attrs.text("xPathToJson")})
		}
	}
	// Only the filters that mark entries read have a place in OPML.
	var filters []struct {
		Search  string   `json:"search"`
		Actions []string `json:"actions"`
	}
	_ = json.Unmarshal(attrs["filters"], &filters)
	var read []string
	for _, filter := range filters {
		if slices.Contains(filter.Actions, "read") {
			read = append(read, filter.Search)
		}
	}
	out = append(out,
		attribute{"frss:filtersActionRead", strings.TrimSpace(strings.Join(read, "\n"))},
		attribute{"frss:cssFullContent", f.PathEntries},
		attribute{"frss:cssFullContentConditions", lines(attrs["path_entries_conditions"])},
		attribute{"frss:cssContentFilter", attrs.text("path_entries_filter")},
	)
	curl := readAttributes(attrs["curl_params"])
	for _, field := range curlFields {
		value := scalar(curl[field.key])
		if field.name == "CURLOPT_PROXYTYPE" && value == "3" { // an old way to say "no proxy"
			value = "-1"
		}
		out = append(out, attribute{"frss:" + field.name, value})
	}
	out = append(out, attribute{"frss:CURLOPT_HTTPHEADER", lines(curl[curlHTTPHeader])})
	return slices.DeleteFunc(out, func(a attribute) bool { return a.value == "" })
}

// Title is the title of the documents Export writes.
const Title = "freshgo"

// Export writes the categories and feeds of the user as OPML. Categories
// come in the order the user gave them, then by name; feeds by name, both
// as the language of the user orders names.
func Export(ctx context.Context, db *store.Store, u *store.User, created time.Time) ([]byte, error) {
	categories, err := db.Categories(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	feeds, err := db.Feeds(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var settings struct {
		Language string `json:"language"`
	}
	// Settings of another shape mean the default order.
	_ = json.Unmarshal(u.Settings, &settings)
	order := collate.New(language.Make(settings.Language), collate.Numeric)
	position := func(c *store.Category) (int, bool) {
		var p int
		raw, set := readAttributes(c.Attributes)["position"]
		return p, set && json.Unmarshal(raw, &p) == nil
	}
	slices.SortStableFunc(categories, func(a, b *store.Category) int {
		pa, okA := position(a)
		pb, okB := position(b)
		switch {
		case okA && okB && pa != pb:
			return cmp.Compare(pa, pb)
		case okA != okB && okA:
			return -1
		case okA != okB:
			return 1
		}
		return order.CompareString(a.Name, b.Name)
	})
	slices.SortStableFunc(feeds, func(a, b *store.Feed) int { return order.CompareString(displayName(a), displayName(b)) })

	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<opml xmlns:frss="` + namespace + `" version="2.0">` + "\n  <head>\n    <title>")
	escape(&b, Title)
	b.WriteString("</title>\n    <dateCreated>" + created.Format(time.RFC1123Z) + "</dateCreated>\n  </head>\n  <body>\n")
	for _, c := range categories {
		b.WriteString(`    <outline text="`)
		escape(&b, c.Name)
		b.WriteString(`"`)
		if c.Kind == kindDynamicOPML {
			if address := readAttributes(c.Attributes).text("opml_url"); address != "" {
				b.WriteString(` frss:opmlUrl="`)
				escape(&b, address)
				b.WriteString(`"`)
			}
		}
		b.WriteString(">\n")
		for _, f := range feeds {
			if f.CategoryID != c.ID {
				continue
			}
			b.WriteString("      <outline")
			for _, a := range outline(f) {
				b.WriteString(" " + a.name + `="`)
				escape(&b, a.value)
				b.WriteString(`"`)
			}
			b.WriteString("/>\n")
		}
		b.WriteString("    </outline>\n")
	}
	b.WriteString("  </body>\n</opml>\n")
	return b.Bytes(), nil
}

func escape(w io.Writer, s string) {
	// Writing to a buffer does not fail.
	_ = xml.EscapeText(w, []byte(s))
}

// ErrDocument is returned for a document that is not OPML.
var ErrDocument = errors.New("opml: the document cannot be read")

// ErrIncomplete is returned by Import when some feeds of the document were
// not added; the rest were.
var ErrIncomplete = errors.New("opml: some feeds were not imported")

// node is an outline with the attributes as written, namespace prefixes
// included.
type node struct {
	attrs    map[string]string
	children []*node
}

// parse reads the outlines of the body of an OPML document.
func parse(data []byte) ([]*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var (
		root  = &node{}
		stack = []*node{root}
		opml  bool
	)
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDocument, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "opml") {
				opml = true
			}
			if !strings.EqualFold(t.Name.Local, "outline") {
				continue
			}
			n := &node{attrs: map[string]string{}}
			for _, a := range t.Attr {
				name := a.Name.Local
				if a.Name.Space != "" {
					name = a.Name.Space + ":" + name
				}
				n.attrs[name] = a.Value
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "outline") && len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if !opml {
		return nil, fmt.Errorf("%w: no opml element", ErrDocument)
	}
	return root.children, nil
}

// group is the feeds a document puts into one category, in document order.
type group struct {
	// category is the outline of the category; nil for feeds outside any,
	// which go to the default one.
	category *node
	name     string
	feeds    []*node
}

// groups sorts the feeds of the document by the category they end up in. A
// category inside a category stands on its own: FreshRSS has one level.
func groups(outlines []*node) []*group {
	var (
		list   []*group
		byName = map[string]*group{}
	)
	get := func(name string) *group {
		g := byName[name]
		if g == nil {
			g = &group{name: name}
			byName[name] = g
			list = append(list, g)
		}
		return g
	}
	var walk func(outlines []*node, parent string)
	walk = func(outlines []*node, parent string) {
		for _, n := range outlines {
			in := parent
			// A feed at the top may name its category in an attribute.
			if category, ok := n.attrs["category"]; ok && parent == "" {
				var names []string
				for _, name := range strings.Split(category, ",") {
					names = append(names, strings.TrimSpace(name))
				}
				in = strings.Join(names, ", ")
				if g := get(in); g.category == nil {
					g.category = &node{attrs: map[string]string{"text": in}}
				}
			}
			if len(n.children) > 0 {
				name := cmp.Or(strings.TrimSpace(n.attrs["text"]), strings.TrimSpace(n.attrs["title"]), in)
				walk(n.children, name)
				get(name).category = n
			}
			if _, isFeed := n.attrs["xmlUrl"]; isFeed {
				g := get(in)
				g.feeds = append(g.feeds, n)
			}
		}
	}
	walk(outlines, "")
	return list
}

// unicityCriteria is what a uniqueness criterion looks like. FreshRSS leaves
// the digits out, and so cannot read back the "sha1:…" criteria it writes.
var unicityCriteria = regexp.MustCompile(`^[a-z0-9:_-]{2,64}$`)

var lineBreak = regexp.MustCompile(`\r\n|[\n\v\f\r\x{85}\x{2028}\x{2029}]`)

// truthy reads a boolean attribute.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// feedOf turns the outline of a feed into the feed to store.
func feedOf(n *node, userID, categoryID int64) (*store.Feed, error) {
	address := strings.TrimSpace(n.attrs["xmlUrl"])
	if lower := strings.ToLower(address); address != "" && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		address = "https://" + strings.TrimLeft(address, "/")
	}
	if u, err := url.Parse(address); err != nil || u.Host == "" {
		return nil, fmt.Errorf("opml: %q is not the address of a feed", n.attrs["xmlUrl"])
	}
	f := &store.Feed{
		UserID: userID, CategoryID: categoryID, URL: address,
		Name:        cmp.Or(n.attrs["text"], n.attrs["title"]),
		Website:     n.attrs["htmlUrl"],
		Description: n.attrs["description"],
		Priority:    store.PriorityMain,
		PathEntries: n.attrs["frss:cssFullContent"],
	}
	kind := strings.ToLower(n.attrs["type"])
	if kind == "json+dotpath" { // the name FreshRSS 1.24.0-dev used
		kind = "json+dotnotation"
	}
	for k, name := range kindTypes {
		if strings.ToLower(name) == kind {
			f.Kind = k
		}
	}
	for priority, name := range priorityNames {
		if name == strings.ToLower(n.attrs["frss:priority"]) {
			f.Priority = priority
		}
	}
	if ttl, ok := n.attrs["frss:ttl"]; ok {
		f.TTL, _ = strconv.Atoi(strings.TrimSpace(ttl))
	}

	attrs := map[string]any{}
	if criteria := n.attrs["frss:unicityCriteria"]; criteria != "id" && unicityCriteria.MatchString(criteria) {
		attrs["unicityCriteria"] = criteria
	}
	if truthy(n.attrs["frss:unicityCriteriaForced"]) {
		attrs["unicityCriteriaForced"] = true
	}
	if conditions, ok := n.attrs["frss:cssFullContentConditions"]; ok {
		attrs["path_entries_conditions"] = lineBreak.Split(conditions, -1)
	}
	if filter, ok := n.attrs["frss:cssContentFilter"]; ok {
		attrs["path_entries_filter"] = filter
	} else if filter, ok := n.attrs["frss:cssFullContentFilter"]; ok {
		attrs["path_entries_filter"] = filter
	}
	if filters, ok := n.attrs["frss:filtersActionRead"]; ok {
		type rule struct {
			Search  string   `json:"search"`
			Actions []string `json:"actions"`
		}
		var rules []rule
		for _, search := range lineBreak.Split(filters, -1) {
			if search = strings.TrimSpace(search); search != "" {
				rules = append(rules, rule{search, []string{"read"}})
			}
		}
		if rules != nil {
			attrs["filters"] = rules
		}
	}
	scraped := func(prefix, key string) {
		settings := map[string]string{}
		for _, field := range scrapeFields {
			if value, ok := n.attrs[prefix+strings.ToUpper(field[:1])+field[1:]]; ok {
				settings[field] = value
			}
		}
		if len(settings) > 0 {
			attrs[key] = settings
		}
	}
	scraped("frss:xPath", "xpath")
	scraped("frss:json", "json_dotnotation")
	if path, ok := n.attrs["frss:xPathToJson"]; ok {
		attrs["xPathToJson"] = path
	}

	curl := map[string]any{}
	for _, field := range curlFields {
		value, ok := n.attrs["frss:"+field.name]
		if !ok {
			continue
		}
		switch field.name {
		case "CURLOPT_COOKIEFILE":
			// Only switches the cookie engine on; a file is not to be named.
			curl[field.key] = ""
		case "CURLOPT_FOLLOWLOCATION", "CURLOPT_POST":
			// FreshRSS takes any text for true, "false" included, which is
			// what it writes for a feed that must not follow redirects.
			curl[field.key] = truthy(value)
		case "CURLOPT_MAXREDIRS", "CURLOPT_PROXYTYPE":
			number, _ := strconv.Atoi(strings.TrimSpace(value))
			if field.name == "CURLOPT_PROXYTYPE" && number == 3 {
				number = -1
			}
			curl[field.key] = number
		default:
			curl[field.key] = value
		}
	}
	if headers, ok := n.attrs["frss:CURLOPT_HTTPHEADER"]; ok {
		curl[curlHTTPHeader] = lineBreak.Split(headers, -1)
	}
	if len(curl) > 0 {
		attrs["curl_params"] = curl
	}
	if len(attrs) > 0 {
		raw, err := json.Marshal(attrs)
		if err != nil {
			return nil, err
		}
		f.Attributes = raw
	}
	return f, nil
}

// merged lays the attributes a document gives a feed over the ones the feed
// has: objects are merged key by key, anything else is replaced.
func merged(existing, imported json.RawMessage) (json.RawMessage, error) {
	var under, over map[string]any
	if json.Unmarshal(existing, &under) != nil || under == nil {
		under = map[string]any{}
	}
	if json.Unmarshal(imported, &over) != nil {
		over = nil
	}
	return json.Marshal(mergeObjects(under, over))
}

func mergeObjects(under, over map[string]any) map[string]any {
	for key, value := range over {
		below, isObject := under[key].(map[string]any)
		above, isAlsoObject := value.(map[string]any)
		if isObject && isAlsoObject {
			under[key] = mergeObjects(below, above)
			continue
		}
		under[key] = value
	}
	return under
}

// Limits bound the number of categories and of feeds a user may have; zero
// is no bound.
type Limits struct {
	Feeds      int
	Categories int
}

// Import adds the categories and feeds of an OPML document to those of the
// user and returns the feeds it added; their entries are for a refresh to
// fetch. A feed the user already has, by address, stays where it is and
// takes its name, site, description, kind and settings from the document;
// if it was muted, it no longer is. When some feeds could not be added, the
// rest still are, and the error is ErrIncomplete.
//
// limits bound what the user may end up with: a category or a feed past
// them is not added and makes the import incomplete.
func Import(ctx context.Context, db *store.Store, registry *hooks.Registry, u *store.User, data []byte, limits Limits) ([]*store.Feed, error) {
	outlines, err := parse(data)
	if err != nil {
		return nil, err
	}
	var (
		added      []*store.Feed
		incomplete bool
	)
	err = db.InTx(ctx, func(tx *store.Store) error {
		added, incomplete = nil, false
		categories, err := tx.Categories(ctx, u.ID)
		if err != nil {
			return err
		}
		feeds, err := tx.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		var (
			byName   = map[string]int64{}
			known    = map[string]*store.Feed{}
			position = -1
		)
		for _, c := range categories {
			byName[c.Name] = c.ID
			var p int
			if raw, set := readAttributes(c.Attributes)["position"]; set && json.Unmarshal(raw, &p) == nil {
				position = max(position, p)
			}
		}
		for _, f := range feeds {
			known[f.URL] = f
		}
		for _, g := range groups(outlines) {
			if len(g.feeds) == 0 {
				continue
			}
			categoryID, exists := byName[g.name]
			if !exists && g.category != nil && g.name != "" && limits.Categories > 0 && len(byName) >= limits.Categories {
				// One category too many: its feeds go to the default one.
				incomplete = true
			} else if !exists && g.category != nil && g.name != "" {
				// New categories line up after the ones the user has put
				// in order, as they come in the document.
				position++
				c := &store.Category{UserID: u.ID, Name: g.name}
				attrs := map[string]any{"position": position}
				if address := strings.TrimSpace(g.category.attrs["frss:opmlUrl"]); address != "" {
					c.Kind = kindDynamicOPML
					attrs["opml_url"] = address
				}
				if c.Attributes, err = json.Marshal(attrs); err != nil {
					return err
				}
				switch err := tx.CreateCategory(ctx, c); {
				case errors.Is(err, store.ErrConflict):
					// A label has the name: the feeds go to the default category.
					incomplete = true
				case err != nil:
					return err
				default:
					categoryID = c.ID
					byName[g.name] = c.ID
				}
			}
			if categoryID == 0 {
				categoryID = store.DefaultCategoryID
			}
			for _, n := range g.feeds {
				f, err := feedOf(n, u.ID, categoryID)
				if err != nil {
					incomplete = true
					continue
				}
				f, ok := registry.FeedBeforeInsert.Call(ctx, f)
				if !ok {
					incomplete = true
					continue
				}
				if has := known[f.URL]; has != nil {
					has.Kind, has.Name, has.Website, has.Description = f.Kind, f.Name, f.Website, f.Description
					has.PathEntries = f.PathEntries
					has.TTL = max(has.TTL, -has.TTL)
					if has.Attributes, err = merged(has.Attributes, f.Attributes); err != nil {
						return err
					}
					if err := tx.UpdateFeed(ctx, has); err != nil {
						return err
					}
					continue
				}
				if limits.Feeds > 0 && len(known) >= limits.Feeds {
					incomplete = true
					continue
				}
				if err := tx.CreateFeed(ctx, f); err != nil {
					return err
				}
				known[f.URL] = f
				added = append(added, f)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if incomplete {
		return added, ErrIncomplete
	}
	return added, nil
}

// Feeds returns the feeds an OPML document lists, each address once, as
// feeds of the given category. Nothing is stored. The document is taken for
// one somebody else wrote: what its feeds say about how they are to be
// requested (proxy, cookies, headers) is left out, as FreshRSS does for the
// OPML a category mirrors.
func Feeds(data []byte, userID, categoryID int64) ([]*store.Feed, error) {
	outlines, err := parse(data)
	if err != nil {
		return nil, err
	}
	var (
		feeds []*store.Feed
		seen  = map[string]bool{}
	)
	for _, g := range groups(outlines) {
		for _, n := range g.feeds {
			f, err := feedOf(n, userID, categoryID)
			if err != nil || seen[f.URL] {
				continue
			}
			seen[f.URL] = true
			attrs := readAttributes(f.Attributes)
			if _, has := attrs["curl_params"]; has {
				delete(attrs, "curl_params")
				if f.Attributes, err = json.Marshal(attrs); err != nil {
					return nil, err
				}
			}
			feeds = append(feeds, f)
		}
	}
	return feeds, nil
}
