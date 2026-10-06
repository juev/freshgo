-- Validators of the last fetched copy of the feed, for conditional requests.
ALTER TABLE feeds ADD COLUMN http_etag TEXT NOT NULL DEFAULT '';
ALTER TABLE feeds ADD COLUMN http_last_modified TEXT NOT NULL DEFAULT '';
