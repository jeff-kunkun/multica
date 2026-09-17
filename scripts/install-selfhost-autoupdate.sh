#!/usr/bin/env bash
# Install the systemd unit + timer that keep a self-hosted checkout on the tip
# of an upstream branch.
#
# Idempotent by construction: the unit files are rendered into a temp file and
# only moved into place when their content actually changed, so re-running this
# on an already-installed box does not bump the timer's start time or restart
# anything. The rollback story is `systemctl disable --now`.
#
# Usage: sudo scripts/install-selfhost-autoupdate.sh [options]
#   --repo-dir DIR    checkout to update (default: this script's parent repo)
#   --branch NAME     branch to follow (default: kun)
#   --unit-dir DIR    where to write the units (default: /etc/systemd/system)
#   --state-dir DIR   where the script keeps its status file (default: /var/lib/multica)
#   --no-enable       write the units but do not enable/start the timer
#
# Runbook: docs/kun/selfhost-autoupdate.md
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BRANCH=kun
UNIT_DIR=/etc/systemd/system
STATE_DIR=/var/lib/multica
ENABLE=1

usage() {
  sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
  --repo-dir)
    [ $# -ge 2 ] || { echo "--repo-dir needs a value" >&2; exit 2; }
    REPO_DIR="$2"
    shift 2
    ;;
  --branch)
    [ $# -ge 2 ] || { echo "--branch needs a value" >&2; exit 2; }
    BRANCH="$2"
    shift 2
    ;;
  --unit-dir)
    [ $# -ge 2 ] || { echo "--unit-dir needs a value" >&2; exit 2; }
    UNIT_DIR="$2"
    shift 2
    ;;
  --state-dir)
    [ $# -ge 2 ] || { echo "--state-dir needs a value" >&2; exit 2; }
    STATE_DIR="$2"
    shift 2
    ;;
  --no-enable) ENABLE=0; shift ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
  esac
done

REPO_DIR="$(cd "$REPO_DIR" && pwd)"
TEMPLATE_DIR="$REPO_DIR/deploy/systemd"

# Fail loudly on a path typo: a unit pointing at the wrong checkout installs
# cleanly and then silently never updates anything.
if [ ! -f "$REPO_DIR/docker-compose.selfhost.yml" ]; then
  echo "not a Multica self-host checkout: $REPO_DIR (no docker-compose.selfhost.yml)" >&2
  exit 1
fi
if [ ! -f "$REPO_DIR/scripts/selfhost-autoupdate.sh" ]; then
  echo "missing $REPO_DIR/scripts/selfhost-autoupdate.sh" >&2
  exit 1
fi
for template in multica-autoupdate.service multica-autoupdate.timer; do
  if [ ! -f "$TEMPLATE_DIR/$template" ]; then
    echo "missing unit template $TEMPLATE_DIR/$template" >&2
    exit 1
  fi
done

mkdir -p "$UNIT_DIR" "$STATE_DIR"

render() {
  sed -e "s#@REPO_DIR@#${REPO_DIR}#g" -e "s#@BRANCH@#${BRANCH}#g" "$TEMPLATE_DIR/$1"
}

changed=0
for unit in multica-autoupdate.service multica-autoupdate.timer; do
  target="$UNIT_DIR/$unit"
  tmp="$(mktemp "${TMPDIR:-/tmp}/multica-unit.XXXXXX")"
  render "$unit" >"$tmp"
  if [ -f "$target" ] && cmp -s "$tmp" "$target"; then
    echo "unchanged: $target"
    rm -f "$tmp"
    continue
  fi
  install -m 0644 "$tmp" "$target" 2>/dev/null || cp "$tmp" "$target"
  rm -f "$tmp"
  echo "wrote: $target"
  changed=1
done

if [ "$ENABLE" -eq 0 ]; then
  echo "skipping systemctl (--no-enable); units are in $UNIT_DIR"
  exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemctl is not available; units were written but not enabled" >&2
  exit 1
fi

if [ "$changed" -eq 1 ]; then
  systemctl daemon-reload
fi
systemctl enable --now multica-autoupdate.timer

echo ""
echo "Installed. The timer runs multica-autoupdate.service 5 minutes from now and every 15 minutes after that."
echo "  systemctl list-timers multica-autoupdate.timer"
echo "  systemctl start multica-autoupdate.service     # upgrade now, in the foreground"
echo "  systemctl disable --now multica-autoupdate.timer  # stop following the branch"
echo "  cat ${STATE_DIR}/autoupdate.json               # what the last run did"
