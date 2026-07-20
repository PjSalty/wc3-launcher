#!/usr/bin/env bash
#
# package-preconfigured.sh
#
# Build a preconfigured wc3-launcher zip for your players: they unzip and run,
# with zero setup. It downloads the signed release binaries and drops a
# wc3-launcher.json next to each, so the launcher connects to YOUR server on
# first run without anyone entering anything.
#
# Your settings come from the ENVIRONMENT, never the command line, so the relay
# token is not exposed in your shell history or process list. Load them from
# your secret store first, e.g.:
#   export WC3_SERVER="wc3.example.com"
#   export WC3_RELAY_TOKEN="$(your-secret-tool get wc3/relay_token)"
#   export WC3_RELAY_CERT_PIN="base64-spki-hash"   # optional, public info
#   export WC3_GATEWAY="My Realm"                    # optional, cosmetic
#   ./hack/package-preconfigured.sh
#
# Output: dist/wc3-launcher-windows.zip and dist/wc3-launcher-linux.zip.

set -euo pipefail

# --- inputs (from env) -------------------------------------------------------
: "${WC3_SERVER:?set WC3_SERVER to your PvPGN + relay host}"
export WC3_SERVER
export TOKEN="${WC3_RELAY_TOKEN:-}"
export CERTPIN="${WC3_RELAY_CERT_PIN:-}"
export GATEWAY="${WC3_GATEWAY:-}"
VERSION="${WC3_VERSION:-latest}"
REPO="${WC3_REPO:-PjSalty/wc3-launcher}"
OUT="${WC3_OUT:-dist}"

for tool in curl sha256sum python3; do
  command -v "$tool" >/dev/null || { echo "error: '$tool' is required" >&2; exit 1; }
done

# --- resolve the release + fetch checksums -----------------------------------
if [ "$VERSION" = "latest" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$VERSION" ] || { echo "error: could not resolve the latest release tag" >&2; exit 1; }
fi
BASE="https://github.com/$REPO/releases/download/$VERSION"
echo "packaging $REPO $VERSION for server '$WC3_SERVER'"

mkdir -p "$OUT"
OUT_ABS=$(cd "$OUT" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL "$BASE/SHA256SUMS" -o "$work/SHA256SUMS"

# --- the shared config the launcher reads next to its binary -----------------
# Encoded by json.dumps so a value with a quote or backslash cannot corrupt it.
# Values come from the environment above; nothing is printed.
python3 -c 'import json,os,sys; open(sys.argv[1],"w").write(json.dumps({"server":os.environ["WC3_SERVER"],"token":os.environ["TOKEN"],"certPin":os.environ["CERTPIN"],"gateway":os.environ["GATEWAY"]}, indent=2)+"\n")' "$work/wc3-launcher.json"

cat > "$work/README.txt" <<'TXT'
Warcraft III launcher

1. Unzip this folder anywhere you like.
2. Run wc3-launcher.exe (Windows) or ./wc3-launcher (Linux).
   Windows asks for administrator: that is needed to point the game at the realm.
3. First run installs Warcraft III from Blizzard's own free download, syncs the
   maps, and launches the game. Nothing to type. Next time, use the desktop icon
   it creates.

You do not need to change anything: this build is already pointed at the server.
TXT

# --- package each platform ---------------------------------------------------
package() {
  local plat="$1" bin="$2"
  local zip="$OUT_ABS/wc3-launcher-$plat.zip"
  curl -fsSL "$BASE/$bin" -o "$work/$bin"
  # integrity: the release is checksummed (and cosign-signed); verify the hash.
  ( cd "$work" && grep " $bin\$" SHA256SUMS | sha256sum -c - >/dev/null ) \
    || { echo "error: checksum verification failed for $bin" >&2; exit 1; }
  python3 -c '
import sys, zipfile
out, binp, binn, jsonp, readme = sys.argv[1:6]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    z.write(binp, binn)
    z.write(jsonp, "wc3-launcher.json")
    z.write(readme, "README.txt")
' "$zip" "$work/$bin" "$bin" "$work/wc3-launcher.json" "$work/README.txt"
  echo "  built ${zip#"$OUT_ABS"/}  ($(du -h "$zip" | cut -f1))"
}

package windows wc3-launcher.exe
package linux   wc3-launcher

echo "done -> $OUT_ABS/"
