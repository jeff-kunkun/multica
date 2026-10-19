#!/usr/bin/env bash
# Ensure this Mac has the fixed local code-signing identity for Multica Desktop
# and print `<identity name>\t<keychain path>` on stdout.
#
# Why: an ad-hoc signature changes with every build, so macOS treats each
# upgrade as a new app and drops privacy grants like Full Disk Access. A
# self-signed certificate that stays the same gives every build the same
# designated requirement, so grants survive upgrades.
#
# The identity lives in its own keychain with a random password kept next to
# it (0600), so packaging can unlock it without prompts. Nothing goes into the
# repository. Deleting the keychain mints a new certificate next time, and
# every Mac then has to grant Full Disk Access once more.
set -euo pipefail

NAME="${MULTICA_MAC_SIGN_IDENTITY:-Multica Local Signing}"
STATE_DIR="${MULTICA_MAC_SIGN_DIR:-$HOME/.multica/desktop-signing}"
KEYCHAIN="$STATE_DIR/signing.keychain-db"
PASSFILE="$STATE_DIR/keychain-password"

[ "$(uname)" = "Darwin" ] || { echo "mac-local-signing: macOS only" >&2; exit 1; }

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

if [ ! -f "$KEYCHAIN" ]; then
  umask 077
  /usr/bin/openssl rand -hex 24 > "$PASSFILE"
  PASS="$(cat "$PASSFILE")"
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  cat > "$WORK/cert.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
  /usr/bin/openssl req -new -x509 -newkey rsa:2048 -nodes -days 3650 \
    -config "$WORK/cert.cnf" -keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null
  /usr/bin/openssl pkcs12 -export -inkey "$WORK/key.pem" -in "$WORK/cert.pem" \
    -name "$NAME" -passout "pass:$PASS" -out "$WORK/identity.p12"
  security create-keychain -p "$PASS" "$KEYCHAIN"
  security set-keychain-settings "$KEYCHAIN" # no auto-lock
  security unlock-keychain -p "$PASS" "$KEYCHAIN"
  security import "$WORK/identity.p12" -k "$KEYCHAIN" -P "$PASS" \
    -T /usr/bin/codesign >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s \
    -k "$PASS" "$KEYCHAIN" >/dev/null
  echo "mac-local-signing: created \"$NAME\" in $KEYCHAIN" >&2
fi

security unlock-keychain -p "$(cat "$PASSFILE")" "$KEYCHAIN"

# codesign resolves the certificate chain only through the search list.
SEARCH=()
while IFS= read -r line; do
  [ -n "$line" ] && SEARCH+=("$line")
done < <(security list-keychains -d user | sed -e 's/^[[:space:]]*"//' -e 's/"$//')
if ! printf '%s\n' "${SEARCH[@]}" | grep -qxF "$KEYCHAIN"; then
  security list-keychains -d user -s "$KEYCHAIN" "${SEARCH[@]}"
fi

if ! security find-identity -p codesigning "$KEYCHAIN" | grep -qF "\"$NAME\""; then
  echo "mac-local-signing: \"$NAME\" missing from $KEYCHAIN; delete $STATE_DIR to recreate" >&2
  exit 1
fi

printf '%s\t%s\n' "$NAME" "$KEYCHAIN"
