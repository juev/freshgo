package search

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// String prints a condition for the tests.
func (c *Condition) String() string {
	subs := make([]string, len(c.Of))
	for i, sub := range c.Of {
		subs[i] = sub.String()
	}
	span := fmt.Sprintf("[%d,%d]", c.Min, c.Max)
	switch c.Kind {
	case CondAnd:
		if len(c.Of) == 0 {
			return "always"
		}
		return "and(" + strings.Join(subs, ",") + ")"
	case CondOr:
		if len(c.Of) == 0 {
			return "never"
		}
		return "or(" + strings.Join(subs, ",") + ")"
	case CondNot:
		return "not(" + subs[0] + ")"
	case CondEntries:
		return fmt.Sprint("entries", c.IDs)
	case CondFeeds:
		return fmt.Sprint("feeds", c.IDs)
	case CondCategories:
		return fmt.Sprint("categories", c.IDs)
	case CondLabels:
		return fmt.Sprint("labels", c.IDs)
	case CondLabelNames:
		return fmt.Sprint("names", c.Names)
	case CondAnyLabel:
		return "labelled"
	case CondAdded:
		return "added" + span
	case CondPublished:
		return "published" + span
	case CondModified:
		return "modified" + span
	case CondUserModified:
		return "usermodified" + span
	}
	return "?"
}

// R6: what a database can check of a query is told apart from its texts,
// and a part with texts is never denied.
func TestCondition(t *testing.T) {
	opts := Options{Now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), Labels: true}
	for query, want := range map[string]string{
		"":                              "always",
		"hello":                         "always",
		"f:1":                           "feeds[1]",
		"f:1,2 hello":                   "feeds[1 2]",
		"-f:1 c:2":                      "and(not(feeds[1]),categories[2])",
		"f:":                            "never",
		"-f:":                           "always",
		"e:7,007,x":                     "entries[7]",
		"e:007":                         "never",
		"f:1 OR c:2":                    "or(feeds[1],categories[2])",
		"f:1 OR hello":                  "always",
		"(f:1 a) OR (f:2 b)":            "or(feeds[1],feeds[2])",
		"(f:1) (hello)":                 "feeds[1]",
		"!(f:1)":                        "not(feeds[1])",
		"!(f:1 hello)":                  "always",
		"(c:2) !(f:1 hello)":            "categories[2]",
		"(c:2) !(f:1)":                  "and(categories[2],not(feeds[1]))",
		"(c:2) OR !(f:1)":               "or(categories[2],not(feeds[1]))",
		"(c:2) OR !(f:1 hello)":         "always",
		"L:1 L:2,3":                     "and(labels[1],labels[2 3])",
		"L:* -L:4":                      "and(labelled,not(labels[4]))",
		"-L:*":                          "not(labelled)",
		`labels:work,home -label:"x y"`: "and(names[work home],not(names[x y]))",
		"date:2014-03-01/2014-03-02":    "added[1393632000,1393804799]",
		"pubdate:2014-03-01/":           "published[1393632000,0]",
		"mdate:/2014-03-01":             "modified[0,1393718399]",
		"-userdate:2014-03-01":          "and(not(usermodified[1393632000,0]),not(usermodified[0,1393718399]))",
	} {
		q, err := Parse(query, opts)
		if err != nil {
			t.Errorf("%q: %v", query, err)
			continue
		}
		if got := q.Condition().String(); got != want {
			t.Errorf("%q: condition %s, want %s", query, got, want)
		}
	}

	// More than a database takes as one expression: no test, Match decides.
	var long []string
	for i := range 1100 {
		long = append(long, fmt.Sprintf("f:%d", i+1))
	}
	for _, query := range []string{strings.Join(long, " OR "), "L:" + strings.Join(long, " L:")} {
		q, err := Parse(query, opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := q.Condition().String(); got != "always" {
			t.Errorf("a query of 1100 operators: condition of %d bytes, want always", len(got))
		}
	}

	// A filter does not look at labels.
	q, err := Parse("L:1 labels:work hello", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Condition().String(); got != "always" || q.UsesLabels() {
		t.Errorf("without Options.Labels: condition %s, uses labels %v", got, q.UsesLabels())
	}
}

// R6: with Options.Labels a query asks for labels the way a search of the
// database of FreshRSS does; without, as before, it does not look at them.
func TestMatchLabels(t *testing.T) {
	work, home := Label{ID: 1, Name: "work"}, Label{ID: 2, Name: "home"}
	entries := []*Entry{
		{ID: 1, Title: "plain"},
		{ID: 2, Title: "at work", Labels: []Label{work}},
		{ID: 3, Title: "both", Labels: []Label{work, home}},
	}
	for query, want := range map[string][]int64{
		"L:1":                  {2, 3},
		"L:2":                  {3},
		"L:1,2":                {2, 3},
		"L:1 L:2":              {3},
		"L:9":                  nil,
		"L:*":                  {2, 3},
		"-L:*":                 {1},
		"-L:2":                 {1, 2},
		"-L:1,2":               {1},
		"labels:work":          {2, 3},
		"labels:home,nothing":  {3},
		"label:Work":           nil,
		"-labels:home":         {1, 2},
		"labels:work -L:2":     {2},
		"L:2 OR plain":         {1, 3},
		"!(L:1 work)":          {1, 3},
		"L:1 intitle:both":     {3},
		"intitle:plain OR L:2": {1, 3},
	} {
		q, err := Parse(query, Options{Labels: true})
		if err != nil {
			t.Errorf("%q: %v", query, err)
			continue
		}
		var got []int64
		for _, e := range entries {
			if q.Match(e) {
				got = append(got, e.ID)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q matches %v, want %v", query, got, want)
		}
		if !q.UsesLabels() {
			t.Errorf("%q is not said to use labels", query)
		}
	}

	q, err := Parse("L:9 -L:* labels:nothing", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !q.Match(e) {
			t.Errorf("a filter that names labels does not match entry %d", e.ID)
		}
	}
}
