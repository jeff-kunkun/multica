#!/bin/sh
set -eu
cp "$(dirname "$0")/baseline.go" "${1:?target copy required}"
