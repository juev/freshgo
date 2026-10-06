package scrape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// object is a JSON object with its members in document order: the items of
// a feed may be the members of an object, and their order matters.
type object struct {
	keys   []string
	values map[string]any
}

// decodeJSON reads a JSON document into objects, []any, string, json.Number,
// bool and nil.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON document")
	}
	return v, nil
}

func decodeValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		o := &object{values: map[string]any{}}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, _ := key.(string)
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			if _, seen := o.values[k]; !seen {
				o.keys = append(o.keys, k)
			}
			o.values[k] = v
		}
		_, err := d.Token()
		return o, err
	case '[':
		list := []any{}
		for d.More() {
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := d.Token()
		return list, err
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// member returns what PHP finds under the key of an array: a member of an
// object, or an element of a list when the key is its index.
func member(v any, key string) (any, bool) {
	switch c := v.(type) {
	case *object:
		m, ok := c.values[key]
		return m, ok
	case []any:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(c) || strconv.Itoa(i) != key {
			return nil, false
		}
		return c[i], true
	}
	return nil, false
}

// elements returns the values of a list or of an object, in order.
func elements(v any) []any {
	switch c := v.(type) {
	case []any:
		return c
	case *object:
		out := make([]any, len(c.keys))
		for i, k := range c.keys {
			out[i] = c.values[k]
		}
		return out
	}
	return nil
}

var (
	quoted   = regexp.MustCompile(`^(?:'([^&]*)'|"([^&]*)")$`)
	quotes   = regexp.MustCompile(`'.*?'|".*?"`)
	brackets = regexp.MustCompile(`\[(\d+)\]`)
)

// The character FreshRSS hides an ampersand behind while it splits a path
// on the concatenation operator.
const hiddenAmpersand = "＆"

// lookup is FreshRSS_dotNotation_Util::get: the value at a path such as
// `data.posts`, `items[0].title`, a quoted literal, or several of these
// joined with `&`. The second result is false when there is nothing there.
func lookup(v any, path string) (any, bool) {
	switch v.(type) {
	case *object, []any:
	default:
		return nil, false
	}
	path = phpTrim(path)
	if path == "" || path == "." || path == "$" {
		return v, true
	}
	if m := quoted.FindStringSubmatch(path); m != nil {
		return strings.ReplaceAll(m[1]+m[2], hiddenAmpersand, "&"), true
	}
	path = quotes.ReplaceAllStringFunc(path, func(literal string) string {
		return strings.ReplaceAll(literal, "&", hiddenAmpersand)
	})
	if parts := strings.Split(path, "&"); len(parts) > 1 {
		var text strings.Builder
		for _, part := range parts {
			if result, ok := lookup(v, part); ok {
				if s, ok := scalar(result); ok {
					text.WriteString(s)
				}
			}
		}
		return text.String(), true
	}
	path = brackets.ReplaceAllString(path, ".$1")
	// A key that exists as written wins over reading it as a path.
	if m, ok := member(v, path); ok {
		return m, true
	}
	for _, segment := range strings.Split(path, ".") {
		m, ok := member(v, segment)
		if !ok {
			return nil, false
		}
		v = m
	}
	return v, true
}

// scalar is the string PHP makes of a scalar value.
func scalar(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case bool:
		if s {
			return "1", true
		}
		return "", true
	case json.Number:
		if i, err := s.Int64(); err == nil && !strings.ContainsAny(s.String(), ".eE") {
			return strconv.FormatInt(i, 10), true
		}
		f, err := s.Float64()
		if err != nil {
			return s.String(), true
		}
		return phpFloat(f), true
	}
	return "", false
}

// phpFloat is the string PHP makes of a float: 14 significant digits, and
// for large and small values an exponent with a mantissa that always has a
// fractional part.
func phpFloat(f float64) string {
	s := strconv.FormatFloat(f, 'G', 14, 64)
	mantissa, exponent, ok := strings.Cut(s, "E")
	if !ok {
		return s
	}
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	sign := exponent[:1]
	return mantissa + "E" + sign + strings.TrimLeft(exponent[1:], "0")
}

// text is FreshRSS_dotNotation_Util::getString: empty when there is no
// scalar at the path.
func text(v any, path string) string {
	if result, ok := lookup(v, path); ok {
		s, _ := scalar(result)
		return s
	}
	return ""
}

// jsonFeedNotation maps the fields of a standard JSON Feed.
var jsonFeedNotation = map[string]string{
	"feedTitle":       "title",
	"item":            "items",
	"itemTitle":       "title",
	"itemContent":     "content_text",
	"itemContentHTML": "content_html",
	"itemUri":         "url",
	"itemTimestamp":   "date_published",
	"itemTimeFormat":  `Y-m-d\TH:i:s.vP`,
	"itemThumbnail":   "image",
	"itemCategories":  "tags",
	"itemUid":         "id",
}

// scrapeJSON is FreshRSS_dotNotation_Util::convertJsonToRss.
func scrapeJSON(body []byte, src Source, notation map[string]string) (*document, error) {
	if notation["item"] == "" {
		return nil, fmt.Errorf("%w: no path to the items", ErrSettings)
	}
	root, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDocument, err)
	}
	switch root.(type) {
	case *object, []any:
	default:
		return nil, fmt.Errorf("%w: the JSON document is neither an object nor a list", ErrDocument)
	}
	get := func(v any, key string) string {
		path, ok := notation[key]
		if !ok {
			return ""
		}
		return text(v, path)
	}

	doc := &document{title: get(root, "feedTitle")}
	if doc.title == "" {
		doc.title = src.Name
	}
	found, _ := lookup(root, notation["item"])
	for _, entry := range elements(found) {
		it := item{link: get(entry, "itemUri")}
		// An item without a link is skipped.
		if it.link == "" || it.link == "0" {
			continue
		}
		it.title = get(entry, "itemTitle")
		it.author = get(entry, "itemAuthor")
		it.content = get(entry, "itemContent")
		// A path to HTML content replaces the plain one even when it leads nowhere.
		if _, ok := notation["itemContentHTML"]; ok {
			it.content = get(entry, "itemContentHTML")
		}
		it.timestamp = timestamp(get(entry, "itemTimestamp"), notation["itemTimeFormat"], src.Location)
		if path, ok := notation["itemCategories"]; ok {
			switch categories, _ := lookup(entry, path); c := categories.(type) {
			case string:
				if c != "" {
					it.tags = []string{c}
				}
			case []any, *object:
				for _, tag := range elements(c) {
					if s, ok := tag.(string); ok {
						it.tags = append(it.tags, s)
					}
				}
			}
		}
		it.thumbnail = get(entry, "itemThumbnail")
		it.guid = get(entry, "itemUid")
		if it.finish() {
			doc.items = append(doc.items, it)
		}
	}
	if len(elements(found)) == 0 {
		return nil, ErrNoItems
	}
	return doc, nil
}
