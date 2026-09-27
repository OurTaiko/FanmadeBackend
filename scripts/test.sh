#!/bin/sh
# Run the complete suite against a private disposable PostgreSQL cluster.
set -eu
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-/tmp/fanmade-go-build}"
if [ -n "${DATABASE_TEST_URL:-}" ]; then
  go test -race ./... "$@"
  go vet ./...
  exit 0
fi
PG_BINDIR="${PG_BINDIR:-$(pg_config --bindir)}"
FANMADE_TEST_TMP="$(mktemp -d /tmp/fanmade-pg.XXXXXX)"
cleanup() {
  "$PG_BINDIR/pg_ctl" -D "$FANMADE_TEST_TMP/data" -m immediate stop >/dev/null 2>&1 || true
  rm -rf -- "$FANMADE_TEST_TMP"
}
trap cleanup EXIT HUP INT TERM
"$PG_BINDIR/initdb" -D "$FANMADE_TEST_TMP/data" -A trust -U fanmade_test --no-locale -E UTF8 >"$FANMADE_TEST_TMP/init.log"
# Disable TCP. The socket and data are both in this test's private directory.
"$PG_BINDIR/pg_ctl" -D "$FANMADE_TEST_TMP/data" -l "$FANMADE_TEST_TMP/server.log" \
  -o "-k $FANMADE_TEST_TMP -p 55438 -c listen_addresses='' -c fsync=off" -w start >/dev/null
export DATABASE_TEST_URL="postgres://fanmade_test@/postgres?host=$FANMADE_TEST_TMP&port=55438&sslmode=disable"
go test -race ./... "$@"
go vet ./...
