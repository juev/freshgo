#!/bin/sh
# Regenerates the answers of FreshRSS to public requests for saved queries:
# a FreshRSS from the same image that produced ../sqlite is started on a copy
# of that data directory, alice is given the queries of queries.json, and
# api/query.php is asked for each of them as RSS and as JSON. Needs Docker.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

freshrss_image="freshrss/freshrss:1.30.1"
app="freshgo-queries-$$"

cleanup() {
	docker rm -f "$app" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --name "$app" -p 127.0.0.1::80 -e TZ=UTC "$freshrss_image" >/dev/null
docker cp "$here/../sqlite/data/." "$app:/var/www/FreshRSS/data/"
docker cp "$here/queries.json" "$app:/tmp/queries.json"
docker exec "$app" php -r '
	$file = "/var/www/FreshRSS/data/users/alice/config.php";
	$conf = include $file;
	$conf["queries"] = json_decode(file_get_contents("/tmp/queries.json"), true);
	file_put_contents($file, "<?php\nreturn " . var_export($conf, true) . ";\n");
'
docker exec "$app" chown -R www-data:www-data /var/www/FreshRSS/data
port=$(docker port "$app" 80/tcp | head -n 1 | sed 's/.*://')
tries=0
until curl -s -o /dev/null "http://127.0.0.1:$port/api/greader.php"; do
	tries=$((tries + 1))
	[ "$tries" -lt 60 ] || { echo "FreshRSS did not start" >&2; docker logs "$app" >&2; exit 1; }
	sleep 1
done

for token in $(sed -n 's/.*"token": *"\([A-Za-z0-9]*\)".*/\1/p' "$here/queries.json"); do
	for format in rss greader; do
		curl -sf "http://127.0.0.1:$port/api/query.php?user=alice&t=$token&f=$format&nb=100" > "$here/$token.$format"
	done
done
curl -sf "http://127.0.0.1:$port/api/query.php?user=alice&t=tokenblogs1&f=rss&nb=100&search=intitle%3ARSS" > "$here/tokenblogs1.search.rss"
