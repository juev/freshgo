#!/bin/sh
# Runs the test suite against SQLite and a throwaway PostgreSQL in Docker.
set -eu

image="postgres:17-alpine"
container="freshgo-test-postgres-$$"

cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --name "$container" \
	-e POSTGRES_USER=freshgo -e POSTGRES_PASSWORD=freshgo -e POSTGRES_DB=freshgo \
	-p 127.0.0.1::5432 "$image" >/dev/null

# The image restarts the server once during initialization, so a single
# successful probe is not enough: ask for the final server over TCP.
tries=0
until docker exec "$container" pg_isready -q -h 127.0.0.1 -U freshgo -d freshgo &&
	docker exec "$container" psql -q -h 127.0.0.1 -U freshgo -d freshgo -c 'SELECT 1' >/dev/null 2>&1; do
	tries=$((tries + 1))
	if [ "$tries" -ge 60 ]; then
		echo "PostgreSQL did not start:" >&2
		docker logs "$container" >&2
		exit 1
	fi
	sleep 1
done

port=$(docker port "$container" 5432/tcp | head -n 1 | sed 's/.*://')
export FRESHGO_TEST_POSTGRES_URL="postgres://freshgo:freshgo@127.0.0.1:${port}/freshgo?sslmode=disable"

go test -race -count=1 "$@" ./...
