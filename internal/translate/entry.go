package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/juev/freshgo/internal/store"
)

// ErrSameLanguage is returned for an entry whose text is in the language
// it was to be translated into.
var ErrSameLanguage = errors.New("translate: the text is in that language already")

// attribute is where an entry keeps its translations, by language.
const attribute = "translations"

// record is the translation of an entry into one language, as far as it
// has come.
type record struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	// Done of Total paragraphs were asked for; Failed of them stand in the
	// language of the source.
	Done   int `json:"done"`
	Total  int `json:"total"`
	Failed int `json:"failed,omitempty"`
	// At is how many runs of the text, with words or without, are behind:
	// where the next step goes on.
	At int `json:"at"`
	// Of is the hash of the title and the text the translation was made
	// from: when the entry has another text, it has no translation.
	Of string `json:"of"`
	// Hidden is set while the reader looks at the original.
	Hidden bool `json:"hidden,omitempty"`
}

func hashOf(e *store.Entry) string {
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Content))
	return hex.EncodeToString(sum[:8])
}

func records(e *store.Entry) (map[string]json.RawMessage, map[string]record) {
	attrs := map[string]json.RawMessage{}
	_ = json.Unmarshal(e.Attributes, &attrs)
	all := map[string]record{}
	_ = json.Unmarshal(attrs[attribute], &all)
	return attrs, all
}

// State is the translation of an entry into a language as a page shows it.
type State struct {
	// Exists reports a translation of the text the entry has now, whole or
	// in part; Shown that the reader looks at it and not at the original.
	Exists, Shown bool
	// Title and Content are what to show: the translation when Shown.
	Title, Content string
	// Done of Total paragraphs were asked for, Failed of them in vain.
	Done, Total, Failed int
}

// Complete reports that nothing is left to translate.
func (s State) Complete() bool { return s.Exists && s.Done >= s.Total }

// Of returns the state of the translation of an entry into a language.
func Of(e *store.Entry, tag string) State {
	_, all := records(e)
	rec, ok := all[tag]
	if !ok || rec.Of != hashOf(e) {
		return State{Title: e.Title, Content: e.Content}
	}
	s := State{Exists: true, Shown: !rec.Hidden, Title: e.Title, Content: e.Content, Done: rec.Done, Total: rec.Total, Failed: rec.Failed}
	if s.Shown {
		s.Title, s.Content = rec.Title, rec.Content
	}
	return s
}

// Translator translates the entries of a database.
type Translator struct {
	db     *store.Store
	client *Client
}

// New returns a Translator.
func New(db *store.Store, client *Client) *Translator {
	return &Translator{db: db, client: client}
}

// sampleSize is about how many bytes of a text the model is shown to tell
// its language.
const sampleSize = 600

// Step translates the next paragraphs of an entry into the language of
// the tag, stores them and returns how far the translation is. The first
// step asks whether the text is in that language already, which is
// ErrSameLanguage and stores nothing, and translates the title. A
// translation the reader had hidden is shown again.
func (t *Translator) Step(ctx context.Context, userID, entryID int64, tag language.Tag) (State, error) {
	e, err := t.db.EntryByID(ctx, userID, entryID)
	if err != nil {
		return State{}, err
	}
	name, key := display.English.Tags().Name(tag), tag.String()
	_, all := records(e)
	rec, ok := all[key]
	var st Stats
	size := batchBytes
	if !ok || rec.Of != hashOf(e) {
		size = openingBytes
		rec = record{Of: hashOf(e), Title: e.Title, Content: e.Content}
		root, err := parse(e.Content)
		if err != nil {
			return State{}, err
		}
		// As the parser writes it: the steps count the runs of this text.
		rec.Content = render(root)
		paragraphs := units(root)
		rec.Total = len(paragraphs)
		var sample strings.Builder
		for _, p := range paragraphs {
			if sample.Len() > sampleSize {
				break
			}
			sample.WriteString(p.plain + "\n")
		}
		if sample.Len() == 0 {
			sample.WriteString(e.Title)
		}
		// The two questions are asked at once: the reader is waiting.
		var (
			asked    sync.WaitGroup
			titled   Stats
			title    string
			titleErr error
		)
		if letters.MatchString(e.Title) {
			asked.Go(func() { title, titleErr = t.client.alone(ctx, e.Title, name, &titled) })
		}
		same, err := t.client.written(ctx, sample.String(), name, &st)
		asked.Wait()
		if err != nil {
			return State{}, err
		}
		if same {
			return Of(e, key), ErrSameLanguage
		}
		if titleErr != nil {
			return State{}, titleErr
		}
		if title != "" && !mark.MatchString(title) {
			rec.Title = title
		}
	}
	root, err := parse(rec.Content)
	if err != nil {
		return State{}, err
	}
	// The runs of a text keep their numbers while it is translated, so
	// the step goes on after the last run it has dealt with.
	every := runs(root)
	var rest []*unit
	for _, run := range every[min(rec.At, len(every)):] {
		if run.words {
			rest = append(rest, run)
		}
	}
	n := first(rest, size)
	if err := t.client.translate(ctx, rest[:n], name, &st); err != nil {
		return State{}, err
	}
	rec.Done, rec.Failed, rec.Hidden, rec.At = rec.Done+n, rec.Failed+st.Failed, false, len(every)
	if n < len(rest) {
		for i, run := range every {
			if run == rest[n] {
				rec.At = i
			}
		}
	} else {
		rec.Done = rec.Total
	}
	rec.Content = render(root)
	return t.store(ctx, userID, entryID, key, func(fresh *store.Entry, _ record, _ bool) (record, bool) {
		// The text was changed meanwhile: what was translated is of another text.
		return rec, hashOf(fresh) == rec.Of
	})
}

// All translates what is left of an entry.
func (t *Translator) All(ctx context.Context, userID, entryID int64, tag language.Tag) (State, error) {
	for {
		state, err := t.Step(ctx, userID, entryID, tag)
		if err != nil || state.Complete() || !state.Exists {
			return state, err
		}
	}
}

// Show makes the reader see the translation of an entry, or its original.
func (t *Translator) Show(ctx context.Context, userID, entryID int64, tag language.Tag, shown bool) (State, error) {
	return t.store(ctx, userID, entryID, tag.String(), func(_ *store.Entry, rec record, ok bool) (record, bool) {
		rec.Hidden = !shown
		return rec, ok
	})
}

// store writes the record change returns for an entry as it is now, when
// it says to, and returns the state of the translation after it.
func (t *Translator) store(ctx context.Context, userID, entryID int64, key string, change func(e *store.Entry, rec record, ok bool) (record, bool)) (State, error) {
	var state State
	err := t.db.InTx(ctx, func(tx *store.Store) error {
		e, err := tx.EntryByID(ctx, userID, entryID)
		if err != nil {
			return err
		}
		attrs, all := records(e)
		old, ok := all[key]
		rec, write := change(e, old, ok && old.Of == hashOf(e))
		if write {
			// Translations of a text the entry no longer has go.
			for other, r := range all {
				if r.Of != hashOf(e) {
					delete(all, other)
				}
			}
			all[key] = rec
			if attrs[attribute], err = json.Marshal(all); err != nil {
				return err
			}
			if e.Attributes, err = json.Marshal(attrs); err != nil {
				return err
			}
			if err := tx.UpdateEntry(ctx, e); err != nil {
				return err
			}
		}
		state = Of(e, key)
		return nil
	})
	return state, err
}
