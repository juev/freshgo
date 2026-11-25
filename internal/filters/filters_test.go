package filters

import "testing"

func TestRegexCompatibility(t *testing.T) {
	for _, tc := range []struct{ pattern, title string }{
		{`^\w+$`, "Москва"},
		{"^(one|\ntwo)$", "two"},
		{"^(one|\r\ntwo)$", "two"},
		{"^(one|\rtwo)$", "two"},
	} {
		compiled, err := Compile([]Filter{{Enabled: true, Rules: []Rule{{Field: "title", Pattern: tc.pattern}},
			Actions: []Action{{Type: ActionStar}}}})
		if err != nil {
			t.Fatal(err)
		}
		if actions := Actions(compiled, Article{Title: tc.title}); len(actions) != 1 {
			t.Errorf("%q did not match %q", tc.pattern, tc.title)
		}
	}
}

func TestMatchRules(t *testing.T) {
	matching := Rule{Field: "title", Pattern: "^News$"}
	missing := Rule{Field: "title", Pattern: "^Sports$"}
	inverted := missing
	inverted.Inverse = true
	scoped := matching
	scoped.FeedIDs = []int64{2}
	legacy := scoped
	legacy.LegacyScope = true

	for _, tc := range []struct {
		name         string
		rules        []Rule
		any, inverse bool
		want         bool
	}{
		{name: "all match", rules: []Rule{matching, inverted}, want: true},
		{name: "one fails AND", rules: []Rule{matching, missing}},
		{name: "one matches OR", rules: []Rule{missing, matching}, any: true, want: true},
		{name: "none match OR", rules: []Rule{missing, missing}, any: true},
		{name: "invert AND result", rules: []Rule{matching, missing}, inverse: true, want: true},
		{name: "invert OR result", rules: []Rule{missing, matching}, any: true, inverse: true},
		{name: "modern scope is checked before inversion and short circuit", rules: []Rule{missing, scoped}, inverse: true},
		{name: "legacy AND ignores an out-of-scope rule", rules: []Rule{matching, legacy}, want: true},
		{name: "OR ignores an out-of-scope rule", rules: []Rule{scoped, matching}, any: true, want: true},
		{name: "an inverted filter still needs an applicable rule", rules: []Rule{legacy}, inverse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := Compile([]Filter{{Enabled: true, MatchAny: tc.any, Inverse: tc.inverse,
				Rules: tc.rules, Actions: []Action{{Type: ActionRead}}}})
			if err != nil {
				t.Fatal(err)
			}
			got := len(Actions(compiled, Article{FeedID: 1, Title: "News"})) > 0
			if got != tc.want {
				t.Errorf("matched = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRegexErrorDoesNotMatchWhenInverted(t *testing.T) {
	compiled, err := Compile([]Filter{{Enabled: true, Inverse: true,
		Rules: []Rule{{Field: "title", Pattern: ".+"}}, Actions: []Action{{Type: ActionRead}}}})
	if err != nil {
		t.Fatal(err)
	}
	if actions := Actions(compiled, Article{Title: "\xff"}); len(actions) != 0 {
		t.Errorf("regex error produced actions: %+v", actions)
	}
}
