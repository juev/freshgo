-- Whether an app subscribed through the API rather than a browser from the
-- interface. An app takes a message that shows nothing, so it is told of read
-- and starred entries as well; a browser has to show every message.
ALTER TABLE push_subscriptions ADD COLUMN app BOOLEAN NOT NULL DEFAULT FALSE;
