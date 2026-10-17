-- The title and the content of every entry, folded by search.Fold, in
-- indexes of trigrams: a search for a string narrows the entries down to
-- those that contain it before they are matched one by one. freshgo fills the
-- table, the existing entries included, after this migration.
--
-- pg_trgm comes with PostgreSQL and may be created by whoever can create in
-- the database. Its operator class is named with the schema it is in, which
-- is not on the search path when it was created for another schema.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE entry_texts (
	user_id BIGINT NOT NULL,
	entry_id BIGINT NOT NULL,
	title TEXT NOT NULL,
	content TEXT NOT NULL,
	PRIMARY KEY (user_id, entry_id),
	FOREIGN KEY (user_id, entry_id) REFERENCES entries (user_id, id) ON DELETE CASCADE
);

DO $$
DECLARE
	trgm TEXT := (SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pg_trgm');
BEGIN
	EXECUTE format('CREATE INDEX entry_texts_title_index ON entry_texts USING gin (title %I.gin_trgm_ops)', trgm);
	EXECUTE format('CREATE INDEX entry_texts_content_index ON entry_texts USING gin (content %I.gin_trgm_ops)', trgm);
END
$$;
