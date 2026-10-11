#!/bin/sh
set -eu
[ "$#" -eq 1 ] || { echo "usage: ROLLBACK.sh target-copy" >&2; exit 2; }
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cp "$script_dir/BASELINE.go" "$1"
