package ttrss

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/transfer"
)

// MaxOPML is the largest filter export the importer reads.
const MaxOPML = 16 << 20

var ErrFilterExport = errors.New("ttrss: expected an OPML export with settings")

type outline struct {
	Text     string    `xml:"text,attr"`
	URL      string    `xml:"xmlUrl,attr"`
	Type     string    `xml:"filter-type,attr"`
	Data     string    `xml:",chardata"`
	Children []outline `xml:"outline"`
}

type exportBool bool

func (b *exportBool) UnmarshalJSON(data []byte) error {
	switch strings.Trim(string(data), `"`) {
	case "true", "t", "1":
		*b = true
	case "false", "f", "0", "null", "":
		*b = false
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}
	return nil
}

type exportedFilter struct {
	Title   string       `json:"title"`
	Enabled exportBool   `json:"enabled"`
	Any     exportBool   `json:"match_any_rule"`
	Inverse exportBool   `json:"inverse"`
	Order   *json.Number `json:"order_id"`
	Rules   []exportedRule
	Actions []struct {
		ID    json.Number `json:"action_id"`
		Value string      `json:"action_param"`
	}
}

type exportedRule struct {
	Field    json.Number         `json:"filter_type"`
	Pattern  string              `json:"reg_exp"`
	Inverse  exportBool          `json:"inverse"`
	Category exportBool          `json:"cat_filter"`
	Feed     string              `json:"feed"`
	Match    [][]json.RawMessage `json:"match"`
}

var exportFields = map[json.Number]string{
	"1": "title", "2": "content", "3": "both", "4": "link", "6": "author", "7": "tag",
}

var exportActions = map[json.Number]string{
	"1": filters.ActionSkip, "2": filters.ActionRead, "3": filters.ActionStar,
	"7": filters.ActionLabel, "8": filters.ActionStop,
}

// exportFilters calls the web export with the session created by the API login.
func (c *client) exportFilters(ctx context.Context, base string) ([]transfer.RemoteFilter, []string, error) {
	cookie := &http.Cookie{Name: "ttrss_sid", Value: c.sid}
	if err := cookie.Valid(); err != nil {
		return nil, nil, err
	}
	resp, err := c.fetcher.Fetch(ctx, fetch.Request{
		URL:    base + "/backend.php?op=OPML&method=export&include_settings=1",
		Accept: fetch.AcceptXML,
		Params: fetch.Params{Cookie: cookie.String(), NoRedirects: true},
	})
	if err != nil {
		return nil, nil, err
	}
	if c.read+len(resp.Body) > maxBytes || len(resp.Body) > MaxOPML {
		return nil, nil, ErrFilterExport
	}
	return readFilterExport(bytes.NewReader(resp.Body))
}

func readFilterExport(r io.Reader) ([]transfer.RemoteFilter, []string, error) {
	var doc struct {
		XMLName xml.Name  `xml:"opml"`
		Body    []outline `xml:"body>outline"`
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxOPML+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > MaxOPML || xml.Unmarshal(data, &doc) != nil {
		return nil, nil, ErrFilterExport
	}

	var records []outline
	found := false
	index := exportIndex{feeds: make(map[string]string), categories: make(map[string]*outline)}
	for i := range doc.Body {
		item := &doc.Body[i]
		if item.URL == "" && item.Text == "tt-rss-filters" {
			found = true
			records = append(records, item.Children...)
			continue
		}
		index.add(item)
	}
	if !found {
		return nil, nil, ErrFilterExport
	}

	type orderedFilter struct {
		filter   transfer.RemoteFilter
		order    int64
		orderSet bool
	}
	var ordered []orderedFilter
	var warnings []string
	for i, record := range records {
		var imported exportedFilter
		if record.Type != "2" || json.Unmarshal([]byte(record.Data), &imported) != nil {
			warnings = append(warnings, fmt.Sprintf("filter %d: unsupported filter export record", i+1))
			continue
		}
		filter, err := imported.convert(index)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("filter %q skipped: %v", imported.Title, err))
			continue
		}
		item := orderedFilter{filter: filter}
		if imported.Order != nil {
			item.order, err = imported.Order.Int64()
			item.orderSet = err == nil
		}
		ordered = append(ordered, item)
	}
	slices.SortStableFunc(ordered, func(a, b orderedFilter) int {
		switch {
		case a.orderSet && !b.orderSet:
			return -1
		case b.orderSet && !a.orderSet:
			return 1
		case a.order != b.order:
			return cmp.Compare(a.order, b.order)
		default:
			return cmp.Compare(a.filter.Title, b.filter.Title)
		}
	})
	result := make([]transfer.RemoteFilter, 0, len(ordered))
	for _, item := range ordered {
		result = append(result, item.filter)
	}
	return result, warnings, nil
}

