#!/bin/sh
# Regenerates sanitize.json and feeds.json: what FreshRSS itself makes of
# sanitize-cases.json, of the corpus and of feeds/. Needs Docker.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
image=freshrss/freshrss:1.30.1

run() {
	docker run --rm -i \
		-v "$here":/oracle:ro \
		-v "$here/../corpus":/corpus:ro \
		-v "$here/force-https.txt":/var/www/FreshRSS/data/force-https.txt:ro \
		"$image" "$@"
}

run php /oracle/sanitize.php < "$here/sanitize-cases.json" > "$here/sanitize.json"
run sh -c 'php /oracle/feed.php /corpus/atom.xml /corpus/rss.xml /corpus/rss-noid.xml /oracle/feeds/*' > "$here/feeds.json"
