#!/usr/bin/env bash
set -euo pipefail

# Run a command against a database created for this run and dropped when it
# ends.
#
#   bash scripts/test-db.sh -- go test ./internal/... -count=1
#
# Why this exists. DB-backed Go tests used to share whatever database
# DATABASE_URL named — the same one a `make dev` server is reading and writing.
# Two consequences, both observed:
#
#   1. Rows an earlier run left behind change what a later run observes. A test
#      that counts rows, or counts catalog reads, then passes alone and fails
#      in the full suite (or the other way round).
#   2. When the shared instance is out of connection slots the tests do not
#      fail, they *skip* — a green run that asserted nothing. Every suite here
#      treats "could not connect" as "skip", by design, so a laptop whose
#      worktree database is called something other than the built-in default
#      gets a hollow pass.
#
# So a run gets its own database: created empty on the same server, migrated
# with the same `cmd/migrate` the application uses, exported to the wrapped
# command, and dropped again on the way out. Isolation happens at the runner
# layer because that is the one place every DB-backed package already reads
# from — DATABASE_URL — so no test file has to know about it.
#
# Environment:
#   DATABASE_URL / TEST_DATABASE_URL  the server and database to copy the
#                                     schema from; TEST_DATABASE_URL names an
#                                     already-provisioned database and skips
#                                     creation entirely (CI, remote servers).
#   ENV_FILE                          env file to read those from when they
#                                     are not already exported (default:
#                                     .env, then .env.worktree, matching make).
#   MULTICA_TEST_DB_KEEP=1            keep the database instead of dropping it.
#   MULTICA_TEST_DB_QUIET=1           hide the migrate output.

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

usage() {
  echo "usage: $0 [--keep] [--quiet] -- <command> [args...]" >&2
}

keep="${MULTICA_TEST_DB_KEEP:-0}"
quiet="${MULTICA_TEST_DB_QUIET:-0}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --keep) keep=1; shift ;;
    --quiet) quiet=1; shift ;;
    --) shift; break ;;
    *) usage; exit 2 ;;
  esac
done
[ "$#" -gt 0 ] || { usage; exit 2; }

# The env file is how a bare `bash scripts/test-db.sh -- ...` finds this
# checkout's database. An exported DATABASE_URL wins over the file: make,
# check.sh and CI all export one, and re-reading the file must not replace it.
if [ -z "${DATABASE_URL:-}" ] && [ -z "${TEST_DATABASE_URL:-}" ]; then
  env_file="${ENV_FILE:-}"
  if [ -z "$env_file" ]; then
    if [ -f "$REPO_ROOT/.env" ]; then
      env_file="$REPO_ROOT/.env"
    elif [ -f "$REPO_ROOT/.env.worktree" ]; then
      env_file="$REPO_ROOT/.env.worktree"
    fi
  fi
  if [ -n "$env_file" ] && [ -f "$env_file" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$env_file"
    set +a
  fi
fi

# An already-provisioned test database is used as-is. That keeps CI (which owns
# its own Postgres service) and a remote self-hosted server out of the create
# path, where a test run has no business making databases.
if [ -n "${TEST_DATABASE_URL:-}" ]; then
  echo "==> Using TEST_DATABASE_URL as-is (no run database created)"
  export DATABASE_URL="$TEST_DATABASE_URL"
  exec "$@"
fi

base_url="${DATABASE_URL:-postgres://${POSTGRES_USER:-multica}:${POSTGRES_PASSWORD:-multica}@localhost:${POSTGRES_PORT:-5432}/${POSTGRES_DB:-multica}?sslmode=disable}"

# The maintenance connection: same server and credentials, `postgres` as the
# database, because CREATE DATABASE cannot run from inside the database it is
# about to copy.
admin_url="$(printf '%s' "$base_url" | sed -E 's#(://[^/]+/)[^?]*#\1postgres#')"
case "$admin_url" in
  *"/postgres"*|*"/postgres?"*) ;;
  *) admin_url="${base_url%/}/postgres" ;;
esac

base_db="$(printf '%s' "$base_url" | sed -E 's#.*/([^/?]*)(\?.*)?$#\1#')"
case "$base_db" in
  *"://"*|"") base_db="${POSTGRES_DB:-multica}" ;;
esac

# Identifiers cap at 63 bytes, and worktree database names are already long
# (`multica_worktree_406`), so the base gets truncated rather than the suffix
# that makes the name unique.
run_db="$(printf '%.40s' "$base_db")_t$(date +%s)$$"
run_url="$(printf '%s' "$base_url" | sed -E "s#(://[^/]+/)[^?]*#\\1${run_db}#")"

if ! command -v psql >/dev/null 2>&1; then
  echo "psql is required to provision the test database" >&2
  exit 1
fi

echo "==> Creating test database '$run_db'"
if ! psql "$admin_url" -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE \"$run_db\"" >/dev/null; then
  echo "Could not create '$run_db' on the server DATABASE_URL names." >&2
  echo "Set TEST_DATABASE_URL to an existing database to run without isolation." >&2
  exit 1
fi

cleanup() {
  local status=$?
  if [ "$keep" = "1" ]; then
    echo "==> Keeping test database '$run_db' (MULTICA_TEST_DB_KEEP=1)"
  else
    echo "==> Dropping test database '$run_db'"
    psql "$admin_url" -q -c "DROP DATABASE IF EXISTS \"$run_db\" WITH (FORCE)" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
# PIPE and HUP are in the list because `make test | tee log` is a normal way to
# watch a run: the shell dies of SIGPIPE when the reader goes away, and an
# untrapped signal skips the EXIT trap and leaks the database.
trap cleanup EXIT INT TERM HUP PIPE

echo "==> Migrating '$run_db'"
if [ "$quiet" = "1" ]; then
  (cd "$REPO_ROOT/server" && DATABASE_URL="$run_url" go run ./cmd/migrate up) >/dev/null
else
  (cd "$REPO_ROOT/server" && DATABASE_URL="$run_url" go run ./cmd/migrate up)
fi

# MULTICA_REQUIRE_TEST_DB turns a suite that could not reach its database from
# a silent skip into a failure. The database was created two lines above, so
# anything that still skips is a real problem this run should not hide.
export DATABASE_URL="$run_url"
export TEST_DATABASE_URL="$run_url"
export MULTICA_REQUIRE_TEST_DB=1

# Migrations commit transactions of their own, so the counter is read AFTER
# them: whatever it gains from here on was run by the suite.
transactions_before="$(psql "$admin_url" -tAc \
  "SELECT coalesce((SELECT xact_commit FROM pg_stat_database WHERE datname = '$run_db'), 0)")"

"$@"
status=$?

# Every DB-backed suite here skips when its database is unreachable, which is
# right on a laptop without Postgres and wrong when this script just stood one
# up: `go test` would report ok for a package whose entire DB suite asserted
# nothing. One committed read is enough to prove the suite reached it, and the
# counter is cheap enough to check on every run.
if [ "${MULTICA_TEST_DB_EXPECT_USE:-0}" = "1" ]; then
  transactions_after="$(psql "$admin_url" -tAc \
    "SELECT coalesce((SELECT xact_commit FROM pg_stat_database WHERE datname = '$run_db'), 0)")"
  if [ "$transactions_after" = "$transactions_before" ]; then
    echo "==> No test connected to '$run_db': the DB-backed suite skipped and this run proved nothing" >&2
    status=1
  fi
fi

exit "$status"
