-- What went wrong for a user, for the page that shows it: warnings and
-- errors of the server that name the user. time is Unix seconds.
CREATE TABLE logs (
	id BIGSERIAL PRIMARY KEY,
	time BIGINT NOT NULL,
	level TEXT NOT NULL,
	user_name TEXT NOT NULL,
	message TEXT NOT NULL
);
CREATE INDEX logs_user_index ON logs (user_name, id);
CREATE INDEX logs_time_index ON logs (time);
