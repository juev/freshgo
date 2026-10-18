package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
)

// summaryAttribute is where an entry keeps its summary.
const summaryAttribute = "summary"

const (
	// LongText is how many characters of text make an entry long: the
	// feeds that ask for summaries get one for each new entry above it.
	LongText = 3000
	// summaryInput bounds the text the model is sent; a longer one is cut,
	// and what the model summarises is its beginning.
	summaryInput = 12000
)

// ErrNoText is returned for an entry that has no text to summarise.
var ErrNoText = errors.New("translate: the entry has no text to summarise")

// summaryRecord is the summary of an entry, in one language.
type summaryRecord struct {
	Text string `json:"text"`
	// Of is the hash of the title and the text the summary was made from:
	// when the entry has another text, it has no summary.
	Of       string `json:"of"`
	Language string `json:"language"`
	// At is when the summary was made, in Unix seconds.
	At int64 `json:"at"`
}

// SummaryOf returns the summary of an entry in the language of the tag;
// ok is false when the entry has none for the text it has now.
func SummaryOf(e *store.Entry, tag language.Tag) (text string, ok bool) {
	attrs := map[string]json.RawMessage{}
	_ = json.Unmarshal(e.Attributes, &attrs)
	var rec summaryRecord
	if json.Unmarshal(attrs[summaryAttribute], &rec) != nil || rec.Text == "" || rec.Of != hashOf(e) || rec.Language != tag.String() {
		return "", false
	}
	return rec.Text, true
}

// Long reports whether the text of an entry is longer than LongText.
func Long(e *store.Entry) bool {
	// A text of LongText characters or fewer comes back whole from both
	// calls; a longer one is cut at a different place by each.
	return sanitize.Text(e.Content, LongText) != sanitize.Text(e.Content, 2*LongText)
}

// UserLanguage is the language a user has summaries in: the one entries
// are translated into, or else that of the interface, or else English.
func UserLanguage(settings json.RawMessage) language.Tag {
	var s struct {
		TranslateTo string `json:"translate_to"`
		Language    string `json:"language"`
	}
	_ = json.Unmarshal(settings, &s)
	for _, code := range []string{s.TranslateTo, s.Language} {
		if tag, err := language.Parse(code); err == nil && code != "" {
			return tag
		}
	}
	return language.English
}

// Summarise asks the model for a summary of an entry in the language of
// the tag, stores it in the attributes of the entry and returns it. The
// title and the text are not touched. A model that fails, or whose answer
// is cut short, is an error and stores nothing. An entry whose text was
// changed meanwhile gets no summary and an empty answer.
func (t *Translator) Summarise(ctx context.Context, userID, entryID int64, tag language.Tag) (string, error) {
	e, err := t.db.EntryByID(ctx, userID, entryID)
	if err != nil {
		return "", err
	}
	text := sanitize.Text(e.Content, summaryInput)
	if text == "" {
		return "", ErrNoText
	}
	system := "You summarise articles. The user sends an article. Write its summary in " + display.English.Tags().Name(tag) +
		", whatever language the article is in: at most five sentences of plain text, with no markup, lists or headings. " +
		"Answer with the summary alone."
	answer, err := t.client.ask(ctx, system, "Title: "+e.Title+"\n\n"+text, &Stats{})
	if err != nil {
		return "", err
	}
	if answer == "" {
		return "", errors.New("translate: no summary")
	}
	rec := summaryRecord{Text: answer, Of: hashOf(e), Language: tag.String(), At: time.Now().Unix()}
	var stored string
	err = t.db.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.EntryByID(ctx, userID, entryID)
		if err != nil {
			return err
		}
		if hashOf(fresh) != rec.Of {
			return nil
		}
		attrs := map[string]json.RawMessage{}
		_ = json.Unmarshal(fresh.Attributes, &attrs)
		if attrs[summaryAttribute], err = json.Marshal(rec); err != nil {
			return err
		}
		if fresh.Attributes, err = json.Marshal(attrs); err != nil {
			return err
		}
		stored = rec.Text
		return tx.UpdateEntry(ctx, fresh)
	})
	return stored, err
}

// SummariseLong summarises the long entries among those of a user, in the
// language of the user, one after the other. An entry that fails does not
// keep the others from being tried; the errors are joined.
func (t *Translator) SummariseLong(ctx context.Context, u *store.User, entries []*store.Entry) error {
	tag := UserLanguage(u.Settings)
	var errs []error
	for _, e := range entries {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if !Long(e) {
			continue
		}
		if _, err := t.Summarise(ctx, u.ID, e.ID, tag); err != nil {
			errs = append(errs, fmt.Errorf("entry %d: %w", e.ID, err))
		}
	}
	return errors.Join(errs...)
}
