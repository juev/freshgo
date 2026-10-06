#!/bin/sh
# Regenerates responses.json: what FreshRSS answers to the requests of
# cases.json. A FreshRSS from the same image that produced ../sqlite is
# started on a copy of that data directory, with the corpus served next to
# it, and the comparison test of internal/greader is run against it in
# recording mode. Needs Docker and Go.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/../../.." && pwd)

freshrss_image="freshrss/freshrss:1.30.1"
nginx_image="nginx:1.29-alpine"
feeds_host="feeds.freshgo.test"

id="freshgo-api-$$"
net="$id"
feeds="$id-feeds"
app="$id-app"

cleanup() {
	docker rm -f "$app" "$feeds" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker network create "$net" >/dev/null
docker run -d --name "$feeds" --network "$net" --network-alias "$feeds_host" "$nginx_image" >/dev/null
docker cp "$here/../corpus/." "$feeds:/usr/share/nginx/html/"

# The feeds live on a private address; the allowlist lets FreshRSS fetch them.
docker run -d --name "$app" --network "$net" -p 127.0.0.1::80 \
	-e TZ=UTC -e INTERNAL_HOST_ALLOWLIST='*' "$freshrss_image" >/dev/null
docker cp "$here/../sqlite/data/." "$app:/var/www/FreshRSS/data/"
docker exec "$app" chown -R www-data:www-data /var/www/FreshRSS/data
port=$(docker port "$app" 80/tcp | head -n 1 | sed 's/.*://')
tries=0
until curl -s -o /dev/null "http://127.0.0.1:$port/api/greader.php"; do
	tries=$((tries + 1))
	[ "$tries" -lt 60 ] || { echo "FreshRSS did not start" >&2; docker logs "$app" >&2; exit 1; }
	sleep 1
done

cd "$root"
FRESHGO_RECORD_API="http://127.0.0.1:$port" go test -count=1 -run '^TestReferenceAPI$' ./internal/greader
