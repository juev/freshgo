-- How many refreshes of the feed have failed in a row: the more, the longer
-- the feed waits before it is tried again.
ALTER TABLE feeds ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
