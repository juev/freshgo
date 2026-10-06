package search

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseRules(t *testing.T) {
	queries := ParseSavedQueries(json.RawMessage(`[
		{"name": "Ads", "search": "intitle:реклама OR intitle:sponsored", "get": "a"},
		{"url": "?get=c_2"},
		{"name": "Long", "search": "intext:/\\w{500}/"}]`))
	if len(queries) != 3 || queries[0].Name != "Ads" || queries[1] != (SavedQuery{}) || queries[2].Name != "Long" {
		t.Fatalf("saved queries: %+v", queries)
	}
	rules, problems := ParseRules(json.RawMessage(`[
		{"search": "search:Ads -author:Editor", "actions": ["read", "star"]},
		{"search": "intitle:/(a)\\1/", "actions": ["read"]},
		{"search": "", "actions": ["read"]},
		{"search": "no actions", "actions": []},
		{"search": "S:2", "actions": ["label"]},
		"not a rule"]`), Options{Queries: queries})
	if len(rules) != 2 || len(problems) != 1 {
		t.Fatalf("%d rules, problems %v; want 2 rules and 1 problem", len(rules), problems)
	}
	if !errors.Is(problems[0], ErrRegexp) {
		t.Errorf("problem %v, want ErrRegexp", problems[0])
	}
	first := rules[0]
	if first.Search != "search:Ads -author:Editor" || !first.Has(ActionRead) || !first.Has(ActionStar) || first.Has(ActionLabel) {
		t.Errorf("rule %+v", first)
	}
	// Case is ignored for letters of any alphabet.
	for title, want := range map[string]bool{"Реклама недели": true, "Sponsored post": true, "Новости": false} {
		if got := first.Query.Match(&Entry{Title: title, Authors: []string{"Reporter"}}); got != want {
			t.Errorf("title %q: match %v, want %v", title, got, want)
		}
	}
	if first.Query.Match(&Entry{Title: "реклама", Authors: []string{"The Editor"}}) {
		t.Error("the excluded author matched")
	}

	for _, raw := range []string{``, `null`, `{}`, `"x"`} {
		if rules, problems := ParseRules(json.RawMessage(raw), Options{}); rules != nil || problems != nil {
			t.Errorf("ParseRules(%s) = %v, %v", raw, rules, problems)
		}
	}
}
