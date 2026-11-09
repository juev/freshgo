package greader

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
	"golang.org/x/text/language"
)

// TranslationPath is an optional, API-authenticated extension, not Google Reader API.
const TranslationPath = "/api/freshgo/translation"

func translationRequest(q *request) bool {
	return len(q.parts) >= 4 && q.parts[1] == "api" && q.parts[2] == "freshgo" && q.parts[3] == "translation"
}

type translationResult struct {
	ID           string `json:"id"`
	Language     string `json:"language"`
	SourceHash   string `json:"sourceHash"`
	Revision     string `json:"revision"`
	Exists       bool   `json:"exists"`
	Complete     bool   `json:"complete"`
	SameLanguage bool   `json:"sameLanguage"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Done         int    `json:"done"`
	Total        int    `json:"total"`
	Failed       int    `json:"failed"`
}

func (h *Handler) translation(ctx context.Context, q *request) error {
	if len(q.parts) == 4 && q.r.Method == http.MethodGet {
		return writeJSON(q.w, struct {
			Version int  `json:"version"`
			Enabled bool `json:"enabled"`
		}{1, h.translator != nil})
	}
	if h.translator == nil {
		text(q.w, 404, "Not Found!")
		return nil
	}
	if len(q.parts) != 5 || q.parts[4] != "entry" || (q.r.Method != http.MethodGet && q.r.Method != http.MethodPost) {
		return errBadRequest
	}
	id, err := strconv.ParseInt(q.either("id"), 10, 64)
	if err != nil || id <= 0 {
		return errBadRequest
	}
	rawTag := q.either("language")
	tag, err := language.Parse(rawTag)
	if err != nil || tag == language.Und || len(rawTag) > 64 {
		return errBadRequest
	}
	advance := q.r.Method == http.MethodPost
	done := 0
	revision := q.post("revision")
	if advance {
		done, err = strconv.Atoi(q.post("done"))
		if err != nil || done < 0 || len(revision) != 64 {
			return errBadRequest
		}
	}
	before, err := h.db.EntryByID(ctx, q.user.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		text(q.w, 404, "Not Found!")
		return nil
	}
	if err != nil {
		return err
	}
	if len(before.Content) > maxContent {
		text(q.w, 409, "Reader source is truncated")
		return nil
	}
	e, state, err := h.translator.ForClient(ctx, q.user.ID, id, tag, revision, done, advance)
	switch {
	case errors.Is(err, store.ErrNotFound):
		text(q.w, 404, "Not Found!")
		return nil
	case errors.Is(err, translate.ErrSourceChanged):
		text(q.w, 409, "Source or progress changed")
		return nil
	case err != nil && !errors.Is(err, translate.ErrSameLanguage):
		if ctx.Err() != nil {
			return ctx.Err()
		}
		text(q.w, 502, "Translation service failed")
		return nil
	}
	f, err := h.db.FeedByID(ctx, q.user.ID, e.FeedID)
	if err != nil {
		return err
	}
	original := newItem(e, f, "", nil)
	through, err := h.throughServer(q)
	if err != nil {
		return err
	}
	sourceContent := through(original.Summary.Content)
	// Google Reader clients decode fullwidth title substitutions for display.
	title := strings.NewReplacer("＆", "&", "＜", "<", "＞", ">", "？", "?", "＼", "\\", "／", "/", "，", ",", "；", ";").Replace(original.Title)
	result := translationResult{ID: strconv.FormatInt(id, 10), Language: tag.String(), SourceHash: translate.Fingerprint(title, sourceContent), Revision: translate.Fingerprint(e.Title, e.Content), Exists: state.Exists, Complete: state.Complete(), SameLanguage: state.SameLanguage, Title: state.Title, Content: through(state.Content), Done: state.Done, Total: state.Total, Failed: state.Failed}
	return writeJSON(q.w, result)
}
