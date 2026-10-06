package refresh

import (
	"context"
	"encoding/json"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// labelRules are the rules by which a label attaches itself to new entries.
type labelRules struct {
	tagID int64
	rules []search.Rule
}

// rules reads the filter actions kept in a "filters" value. A rule that
// cannot be used is reported and left out; the others stand.
func (r *Refresher) rules(j *job, raw json.RawMessage, owner string, id int64) []search.Rule {
	rules, problems := search.ParseRules(raw, j.search)
	for _, problem := range problems {
		r.log.Warn("filter is not usable and is skipped", "user", j.user.Name, owner, id, "error", problem)
	}
	return rules
}

// loadRules reads the rules of the user, of the categories and of the labels
// once for a run.
func (r *Refresher) loadRules(ctx context.Context, j *job) error {
	j.search = search.Options{Queries: j.conf.queries, Now: r.now().In(j.conf.location)}
	j.rules = r.rules(j, j.conf.filters, "settings", j.user.ID)
	for id, c := range j.categories {
		c.rules = r.rules(j, c.attrs["filters"], "category", id)
	}
	tags, err := r.db.Tags(ctx, j.user.ID)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		var labelling []search.Rule
		for _, rule := range r.rules(j, readAttributes(tag.Attributes)["filters"], "label", tag.ID) {
			if rule.Has(search.ActionLabel) {
				labelling = append(labelling, rule)
			}
		}
		if len(labelling) > 0 {
			j.labels = append(j.labels, labelRules{tagID: tag.ID, rules: labelling})
		}
	}
	return nil
}

// searchable is the entry as queries see it. An entry that is not stored yet
// stands under the identifier it would get now.
func searchable(e *store.Entry, f *store.Feed, now int64) *search.Entry {
	id := e.ID
	if id == 0 {
		id = now * 1_000_000
	}
	return &search.Entry{
		ID: id, FeedID: e.FeedID, CategoryID: f.CategoryID, Title: e.Title, Authors: e.Authors, Content: e.Content,
		Link: e.Link, Tags: e.Tags, Published: e.Published, LastModified: e.LastModified, LastUserModified: e.LastUserModified,
	}
}

// applyRules lets the rules of the user, of the category and of the feed, in
// this order, mark the entry read or starred. A changed entry is not
// starred: the user may have taken the star off.
func (r *Refresher) applyRules(ctx context.Context, j *job, f *store.Feed, feedRules []search.Rule, e *store.Entry, changed bool, now int64) {
	var categoryRules []search.Rule
	if c := j.categories[f.CategoryID]; c != nil {
		categoryRules = c.rules
	}
	if len(j.rules)+len(categoryRules)+len(feedRules) == 0 {
		return
	}
	subject := searchable(e, f, now)
	for _, rules := range [][]search.Rule{j.rules, categoryRules, feedRules} {
		for _, rule := range rules {
			if !rule.Query.Match(subject) {
				continue
			}
			if rule.Has(search.ActionRead) && !e.IsRead {
				e.IsRead = true
				r.hooks.EntryAutoRead.Call(ctx, hooks.EntryAuto{Entry: e, Why: hooks.WhyFilter})
			}
			if rule.Has(search.ActionStar) && !changed {
				e.IsFavorite = true
			}
		}
	}
}

// labelsFor returns the labels whose rules match a new entry.
func labelsFor(j *job, f *store.Feed, e *store.Entry, now int64) []int64 {
	if len(j.labels) == 0 {
		return nil
	}
	var tagIDs []int64
	subject := searchable(e, f, now)
	for _, label := range j.labels {
		for _, rule := range label.rules {
			if rule.Query.Match(subject) {
				tagIDs = append(tagIDs, label.tagID)
				break
			}
		}
	}
	return tagIDs
}
