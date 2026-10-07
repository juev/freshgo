#!/bin/sh
# Regenerates stats.json: the statistics FreshRSS shows alice of the
# reference installation, and those of her feed 2. A FreshRSS from the same
# image that produced ../sqlite is started on a copy of that data directory.
# Needs Docker.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

freshrss_image="freshrss/freshrss:1.30.1"
app="freshgo-stats-$$"

cleanup() {
	docker rm -f "$app" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --name "$app" -e TZ=UTC "$freshrss_image" >/dev/null
docker cp "$here/../sqlite/data/." "$app:/var/www/FreshRSS/data/"
docker cp "$here/stats.php" "$app:/tmp/stats.php"
docker exec "$app" chown -R www-data:www-data /var/www/FreshRSS/data
docker exec -u www-data "$app" php /tmp/stats.php alice 2 > "$here/stats.json"
