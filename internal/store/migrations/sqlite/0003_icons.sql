-- Icons found at the sites of feeds. A row is shared by every feed that looks
-- for its icon at the same address. content is NULL while nothing was found.
CREATE TABLE icons (
	hash TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	content BLOB,
	content_type TEXT NOT NULL DEFAULT '',
	modified INTEGER NOT NULL DEFAULT 0,
	checked INTEGER NOT NULL DEFAULT 0
);

-- A custom icon is served by its hash as well.
ALTER TABLE custom_icons ADD COLUMN hash TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_icons ADD COLUMN modified INTEGER NOT NULL DEFAULT 0;
CREATE INDEX custom_icons_hash_index ON custom_icons (hash);
