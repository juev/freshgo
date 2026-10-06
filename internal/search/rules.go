package search

import (
	"encoding/json"
	"fmt"
)

// Actions of a rule, as FreshRSS names them.
const (
	ActionRead  = "read"
	ActionStar  = "star"
	ActionLabel = "label"
)

// Rule is a filter action: what happens to the entries a query matches. A
// user, a category, a feed and a label keep theirs in the "filters" value
// of their settings.
type Rule struct {
	// Search is the query as written.
	Search  string
	Actions []string
	Query   *Query
}

// Has reports whether the rule asks for the action.
func (r Rule) Has(action string) bool {
	for _, a := range r.Actions {
		if a == action {
			return true
		}
	}
	return false
}

// ParseRules reads a "filters" value. Entries that are not rules are passed
// over, as in FreshRSS; a rule whose query cannot be read is left out and
// reported in problems.
func ParseRules(raw json.RawMessage, opts Options) (rules []Rule, problems []error) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil, nil
	}
	for _, item := range list {
		var rule struct {
			Search  string   `json:"search"`
			Actions []string `json:"actions"`
		}
		if json.Unmarshal(item, &rule) != nil || !notEmpty(rule.Search) || len(rule.Actions) == 0 {
			continue
		}
		query, err := Parse(rule.Search, opts)
		if err != nil {
			problems = append(problems, fmt.Errorf("filter %q: %w", rule.Search, err))
			continue
		}
		rules = append(rules, Rule{Search: rule.Search, Actions: rule.Actions, Query: query})
	}
	return rules, problems
}

// ParseSavedQueries reads the "queries" value of a user's settings. Every
// entry keeps its place, also one without a search: queries are referred to
// by position.
func ParseSavedQueries(raw json.RawMessage) []SavedQuery {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	queries := make([]SavedQuery, len(list))
	for i, item := range list {
		var q struct {
			Name   string `json:"name"`
			Search string `json:"search"`
		}
		if json.Unmarshal(item, &q) == nil {
			queries[i] = SavedQuery{Name: q.Name, Search: q.Search}
		}
	}
	return queries
}
