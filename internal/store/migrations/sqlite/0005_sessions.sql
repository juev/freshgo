-- Logins to the web interface. token_hash is the SHA-256 of the secret the
-- browser holds, so that reading this table lets nobody in. authenticated is
-- when the password was last typed.
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created INTEGER NOT NULL,
	used INTEGER NOT NULL,
	expires INTEGER NOT NULL,
	persistent BOOLEAN NOT NULL DEFAULT FALSE,
	authenticated INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sessions_user_index ON sessions (user_id);
CREATE INDEX sessions_expires_index ON sessions (expires);
