package search

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const oracleDir = "../../testdata/reference/oracle"

// tree is the query in the form testdata/reference/oracle/search.php prints
// what FreshRSS parsed. Date bounds are left out of a volatile query: they
// depend on the current time.
func (q *Query) tree(volatile bool) map[string]any {
	searches := []any{}
	for _, part := range q.parts {
		searches = append(searches, part.tree(volatile))
	}
	for _, t := range q.terms {
		searches = append(searches, t.tree(volatile))
	}
	return map[string]any{"op": q.op, "searches": searches}
}

func (t *term) tree(volatile bool) map[string]any {
	out := map[string]any{}
	put := func(key string, value any) {
		if v := reflect.ValueOf(value); v.Len() > 0 {
			out[key] = value
		}
	}
	putTexts := func(key string, t texts) {
		put(key, t.plain)
		var sources []string
		for _, p := range t.regex {
			sources = append(sources, p.source)
		}
		put(key+"~", sources)
	}
	putSpan := func(key string, s span) {
		if volatile {
			return
		}
		if s.min != 0 {
			out[key+">"] = s.min
		}
		if s.max != 0 {
			out[key+"<"] = s.max
		}
	}
	put("e", t.entryIDs)
	put("f", t.feedIDs)
	put("c", t.categoryIDs)
	put("L", t.labelIDs)
	put("labels", t.labelNames)
	putTexts("intitle", t.intitle)
	putTexts("intext", t.intext)
	putTexts("author", t.author)
	putTexts("inurl", t.inurl)
	putTexts("tags", t.tags)
	putTexts("search", t.search)
	putSpan("date", t.date)
	putSpan("pubdate", t.pubdate)
	putSpan("mdate", t.modified)
	putSpan("userdate", t.userdate)
	put("-e", t.notEntryIDs)
	put("-f", t.notFeedIDs)
	put("-c", t.notCategoryIDs)
	put("-L", t.notLabelIDs)
	put("-labels", t.notLabelNames)
	putTexts("-intitle", t.notIntitle)
	putTexts("-intext", t.notIntext)
	putTexts("-author", t.notAuthor)
	putTexts("-inurl", t.notInurl)
	putTexts("-tags", t.notTags)
	putTexts("-search", t.notSearch)
	putSpan("-date", t.notDate)
	putSpan("-pubdate", t.notPubdate)
	putSpan("-mdate", t.notModified)
	putSpan("-userdate", t.notUserdate)
	return out
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(oracleDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// plain is a value after a round trip through JSON, for comparison.
func plain(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Queries that freshgo, on purpose, does not read or match as FreshRSS 1.30.1
// on SQLite does. The reasons are in docs/specs/search.md, Known differences.
var (
	// The regular expression is not one Go's regexp accepts. PHP does not
	// accept these two either and matches nothing.
	unsupportedRegexp = map[string]bool{
		`intitle:/^ab\M/`: true,
		`intext:/^ab\M/`:  true,
	}
	// The entries freshgo matches where FreshRSS matches others.
	ownMatches = map[string][]int{
		// Case is ignored for every letter, as FreshRSS does on PostgreSQL;
		// on SQLite it is for ASCII letters only.
		"CAFÉ": {2},
		// FreshRSS looks in the HTML-encoded title, where an ampersand is
		// &amp; and a quotation mark &quot;; freshgo looks in the title.
		"intitle:amp":        {},
		`intitle:'"quoted"'`: {2},
	}
)

// R11: queries are read, and entries matched, as FreshRSS does it. The
// queries are those of the tests of FreshRSS and more.
func TestAgainstFreshRSS(t *testing.T) {
	var input struct {
		Queries []struct {
			Name   string `json:"name"`
			Search string `json:"search"`
		} `json:"queries"`
		Feeds []struct {
			ID       int64 `json:"id"`
			Category int64 `json:"category"`
		} `json:"feeds"`
		Entries []struct {
			ID           string   `json:"id"`
			Feed         int64    `json:"feed"`
			Title        string   `json:"title"`
			Authors      []string `json:"authors"`
			Content      string   `json:"content"`
			Link         string   `json:"link"`
			Published    int64    `json:"published"`
			Tags         []string `json:"tags"`
			Modified     int64    `json:"modified"`
			UserModified int64    `json:"userModified"`
		} `json:"entries"`
		Cases []struct {
			Q        string `json:"q"`
			Volatile bool   `json:"volatile"`
		} `json:"cases"`
	}
	readJSON(t, "search-cases.json", &input)
	var want []struct {
		Q       string `json:"q"`
		Tree    any    `json:"tree"`
		Matches []int  `json:"matches"`
		Error   string `json:"error"`
	}
	readJSON(t, "search.json", &want)
	if len(want) != len(input.Cases) || len(want) < 400 {
		t.Fatalf("%d cases and %d results", len(input.Cases), len(want))
	}

	// The reference ran on a server in UTC.
	opts := Options{Now: time.Now().UTC()}
	for _, q := range input.Queries {
		opts.Queries = append(opts.Queries, SavedQuery{Name: q.Name, Search: q.Search})
	}
	categories := map[int64]int64{}
	for _, f := range input.Feeds {
		categories[f.ID] = f.Category
	}
	var entries []*Entry
	for _, e := range input.Entries {
		var id int64
		if err := json.Unmarshal([]byte(e.ID), &id); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, &Entry{
			ID: id, FeedID: e.Feed, CategoryID: categories[e.Feed], Title: e.Title, Authors: e.Authors, Content: e.Content,
			Link: e.Link, Tags: e.Tags, Published: e.Published, LastModified: e.Modified, LastUserModified: e.UserModified,
		})
	}

	seen := map[string]bool{}
	for i, c := range input.Cases {
		if want[i].Q != c.Q {
			t.Fatalf("case %d is %q in search-cases.json and %q in search.json", i, c.Q, want[i].Q)
		}
		seen[c.Q] = true
		q, err := Parse(c.Q, opts)
		if unsupportedRegexp[c.Q] {
			if !errors.Is(err, ErrRegexp) {
				t.Errorf("%q: error %v, want ErrRegexp", c.Q, err)
			}
			continue
		}
		if err != nil || want[i].Error != "" {
			if err == nil || want[i].Error == "" {
				t.Errorf("%q: error %v, FreshRSS says %q", c.Q, err, want[i].Error)
			}
			continue
		}
		if got := plain(t, q.tree(c.Volatile)); !reflect.DeepEqual(got, want[i].Tree) {
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want[i].Tree)
			t.Errorf("%q is read as\n     %s\nwant %s", c.Q, gotJSON, wantJSON)
			continue
		}
		matches := []int{}
		for n, e := range entries {
			if q.Match(e) {
				matches = append(matches, n)
			}
		}
		wantMatches := want[i].Matches
		if ours, differs := ownMatches[c.Q]; differs {
			if reflect.DeepEqual(wantMatches, ours) {
				t.Errorf("%q: FreshRSS matches %v as well; the case is no difference any more", c.Q, ours)
			}
			wantMatches = ours
		}
		if !reflect.DeepEqual(matches, wantMatches) {
			t.Errorf("%q matches entries %v, want %v", c.Q, matches, wantMatches)
		}
	}
	for q := range unsupportedRegexp {
		if !seen[q] {
			t.Errorf("no case %q", q)
		}
	}
	for q := range ownMatches {
		if !seen[q] {
			t.Errorf("no case %q", q)
		}
	}
}

func TestLimits(t *testing.T) {
	if _, err := Parse(strings.Repeat("a ", maxLength/2+1), Options{}); !errors.Is(err, ErrTooLong) {
		t.Errorf("a query over the length limit: error %v, want ErrTooLong", err)
	}
	deep := strings.Repeat("(", maxDepth+1) + "a" + strings.Repeat(")", maxDepth+1)
	if _, err := Parse(deep, Options{}); !errors.Is(err, ErrTooDeep) {
		t.Errorf("parentheses over the depth limit: error %v, want ErrTooDeep", err)
	}
	if _, err := Parse(strings.Repeat("(", maxDepth)+"a"+strings.Repeat(")", maxDepth), Options{}); err != nil {
		t.Errorf("parentheses at the depth limit: %v", err)
	}
}
