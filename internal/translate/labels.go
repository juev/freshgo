package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
)

// labelsAttribute is where an entry keeps the labels the model suggested.
const labelsAttribute = "label_suggestions"

const (
	// MaxLabelName bounds the name of a label, in characters.
	MaxLabelName = 191
	// maxSuggestions is how many labels the model is asked to suggest and
	// how many are kept.
	maxSuggestions = 3
	// suggestInput bounds the text the model is sent for suggestions.
	suggestInput = 6000
	// knownLabels bounds how many of the labels a user has the model is told.
	knownLabels = 200
)

// ErrNoLabels is returned when the answer of the model holds no name that
// can be a label.
var ErrNoLabels = errors.New("translate: the model suggested no usable label")

// suggestionRecord is the labels suggested for an entry.
type suggestionRecord struct {
	Names []string `json:"names"`
	// Of is the hash of the title and the text the labels were suggested
	// for: when the entry has another text, they are not offered.
	Of string `json:"of"`
	// At is when they were suggested, in Unix seconds.
	At int64 `json:"at"`
}

// LabelSuggestions returns the label names suggested for an entry that are
// still waiting for the user, for the text the entry has now.
func LabelSuggestions(e *store.Entry) []string {
	attrs := map[string]json.RawMessage{}
	_ = json.Unmarshal(e.Attributes, &attrs)
	var rec suggestionRecord
	if json.Unmarshal(attrs[labelsAttribute], &rec) != nil || rec.Of != hashOf(e) {
		return nil
	}
	return rec.Names
}

var listMark = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)])(?:\s+|$)`)

// answerNames reads the names in the answer of the model: a JSON array of
// strings, or else one name a line, with list marks and quotes taken off.
func answerNames(answer string) []string {
	var names []string
	if json.Unmarshal([]byte(answer), &names) == nil {
		return names
	}
	names = nil
	for line := range strings.SplitSeq(answer, "\n") {
		line = listMark.ReplaceAllString(line, "")
		names = append(names, strings.Trim(strings.TrimSpace(line), "\"'`"))
	}
	return names
}

// pickLabels chooses the names to suggest from the answer of the model: no
// empty or long name, none twice whatever the case, none a category has
// (a label cannot be named so) and none the entry has already as a label;
// a name that differs from a label of the user in case only is that label.
// At most maxSuggestions come back, in the order of the answer.
func pickLabels(answer string, existing, categories, applied []string) []string {
	exact := func(list []string, name string) bool { return slices.Contains(list, name) }
	folded := func(list []string, name string) bool {
		return slices.ContainsFunc(list, func(other string) bool { return strings.EqualFold(other, name) })
	}
	var picked []string
	for _, name := range answerNames(answer) {
		name = strings.Join(strings.Fields(name), " ")
		if i := slices.IndexFunc(existing, func(l string) bool { return strings.EqualFold(l, name) }); i >= 0 {
			name = existing[i]
		}
		switch {
		case name == "", utf8.RuneCountInString(name) > MaxLabelName:
		case exact(categories, name), folded(applied, name), folded(picked, name):
		default:
			picked = append(picked, name)
		}
		if len(picked) == maxSuggestions {
			break
		}
	}
	return picked
}

// SuggestLabels asks the model which labels fit an entry, given the labels
// the user has, stores the names in the attributes of the entry and
// returns them. Nothing is attached to the entry. A model that fails, whose
// answer is cut short or holds no usable name stores nothing and is an
// error. An entry whose text was changed meanwhile gets no suggestions.
func (t *Translator) SuggestLabels(ctx context.Context, userID, entryID int64) ([]string, error) {
	e, err := t.db.EntryByID(ctx, userID, entryID)
	if err != nil {
		return nil, err
	}
	text := sanitize.Text(e.Content, suggestInput)
	if text == "" && e.Title == "" {
		return nil, ErrNoText
	}
	tags, err := t.db.Tags(ctx, userID)
	if err != nil {
		return nil, err
	}
	existing := make([]string, len(tags))
	for i, tag := range tags {
		existing[i] = tag.Name
	}
	categories, err := t.db.Categories(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = c.Name
	}
	have, err := t.db.EntryLabels(ctx, userID, []int64{entryID})
	if err != nil {
		return nil, err
	}

	system := fmt.Sprintf("You suggest labels for articles. The user sends the labels they have and an article. "+
		"Answer with at most %d label names, one on a line and nothing else: prefer the labels the user has when they fit, "+
		"and make a new name of one or two words only when none does.", maxSuggestions)
	known := "none"
	if len(existing) > 0 {
		known = strings.Join(existing[:min(len(existing), knownLabels)], "\n")
	}
	answer, err := t.client.ask(ctx, system, "Labels:\n"+known+"\n\nTitle: "+e.Title+"\n\n"+text, &Stats{})
	if err != nil {
		return nil, err
	}
	picked := pickLabels(answer, existing, names, have[entryID])
	if len(picked) == 0 {
		return nil, ErrNoLabels
	}
	rec := suggestionRecord{Names: picked, Of: hashOf(e), At: time.Now().Unix()}
	var stored []string
	err = changeSuggestions(ctx, t.db, userID, entryID, func(fresh *store.Entry, attrs map[string]json.RawMessage) (bool, error) {
		if hashOf(fresh) != rec.Of {
			return false, nil
		}
		raw, err := json.Marshal(rec)
		attrs[labelsAttribute] = raw
		stored = rec.Names
		return true, err
	})
	return stored, err
}

// SuggestLabelsFor suggests labels for the entries of a user one after the
// other. An entry that fails does not keep the others from being tried; the
// errors are joined.
func (t *Translator) SuggestLabelsFor(ctx context.Context, u *store.User, entries []*store.Entry) error {
	var errs []error
	for _, e := range entries {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if _, err := t.SuggestLabels(ctx, u.ID, e.ID); err != nil {
			errs = append(errs, fmt.Errorf("entry %d: %w", e.ID, err))
		}
	}
	return errors.Join(errs...)
}

// DropSuggestion takes a name off the suggestions of an entry, all of them
// when the name is empty. An entry that has none left keeps no record.
func DropSuggestion(ctx context.Context, db *store.Store, userID, entryID int64, name string) error {
	return changeSuggestions(ctx, db, userID, entryID, func(fresh *store.Entry, attrs map[string]json.RawMessage) (bool, error) {
		var rec suggestionRecord
		if json.Unmarshal(attrs[labelsAttribute], &rec) != nil {
			return false, nil
		}
		rec.Names = slices.DeleteFunc(rec.Names, func(n string) bool { return name == "" || n == name })
		if len(rec.Names) == 0 {
			delete(attrs, labelsAttribute)
			return true, nil
		}
		raw, err := json.Marshal(rec)
		attrs[labelsAttribute] = raw
		return true, err
	})
}

// changeSuggestions lets change edit the attributes of the entry as it is
// stored now, in a transaction, and stores them when change says it did.
func changeSuggestions(ctx context.Context, db *store.Store, userID, entryID int64, change func(*store.Entry, map[string]json.RawMessage) (bool, error)) error {
	return db.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.EntryByID(ctx, userID, entryID)
		if err != nil {
			return err
		}
		attrs := map[string]json.RawMessage{}
		_ = json.Unmarshal(fresh.Attributes, &attrs)
		changed, err := change(fresh, attrs)
		if err != nil || !changed {
			return err
		}
		if fresh.Attributes, err = json.Marshal(attrs); err != nil {
			return err
		}
		return tx.UpdateEntry(ctx, fresh)
	})
}
