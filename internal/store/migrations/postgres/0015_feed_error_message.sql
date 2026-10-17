-- What the last refresh of the feed failed with, empty when it succeeded.
ALTER TABLE feeds ADD COLUMN error_message TEXT NOT NULL DEFAULT '';
