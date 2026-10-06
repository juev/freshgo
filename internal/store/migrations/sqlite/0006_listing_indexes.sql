-- The counts of unread entries by feed are read on every page of the web
-- interface: with the identifier in the index they need no row of the table.
DROP INDEX entries_feed_read_index;
CREATE INDEX entries_feed_read_index ON entries (user_id, feed_id, is_read, id);

-- Entries listed by their date.
CREATE INDEX entries_published_index ON entries (user_id, published, id);
