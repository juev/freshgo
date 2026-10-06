#!/bin/sh
# Regenerates sanitize.json, feeds.json and scrape.json: what FreshRSS itself
# makes of sanitize-cases.json, of the corpus and feeds/, and of the pages of
# scrape-cases.json. Needs Docker.
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
