package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
)

func importFilters(ctx context.Context, tx *store.Store, user int64, account string, incoming []RemoteFilter, feeds map[string]*store.Feed, feedIDs map[int64]bool, report *Report) error {
	if len(incoming) == 0 {
		return nil
	}
	if account == "" {
		return errors.New("filter import has no source account")
	}
	return tx.UpdateUserSettings(ctx, user, func(settings map[string]json.RawMessage) error {
		categories, err := tx.Categories(ctx, user)
		if err != nil {
			return err
		}
		labels, err := tx.Tags(ctx, user)
		if err != nil {
			return err
		}
		saved, err := filters.Decode(settings[filters.SettingsKey])
		if err != nil {
			return err
		}
		targets := filterTargets{feeds: feeds, feedIDs: feedIDs, categories: categories, labels: labels}
		existingCount := len(saved)
		localOrder := locallyReordered(saved, account)
		var order []int
		seenOrder := make(map[int]bool)
		rememberOrder := func(index int) {
			if index < 0 || seenOrder[index] {
				return
			}
			seenOrder[index] = true
			order = append(order, index)
		}
		changed := false
		counts := make(map[string]int, len(incoming))
		for _, incomingFilter := range incoming {
			counts[incomingFilter.Title]++
		}
		for _, incomingFilter := range incoming {
			plan := planFilterImport(account, incomingFilter, saved, existingCount, counts[incomingFilter.Title], targets)
			decision := plan.decision
			if decision.warning != "" {
				filterImportWarning(report, incomingFilter.Title, decision.warning)
			}
			if decision.action == keepFilter {
				rememberOrder(decision.index)
				continue
			}
			resolvedFilter, resolvedHash := plan.filter, plan.resolvedHash
			for _, pending := range plan.missing {
				label := store.TagNamed(targets.labels, pending.name)
				if label == nil {
					label = &store.Tag{UserID: user, Name: pending.name}
					if err := tx.CreateTag(ctx, label); err != nil {
						return err
					}
					targets.labels = append(targets.labels, label)
				}
				resolvedFilter.Actions[pending.index].LabelID = label.ID
				plan.bindings.remember("label", pending.name, label.ID)
			}
			if len(plan.missing) > 0 {
				resolvedHash = filterHash(resolvedFilter)
			}
			index := decision.index
			metadata := &filters.Source{
				Account: account, Title: incomingFilter.Title,
				Snapshot: resolvedHash, Revision: plan.sourceHash, Bindings: plan.bindings.current,
			}
			if index >= 0 {
				metadata.Order = saved[index].Source.Order
			}
			resolvedFilter.Source = metadata
			switch decision.action {
			case refreshSource:
				saved[index].Source = metadata
			case replaceFilter:
				saved[index] = resolvedFilter
				report.FiltersUpdated++
			case addFilter:
				index = len(saved)
				saved = append(saved, resolvedFilter)
				report.Filters++
			}
			changed = true
			rememberOrder(index)
		}
		if reconcileFilterOrder(saved, account, order, localOrder, report) {
			changed = true
		}
		if changed {
			return filters.Save(settings, saved)
		}
		return nil
	})
}

func filterImportWarning(report *Report, title, reason string) {
	report.Incomplete = true
	report.Warnings = append(report.Warnings, fmt.Sprintf("filter %q skipped: %s", title, reason))
}

type filterSource struct {
	account string
	title   string
}

func originOf(filter filters.Filter) filterSource {
	if filter.Source == nil {
		return filterSource{}
	}
	return filterSource{account: filter.Source.Account, title: filter.Source.Title}
}

func filterHash(filter filters.Filter) string {
	filter.Source = nil
	return hashFilterContent(filter)
}

