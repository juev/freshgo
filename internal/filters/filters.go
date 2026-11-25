package filters

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/go-regexp/pcre2"
)

const (
	SettingsKey     = "article_filters"
	MaxFilters      = 100
	MaxRules        = 50
	MaxActions      = 50
	MaxPatternBytes = 8192

	ActionSkip  = "skip"
	ActionRead  = "read"
	ActionStar  = "star"
	ActionLabel = "label"
	ActionStop  = "stop"
)

type Filter struct {
	Title    string   `json:"title"`
	Enabled  bool     `json:"enabled"`
	MatchAny bool     `json:"match_any,omitempty"`
	Inverse  bool     `json:"inverse,omitempty"`
	Rules    []Rule   `json:"rules"`
	Actions  []Action `json:"actions"`
	Source   *Source  `json:"source,omitempty"`
}

// Source identifies an imported filter. Tiny Tiny RSS exports omit filter IDs.
type Source struct {
	Account string `json:"account"`
	Title   string `json:"title"`

	// Snapshot hashes the last accepted content with destination IDs and is
	// the baseline for local edits.
	Snapshot string `json:"snapshot,omitempty"`
	// Revision hashes the original source content.
	Revision string `json:"revision,omitempty"`
	// Order is the baseline for detecting local moves.
	Order int `json:"order,omitempty"`
	// Bindings remember the destination ID for each source target.
	Bindings map[string]int64 `json:"bindings,omitempty"`
}

type Rule struct {
	Field       string  `json:"field"`
	Pattern     string  `json:"pattern"`
	Inverse     bool    `json:"inverse,omitempty"`
	FeedIDs     []int64 `json:"feed_ids,omitempty"`
	CategoryIDs []int64 `json:"category_ids,omitempty"`
	// LegacyScope lets an imported legacy rule outside its scope be ignored in an AND filter.
	LegacyScope bool `json:"legacy_scope,omitempty"`
}

type Action struct {
	Type    string `json:"type"`
	LabelID int64  `json:"label_id,omitempty"`
}

type compiledRule struct {
	Rule
	pattern *pcre2.Code
}

type Compiled struct {
	filter Filter
	rules  []compiledRule
}

type Article struct {
	FeedID     int64
	CategoryID int64
	Title      string
	Content    string
	Link       string
	Authors    []string
	Tags       []string
}

func Decode(raw json.RawMessage) ([]Filter, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var saved []Filter
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	return saved, nil
}

func Save(settings map[string]json.RawMessage, saved []Filter) error {
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	settings[SettingsKey] = raw
	return nil
}

// NormalizePattern stores editor and imported line breaks in the same form.
func NormalizePattern(pattern string) string {
	return strings.ReplaceAll(strings.ReplaceAll(pattern, "\r\n", "\n"), "\r", "\n")
}

// NormalizeTargets sorts scope IDs so their order in a form or export is immaterial.
func (f *Filter) NormalizeTargets() {
	for i := range f.Rules {
		rule := &f.Rules[i]
		slices.Sort(rule.FeedIDs)
		slices.Sort(rule.CategoryIDs)
		rule.FeedIDs = slices.Compact(rule.FeedIDs)
		rule.CategoryIDs = slices.Compact(rule.CategoryIDs)
	}
}
func Validate(filter Filter) error {
	_, err := compileFilter(filter)
	return err
}

func Compile(saved []Filter) ([]Compiled, error) {
	if len(saved) > MaxFilters {
		return nil, fmt.Errorf("at most %d filters are supported", MaxFilters)
	}
	var compiled []Compiled
	for i, filter := range saved {
		f, err := compileFilter(filter)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", i+1, err)
		}
		if filter.Enabled && len(f.rules) > 0 && len(filter.Actions) > 0 {
			compiled = append(compiled, f)
		}
	}
	return compiled, nil
}

func compileFilter(filter Filter) (Compiled, error) {
	f := Compiled{filter: filter}
	if len(filter.Rules) > MaxRules || len(filter.Actions) > MaxActions {
		return f, fmt.Errorf("at most %d rules and %d actions per filter are supported", MaxRules, MaxActions)
	}
	compiled, err := compileRules(filter.Rules)
	if err != nil {
		return f, err
	}
	f.rules = compiled
	for i, action := range filter.Actions {
		switch action.Type {
		case ActionSkip, ActionRead, ActionStar, ActionStop:
		case ActionLabel:
			if action.LabelID <= 0 {
				return f, fmt.Errorf("action %d requires a label name", i+1)
			}
		default:
			return f, fmt.Errorf("action %d has unsupported type %q", i+1, action.Type)
		}
	}
	return f, nil
}

