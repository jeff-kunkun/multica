#!/bin/bash
# Replaces the office virtual-screen tools in ~/.agents/vd with this repository's version.
# Run it only after Kun has signed off (DENE-2261). It:
#   - backs up the whole folder to ~/.agents/vd.backup-<time> first;
#   - replaces only the tool files; state.json, control/ (the stop latch and owned windows),
#     devtools-endpoint and peek/ stay as they are, so a stopped Mac stays stopped;
#   - does not restart anything. The broker LaunchAgent picks the new files up on its next restart.
# Roll back:  rm -rf ~/.agents/vd && mv ~/.agents/vd.backup-<time> ~/.agents/vd
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "install-local: macOS only" >&2
  exit 1
fi

source_dir="$(cd "$(dirname "$0")" && pwd)"
destination="${OFFICE_VD_HOME:-$HOME/.agents/vd}"
staging="$(mktemp -d "${TMPDIR:-/tmp}/office-vd-build.XXXXXX")"
trap 'rm -rf "$staging"' EXIT

node "$source_dir/build.mjs" --out "$staging"

if [[ -d "$destination" ]]; then
  backup="$destination.backup-$(date '+%Y%m%d-%H%M%S')"
  cp -a "$destination" "$backup"
  echo "Backed up $destination to $backup"
fi
mkdir -p "$destination"

for f in latch.mjs broker.mjs vd.mjs vd-hold screen-capture LICENSE.yomado README.md; do
  cp "$staging/$f" "$destination/$f.new"
  mv -f "$destination/$f.new" "$destination/$f"
done
chmod 755 "$destination/vd-hold" "$destination/screen-capture"

echo "Installed office virtual-screen tools into $destination."
echo "To load the new broker:  launchctl kickstart -k gui/$(id -u)/com.kun.office-vd-broker"
echo "vd-hold keeps running the old binary until its next restart (restarting it re-creates the display)."
