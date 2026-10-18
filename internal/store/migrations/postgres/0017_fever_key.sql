-- The key a Fever client logs in with: md5 of "name:API password", stored when
-- the API password is set because the bcrypt hash cannot give it back. Empty
-- while the user has not set the API password since Fever was added.
ALTER TABLE users ADD COLUMN fever_key TEXT NOT NULL DEFAULT '';
CREATE INDEX users_fever_key ON users (fever_key) WHERE fever_key <> '';
