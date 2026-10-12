-- The moment before which the server of the feed asked not to be requested:
-- how long it called its answer fresh, or the Retry-After of a 429 or 503.
ALTER TABLE feeds ADD COLUMN not_before BIGINT NOT NULL DEFAULT 0;
