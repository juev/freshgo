package transfer

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
)

func TestPlanFilterImport(t *testing.T) {
	exported := RemoteFilter{
		Title: "Keep news", Enabled: true,
		Rules:   []RemoteFilterRule{{Field: "title", Pattern: "news"}},
		Actions: []RemoteFilterAction{{Type: filters.ActionLabel, Label: "News"}},
	}
	targets := filterTargets{labels: []*store.Tag{{ID: 1, Name: "News"}}}
	initial := planFilterImport("source", exported, nil, 0, 1, targets)
	if initial.decision.action != addFilter || len(initial.missing) != 0 || initial.filter.Actions[0].LabelID != 1 {
		t.Fatalf("initial plan = %+v", initial)
	}
	saved := initial.filter
	saved.Source = &filters.Source{
		Account: "source", Title: exported.Title, Snapshot: initial.resolvedHash,
		Revision: initial.sourceHash, Bindings: initial.bindings.current,
	}

	edited := exported
	edited.Rules = []RemoteFilterRule{{Field: "title", Pattern: "technology"}}
	local := saved
	local.Enabled = false
	duplicate := saved
	duplicate.Source = nil
	renamed := exported
	renamed.Actions = []RemoteFilterAction{{Type: filters.ActionLabel, Label: "Reading"}}
	renamedTargets := filterTargets{labels: []*store.Tag{{ID: 1, Name: "Reading"}}}

	full := make([]filters.Filter, filters.MaxFilters)
	full[len(full)-1] = saved

	for _, tc := range []struct {
		name          string
		incoming      RemoteFilter
		saved         []filters.Filter
		existingCount int
		targets       filterTargets
		want          importAction
		index         int
		warn          bool
		missing       int
	}{
		{name: "new label is only planned", incoming: exported, want: addFilter, index: -1, missing: 1},
		{name: "unchanged revision matches a new record too", incoming: exported, saved: []filters.Filter{saved}, want: keepFilter},
		{name: "equivalent filter added in this batch is kept at the limit", incoming: renamed, saved: full, existingCount: len(full) - 1, targets: renamedTargets, want: keepFilter, index: len(full) - 1},
		{name: "source binding changed without changing local content", incoming: renamed, saved: []filters.Filter{local}, existingCount: 1, targets: renamedTargets, want: refreshSource},
		{name: "local duplicate does not intercept a known source", incoming: renamed, saved: []filters.Filter{duplicate, saved}, existingCount: 2, targets: renamedTargets, want: refreshSource, index: 1},
		{name: "local duplicate keeps its local ownership", incoming: exported, saved: []filters.Filter{duplicate}, existingCount: 1, targets: targets, want: keepFilter, index: -1},
		{name: "pending label differs from saved content", incoming: renamed, saved: []filters.Filter{saved}, existingCount: 1, targets: targets, want: replaceFilter, missing: 1},
		{name: "source edit replaces untouched content", incoming: edited, saved: []filters.Filter{saved}, existingCount: 1, targets: targets, want: replaceFilter},
		{name: "competing edits keep the local filter", incoming: edited, saved: []filters.Filter{local}, existingCount: 1, targets: targets, want: keepFilter, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planFilterImport("source", tc.incoming, tc.saved, tc.existingCount, 1, tc.targets)
			if plan.decision.action != tc.want || plan.decision.index != tc.index || (plan.decision.warning != "") != tc.warn || len(plan.missing) != tc.missing {
				t.Errorf("plan = %+v, want action %v at index %d, warning %t, missing labels %d", plan, tc.want, tc.index, tc.warn, tc.missing)
			}
		})
	}
}

func TestFilterOrderUsesOnlyTheImportedAccount(t *testing.T) {
	saved := []filters.Filter{
		{Title: "Other B", Source: &filters.Source{Account: "other", Order: 2}},
		{Title: "First", Source: &filters.Source{Account: "source", Order: 1}},
		{Title: "Other A", Source: &filters.Source{Account: "other", Order: 1}},
		{Title: "Second", Source: &filters.Source{Account: "source", Order: 2}},
	}
	var report Report
	reordered := locallyReordered(saved, "source")
	if !reconcileFilterOrder(saved, "source", []int{3, 1}, reordered, &report) || report.Incomplete {
		t.Fatalf("reconciling source order = %+v", report)
	}
	for i, want := range []string{"Other B", "Second", "Other A", "First"} {
		if saved[i].Title != want {
			t.Errorf("filter %d = %q, want %q", i, saved[i].Title, want)
		}
	}
	if saved[0].Source.Order != 2 || saved[2].Source.Order != 1 {
		t.Error("the other account's order baseline changed")
	}
}

