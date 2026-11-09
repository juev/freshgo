package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/juev/freshgo/internal/store"
	"golang.org/x/text/language"
)

// ErrSourceChanged refuses stale work, including a source changed while the model answered.
var ErrSourceChanged = errors.New("translate: source or progress changed")

// Fingerprint identifies the complete source title and HTML, before presentation transforms.
func Fingerprint(title, content string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// Stored returns translation content independently of the web reader's display preference.
func Stored(e *store.Entry, tag string) State {
	_, all := records(e)
	rec, ok := all[tag]
	if !ok || rec.Of != hashOf(e) {
		return State{}
	}
	return State{SameLanguage: rec.SameLanguage, Exists: true, Shown: !rec.Hidden, Title: rec.Title, Content: rec.Content, Done: rec.Done, Total: rec.Total, Failed: rec.Failed}
}

// ForClient reads a representation, or commits at most the next observed step.
// The progress precondition makes a repeated committed request read-only. All
// callers share the translator's cancellable gate with web translation steps.
func (t *Translator) ForClient(ctx context.Context, userID, entryID int64, tag language.Tag, revision string, done int, advance bool) (*store.Entry, State, error) {
	if err := t.steps.Acquire(ctx, 1); err != nil {
		return nil, State{}, err
	}
	defer t.steps.Release(1)
	e, err := t.db.EntryByID(ctx, userID, entryID)
	if err != nil {
		return nil, State{}, err
	}
	source := Fingerprint(e.Title, e.Content)
	state := Stored(e, tag.String())
	if !advance {
		return e, state, nil
	}
	if revision != source || done > state.Done {
		return nil, State{}, ErrSourceChanged
	}
	if state.Complete() || done < state.Done {
		return e, state, nil
	}
	_, err = t.step(ctx, userID, entryID, tag, false)
	if errors.Is(err, ErrSameLanguage) {
		_, err = t.store(ctx, userID, entryID, tag.String(), func(fresh *store.Entry, _ record, _ bool) (record, bool) {
			return record{SameLanguage: true, Title: e.Title, Content: e.Content, Of: hashOf(e)}, Fingerprint(fresh.Title, fresh.Content) == source
		})
	}
	if err != nil && !errors.Is(err, ErrSameLanguage) {
		return nil, State{}, err
	}
	fresh, readErr := t.db.EntryByID(ctx, userID, entryID)
	if readErr != nil {
		return nil, State{}, readErr
	}
	if Fingerprint(fresh.Title, fresh.Content) != source {
		return nil, State{}, ErrSourceChanged
	}
	return fresh, Stored(fresh, tag.String()), err
}
