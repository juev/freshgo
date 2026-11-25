package transfer

import (
	"fmt"
	"strings"

	"github.com/juev/freshgo/internal/filters"
)

// RemoteFilter is a source filter whose targets still use source names and URLs.
type RemoteFilter struct {
	Title    string               `json:"title"`
	Enabled  bool                 `json:"enabled"`
	MatchAny bool                 `json:"match_any,omitempty"`
	Inverse  bool                 `json:"inverse,omitempty"`
	Rules    []RemoteFilterRule   `json:"rules"`
	Actions  []RemoteFilterAction `json:"actions"`
}

type RemoteFilterRule struct {
	Field       string   `json:"field"`
	Pattern     string   `json:"pattern"`
	Inverse     bool     `json:"inverse,omitempty"`
	Feeds       []string `json:"feeds,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	LegacyScope bool     `json:"legacy_scope,omitempty"`
}

type RemoteFilterAction struct {
	Type  string `json:"type"`
	Label string `json:"value,omitempty"`
}

func (f RemoteFilter) Validate() error {
	if len(f.Rules) > filters.MaxRules || len(f.Actions) > filters.MaxActions {
		return fmt.Errorf("at most %d rules and %d actions per filter are supported", filters.MaxRules, filters.MaxActions)
	}
	content := f.filter()
	if err := filters.ValidateRules(content.Rules); err != nil {
		return err
	}
	for i, action := range f.Actions {
		if action.Type == filters.ActionLabel && strings.TrimSpace(action.Label) == "" {
			return fmt.Errorf("action %d requires a label name", i+1)
		}
	}
	return nil
}

// filter copies the properties shared with the saved filter. Target IDs are
// assigned by the importer before the result is saved.
func (f RemoteFilter) filter() filters.Filter {
	filter := filters.Filter{Title: f.Title, Enabled: f.Enabled, MatchAny: f.MatchAny, Inverse: f.Inverse}
	for _, rule := range f.Rules {
		filter.Rules = append(filter.Rules, filters.Rule{
			Field: rule.Field, Pattern: rule.Pattern, Inverse: rule.Inverse, LegacyScope: rule.LegacyScope,
		})
	}
	for _, action := range f.Actions {
		filter.Actions = append(filter.Actions, filters.Action{Type: action.Type})
	}
	return filter
}