func TestRepeatFilterImportKeepsRenamedTargets(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "{}")
		remote := &Remote{
			FilterAccount: "source",
			Categories:    []string{"News"},
			Filters: []RemoteFilter{{
				Title: "Label updates", Enabled: true,
				Rules:   []RemoteFilterRule{{Field: "content", Pattern: "updates", Categories: []string{"News"}}},
				Actions: []RemoteFilterAction{{Type: filters.ActionLabel, Label: "Updates"}},
			}},
		}
		source := &remote.Filters[0]
		saved, _ := w.importSingleFilter(u, remote)
		categories, err := w.db.Categories(ctx, u.ID)
		w.must(err)
		news := store.CategoryNamed(categories, "News")
		if news == nil {
			t.Fatal("initial import did not create News")
		}
		labelID, revision := saved.Actions[0].LabelID, saved.Source.Revision
		news.Name = "Renamed News"
		w.must(w.db.UpdateCategory(ctx, news))
		w.must(w.db.RenameTag(ctx, u.ID, labelID, "B"))

		source.Actions[0].Label = "B"
		saved, report := w.importSingleFilter(u, remote)
		if report.FiltersUpdated != 0 || report.Incomplete || saved.Source.Revision == revision {
			t.Fatalf("equivalent import did not refresh bindings: %+v, filter %+v", report, saved)
		}

		w.must(w.db.RenameTag(ctx, u.ID, labelID, "C"))
		source.Rules[0].Pattern = "revised"
		saved, report = w.importSingleFilter(u, remote)
		if report.FiltersUpdated != 1 || report.Incomplete || saved.Rules[0].Pattern != "revised" ||
			!slices.Equal(saved.Rules[0].CategoryIDs, []int64{news.ID}) || saved.Actions[0].LabelID != labelID {
			t.Fatalf("source edit lost a binding: %+v, filter %+v", report, saved)
		}
		labels, err := w.db.Tags(ctx, u.ID)
		w.must(err)
		if store.TagNamed(labels, "B") != nil {
			t.Error("the old label was recreated")
		}

		saved.Enabled = false
		w.must(w.db.UpdateUserSettings(ctx, u.ID, func(settings map[string]json.RawMessage) error {
			return filters.Save(settings, []filters.Filter{saved})
		}))
		source.Actions[0].Label = "C"
		saved, report = w.importSingleFilter(u, remote)
		if saved.Enabled || report.FiltersUpdated != 0 || report.Incomplete {
			t.Fatalf("equivalent import overwrote a local edit: %+v, filter %+v", report, saved)
		}
	})
}

func TestRepeatFilterImportKeepsBoundFeedWithSharedURL(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "{}")
		const address = "https://example.org/feed.xml"
		remote := &Remote{
			FilterAccount: "source",
			Feeds:         []RemoteFeed{{Key: "1", Title: "Blog", URL: address}},
			Filters: []RemoteFilter{{
				Title: "Read headlines", Enabled: true,
				Rules:   []RemoteFilterRule{{Field: "title", Pattern: "^Entry", Feeds: []string{address}}},
				Actions: []RemoteFilterAction{{Type: filters.ActionRead}},
			}},
		}
		saved, _ := w.importSingleFilter(u, remote)
		bound := saved.Rules[0].FeedIDs[0]
		other := &store.Feed{UserID: u.ID, URL: "https://other.example/feed.xml", Name: "Other"}
		w.must(w.db.CreateFeed(ctx, other))
		other.URL = address
		w.must(w.db.UpdateFeed(ctx, other))

		remote.Filters[0].Rules[0].Pattern = "^Changed"
		saved, report := w.importSingleFilter(u, remote)
		if report.FiltersUpdated != 1 || report.Incomplete || saved.Rules[0].Pattern != "^Changed" ||
			!slices.Equal(saved.Rules[0].FeedIDs, []int64{bound}) {
			t.Fatalf("source edit with a shared URL = %+v, filter %+v", report, saved)
		}
	})
}

func (w *world) importSingleFilter(u *store.User, remote *Remote) (filters.Filter, Report) {
	w.t.Helper()
	ctx := context.Background()
	report, err := ImportRemote(ctx, w.db, &hooks.Registry{}, u, remote, Options{Now: now})
	w.must(err)
	user, err := w.db.UserByID(ctx, u.ID)
	w.must(err)
	var settings map[string]json.RawMessage
	w.must(json.Unmarshal(user.Settings, &settings))
	saved, err := filters.Decode(settings[filters.SettingsKey])
	w.must(err)
	if len(saved) != 1 {
		w.t.Fatalf("import = %+v, want one saved filter, got %d", report, len(saved))
	}
	return saved[0], report
}