type exportIndex struct {
	feeds      map[string]string
	categories map[string]*outline
}

func (x exportIndex) add(item *outline) {
	if item.URL != "" {
		if previous, exists := x.feeds[item.Text]; exists && previous != item.URL {
			x.feeds[item.Text] = ""
		} else {
			x.feeds[item.Text] = item.URL
		}
		return
	}
	if item.Text == "" || item.Text == "tt-rss-labels" || item.Text == "tt-rss-prefs" {
		return
	}
	if _, exists := x.categories[item.Text]; exists {
		x.categories[item.Text] = nil
	} else {
		x.categories[item.Text] = item
	}
	for i := range item.Children {
		x.add(&item.Children[i])
	}
}

func (x exportIndex) addCategory(rule *transfer.RemoteFilterRule, name string) error {
	item := x.categories[name]
	if item == nil {
		return fmt.Errorf("category %q is missing or ambiguous in the export", name)
	}
	if slices.Contains(rule.Categories, name) {
		return nil
	}
	rule.Categories = append(rule.Categories, name)
	for _, child := range item.Children {
		if child.URL == "" && child.Text != "" {
			if err := x.addCategory(rule, child.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f exportedFilter) convert(index exportIndex) (transfer.RemoteFilter, error) {
	filter := transfer.RemoteFilter{Title: f.Title, Enabled: bool(f.Enabled), MatchAny: bool(f.Any), Inverse: bool(f.Inverse)}
	for _, exported := range f.Rules {
		field := exportFields[exported.Field]
		if field == "" {
			return filter, fmt.Errorf("unsupported rule type %q", exported.Field)
		}
		rule := transfer.RemoteFilterRule{Field: field, Pattern: filters.NormalizePattern(exported.Pattern), Inverse: bool(exported.Inverse), LegacyScope: exported.Match == nil && (exported.Feed != "" || bool(exported.Category))}
		if err := exported.scopes(&rule, index); err != nil {
			return filter, err
		}
		filter.Rules = append(filter.Rules, rule)
	}
	for _, exported := range f.Actions {
		action := transfer.RemoteFilterAction{Type: exportActions[exported.ID]}
		if action.Type == "" {
			return filter, fmt.Errorf("unsupported action %q", exported.ID)
		}
		if action.Type == filters.ActionLabel {
			action.Label = exported.Value
		}
		filter.Actions = append(filter.Actions, action)
	}
	return filter, filter.Validate()
}

func (r exportedRule) scopes(rule *transfer.RemoteFilterRule, index exportIndex) error {
	if r.Match == nil {
		if r.Feed == "" && !r.Category {
			return nil
		}
		return index.addScope(rule, r.Feed, bool(r.Category))
	}
	if len(r.Match) == 0 {
		return errors.New("empty rule scope")
	}

	type scope struct {
		name     string
		category bool
	}
	var scopes []scope
	for _, match := range r.Match {
		var category, zero exportBool
		if len(match) != 3 || json.Unmarshal(match[1], &category) != nil || json.Unmarshal(match[2], &zero) != nil {
			return errors.New("invalid rule scope")
		}
		if zero {
			if !category {
				return nil // All feeds.
			}
			scopes = append(scopes, scope{category: true})
			continue
		}
		var name string
		if err := json.Unmarshal(match[0], &name); err != nil || name == "" {
			return errors.New("invalid rule scope name")
		}
		scopes = append(scopes, scope{name: name, category: bool(category)})
	}
	for _, scope := range scopes {
		if err := index.addScope(rule, scope.name, scope.category); err != nil {
			return err
		}
	}
	return nil
}

func (x exportIndex) addScope(rule *transfer.RemoteFilterRule, name string, category bool) error {
	if category {
		if name == "" {
			// An empty name identifies the default category during import.
			rule.Categories = append(rule.Categories, name)
			return nil
		}
		return x.addCategory(rule, name)
	}
	if address := x.feeds[name]; address != "" {
		rule.Feeds = append(rule.Feeds, address)
		return nil
	}
	return fmt.Errorf("feed %q is missing or ambiguous in the export", name)
}
