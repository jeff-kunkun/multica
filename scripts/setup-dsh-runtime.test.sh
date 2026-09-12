#!/usr/bin/env bash
# Stubbed coverage for scripts/setup-dsh-runtime.sh.
# Does not talk to npm, GitHub, or a real dsh install.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/setup-dsh-runtime.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

require_contains() {
  local file=$1 expected=$2
  if ! grep -Fq -- "$expected" "$file"; then
    echo "Expected output to contain: $expected" >&2
    echo "Observed:" >&2
    sed 's/^/  /' "$file" >&2
    exit 1
  fi
}

require_not_contains() {
  local file=$1 unexpected=$2
  if grep -Fq -- "$unexpected" "$file"; then
    echo "Did not expect output to contain: $unexpected" >&2
    echo "Observed:" >&2
    sed 's/^/  /' "$file" >&2
    exit 1
  fi
}

setup_stubs() {
  local stub_bin=$1
  local log=$2
  local node_version=${3:-v22.19.0}
  mkdir -p "$stub_bin"

  cat >"$stub_bin/node" <<EOF
#!/usr/bin/env bash
echo "$node_version"
EOF
  chmod +x "$stub_bin/node"

  cat >"$stub_bin/npm" <<EOF
#!/usr/bin/env bash
printf 'npm %s\n' "\$*" >>"$log"
if [ "\${1:-}" = "install" ]; then
  mkdir -p "$stub_bin"
  cat >"$stub_bin/dsh" <<'DSH'
#!/usr/bin/env bash
printf 'dsh %s\n' "\$*" >>"$log"
case " \$* " in
  *" --version "*) echo "0.1.5-rc.1"; exit 0 ;;
  *" plugin "*add*) exit 0 ;;
  *" --probe "*)
    printf '%s\n' '{"v":1,"type":"probe","runtime":"dsh","plugin_version":"0.1.0-private.1","protocol_version":1}'
    exit 0
    ;;
esac
exit 0
DSH
  chmod +x "$stub_bin/dsh"
fi
exit 0
EOF
  chmod +x "$stub_bin/npm"

  cat >"$stub_bin/pnpm" <<EOF
#!/usr/bin/env bash
printf 'pnpm %s cwd=%s\n' "\$*" "\$(pwd)" >>"$log"
if [ "\${1:-}" = "build" ]; then
  mkdir -p dist
  echo "built" > dist/index.js
fi
echo "10.28.2"
exit 0
EOF
  chmod +x "$stub_bin/pnpm"

  cat >"$stub_bin/git" <<EOF
#!/usr/bin/env bash
printf 'git %s\n' "\$*" >>"$log"
dest=""
prev=""
for arg in "\$@"; do
  if [ "\$prev" = "clone" ]; then
    dest="\$arg"
  fi
  prev="\$arg"
done
# last arg of \`git clone URL DIR\` is the destination
if [ "\${1:-}" = "clone" ]; then
  dest="\${@: -1}"
  mkdir -p "\$dest"
  echo cloned > "\$dest/.cloned"
  mkdir -p "\$dest/.git"
fi
exit 0
EOF
  chmod +x "$stub_bin/git"

  cat >"$stub_bin/dsh" <<EOF
#!/usr/bin/env bash
printf 'dsh %s\n' "\$*" >>"$log"
case " \$* " in
  *" --version "*) echo "0.1.5-rc.1"; exit 0 ;;
  *" plugin "*add*) exit 0 ;;
  *" --probe "*)
    printf '%s\n' '{"v":1,"type":"probe","runtime":"dsh","plugin_version":"0.1.0-private.1","protocol_version":1}'
    exit 0
    ;;
esac
exit 0
EOF
  chmod +x "$stub_bin/dsh"
}

# ---------------------------------------------------------------------------
# Missing Node.js fails closed.
# ---------------------------------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/stub-bin" "$tmp/home"
cat >"$tmp/stub-bin/true" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$tmp/stub-bin/true"
status=0
PATH="$tmp/stub-bin:/usr/bin:/bin" HOME="$tmp/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp/runtime" \
  bash "$SCRIPT" >"$tmp/out" 2>"$tmp/err" || status=$?
[ "$status" -ne 0 ] || fail "setup must fail when node is missing"
require_contains "$tmp/err" "Node.js is not on PATH"

# ---------------------------------------------------------------------------
# Node too old fails closed.
# ---------------------------------------------------------------------------
tmp2="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2"' EXIT
setup_stubs "$tmp2/stub-bin" "$tmp2/commands.log" "v18.20.4"
status=0
PATH="$tmp2/stub-bin:/usr/bin:/bin" HOME="$tmp2/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp2/runtime" \
  bash "$SCRIPT" >"$tmp2/out" 2>"$tmp2/err" || status=$?
[ "$status" -ne 0 ] || fail "setup must fail on Node 18"
require_contains "$tmp2/err" "Node.js 20+ is required"

