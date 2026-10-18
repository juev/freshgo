package refresh

import (
	"context"

	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// suggest has the model suggest labels for the new entries that a rule with
// the action "suggest" of the user, of the category or of the feed matches.
// It runs as summarise does; the entries are not labelled, the suggestions
// wait with the entry for the user to accept.
func (r *Refresher) suggest(j *job, f *store.Feed, feedRules []search.Rule, entries []*store.Entry, now int64) {
	if r.Suggest == nil || len(entries) == 0 {
		return
	}
	var categoryRules []search.Rule
	if c := j.categories[f.CategoryID]; c != nil {
		categoryRules = c.rules
	}
	var matching []*store.Entry
	for _, e := range entries {
		subject := searchable(e, f, now)
		if matches(subject, j.rules, categoryRules, feedRules) {
			matching = append(matching, e)
		}
	}
	if len(matching) == 0 {
		return
	}
	r.background(j.user, "labels were not suggested for entries", func(ctx context.Context) error {
		return r.Suggest(ctx, j.user, matching)
	})
}

// matches reports whether a rule that asks for suggestions matches the entry.
func matches(subject *search.Entry, lists ...[]search.Rule) bool {
	for _, rules := range lists {
		for _, rule := range rules {
			if rule.Has(search.ActionSuggest) && rule.Query.Match(subject) {
				return true
			}
		}
	}
	return false
}
