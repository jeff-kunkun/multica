#!/usr/bin/env bash
# One-shot DeepSeek Harness (dsh) + Multica runtime-profile setup.
#
# Self-hosted Multica already speaks dsh (MUL-7232 / PR #6923), but the
# bridge package @multica-ai/dsh-runtime is not on a public npm registry
# yet (upstream issue #6936). This wizard clones the public bridge,
# builds it, and registers it as the `multica` dsh profile.
#
# Usage:
#   bash scripts/setup-dsh-runtime.sh
#   bash scripts/setup-dsh-runtime.sh --runtime-dir ~/.agents/dsh-multica-runtime
#
# Never prints credential values.
set -euo pipefail

RUNTIME_REPO="${MULTICA_DSH_RUNTIME_REPO:-https://github.com/multica-ai/dsh-multica-runtime.git}"
RUNTIME_DIR="${MULTICA_DSH_RUNTIME_DIR:-$HOME/.agents/dsh-multica-runtime}"
DSH_PACKAGE="${MULTICA_DSH_PACKAGE:-@deepseek-ai/dsh@latest}"
DSH_PROFILE="${MULTICA_DSH_PROFILE:-multica}"
MIN_NODE_MAJOR=20
SKIP_DSH_INSTALL=0
SKIP_PROBE=0
NO_UPDATE=0
DRY_RUN=0

if [ -t 1 ] || [ -t 2 ]; then
  BOLD='\033[1m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  CYAN='\033[0;36m'
  RESET='\033[0m'
else
  BOLD='' GREEN='' YELLOW='' RED='' CYAN='' RESET=''
fi

info()  { printf "${BOLD}${CYAN}==> %s${RESET}\n" "$*"; }
ok()    { printf "${BOLD}${GREEN}✓ %s${RESET}\n" "$*"; }
warn()  { printf "${BOLD}${YELLOW}⚠ %s${RESET}\n" "$*" >&2; }
fail()  { printf "${BOLD}${RED}✗ %s${RESET}\n" "$*" >&2; exit 1; }

command_exists() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<'EOF'
Usage: bash scripts/setup-dsh-runtime.sh [options]

Install DeepSeek Harness (dsh) if needed, clone/build the Multica runtime
bridge, and register it as the `multica` dsh profile.

Options:
  --runtime-dir DIR     Bridge checkout (default: ~/.agents/dsh-multica-runtime)
  --repo URL            Bridge git URL (default: multica-ai/dsh-multica-runtime)
  --dsh-package SPEC    npm spec for the CLI (default: @deepseek-ai/dsh@latest)
  --skip-dsh-install    Require an existing `dsh` on PATH
  --skip-probe          Skip `dsh --profile multica --probe`
  --no-update           Do not git-pull an existing bridge checkout
  --dry-run             Print the plan; do not install, clone, build, or add
  -h, --help            Show this help

Environment:
  MULTICA_DSH_RUNTIME_DIR    Same as --runtime-dir
  MULTICA_DSH_RUNTIME_REPO   Same as --repo
  MULTICA_DSH_PACKAGE        Same as --dsh-package
  DEEPSEEK_API_KEY           Needed for model calls; probe does not require it
EOF
}

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf "dry-run: %s\n" "$*"
    return 0
  fi
  "$@"
}

node_major() {
  local raw major
  raw="$(node -v 2>/dev/null || node --version 2>/dev/null || true)"
  raw="${raw#v}"
  major="${raw%%.*}"
  case "$major" in
    ''|*[!0-9]*) echo 0 ;;
    *) echo "$major" ;;
  esac
}

ensure_node() {
  info "Checking Node.js"
  if ! command_exists node; then
    fail "Node.js is not on PATH. Install Node.js ${MIN_NODE_MAJOR}+ first."
  fi
  local major
  major="$(node_major)"
  if [ "$major" -lt "$MIN_NODE_MAJOR" ]; then
    fail "Node.js ${MIN_NODE_MAJOR}+ is required (found $(node -v 2>/dev/null || echo unknown))."
  fi
  ok "Node.js $(node -v 2>/dev/null || echo ok)"
  if [ "$major" -lt 22 ]; then
    warn "The bridge package.json prefers Node ^22.19 or >=24; continuing with Node $major."
  fi
}

ensure_npm() {
  if ! command_exists npm; then
    fail "npm is not on PATH. It ships with Node.js."
  fi
}

ensure_pnpm() {
  info "Checking pnpm"
  if command_exists pnpm; then
    ok "pnpm $(pnpm -v 2>/dev/null | tail -n 1)"
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    printf "dry-run: install pnpm (corepack or npm -g)\n"
    return 0
  fi
  if command_exists corepack; then
    info "Enabling pnpm via corepack"
    corepack enable
    corepack prepare pnpm@latest --activate
  else
    info "Installing pnpm with npm"
    npm install -g pnpm
  fi
  command_exists pnpm || fail "pnpm install did not put pnpm on PATH."
  ok "pnpm $(pnpm -v 2>/dev/null | tail -n 1)"
}

