-- What a listing by identifier filters on, beside the identifier: the bounds
-- of time and "unread or starred" are answered from this index, where they
-- used to open every row of the user. A row is long, the text of the entry
-- being part of it.
CREATE INDEX entries_state_index ON entries (user_id, id, feed_id, is_read, is_favorite, last_modified);

-- With the feed in them, the indexes on one state answer a list of
-- identifiers without a row as well, and stay the ones chosen for it: they
-- hold the entries in that state alone.
DROP INDEX entries_read_index;
CREATE INDEX entries_read_index ON entries (user_id, is_read, id, feed_id);
DROP INDEX entries_favorite_index;
CREATE INDEX entries_favorite_index ON entries (user_id, is_favorite, id, feed_id);