func hashFilterContent(content any) string {
	payload, _ := json.Marshal(content)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

type importAction uint8

const (
	keepFilter importAction = iota
	refreshSource
	replaceFilter
	addFilter
)

type filterDecision struct {
	action  importAction
	index   int
	warning string
}

type filterPlan struct {
	decision                 filterDecision
	filter                   filters.Filter
	sourceHash, resolvedHash string
	bindings                 filterBindings
	missing                  []missingLabel
}

type filterTargets struct {
	feeds      map[string]*store.Feed
	feedIDs    map[int64]bool
	categories []*store.Category
	labels     []*store.Tag
}

// planFilterImport resolves one filter against the current account without
// writing to the database. The caller creates labels only for accepted plans.
func planFilterImport(account string, incoming RemoteFilter, saved []filters.Filter, existingCount, sourceCount int, targets filterTargets) filterPlan {
	origin := filterSource{account, incoming.Title}
	sourceHash := hashFilterContent(incoming)
	index := slices.IndexFunc(saved, func(existing filters.Filter) bool {
		return originOf(existing) == origin && existing.Source.Revision == sourceHash
	})
	if index >= 0 {
		return filterPlan{decision: filterDecision{action: keepFilter, index: index}}
	}

	existing := saved[:existingCount]
	previous := uniqueImportedFilter(existing, origin, sourceCount)
	bindings := filterBindings{current: make(map[string]int64)}
	if previous >= 0 {
		bindings.previous = existing[previous].Source.Bindings
		if len(bindings.previous) == 0 && hasFilterTargets(existing[previous]) {
			return filterPlan{decision: filterDecision{action: keepFilter, index: previous, warning: "previous target bindings are unavailable"}}
		}
	}
	resolved, missing, err := resolveFilter(incoming, targets, bindings)
	if err != nil {
		return filterPlan{decision: filterDecision{action: keepFilter, index: previous, warning: err.Error()}}
	}
	resolvedHash := filterHash(resolved)
	decision := decideFilterImport(existing, origin, resolvedHash, previous)
	if decision.action == addFilter {
		// An accepted label is visible to later plans. A pending label needs
		// a new ID and cannot match a filter already added in this batch.
		if len(missing) == 0 {
			duplicate := slices.IndexFunc(saved[existingCount:], func(existing filters.Filter) bool {
				return originOf(existing) == origin && filterHash(existing) == resolvedHash
			})
			if duplicate >= 0 {
				return filterPlan{decision: filterDecision{action: keepFilter, index: existingCount + duplicate}}
			}
		}
		if len(saved) >= filters.MaxFilters {
			decision = filterDecision{action: keepFilter, index: -1, warning: "the limit of filters is reached"}
		}
	}
	return filterPlan{
		decision: decision, filter: resolved, sourceHash: sourceHash, resolvedHash: resolvedHash,
		bindings: bindings, missing: missing,
	}
}

func decideFilterImport(saved []filters.Filter, origin filterSource, resolvedHash string, previous int) filterDecision {
	if previous >= 0 {
		existing := saved[previous]
		savedHash := filterHash(existing)
		switch {
		case resolvedHash == savedHash || resolvedHash == existing.Source.Snapshot:
			// Saved label actions and their snapshots have IDs. A pending
			// label has no ID and cannot match either hash.
			return filterDecision{action: refreshSource, index: previous}
		case savedHash != existing.Source.Snapshot:
			return filterDecision{action: keepFilter, index: previous, warning: "the filter was edited in FreshGo"}
		default:
			return filterDecision{action: replaceFilter, index: previous}
		}
	}

	sourceConflict, localTitle := false, false
	for i, existing := range saved {
		switch {
		case existing.Source != nil && originOf(existing) == origin:
			if filterHash(existing) == resolvedHash || existing.Source.Snapshot == resolvedHash {
				return filterDecision{action: keepFilter, index: i}
			}
			sourceConflict = true
		case existing.Source == nil:
			if filterHash(existing) == resolvedHash {
				return filterDecision{action: keepFilter, index: -1}
			}
			localTitle = localTitle || (existing.Title == origin.title && origin.title != "")
		}
	}
	switch {
	case sourceConflict:
		return filterDecision{action: keepFilter, index: -1, warning: "the export does not uniquely identify this filter"}
	case localTitle:
		return filterDecision{action: keepFilter, index: -1, warning: "an existing filter has the same title and no known source"}
	default:
		return filterDecision{action: addFilter, index: -1}
	}
}

func locallyReordered(saved []filters.Filter, account string) bool {
	last := 0
	for _, filter := range saved {
		source := filter.Source
		if source == nil || source.Account != account || source.Order == 0 {
			continue
		}
		if source.Order < last {
			return true
		}
		last = source.Order
	}
	return false
}

func reconcileFilterOrder(saved []filters.Filter, account string, positions []int, localOrder bool, report *Report) bool {
	if len(positions) == 0 {
		return false
	}
	slots := slices.Clone(positions)
	slices.Sort(slots)
	moved := !slices.Equal(slots, positions)
	if moved {
		if localOrder {
			report.Incomplete = true
			report.Warnings = append(report.Warnings, "filter order kept because imported filters were reordered in FreshGo")
		} else {
			// Only move imported filters among their own slots in the list.
			ordered := make([]filters.Filter, len(positions))
			for i, position := range positions {
				ordered[i] = saved[position]
			}
			for i, slot := range slots {
				saved[slot] = ordered[i]
			}
		}
	}
	return recordFilterOrder(saved, account, localOrder) || (moved && !localOrder)
}

func recordFilterOrder(saved []filters.Filter, account string, reorderConflict bool) bool {
	rank := 0
	if reorderConflict {
		// Keep the existing baseline and give newly appended filters ranks after it.
		for _, filter := range saved {
			if filter.Source != nil && filter.Source.Account == account {
				rank = max(rank, filter.Source.Order)
			}
		}
	}
	changed := false
	for i, filter := range saved {
		source := filter.Source
		if source == nil || source.Account != account || (reorderConflict && source.Order != 0) {
			continue
		}
		rank++
		if source.Order != rank {
			origin := *source
			origin.Order = rank
			saved[i].Source = &origin
			changed = true
		}
	}
	return changed
}

func uniqueImportedFilter(saved []filters.Filter, source filterSource, count int) int {
	if source.account == "" || source.title == "" || count != 1 {
		return -1
	}
	match := -1
	for i, existing := range saved {
		if originOf(existing) == source {
			if match >= 0 {
				return -1
			}
			match = i
		}
	}
	return match
}

func hasFilterTargets(filter filters.Filter) bool {
	return slices.ContainsFunc(filter.Rules, func(rule filters.Rule) bool {
		return len(rule.FeedIDs)+len(rule.CategoryIDs) > 0
	}) || slices.ContainsFunc(filter.Actions, func(action filters.Action) bool { return action.LabelID != 0 })
}

type filterBindings struct {
	previous, current map[string]int64
}

func bindingKey(kind, value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(kind+"\x00"+value)))
}

