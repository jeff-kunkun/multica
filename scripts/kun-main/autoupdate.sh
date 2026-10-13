#!/usr/bin/env bash
# Keeps the kun-main instance on the tip of kun-main.
#
# Run by multica-main-autoupdate.timer on the host. It waits until the image
# builder has published sha-<tip> images for both services, then recreates the
# stack from those images, checks /health, and rolls back to the previous tag on
# failure. It only ever touches the compose project named in $PROJECT, so the
# kun-line instance on the same host is out of reach.
set -euo pipefail

# The run checks the repo out at another commit, which rewrites this file;
# bash reads scripts lazily, so parse everything before doing anything.
{

: "${REPO_DIR:=/opt/multica-main}"
: "${BRANCH:=kun-main}"
: "${PROJECT:=multica-main}"
: "${REGISTRY:=ghcr.io/kkunkunya}"
: "${IMAGE_PREFIX:=multica-main}"
: "${STATE_FILE:=/var/lib/multica-main/autoupdate.json}"
: "${LOCK_FILE:=/run/lock/multica-main-autoupdate.lock}"
: "${HEALTH_URL:=http://127.0.0.1:8090/health}"
: "${HEALTH_TIMEOUT_SECONDS:=180}"

exec 9>"$LOCK_FILE"
flock -n 9 || { echo "another run holds $LOCK_FILE"; exit 0; }

log() { echo "[$(date -u +%FT%TZ)] $*"; }

state() { # state <deployed> <target> <result>
  mkdir -p "$(dirname "$STATE_FILE")"
  printf '{"deployed_commit":"%s","target_commit":"%s","result":"%s","last_check_at":"%s"}\n' \
    "$1" "$2" "$3" "$(date -u +%FT%TZ)" >"$STATE_FILE.tmp"
  mv "$STATE_FILE.tmp" "$STATE_FILE"
}

compose() { # compose <sha> <args...>
  local sha=$1; shift
  MULTICA_BACKEND_IMAGE="$REGISTRY/$IMAGE_PREFIX-backend" \
  MULTICA_WEB_IMAGE="$REGISTRY/$IMAGE_PREFIX-web" \
  MULTICA_IMAGE_TAG="sha-$sha" \
    docker compose -p "$PROJECT" --project-directory "$REPO_DIR" \
      --env-file "$REPO_DIR/.env" -f "$REPO_DIR/docker-compose.selfhost.yml" "$@"
}

healthy() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
  while ((SECONDS < deadline)); do
    curl -fsS -m 5 "$HEALTH_URL" >/dev/null 2>&1 && return 0
    sleep 5
  done
  return 1
}

cd "$REPO_DIR"
deployed=""
[[ -f "$STATE_FILE" ]] && deployed=$(sed -n 's/.*"deployed_commit":"\([0-9a-f]*\)".*/\1/p' "$STATE_FILE")

git fetch -q origin "$BRANCH"
target=$(git rev-parse "origin/$BRANCH")

if [[ "$target" == "$deployed" ]] && healthy; then
  state "$deployed" "$target" noop
  exit 0
fi

for svc in backend web; do
  if ! docker manifest inspect "$REGISTRY/$IMAGE_PREFIX-$svc:sha-$target" >/dev/null 2>&1; then
    log "waiting for $IMAGE_PREFIX-$svc:sha-$target"
    state "$deployed" "$target" waiting_for_images
    exit 0
  fi
done

log "deploying $target (was ${deployed:-none})"
git checkout -q --detach "$target"
compose "$target" pull -q
compose "$target" up -d --remove-orphans

if healthy; then
  state "$target" "$target" deployed
  docker image prune -f >/dev/null
  log "deployed $target"
  exit 0
fi

log "health check failed for $target"
if [[ -n "$deployed" ]]; then
  git checkout -q --detach "$deployed"
  compose "$deployed" up -d --remove-orphans
  healthy && log "rolled back to $deployed" || log "rollback to $deployed is unhealthy too"
fi
state "$deployed" "$target" failed
exit 1
}
