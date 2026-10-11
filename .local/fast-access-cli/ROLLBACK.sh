#!/bin/sh
set -eu
if [ "$#" -ne 1 ] || [ ! -f "$1/server/cmd/multica/main.go" ]; then
  echo 'Usage: ROLLBACK.sh /path/to/disposable/source-copy' >&2
  exit 2
fi
artifact_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tar -xf "$artifact_dir/BASELINE.tar" -C "$1"
rm -f -- "$1/server/cmd/multica/cmd_fast_access.go" "$1/server/cmd/multica/cmd_fast_access_test.go"