func (b filterBindings) remember(kind, value string, id int64) {
	b.current[bindingKey(kind, value)] = id
}

type missingLabel struct {
	index int
	name  string
}

func resolveFilter(incoming RemoteFilter, targets filterTargets, bindings filterBindings) (filters.Filter, []missingLabel, error) {
	filter := incoming.filter()
	for i, original := range incoming.Rules {
		rule := &filter.Rules[i]
		for _, address := range original.Feeds {
			checked, err := refresh.CheckURL(address)
			if err != nil {
				return filter, nil, errors.New("a feed in its scope has an invalid address")
			}
			address := withoutCredentials(checked)
			id := bindings.previous[bindingKey("feed", address)]
			if id != 0 {
				if !targets.feedIDs[id] {
					return filter, nil, errors.New("a previously bound feed in its scope is unavailable")
				}
			} else if feed := targets.feeds[address]; feed != nil {
				id = feed.ID
			} else {
				return filter, nil, errors.New("a feed in its scope is unavailable")
			}
			bindings.remember("feed", address, id)
			rule.FeedIDs = append(rule.FeedIDs, id)
		}
		for _, name := range original.Categories {
			var category *store.Category
			id := bindings.previous[bindingKey("category", name)]
			if name == "" && id == 0 {
				id = store.DefaultCategoryID
			}
			if id != 0 {
				index := slices.IndexFunc(targets.categories, func(c *store.Category) bool { return c.ID == id })
				if index >= 0 {
					category = targets.categories[index]
				}
			} else {
				category = store.CategoryNamed(targets.categories, name)
			}
			if category == nil {
				return filter, nil, fmt.Errorf("category %q in its scope is unavailable", name)
			}
			bindings.remember("category", name, category.ID)
			rule.CategoryIDs = append(rule.CategoryIDs, category.ID)
		}
	}
	var missing []missingLabel
	for i, action := range incoming.Actions {
		if action.Type != filters.ActionLabel {
			continue
		}
		if strings.ContainsRune(action.Label, 0) {
			return filter, nil, errors.New("invalid label name")
		}
		var label *store.Tag
		if id := bindings.previous[bindingKey("label", action.Label)]; id != 0 {
			index := slices.IndexFunc(targets.labels, func(tag *store.Tag) bool { return tag.ID == id })
			if index < 0 {
				return filter, nil, fmt.Errorf("previously bound label %q is unavailable", action.Label)
			}
			label = targets.labels[index]
		} else {
			label = store.TagNamed(targets.labels, action.Label)
		}
		if label != nil {
			bindings.remember("label", action.Label, label.ID)
			filter.Actions[i].LabelID = label.ID
			continue
		}
		if store.CategoryNamed(targets.categories, action.Label) != nil {
			return filter, nil, fmt.Errorf("a category has the name of label %q", action.Label)
		}
		missing = append(missing, missingLabel{index: i, name: action.Label})
	}
	filter.NormalizeTargets()
	return filter, missing, nil
}
