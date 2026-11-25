package web

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/store"
)

func TestFilteringTab(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		user := s.user("alice")
		feed := s.feed("alice", 4)
		label := &store.Tag{UserID: user.ID, Name: "Filter test"}
		if err := s.db.CreateTag(context.Background(), label); err != nil {
			t.Fatal(err)
		}
		initial := []filters.Filter{
			{Title: "Scoped filter", Enabled: true, MatchAny: true,
				Rules:   []filters.Rule{{Field: "both", Pattern: "keep (this)", Inverse: true, FeedIDs: []int64{feed.ID}}},
				Actions: []filters.Action{{Type: "read"}, {Type: "label", LabelID: label.ID}}},
			{Title: "Ruleless filter", Actions: []filters.Action{{Type: "read"}}},
		}
		if err := s.db.UpdateUserSettings(context.Background(), user.ID, func(settings map[string]json.RawMessage) error {
			return filters.Save(settings, initial)
		}); err != nil {
			t.Fatal(err)
		}

		list := s.shown(filteringPath)
		for _, marker := range []string{`href="/settings/filtering" aria-current="page"`, "Scoped filter", "Ruleless filter", "Inactive"} {
			if !strings.Contains(list, marker) {
				t.Errorf("filter list missing %q", marker)
			}
		}
		if reading := s.shown("/settings/reading"); strings.Contains(reading, `name="article_filters"`) {
			t.Error("Reading still shows the JSON editor")
		}
		getSaved := func() []filters.Filter {
			t.Helper()
			saved, err := filters.Decode(readAttrs(s.user("alice").Settings)[filters.SettingsKey])
			if err != nil {
				t.Fatal(err)
			}
			return saved
		}
		editor := filteringPath + "?filter=0"
		editorPage := s.shown(editor)
		for _, marker := range []string{"All rules (AND)", "Any rule (OR)", `href="/subscriptions#labels"`} {
			if !strings.Contains(editorPage, marker) {
				t.Errorf("filter editor missing %q", marker)
			}
		}
		form := s.formAt(editor, filteringPath)
		if form.Get("rule.0.feeds") != strconv.FormatInt(feed.ID, 10) || form.Get("rule.0.pattern") != "keep (this)" ||
			!form.Has("rule.0.inverse") || form.Get("action.1.label") != strconv.FormatInt(label.ID, 10) || form.Get("match") != "any" {
			t.Fatal("the editor did not populate the imported values")
		}
		stale := maps.Clone(form)
		form.Set("command", "add-rule")
		if draft := s.post(filteringPath, form); draft.status != http.StatusOK || !strings.Contains(draft.body, `name="rule.1.pattern"`) {
			t.Fatalf("adding a draft rule returned %d", draft.status)
		}
		form.Set("rules", "2")
		form.Set("rule.1.field", "author")
		form.Set("rule.1.pattern", "(")
		form.Set("command", "add-action")
		if draft := s.post(filteringPath, form); draft.status != http.StatusOK ||
			!strings.Contains(draft.body, `name="action.2.type"`) || !reflect.DeepEqual(getSaved(), initial) {
			t.Fatalf("editing an unfinished draft returned %d or changed stored filters", draft.status)
		}
		for _, command := range []string{"action-up:1", "action-down:0"} {
			form.Set("command", command)
			draft := s.post(filteringPath, form)
			if draft.status != http.StatusOK || formValues(t, draft.body).Get("action.0.type") != filters.ActionLabel {
				t.Fatalf("%s returned %d or did not move the label action first", command, draft.status)
			}
		}
		form.Set("command", "save")
		if bad := s.post(filteringPath, form); bad.status != http.StatusBadRequest ||
			(!strings.Contains(bad.body, `name="rule.1.pattern"`) || !strings.Contains(bad.body, "Check the filter rules and actions.")) || !reflect.DeepEqual(getSaved(), initial) {
			t.Fatalf("invalid regex returned %d or changed saved filters", bad.status)
		}
		form.Set("rule.1.pattern", "^(One|\r\nEditor)$")
		if location, _ := s.follow(filteringPath, form); location != filteringPath {
			t.Errorf("saved edit redirected to %q", location)
		}
		saved := getSaved()
		if len(saved[0].Rules) != 2 || !reflect.DeepEqual(saved[0].Rules[0], initial[0].Rules[0]) ||
			!reflect.DeepEqual(saved[0].Actions, initial[0].Actions) {
			t.Fatal("editing lost a rule scope or an action")
		}
		if got := saved[0].Rules[1].Pattern; got != "^(One|\nEditor)$" {
			t.Errorf("saved pattern = %q", got)
		}
		stale.Set("command", "save")
		if conflict := s.post(filteringPath, stale); conflict.status != http.StatusConflict || !strings.Contains(conflict.body, "The filter list changed while you were editing.") {
			t.Fatalf("stale edit returned %d", conflict.status)
		}
		move := s.formAt(editor, filteringPath)
		move.Set("command", "down")
		s.follow(filteringPath, move)
		if got := getSaved()[1].Title; got != initial[0].Title {
			t.Errorf("moved filter title = %q", got)
		}
		toggle := s.formAt(filteringPath+"?filter=1", filteringPath)
		toggle.Set("command", "toggle")
		s.follow(filteringPath, toggle)
		if getSaved()[1].Enabled {
			t.Fatal("toggle left the filter enabled")
		}
		remove := s.formAt(filteringPath+"?filter=0&delete=1", filteringPath)
		remove.Set("command", "delete")
		s.follow(filteringPath, remove)
		if len(getSaved()) != 1 {
			t.Fatal("delete did not remove the selected filter")
		}
		add := s.formAt(filteringPath+"?filter=new", filteringPath)
		add.Set("title", "New filter")
		add.Set("command", "save")
		add.Set("rules", "25")
		pattern := strings.Repeat("x", 3000)
		for i := range 25 {
			prefix := "rule." + strconv.Itoa(i) + "."
			add.Set(prefix+"field", "title")
			add.Set(prefix+"pattern", pattern)
		}
		s.follow(filteringPath, add)
		if got := getSaved(); len(got) != 2 || got[1].Title != "New filter" || len(got[1].Rules) != 25 ||
			got[1].Rules[24].Pattern != pattern {
			t.Error("large filter was not appended with all its rules")
		}
	})
}
