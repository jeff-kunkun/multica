#!/usr/bin/env bash
# Build the CLI release archives this fork publishes on its own GitHub
# Releases.
#
#   scripts/build-cli-archives.sh --version v0.4.60 --out dist/cli
#
# Upstream produces these with GoReleaser, but that job is gated on
# `github.repository_owner == 'multica-ai'` (see .github/workflows/release.yml)
# and publishes to `multica-ai/homebrew-tap`, so on this fork it never runs.
# Without it the fork's releases carry only the Desktop installers, and
# `install.sh` / `multica update` have nothing fork-built to download — which
# is how a self-hosted instance ended up handing people the upstream CLI
# (DENE-420).
#
# Archive names and contents mirror .goreleaser.yml exactly, because
# `multica update` looks assets up by name:
#
#   multica-cli-<version>-<os>-<arch>.tar.gz   (zip on windows)  — current
#   multica_<os>_<arch>.tar.gz                 (zip on windows)  — legacy,
#     kept so already-released CLIs can still self-update
#   checksums.txt                              — `sha256<space><name>` lines
#
set -euo pipefail

# The repo to build from is the working directory's checkout, NOT the script's
# own location: cli-release.yml stages this script outside the tree it packages
# so `workflow_dispatch` can backfill a tag older than the script, and deriving
# the root from $BASH_SOURCE there landed on $RUNNER_TEMP/.. instead.
ROOT_DIR="${MULTICA_REPO_ROOT:-$(git rev-parse --show-toplevel)}"

VERSION=""
OUT_DIR=""
# Every platform .goreleaser.yml builds, so a fork release is a drop-in
# replacement for an upstream one.
TARGETS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64)

usage() {
  cat >&2 <<'USAGE'
usage: build-cli-archives.sh --version <vX.Y.Z> --out <dir> [--targets "os/arch ..."]

Builds from the current working directory's git checkout; set MULTICA_REPO_ROOT
to build a different one.
USAGE
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --out)     OUT_DIR="${2:-}"; shift 2 ;;
    --targets) read -r -a TARGETS <<<"${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$VERSION" ] || { echo "--version is required" >&2; usage; }
[ -n "$OUT_DIR" ] || { echo "--out is required" >&2; usage; }

# GoReleaser strips the leading v for {{.Version}}; asset names must match.
BARE_VERSION="${VERSION#v}"

COMMIT="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
# GoReleaser stamps an RFC 3339 build date; keep the same shape.
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

# The extra files GoReleaser puts in every archive.
EXTRA_FILES=(LICENSE NOTICE README.md README.zh.md)

archive_one() {
  local goos="$1" goarch="$2" name="$3"
  local work="$STAGE/$name"
  rm -rf "$work"
  mkdir -p "$work"

  local binary="multica"
  [ "$goos" = "windows" ] && binary="multica.exe"

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go -C "$ROOT_DIR/server" build \
      -trimpath \
      -ldflags "-s -w -X main.version=$BARE_VERSION -X main.commit=$COMMIT -X main.date=$BUILD_DATE" \
      -o "$work/$binary" \
      ./cmd/multica

  # Entries are stored under their bare names (no leading "./"), the same as
  # GoReleaser's archives: `install.sh` extracts the single member `multica`.
  local members=("$binary") f
  for f in "${EXTRA_FILES[@]}"; do
    if [ -f "$ROOT_DIR/$f" ]; then
      cp "$ROOT_DIR/$f" "$work/"
      members+=("$f")
    fi
  done

  if [ "$goos" = "windows" ]; then
    (cd "$work" && zip -q "$OUT_DIR/$name.zip" "${members[@]}")
    echo "$name.zip"
  else
    tar -czf "$OUT_DIR/$name.tar.gz" -C "$work" "${members[@]}"
    echo "$name.tar.gz"
  fi
}

built=()
for target in "${TARGETS[@]}"; do
  goos="${target%%/*}"
  goarch="${target##*/}"
  echo "==> building $goos/$goarch" >&2
  built+=("$(archive_one "$goos" "$goarch" "multica-cli-$BARE_VERSION-$goos-$goarch")")
  built+=("$(archive_one "$goos" "$goarch" "multica_${goos}_${goarch}")")
done

# One manifest covering every archive, in the `<sha256>  <name>` layout the
# CLI's updater parses (server/internal/cli/update.go).
(
  cd "$OUT_DIR"
  : >checksums.txt
  for name in "${built[@]}"; do
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$name" >>checksums.txt
    else
      # macOS: shasum prints the same two-field layout.
      shasum -a 256 "$name" >>checksums.txt
    fi
  done
)

echo "==> wrote ${#built[@]} archives + checksums.txt to $OUT_DIR" >&2