# ---------------------------------------------------------------------------
# Happy path: clone, frozen-lockfile install, build, plugin add, probe.
# ---------------------------------------------------------------------------
tmp3="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2" "$tmp3"' EXIT
mkdir -p "$tmp3/home"
setup_stubs "$tmp3/stub-bin" "$tmp3/commands.log" "v22.19.0"
# Pre-seed dsh so npm -g is skipped.
PATH="$tmp3/stub-bin:/usr/bin:/bin" HOME="$tmp3/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp3/runtime" \
  bash "$SCRIPT" --skip-dsh-install >"$tmp3/out" 2>"$tmp3/err" \
  || fail "happy path failed"
require_contains "$tmp3/commands.log" "git clone https://github.com/multica-ai/dsh-multica-runtime.git"
require_contains "$tmp3/commands.log" "pnpm install --frozen-lockfile"
require_contains "$tmp3/commands.log" "pnpm build"
require_not_contains "$tmp3/commands.log" "--trust-lockfile"
require_contains "$tmp3/commands.log" "dsh plugin --profile multica add"
require_contains "$tmp3/commands.log" "dsh --profile multica --probe"
require_contains "$tmp3/out" "protocol_version"
require_contains "$tmp3/out" "DeepSeek Harness is ready for Multica"
[ -f "$tmp3/runtime/dist/index.js" ] || fail "happy path did not produce dist/index.js"

# ---------------------------------------------------------------------------
# Existing checkout uses git pull, not clone.
# ---------------------------------------------------------------------------
tmp4="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2" "$tmp3" "$tmp4"' EXIT
mkdir -p "$tmp4/home" "$tmp4/runtime/.git"
setup_stubs "$tmp4/stub-bin" "$tmp4/commands.log" "v26.3.0"
PATH="$tmp4/stub-bin:/usr/bin:/bin" HOME="$tmp4/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp4/runtime" \
  bash "$SCRIPT" --skip-dsh-install >"$tmp4/out" 2>"$tmp4/err" \
  || fail "update path failed"
require_contains "$tmp4/commands.log" "git -C $tmp4/runtime fetch --prune origin"
require_contains "$tmp4/commands.log" "git -C $tmp4/runtime pull --ff-only"
require_not_contains "$tmp4/commands.log" "git clone"

# ---------------------------------------------------------------------------
# --no-update skips git pull.
# ---------------------------------------------------------------------------
tmp5="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2" "$tmp3" "$tmp4" "$tmp5"' EXIT
mkdir -p "$tmp5/home" "$tmp5/runtime/.git"
setup_stubs "$tmp5/stub-bin" "$tmp5/commands.log" "v22.19.0"
PATH="$tmp5/stub-bin:/usr/bin:/bin" HOME="$tmp5/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp5/runtime" \
  bash "$SCRIPT" --skip-dsh-install --no-update >"$tmp5/out" 2>"$tmp5/err" \
  || fail "--no-update path failed"
require_not_contains "$tmp5/commands.log" "git clone"
require_not_contains "$tmp5/commands.log" "git -C"
require_contains "$tmp5/out" "Using existing checkout (--no-update)"

# ---------------------------------------------------------------------------
# --dry-run does not clone or add the profile.
# ---------------------------------------------------------------------------
tmp6="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2" "$tmp3" "$tmp4" "$tmp5" "$tmp6"' EXIT
mkdir -p "$tmp6/home"
setup_stubs "$tmp6/stub-bin" "$tmp6/commands.log" "v22.19.0"
PATH="$tmp6/stub-bin:/usr/bin:/bin" HOME="$tmp6/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp6/runtime" \
  bash "$SCRIPT" --dry-run --skip-dsh-install >"$tmp6/out" 2>"$tmp6/err" \
  || fail "dry-run failed"
require_contains "$tmp6/out" "dry-run: git clone"
require_not_contains "$tmp6/commands.log" "git clone"
[ ! -e "$tmp6/runtime" ] || fail "dry-run created the runtime dir"

# ---------------------------------------------------------------------------
# API key value is never printed.
# ---------------------------------------------------------------------------
tmp7="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2" "$tmp3" "$tmp4" "$tmp5" "$tmp6" "$tmp7"' EXIT
mkdir -p "$tmp7/home"
setup_stubs "$tmp7/stub-bin" "$tmp7/commands.log" "v22.19.0"
PATH="$tmp7/stub-bin:/usr/bin:/bin" HOME="$tmp7/home" \
  MULTICA_DSH_RUNTIME_DIR="$tmp7/runtime" \
  DEEPSEEK_API_KEY="sk-secret-must-not-leak" \
  bash "$SCRIPT" --skip-dsh-install >"$tmp7/out" 2>"$tmp7/err" \
  || fail "credential path failed"
require_contains "$tmp7/out" "DEEPSEEK_API_KEY is set (value not printed)"
require_not_contains "$tmp7/out" "sk-secret-must-not-leak"
require_not_contains "$tmp7/err" "sk-secret-must-not-leak"

echo "setup-dsh-runtime.sh tests passed"
