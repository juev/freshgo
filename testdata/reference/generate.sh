#!/bin/sh
# Regenerates the reference data from a real FreshRSS running in Docker:
# once on SQLite (sqlite/data) and once on PostgreSQL (pgsql/data, pgsql/dump.sql).
# The result is committed; tests read it without Docker.
#
# Usage: testdata/reference/generate.sh [sqlite|pgsql]...
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

freshrss_image="freshrss/freshrss:1.30.1"
postgres_image="postgres:17-alpine"
nginx_image="nginx:1.29-alpine"
feeds_host="feeds.freshgo.test"

id="freshgo-ref-$$"
net="$id"
feeds="$id-feeds"
db="$id-db"
app="$id-app"

cleanup() {
	docker rm -f "$app" "$db" "$feeds" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# cli runs a FreshRSS command-line script as the web server user.
cli() {
	docker exec --user www-data "$app" "$@"
}

generate() {
	engine=$1
	out="$here/$engine"
	cleanup
	docker network create "$net" >/dev/null

	docker run -d --name "$feeds" --network "$net" --network-alias "$feeds_host" "$nginx_image" >/dev/null
	docker cp "$here/corpus/." "$feeds:/usr/share/nginx/html/"

	install_db="--db-type sqlite"
	if [ "$engine" = pgsql ]; then
		docker run -d --name "$db" --network "$net" --network-alias db \
			-e POSTGRES_USER=freshrss -e POSTGRES_PASSWORD=freshrss -e POSTGRES_DB=freshrss \
			"$postgres_image" >/dev/null
		tries=0
		until docker exec "$db" psql -q -h 127.0.0.1 -U freshrss -d freshrss -c 'SELECT 1' >/dev/null 2>&1; do
			tries=$((tries + 1))
			[ "$tries" -lt 60 ] || { echo "PostgreSQL did not start" >&2; exit 1; }
			sleep 1
		done
		install_db="--db-type pgsql --db-host db --db-user freshrss --db-password freshrss --db-base freshrss --db-prefix freshrss_"
	fi

	# The feeds live on a private address; the allowlist lets FreshRSS fetch them.
	docker run -d --name "$app" --network "$net" -p 127.0.0.1::80 \
		-e TZ=UTC -e INTERNAL_HOST_ALLOWLIST='*' "$freshrss_image" >/dev/null
	port=$(docker port "$app" 80/tcp | head -n 1 | sed 's/.*://')
	tries=0
	until curl -s -o /dev/null "http://127.0.0.1:$port/"; do
		tries=$((tries + 1))
		[ "$tries" -lt 60 ] || { echo "FreshRSS did not start" >&2; docker logs "$app" >&2; exit 1; }
		sleep 1
	done

	# shellcheck disable=SC2086
	cli ./cli/do-install.php --default-user alice --auth-type form --environment production \
		--base-url http://freshrss.freshgo.test --language en --title FreshRSS --api-enabled $install_db
	cli sh -c 'printf "example.net\n" > data/force-https.txt'
	for user in alice bob; do
		cli ./cli/create-user.php --user "$user" --password "$user-web-password" \
			--api-password "$user-api-password" --language en --no-default-feeds
		docker cp "$here/$user.opml.xml" "$app:/tmp/$user.opml.xml"
		cli ./cli/import-for-user.php --user "$user" --filename "/tmp/$user.opml.xml"
		cli ./cli/actualize-user.php --user "$user"
	done

	docker cp "$here/custom-icon.png" "$app:/tmp/custom-icon.png"
	docker cp "$here/set-custom-icon.php" "$app:/tmp/set-custom-icon.php"
	cli php /tmp/set-custom-icon.php alice 2 /tmp/custom-icon.png

	(cd "$root" && go run ./testdata/reference/scenario "http://127.0.0.1:$port")

	rm -rf "$out"
	mkdir -p "$out/data/users" "$out/data/favicons"
	docker cp "$app:/var/www/FreshRSS/data/config.php" "$out/data/config.php"
	docker cp "$app:/var/www/FreshRSS/data/force-https.txt" "$out/data/force-https.txt"
	docker cp "$app:/var/www/FreshRSS/data/favicons/." "$out/data/favicons/"
	rm -f "$out/data/favicons/index.html" "$out/data/favicons/.gitignore"
	for user in alice bob; do
		mkdir -p "$out/data/users/$user"
		docker cp "$app:/var/www/FreshRSS/data/users/$user/config.php" "$out/data/users/$user/config.php"
		if [ "$engine" = sqlite ]; then
			docker cp "$app:/var/www/FreshRSS/data/users/$user/db.sqlite" "$out/data/users/$user/db.sqlite"
		fi
	done
	if [ "$engine" = pgsql ]; then
		# Plain INSERTs so that the dump loads through any client. Lines starting
		# with a backslash are psql meta-commands (\restrict) and are dropped.
		docker exec "$db" pg_dump -U freshrss -d freshrss --no-owner --no-privileges --inserts |
			grep -v '^\\' > "$out/dump.sql"
	fi
	cleanup
}

[ "$#" -gt 0 ] || set -- sqlite pgsql
for engine in "$@"; do
	case "$engine" in
	sqlite | pgsql) generate "$engine" ;;
	*) echo "unknown engine: $engine" >&2; exit 2 ;;
	esac
done
