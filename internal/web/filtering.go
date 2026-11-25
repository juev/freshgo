package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/filters"
	"github.com/juev/freshgo/internal/store"
)

const filteringPath = "/settings/filtering"

// Leave room for URL encoding to triple the size of each submitted pattern.
const maxFilterForm = 2 << 20

type filteringData struct {
	ConfirmDelete          bool
	LastFilter, LastAction int
	Filters                []filters.Filter
	Version                string
	Index                  string
	Problem                string
	Detail                 string
	Filter                 filters.Filter
	Rules                  []filterRuleOptions
	Actions                []filterActionOptions
}

type filterRuleOptions struct {
	Fields, Feeds, Categories []option
	Rule                      filters.Rule
}

type filterActionOptions struct {
	Types, Labels []option
}

func readFilters(raw json.RawMessage) ([]filters.Filter, string, error) {
	saved, err := filters.Decode(raw)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return saved, hex.EncodeToString(sum[:]), nil
}

func filterIndex(value string, length int) int {
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 || index >= length {
		return -1
	}
	return index
}

func (h *Handler) filteringPage(w http.ResponseWriter, r *http.Request) {
	saved, version, err := readFilters(userSettings(r)[filters.SettingsKey])
	if err != nil {
		h.broken(w, r, err)
		return
	}
	page := filteringData{Filters: saved, Version: version, Index: r.URL.Query().Get("filter")}
	switch page.Index {
	case "":
	case "new":
		page.Filter = filters.Filter{
			Enabled: true,
			Rules:   []filters.Rule{{Field: "title"}},
			Actions: []filters.Action{{Type: filters.ActionRead}},
		}
	default:
		index := filterIndex(page.Index, len(saved))
		if index == -1 {
			h.fail(w, r, http.StatusNotFound)
			return
		}
		page.Filter = saved[index]
		page.ConfirmDelete = r.URL.Query().Get("delete") == "1"
	}
	h.showFiltering(w, r, http.StatusOK, page)
}

