-- The hub a feed last named together with its topic, empty if none. Feeds
-- that are subscribed to already get the hub of their subscription.
ALTER TABLE feeds ADD COLUMN websub_hub TEXT NOT NULL DEFAULT '';
UPDATE feeds SET websub_hub = COALESCE((SELECT hub FROM websub_subscriptions WHERE topic = feeds.websub_topic), '')
WHERE websub_topic <> '';

-- Topics and hubs were not recorded while WebSub was off, and a feed that
-- answers 304 does not tell them: every feed is read whole once more.
UPDATE feeds SET http_etag = '', http_last_modified = '';
