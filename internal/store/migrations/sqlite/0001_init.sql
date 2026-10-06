CREATE TABLE users (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	api_password_hash TEXT NOT NULL DEFAULT '',
	settings TEXT NOT NULL DEFAULT '{}'
);

-- Last identifier issued per user and kind: 'category', 'feed', 'tag', 'entry'.
CREATE TABLE sequences (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	value INTEGER NOT NULL,
	PRIMARY KEY (user_id, name)
);

CREATE TABLE categories (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	id INTEGER NOT NULL,
	name TEXT NOT NULL,
	kind INTEGER NOT NULL DEFAULT 0,
	last_update INTEGER NOT NULL DEFAULT 0,
	error INTEGER NOT NULL DEFAULT 0,
	attributes TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (user_id, id),
	UNIQUE (user_id, name)
);

CREATE TABLE feeds (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	id INTEGER NOT NULL,
	url TEXT NOT NULL,
	kind INTEGER NOT NULL DEFAULT 0,
	category_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	website TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	last_update INTEGER NOT NULL DEFAULT 0,
	priority INTEGER NOT NULL DEFAULT 10,
	path_entries TEXT NOT NULL DEFAULT '',
	http_auth TEXT NOT NULL DEFAULT '',
	error INTEGER NOT NULL DEFAULT 0,
	ttl INTEGER NOT NULL DEFAULT 0,
	attributes TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (user_id, id),
	FOREIGN KEY (user_id, category_id) REFERENCES categories (user_id, id)
);
CREATE INDEX feeds_category_index ON feeds (user_id, category_id);

CREATE TABLE entries (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	id INTEGER NOT NULL,
	feed_id INTEGER NOT NULL,
	guid TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	authors TEXT NOT NULL DEFAULT '[]',
	content TEXT NOT NULL DEFAULT '',
	link TEXT NOT NULL DEFAULT '',
	published INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL DEFAULT 0,
	last_modified INTEGER NOT NULL DEFAULT 0,
	last_user_modified INTEGER NOT NULL DEFAULT 0,
	hash BLOB,
	is_read BOOLEAN NOT NULL DEFAULT FALSE,
	is_favorite BOOLEAN NOT NULL DEFAULT FALSE,
	tags TEXT NOT NULL DEFAULT '[]',
	attributes TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (user_id, id),
	UNIQUE (user_id, feed_id, guid),
	FOREIGN KEY (user_id, feed_id) REFERENCES feeds (user_id, id) ON DELETE CASCADE
);
CREATE INDEX entries_feed_read_index ON entries (user_id, feed_id, is_read);
CREATE INDEX entries_read_index ON entries (user_id, is_read);
CREATE INDEX entries_favorite_index ON entries (user_id, is_favorite);
CREATE INDEX entries_last_seen_index ON entries (user_id, feed_id, last_seen);

CREATE TABLE tags (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	id INTEGER NOT NULL,
	name TEXT NOT NULL,
	attributes TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (user_id, id),
	UNIQUE (user_id, name)
);

CREATE TABLE entry_tags (
	user_id INTEGER NOT NULL,
	tag_id INTEGER NOT NULL,
	entry_id INTEGER NOT NULL,
	PRIMARY KEY (user_id, tag_id, entry_id),
	FOREIGN KEY (user_id, tag_id) REFERENCES tags (user_id, id) ON DELETE CASCADE,
	FOREIGN KEY (user_id, entry_id) REFERENCES entries (user_id, id) ON DELETE CASCADE
);
CREATE INDEX entry_tags_entry_index ON entry_tags (user_id, entry_id);

-- Installation-wide values such as the token salt.
CREATE TABLE settings (
	name TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Icons set by the user for a feed; icons fetched from sites are kept elsewhere.
CREATE TABLE custom_icons (
	user_id INTEGER NOT NULL,
	feed_id INTEGER NOT NULL,
	content BLOB NOT NULL,
	PRIMARY KEY (user_id, feed_id),
	FOREIGN KEY (user_id, feed_id) REFERENCES feeds (user_id, id) ON DELETE CASCADE
);
