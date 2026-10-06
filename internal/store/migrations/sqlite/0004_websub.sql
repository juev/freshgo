-- Subscriptions to WebSub hubs, one per topic whoever reads it. key is the
-- end of the address the hub calls, secret signs what it sends. error is set
-- until a push has arrived and again when the hub lets the feed down.
CREATE TABLE websub_subscriptions (
	topic TEXT PRIMARY KEY,
	hub TEXT NOT NULL,
	key TEXT NOT NULL UNIQUE,
	secret TEXT NOT NULL,
	lease_start INTEGER NOT NULL DEFAULT 0,
	lease_end INTEGER NOT NULL DEFAULT 0,
	error BOOLEAN NOT NULL DEFAULT TRUE
);

-- The topic a feed last announced together with a hub, empty if none.
ALTER TABLE feeds ADD COLUMN websub_topic TEXT NOT NULL DEFAULT '';
CREATE INDEX feeds_websub_topic_index ON feeds (websub_topic);
