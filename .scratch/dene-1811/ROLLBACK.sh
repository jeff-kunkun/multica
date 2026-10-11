#!/bin/sh
set -eu
[ "$#" -eq 1 ] || exit 2
cp "$(dirname "$0")/BASELINE.go" "$1"
