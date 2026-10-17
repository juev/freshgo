package store

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/search"
)

// indexText stores the title and the content of an entry, folded the way a
// search compares them, where a search looks for strings: in a table of
// PostgreSQL with indexes of trigrams, in a table of trigrams of SQLite.
func (s *Store) indexText(ctx context.Context, userID, id int64, title, content string) error {
	title, content = search.Fold(title), search.Fold(content)
	if s.driver == config.DriverPostgres {
		_, err := s.exec(ctx, `
			INSERT INTO entry_texts (user_id, entry_id, title, content) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, entry_id) DO UPDATE SET title = excluded.title, content = excluded.content`,
			userID, id, title, content)
		if err != nil {
			return fmt.Errorf("store: index the texts of entry %d: %w", id, err)
		}
		return nil
	}
	var doc int64
	err := s.queryRow(ctx, `
		INSERT INTO entry_texts (user_id, entry_id) VALUES (?, ?)
		ON CONFLICT (user_id, entry_id) DO UPDATE SET entry_id = excluded.entry_id
		RETURNING doc`, userID, id).Scan(&doc)
	if err == nil {
		_, err = s.exec(ctx, `INSERT OR REPLACE INTO entry_search (rowid, title, content) VALUES (?, ?, ?)`, doc, title, content)
	}
	if err != nil {
		return fmt.Errorf("store: index the texts of entry %d: %w", id, err)
	}
	return nil
}

// indexAllTexts indexes the texts of every entry: those stored before the
// index was. It runs in the transaction of the migration that adds it.
func (s *Store) indexAllTexts(ctx context.Context) error {
	type text struct {
		userID, id     int64
		title, content string
	}
	var afterUser, afterID int64
	for {
		rows, err := s.query(ctx, `
			SELECT user_id, id, title, content FROM entries
			WHERE (user_id, id) > (?, ?) ORDER BY user_id, id LIMIT ?`, afterUser, afterID, textBatch)
		if err != nil {
			return fmt.Errorf("store: index the texts of entries: %w", err)
		}
		batch, err := collect(rows, func(sc scanner) (text, error) {
			var t text
			return t, sc.Scan(&t.userID, &t.id, &t.title, &t.content)
		})
		if err != nil {
			return fmt.Errorf("store: index the texts of entries: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		for _, t := range batch {
			if err := s.indexText(ctx, t.userID, t.id, t.title, t.content); err != nil {
				return err
			}
		}
		last := batch[len(batch)-1]
		afterUser, afterID = last.userID, last.id
	}
}

// minIndexed is the shortest string, in characters, the index finds: it is
// made of runs of three.
const minIndexed = 3

// writeTextCondition writes a condition on the title or the content as a
// test of the index. A string too short for it is no test.
func (s *Store) writeTextCondition(b *strings.Builder, args []any, userID int64, c *search.Condition) []any {
	if utf8.RuneCountInString(c.Text) < minIndexed {
		b.WriteString(`TRUE`)
		return args
	}
	column := `title`
	if c.Kind == search.CondContent {
		column = `content`
	}
	if s.driver == config.DriverPostgres {
		b.WriteString(`id IN (SELECT t.entry_id FROM entry_texts t WHERE t.user_id = ? AND t.` + column + ` LIKE ?)`)
		return append(args, userID, `%`+likeEscaper.Replace(c.Text)+`%`)
	}
	b.WriteString(`id IN (SELECT t.entry_id FROM entry_texts t WHERE t.user_id = ? AND t.doc IN (
		SELECT rowid FROM entry_search WHERE entry_search MATCH ?))`)
	return append(args, userID, column+` : (`+strings.Join(trigrams(c.Text), ` AND `)+`)`)
}

// maxTrigrams bounds the trigrams of one string asked of the index: SQLite
// takes an expression of a few hundred terms at most, and a few dozen narrow
// as well as all.
const maxTrigrams = 32

// trigrams returns the runs of three characters of a string, each once, as
// the terms of a query of the index. The index does not know where in a
// text a trigram is: a text with them all may still not contain the string.
func trigrams(text string) []string {
	runes := []rune(text)
	seen := map[string]bool{}
	var terms []string
	for i := 0; i+minIndexed <= len(runes) && len(terms) < maxTrigrams; i++ {
		trigram := string(runes[i : i+minIndexed])
		if !seen[trigram] {
			seen[trigram] = true
			terms = append(terms, `"`+strings.ReplaceAll(trigram, `"`, `""`)+`"`)
		}
	}
	return terms
}

// likeEscaper makes a string stand for itself in a pattern of LIKE.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