func (h *Handler) showFiltering(w http.ResponseWriter, r *http.Request, status int, page filteringData) {
	v := h.settingsView(r, "filtering")
	if page.Problem != "" {
		page.Problem = v.T(page.Problem)
	}
	page.LastFilter, page.LastAction = len(page.Filters)-1, len(page.Filter.Actions)-1
	if page.Index != "" && !page.ConfirmDelete {
		if err := h.filteringOptions(r, v, &page); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	v.Data = page
	h.render(w, r, status, "filtering", v)
}

func (h *Handler) filteringOptions(r *http.Request, v *view, page *filteringData) error {
	ctx, user := r.Context(), state(r).who.user.ID
	feeds, err := h.db.Feeds(ctx, user)
	if err != nil {
		return err
	}
	categories, err := h.db.Categories(ctx, user)
	if err != nil {
		return err
	}
	labels, err := h.db.Tags(ctx, user)
	if err != nil {
		return err
	}
	feedOptions := make([]option, 0, len(feeds))
	for _, feed := range feeds {
		name := feed.Name
		if name == "" {
			name = feed.URL
		}
		feedOptions = append(feedOptions, option{Value: strconv.FormatInt(feed.ID, 10), Name: name})
	}
	categoryOptions := make([]option, 0, len(categories))
	for _, category := range categories {
		categoryOptions = append(categoryOptions, option{Value: strconv.FormatInt(category.ID, 10), Name: category.Name})
	}
	labelOptions := []option{{Name: v.T("filtering.choose-label")}}
	for _, label := range labels {
		labelOptions = append(labelOptions, option{Value: strconv.FormatInt(label.ID, 10), Name: label.Name})
	}
	fields := filterOptions(v, "filtering.field.", "title", "content", "both", "link", "author", "tag")
	types := filterOptions(v, "filtering.action.", filters.ActionSkip, filters.ActionRead, filters.ActionStar, filters.ActionLabel, filters.ActionStop)
	for _, rule := range page.Filter.Rules {
		page.Rules = append(page.Rules, filterRuleOptions{
			Rule: rule, Fields: selectFilterOptions(fields, []string{rule.Field}),
			Feeds:      selectFilterTargets(v, feedOptions, rule.FeedIDs),
			Categories: selectFilterTargets(v, categoryOptions, rule.CategoryIDs),
		})
	}
	for _, action := range page.Filter.Actions {
		page.Actions = append(page.Actions, filterActionOptions{
			Types:  selectFilterOptions(types, []string{action.Type}),
			Labels: selectFilterTargets(v, labelOptions, []int64{action.LabelID}),
		})
	}
	return nil
}

func filterOptions(v *view, prefix string, keys ...string) []option {
	options := make([]option, 0, len(keys))
	for _, key := range keys {
		options = append(options, option{Value: key, Name: v.T(prefix + key)})
	}
	return options
}

// Keep a saved scope visible after a subscription, category or label was removed.
func selectFilterOptions(options []option, selected []string) []option {
	out := slices.Clone(options)
	for _, value := range selected {
		index := slices.IndexFunc(out, func(item option) bool { return item.Value == value })
		if index >= 0 {
			out[index].On = true
		} else {
			out = append(out, option{Value: value, Name: value, On: true})
		}
	}
	return out
}

func selectFilterTargets(v *view, options []option, ids []int64) []option {
	var selected []string
	for _, id := range ids {
		if id != 0 {
			selected = append(selected, strconv.FormatInt(id, 10))
		}
	}
	out := selectFilterOptions(options, selected)
	for i := len(options); i < len(out); i++ {
		out[i].Name = v.T("filtering.unavailable", out[i].Value)
	}
	return out
}

var errFiltersChanged = errors.New("filters changed while editing")
var errFilterLimit = errors.New("limit of filters reached")

func (h *Handler) saveFiltering(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFilterForm)
	if err := r.ParseForm(); err != nil || !isText(r.PostForm) {
		if errors.As(err, new(*http.MaxBytesError)) {
			h.showFiltering(w, r, http.StatusRequestEntityTooLarge, filteringData{Problem: "filtering.large"})
			return
		}
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	post := r.PostForm
	cmd, row, _ := strings.Cut(post.Get("command"), ":")
	page := filteringData{Index: post.Get("filter"), Version: post.Get("version")}
	switch cmd {
	case "save", "add-rule", "remove-rule", "add-action", "remove-action", "action-up", "action-down":
		filter, err := filterFromForm(post)
		if err != nil {
			h.fail(w, r, http.StatusBadRequest)
			return
		}
		page.Filter = filter
		if cmd != "save" {
			if !editFilterDraft(&page.Filter, cmd, row) {
				h.fail(w, r, http.StatusBadRequest)
				return
			}
			h.showFiltering(w, r, http.StatusOK, page)
			return
		}
		if err := filters.Validate(page.Filter); err != nil {
			page.Problem = "filtering.invalid"
			page.Detail = err.Error()
			h.showFiltering(w, r, http.StatusBadRequest, page)
			return
		}
	case "up", "down", "toggle", "delete":
	default:
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	var current filteringData
	err := h.db.UpdateUserSettings(r.Context(), state(r).who.user.ID, func(settings map[string]json.RawMessage) error {
		saved, version, err := readFilters(settings[filters.SettingsKey])
		if err != nil {
			return err
		}
		if page.Version != version {
			current = filteringData{Filters: saved, Version: version, Problem: "filtering.changed"}
			return errFiltersChanged
		}
		index := filterIndex(page.Index, len(saved))
		switch {
		case cmd == "save" && page.Index == "new":
			if len(saved) >= filters.MaxFilters {
				return errFilterLimit
			}
			saved = append(saved, page.Filter)
		case index == -1:
			return store.ErrNotFound
		default:
			switch cmd {
			case "save":
				page.Filter.Source = saved[index].Source
				saved[index] = page.Filter
			case "delete":
				saved = slices.Delete(saved, index, index+1)
			case "toggle":
				saved[index].Enabled = !saved[index].Enabled
			case "up":
				moveFilterRow(saved, index, -1)
			case "down":
				moveFilterRow(saved, index, 1)
			}
		}
		return filters.Save(settings, saved)
	})
	switch {
	case errors.Is(err, errFiltersChanged):
		h.showFiltering(w, r, http.StatusConflict, current)
	case errors.Is(err, errFilterLimit):
		page.Problem = "filtering.limit"
		h.showFiltering(w, r, http.StatusBadRequest, page)
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.saved", 0)
		http.Redirect(w, r, h.url(filteringPath), http.StatusSeeOther)
	}
}

func filterFromForm(form url.Values) (filters.Filter, error) {
	filter := filters.Filter{
		Title: strings.TrimSpace(form.Get("title")), Enabled: form.Has("enabled"),
		MatchAny: form.Get("match") == "any", Inverse: form.Has("inverse"),
	}
	rules, ruleErr := strconv.Atoi(form.Get("rules"))
	actions, actionErr := strconv.Atoi(form.Get("actions"))
	if ruleErr != nil || actionErr != nil || rules < 0 || rules > filters.MaxRules || actions < 0 || actions > filters.MaxActions {
		return filter, fmt.Errorf("invalid rule or action count")
	}
	for i := range rules {
		prefix := fmt.Sprintf("rule.%d.", i)
		feeds, err := parseFilterTargetIDs(form[prefix+"feeds"])
		if err != nil {
			return filter, err
		}
		categories, err := parseFilterTargetIDs(form[prefix+"categories"])
		if err != nil {
			return filter, err
		}
		rule := filters.Rule{
			Field: form.Get(prefix + "field"), Pattern: filters.NormalizePattern(form.Get(prefix + "pattern")), Inverse: form.Has(prefix + "inverse"),
			FeedIDs: feeds, CategoryIDs: categories, LegacyScope: form.Get(prefix+"legacy_scope") == "1",
		}
		filter.Rules = append(filter.Rules, rule)
	}
	for i := range actions {
		prefix := fmt.Sprintf("action.%d.", i)
		action := filters.Action{Type: form.Get(prefix + "type")}
		if action.Type == filters.ActionLabel {
			if value := form.Get(prefix + "label"); value != "" {
				ids, err := parseFilterTargetIDs([]string{value})
				if err != nil {
					return filter, err
				}
				action.LabelID = ids[0]
			}
		}
		filter.Actions = append(filter.Actions, action)
	}
	filter.NormalizeTargets()
	return filter, nil
}

func parseFilterTargetIDs(values []string) ([]int64, error) {
	var ids []int64
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid filter target")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func editFilterDraft(filter *filters.Filter, cmd, row string) bool {
	switch cmd {
	case "add-rule":
		if len(filter.Rules) >= filters.MaxRules {
			return false
		}
		filter.Rules = append(filter.Rules, filters.Rule{Field: "title"})
	case "remove-rule":
		i := filterIndex(row, len(filter.Rules))
		if i < 0 {
			return false
		}
		filter.Rules = slices.Delete(filter.Rules, i, i+1)
	case "add-action":
		if len(filter.Actions) >= filters.MaxActions {
			return false
		}
		filter.Actions = append(filter.Actions, filters.Action{Type: filters.ActionRead})
	default:
		i := filterIndex(row, len(filter.Actions))
		if i < 0 {
			return false
		}
		switch cmd {
		case "remove-action":
			filter.Actions = slices.Delete(filter.Actions, i, i+1)
		case "action-up":
			moveFilterRow(filter.Actions, i, -1)
		case "action-down":
			moveFilterRow(filter.Actions, i, 1)
		}
	}
	return true
}

// moveFilterRow moves a valid row one place when it has a neighbor.
func moveFilterRow[T any](rows []T, index, offset int) {
	neighbor := index + offset
	if neighbor >= 0 && neighbor < len(rows) {
		rows[index], rows[neighbor] = rows[neighbor], rows[index]
	}
}