// ValidateRules checks rule fields and patterns before a remote filter has IDs.
func ValidateRules(rules []Rule) error {
	_, err := compileRules(rules)
	return err
}

func compileRules(rules []Rule) ([]compiledRule, error) {
	var compiled []compiledRule
	cc := pcre2.NewCompileContext()
	cc.SetMaxPatternLength(MaxPatternBytes)
	for i, rule := range rules {
		switch rule.Field {
		case "title", "content", "both", "link", "author", "tag":
		default:
			return nil, fmt.Errorf("rule %d has unknown field %q", i+1, rule.Field)
		}
		pattern := patternLines.Replace(rule.Pattern)
		if pattern == "" || pattern == "0" {
			return nil, fmt.Errorf("rule %d needs a nonempty pattern; use [0] to match a zero", i+1)
		}
		code, errorCode, offset := pcre2.Compile([]byte(pattern), pcre2.PCRE2_CASELESS|pcre2.PCRE2_UTF|pcre2.PCRE2_UCP, cc)
		if code == nil {
			return nil, fmt.Errorf("rule %d has an invalid PCRE2 pattern at byte %d (error %d)", i+1, offset, errorCode)
		}
		compiled = append(compiled, compiledRule{Rule: rule, pattern: code})
	}
	return compiled, nil
}

// Actions selects the actions in stored order. A stop action ends processing
// at that position, including any later actions in the same filter.
func Actions(filters []Compiled, article Article) []Action {
	var actions []Action
	for _, filter := range filters {
		if !filter.matches(article) {
			continue
		}
		for _, action := range filter.filter.Actions {
			if action.Type == ActionStop {
				return actions
			}
			actions = append(actions, action)
		}
	}
	return actions
}

func (f Compiled) matches(article Article) bool {
	// Scope is evaluated before regexes and before filter inversion.
	applicable := false
	for _, rule := range f.rules {
		if rule.applies(article) {
			applicable = true
		} else if !f.filter.MatchAny && !rule.LegacyScope {
			return false
		}
	}
	if !applicable {
		return false
	}
	matched := !f.filter.MatchAny
	for _, rule := range f.rules {
		if !rule.applies(article) {
			continue
		}
		matchedRule, valid := rule.matches(article)
		if !valid {
			return false
		}
		matchedRule = matchedRule != rule.Inverse
		if f.filter.MatchAny && matchedRule {
			matched = true
			break
		}
		if !f.filter.MatchAny && !matchedRule {
			matched = false
			break
		}
	}
	return matched != f.filter.Inverse
}

func (r compiledRule) applies(article Article) bool {
	return len(r.FeedIDs)+len(r.CategoryIDs) == 0 ||
		slices.Contains(r.FeedIDs, article.FeedID) || slices.Contains(r.CategoryIDs, article.CategoryID)
}

var patternLines = strings.NewReplacer("\r", "", "\n", "")
var compactContent = strings.NewReplacer("\r", "", "\n", "", "\t", "")

func (r compiledRule) matches(article Article) (matched bool, valid bool) {
	var subjects []string
	switch r.Field {
	case "title":
		subjects = []string{article.Title}
	case "both":
		subjects = []string{article.Title, compactContent.Replace(article.Content)}
	case "content":
		subjects = []string{compactContent.Replace(article.Content)}
	case "link":
		subjects = []string{article.Link}
	case "author":
		subjects = []string{strings.Join(article.Authors, ", ")}
	case "tag":
		subjects = article.Tags
		if len(subjects) == 0 {
			subjects = []string{""}
		}
	}
	mc := pcre2.NewMatchContext()
	mc.SetMatchLimit(100000)
	mc.SetDepthLimit(1000)
	mc.SetHeapLimit(2048)
	md := pcre2.NewMatchDataSize(1)
	for _, subject := range subjects {
		rc := pcre2.Match(r.pattern, []byte(subject), 0, 0, md, mc)
		if rc >= 0 {
			return true, true
		}
		if rc != pcre2.PCRE2_ERROR_NOMATCH {
			return false, false
		}
	}
	return false, true
}
