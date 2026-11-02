-- The passkeys of users: credentials of WebAuthn that log a user in without a
-- password. A credential ID names one passkey, so it belongs to one user; a
-- user has a row for each passkey. The credential itself (its public key, the
-- count of signatures, its flags) is kept as the library that checks it
-- writes it, opaque to the store.
CREATE TABLE passkeys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	credential_id BLOB NOT NULL UNIQUE,
	name TEXT NOT NULL,
	credential BLOB NOT NULL,
	created INTEGER NOT NULL,
	used INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX passkeys_user ON passkeys (user_id);
