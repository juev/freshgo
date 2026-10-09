-- Unread and starred entries are listed by identifier: with the identifier in
-- the index the listing reads those entries alone, in order, where it used to
-- open every row of the user to look at the flag.
DROP INDEX entries_read_index;
CREATE INDEX entries_read_index ON entries (user_id, is_read, id);
DROP INDEX entries_favorite_index;
CREATE INDEX entries_favorite_index ON entries (user_id, is_favorite, id);
