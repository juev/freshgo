#!/bin/sh
# Regenerates sanitize.json, feeds.json, scrape.json, purge.json and
# search.json: what FreshRSS itself makes of sanitize-cases.json, of the
# corpus and feeds/, of the pages of scrape-cases.json, of the entries of
# purge-cases.json and of the queries of search-cases.json. Needs Docker.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
image=freshrss/freshrss:1.30.1

run() {
	docker run --rm -i \
		-e INTERNAL_HOST_ALLOWLIST=127.0.0.1:8080 \
		-v "$here":/oracle:ro \
		-v "$here/../corpus":/corpus:ro \
		-v "$here/force-https.txt":/var/www/FreshRSS/data/force-https.txt:ro \
		"$image" "$@"
}

run php /oracle/sanitize.php < "$here/sanitize-cases.json" > "$here/sanitize.json"
run sh -c 'php /oracle/feed.php /corpus/atom.xml /corpus/rss.xml /corpus/rss-noid.xml /oracle/feeds/*' > "$here/feeds.json"
# The pages are fetched over HTTP, as a refresh does, from a server inside the container.
run sh -c 'php -S 127.0.0.1:8080 -t /oracle/pages >/dev/null 2>&1 & sleep 1; php /oracle/scrape.php' \
	< "$here/scrape-cases.json" > "$here/scrape.json"
# The cleanup needs a database: a throwaway installation with one user.
run sh -c 'cd /var/www/FreshRSS && {
	./cli/do-install.php --default-user purge --auth-type none --environment production \
		--base-url http://freshrss.freshgo.test --language en --title FreshRSS --db-type sqlite &&
	./cli/create-user.php --user purge --language en --no-default-feeds &&
	php /oracle/purge.php fill purge &&
	./cli/purge.php --user purge
} >&2 && php /oracle/purge.php kept purge' > "$here/purge.json"
# The search language needs a user as well: saved queries and feed categories.
run sh -c 'cd /var/www/FreshRSS && {
	./cli/do-install.php --default-user search --auth-type none --environment production \
		--base-url http://freshrss.freshgo.test --language en --title FreshRSS --db-type sqlite &&
	./cli/create-user.php --user search --language en --no-default-feeds
} >&2 && php /oracle/search.php search' < "$here/search-cases.json" > "$here/search.json"
