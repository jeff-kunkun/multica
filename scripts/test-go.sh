#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
GUARD_SCRIPT="$SCRIPT_DIR/go-test-with-agent-cli-guard.sh"

usage() {
  echo "usage: $0 [--race] [--only regular|agent]" >&2
}

# The suite is two `go test` invocations: every package outside pkg/agent at
# the default parallelism, then pkg/agent throttled (see below). `--only`
# selects one half so CI can give each its own runner; the default still runs
# both for `make test`, check.sh, and the release workflow.
go_test_args=(test)
forward=()
only=all
while [ "$#" -gt 0 ]; do
  case "$1" in
    --race)
      go_test_args+=(-race)
      forward+=(--race)
      shift
      ;;
    --only)
      case "${2:-}" in
        regular|agent) only=$2 ;;
        *)
          usage
          exit 2
          ;;
      esac
      forward+=(--only "$2")
      shift 2
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

# The whole run shares one database of its own, created and migrated before the
# first package starts and dropped after the last one exits. Doing it here,
# rather than in each package's TestMain, means every DB-backed suite gets the
# same isolation from one place — and this is the only layer that knows the
# suite is a set of separate processes sharing a run.
#
# `--only agent` is left out on purpose: pkg/agent opens no database, and its
# CI job runs on a runner with no Postgres service at all. Standing a database
# up for it would turn "no server reachable" into a failed run of tests that
# never needed one.
#
# MULTICA_TEST_DB_ACTIVE marks the re-exec so the nested invocation cannot
# provision a second database. `${forward[@]+...}` is what keeps a no-argument
# `bash scripts/test-go.sh` — how check.sh calls it — working under `set -u` on
# bash 3.2, where expanding an empty array is an unbound-variable error.
if [ "$only" != agent ] && [ "${MULTICA_TEST_DB_ACTIVE:-0}" != "1" ]; then
  exec env MULTICA_TEST_DB_ACTIVE=1 MULTICA_TEST_DB_EXPECT_USE=1 \
    bash "$SCRIPT_DIR/test-db.sh" -- bash "$0" ${forward[@]+"${forward[@]}"}
fi

cd "$REPO_ROOT/server"

if [ "$only" != agent ]; then
  packages=$(go list ./...)
  regular_packages=()
  for package in $packages; do
    case "$package" in
      */pkg/agent|*/pkg/agent/*) ;;
      *) regular_packages+=("$package") ;;
    esac
  done
  "$GUARD_SCRIPT" -- go "${go_test_args[@]}" "${regular_packages[@]}"
fi

if [ "$only" != regular ]; then
  # Subprocess-backed agent tests have hard deadlines. Limit both package and
  # within-package parallelism so race builds do not starve their parent loops.
  "$GUARD_SCRIPT" -- go "${go_test_args[@]}" -p 2 -parallel 2 ./pkg/agent/...
fi
