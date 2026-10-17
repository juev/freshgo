-- A subscription belongs to one feed: its key and secret are that feed's
-- alone, and a push for it is stored in that feed and no other. The
-- per-topic rows are dropped: every feed gets a subscription of its own at
-- the next refresh, and a hub is asked again then.
DROP TABLE websub_subscriptions;

CREATE TABLE websub_subscriptions (
	user_id BIGINT NOT NULL,
	feed_id BIGINT NOT NULL,
	topic TEXT NOT NULL,
	hub TEXT NOT NULL,
	key TEXT NOT NULL UNIQUE,
	secret TEXT NOT NULL,
	lease_start BIGINT NOT NULL DEFAULT 0,
	lease_end BIGINT NOT NULL DEFAULT 0,
	error BOOLEAN NOT NULL DEFAULT TRUE,
	PRIMARY KEY (user_id, feed_id),
	FOREIGN KEY (user_id, feed_id) REFERENCES feeds (user_id, id) ON DELETE CASCADE
);