ensure_dsh() {
  info "Checking dsh CLI"
  if command_exists dsh; then
    ok "dsh $(dsh --version 2>/dev/null | tail -n 1)"
    return 0
  fi
  if [ "$SKIP_DSH_INSTALL" -eq 1 ]; then
    fail "dsh is not on PATH and --skip-dsh-install was set."
  fi
  info "Installing ${DSH_PACKAGE}"
  run npm install -g "$DSH_PACKAGE"
  if [ "$DRY_RUN" -eq 1 ]; then
    return 0
  fi
  command_exists dsh || fail "npm install -g ${DSH_PACKAGE} did not put dsh on PATH."
  ok "dsh $(dsh --version 2>/dev/null | tail -n 1)"
}

sync_runtime() {
  info "Syncing runtime bridge at ${RUNTIME_DIR}"
  if [ -d "$RUNTIME_DIR/.git" ]; then
    if [ "$NO_UPDATE" -eq 1 ]; then
      ok "Using existing checkout (--no-update)"
      return 0
    fi
    info "Updating existing checkout"
    run git -C "$RUNTIME_DIR" fetch --prune origin
    run git -C "$RUNTIME_DIR" pull --ff-only
    ok "Checkout updated"
    return 0
  fi
  if [ -e "$RUNTIME_DIR" ]; then
    fail "${RUNTIME_DIR} exists but is not a git checkout. Move it aside or pass --runtime-dir."
  fi
  mkdir -p "$(dirname "$RUNTIME_DIR")"
  run git clone "$RUNTIME_REPO" "$RUNTIME_DIR"
  ok "Cloned ${RUNTIME_REPO}"
}

build_runtime() {
  info "Building runtime bridge"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf "dry-run: pnpm install --frozen-lockfile && pnpm build  (in %s)\n" "$RUNTIME_DIR"
    return 0
  fi
  # pnpm 11 added --trust-lockfile (see #6936). pnpm 10 rejects that flag.
  # Frozen lockfile is the portable equivalent for a committed lockfile.
  (cd "$RUNTIME_DIR" && pnpm install --frozen-lockfile && pnpm build)
  [ -f "$RUNTIME_DIR/dist/index.js" ] || fail "Build finished but ${RUNTIME_DIR}/dist/index.js is missing."
  ok "Built ${RUNTIME_DIR}"
}

add_profile() {
  info "Registering dsh profile '${DSH_PROFILE}'"
  run dsh plugin --profile "$DSH_PROFILE" add "$RUNTIME_DIR"
  ok "Profile '${DSH_PROFILE}' points at ${RUNTIME_DIR}"
}

probe_profile() {
  if [ "$SKIP_PROBE" -eq 1 ]; then
    warn "Skipping probe (--skip-probe)"
    return 0
  fi
  info "Probing dsh --profile ${DSH_PROFILE}"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf "dry-run: dsh --profile %s --probe\n" "$DSH_PROFILE"
    return 0
  fi
  local out
  out="$(dsh --profile "$DSH_PROFILE" --probe)"
  printf '%s\n' "$out"
  echo "$out" | grep -q '"type":"probe"' || fail "Probe did not return type=probe."
  echo "$out" | grep -Eq '"protocol_version":[[:space:]]*1' || fail "Probe did not return protocol_version 1."
  ok "Probe protocol_version 1"
}

warn_credentials() {
  if [ -n "${DEEPSEEK_API_KEY:-}" ]; then
    ok "DEEPSEEK_API_KEY is set (value not printed)"
    return 0
  fi
  warn "DEEPSEEK_API_KEY is unset. Probe and --list-models can still pass; a real agent run needs this key or a DSH credential grant."
}

while [ $# -gt 0 ]; do
  case "$1" in
    --runtime-dir)
      [ $# -ge 2 ] || fail "--runtime-dir needs a directory"
      RUNTIME_DIR="$2"
      shift 2
      ;;
    --repo)
      [ $# -ge 2 ] || fail "--repo needs a URL"
      RUNTIME_REPO="$2"
      shift 2
      ;;
    --dsh-package)
      [ $# -ge 2 ] || fail "--dsh-package needs an npm spec"
      DSH_PACKAGE="$2"
      shift 2
      ;;
    --skip-dsh-install) SKIP_DSH_INSTALL=1; shift ;;
    --skip-probe) SKIP_PROBE=1; shift ;;
    --no-update) NO_UPDATE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "Unknown option: $1 (see --help)" ;;
  esac
done

info "DeepSeek Harness runtime setup"
printf "  runtime-dir: %s\n" "$RUNTIME_DIR"
printf "  repo:        %s\n" "$RUNTIME_REPO"
printf "  dsh package: %s\n" "$DSH_PACKAGE"
printf "  profile:     %s\n" "$DSH_PROFILE"
if [ "$DRY_RUN" -eq 1 ]; then
  warn "Dry run: no installs or profile changes"
fi

ensure_node
ensure_npm
ensure_pnpm
ensure_dsh
sync_runtime
build_runtime
add_profile
probe_profile
warn_credentials

ok "DeepSeek Harness is ready for Multica"
printf "Next: restart the daemon if it is already running, then confirm dsh in \`multica daemon status\`.\n"
printf "Do not restart a daemon from inside a task it is hosting — that kills the current run.\n"
