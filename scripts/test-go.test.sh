#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/multica-test-go.XXXXXX")
BIN_DIR="$TEST_DIR/bin"
CALLS_FILE="$TEST_DIR/go-calls.log"
PSQL_CALLS_FILE="$TEST_DIR/psql-calls.log"
COMMITS_FILE="$TEST_DIR/xact-commit"
OUTPUT_FILE="$TEST_DIR/output.log"
RUN_DB_FILE="$TEST_DIR/run-db.log"

cleanup() {
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

mkdir -p "$BIN_DIR"
export MULTICA_TEST_GO_CALLS="$CALLS_FILE"
export MULTICA_TEST_PSQL_CALLS="$PSQL_CALLS_FILE"
export MULTICA_TEST_PSQL_COMMITS="$COMMITS_FILE"
export MULTICA_TEST_RUN_DB="$RUN_DB_FILE"
: >"$CALLS_FILE"
: >"$PSQL_CALLS_FILE"
printf '0' >"$COMMITS_FILE"
: >"$RUN_DB_FILE"

cat >"$BIN_DIR/go" <<'FAKE'
#!/usr/bin/env bash
set -eu

case "${1:-}" in
  list)
    if [ "$#" -ne 2 ] || [ "$2" != "./..." ]; then
      echo "unexpected go list arguments: $*" >&2
      exit 2
    fi
    printf '%s\n' \
      github.com/multica-ai/multica/server \
      github.com/multica-ai/multica/server/internal/daemon \
      github.com/multica-ai/multica/server/pkg/agent \
      github.com/multica-ai/multica/server/pkg/agent/internal/testutil
    ;;
  test)
    printf '%s\n' "$*" >>"$MULTICA_TEST_GO_CALLS"
    ;;
  run)
    # `go run ./cmd/migrate up` against the run's private database.
    printf '%s\n' "$*" >>"$MULTICA_TEST_RUN_DB"
    ;;
  *)
    echo "unexpected go command: $*" >&2
    exit 2
    ;;
esac
FAKE
chmod 755 "$BIN_DIR/go"

# The runner provisions a database with psql, so the suite's contract now
# includes it. The counter is what pg_stat_database.xact_commit looks like for a
# run whose tests actually connected: it moves between the runner's two reads.
cat >"$BIN_DIR/psql" <<'FAKE'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$MULTICA_TEST_PSQL_CALLS"

case "$*" in
  *xact_commit*)
    commits=$(cat "$MULTICA_TEST_PSQL_COMMITS")
    commits=$((commits + 1))
    printf '%s' "$commits" >"$MULTICA_TEST_PSQL_COMMITS"
    printf '%s\n' "$commits"
    ;;
esac
exit 0
FAKE
chmod 755 "$BIN_DIR/psql"

regular_call='test -race github.com/multica-ai/multica/server github.com/multica-ai/multica/server/internal/daemon'
agent_call='test -race -p 2 -parallel 2 ./pkg/agent/...'

# $1: case label; $2: expected go calls, one per line. Clears the log after.
expect_calls() {
  actual_calls=$(cat "$CALLS_FILE")
  if [ "$actual_calls" != "$2" ]; then
    echo "$1: unexpected go test calls:" >&2
    printf '%s\n' "$actual_calls" >&2
    exit 1
  fi
  : >"$CALLS_FILE"
}

# The run is wrapped in a private database: one created and one dropped per
# invocation, and the migration applied to the database that was created rather
# than to DATABASE_URL.
expect_provisioned_database() {
  label=$1
  if ! grep -q 'CREATE DATABASE' "$PSQL_CALLS_FILE"; then
    echo "$label did not create a run database:" >&2
    cat "$PSQL_CALLS_FILE" >&2
    exit 1
  fi
  if ! grep -q 'DROP DATABASE' "$PSQL_CALLS_FILE"; then
    echo "$label did not drop the database it created:" >&2
    cat "$PSQL_CALLS_FILE" >&2
    exit 1
  fi
  if ! grep -q 'cmd/migrate up' "$RUN_DB_FILE"; then
    echo "$label did not migrate the run database:" >&2
    cat "$RUN_DB_FILE" >&2
    exit 1
  fi
  : >"$PSQL_CALLS_FILE"
  : >"$RUN_DB_FILE"
}

# $1: case label; the rest are arguments for test-go.sh. Asserts the usage
# exit status, the usage line, and that go was never invoked.
expect_usage_failure() {
  label=$1
  shift
  set +e
  PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" "$@" >"$OUTPUT_FILE" 2>&1
  status=$?
  set -e

  if [ "$status" -ne 2 ]; then
    echo "$label returned status $status, want 2" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
  if [ -s "$CALLS_FILE" ]; then
    echo "$label invoked go:" >&2
    cat "$CALLS_FILE" >&2
    exit 1
  fi
  # Usage errors must be answered before anything is provisioned: a typo must
  # not cost a database.
  if [ -s "$PSQL_CALLS_FILE" ] || [ -s "$RUN_DB_FILE" ]; then
    echo "$label provisioned a database before rejecting its arguments:" >&2
    cat "$PSQL_CALLS_FILE" "$RUN_DB_FILE" >&2
    exit 1
  fi
  if ! grep -q '^usage: .*test-go.sh \[--race\] \[--only regular|agent\]$' "$OUTPUT_FILE"; then
    echo "$label did not print usage" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
}

PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --race
expect_calls "--race" "$regular_call
$agent_call"
expect_provisioned_database "--race"

PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --race --only regular
expect_calls "--only regular" "$regular_call"
expect_provisioned_database "--only regular"

# Option order must not matter: CI spells it one way, humans another.
PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --only agent --race
expect_calls "--only agent" "$agent_call"
expect_provisioned_database "--only agent"

expect_usage_failure "unknown option" --unknown
expect_usage_failure "unknown --only scope" --only everything
expect_usage_failure "missing --only scope" --only

echo "test-go.test.sh: PASS"
