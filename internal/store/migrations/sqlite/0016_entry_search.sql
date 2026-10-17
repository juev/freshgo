-- The title and the content of every entry, folded by search.Fold, in an
-- index of trigrams: a search for a string narrows the entries down to those
-- that have its trigrams before they are matched one by one. The index keeps
-- no copy of the texts, and not where in a text a trigram is, which makes it
-- a fifth of the size and three times as fast to write; entry_texts ties its
-- rows to the entries, and its rows go with them. freshgo fills both, the
-- existing entries included, after this migration.
CREATE TABLE entry_texts (
	doc INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL,
	entry_id INTEGER NOT NULL,
	UNIQUE (user_id, entry_id),
	FOREIGN KEY (user_id, entry_id) REFERENCES entries (user_id, id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE entry_search USING fts5 (
	title, content, content = '', contentless_delete = 1, detail = column, tokenize = 'trigram case_sensitive 1'
);

CREATE TRIGGER entry_texts_delete AFTER DELETE ON entry_texts BEGIN
	DELETE FROM entry_search WHERE rowid = old.doc;
END;
