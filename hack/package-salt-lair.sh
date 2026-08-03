#!/usr/bin/env bash
#
# package-salt-lair.sh
#
# Build the preconfigured "SaltWC3-Launcher" zip handed to friends, in the folder
# layout they already know:
#
#   SaltWC3-Launcher/
#     SaltWC3-Launcher.exe       (0755)
#     SaltWC3-Launcher-linux     (0755)
#     wc3-launcher.json          (server + token + cert pin + realm name)
#     README.txt
#
# It takes the CI-built generic binaries for a tag, verifies the checksums CI
# published, and injects the connection settings from this project's CI variables.
# The relay token flows environment -> json only; it is never printed, never put
# on a command line, and never committed.
#
# This lives in the repo on purpose. The previous version of it existed only in a
# scratch directory, which was then lost, and with it the only way to rebuild the
# artifact players actually run.
#
# Usage:  ./hack/package-salt-lair.sh v1.3.8 [output-dir]
#
# Requires: glab (authenticated), python3, unzip, sha256sum.

set -euo pipefail

REF="${1:?usage: package-salt-lair.sh <tag> [output-dir]}"
DIST="${2:-$HOME/GIT/wc3-launcher-dist}"
PROJ="games%2Fwc3-launcher"

# glab resolves its host from the current directory's git remote. This script is
# routinely run from the dist directory, which is not a repo, so pin the host or
# every call fails with an unhelpful "Unauthenticated".
export GITLAB_HOST="${GITLAB_HOST:-gitlab.salt.saltstice.com}"

for tool in glab python3 unzip sha256sum; do
  command -v "$tool" >/dev/null || { echo "error: '$tool' is required" >&2; exit 1; }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$DIST"

echo "== 1. download the $REF build artifact =="
# Artifact download BY REF 404s for tags on this GitLab, so resolve the tag's
# pipeline and pull the build job's artifact by job id instead.
PID="$(glab api "projects/$PROJ/pipelines?ref=$REF&per_page=1" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d[0]["id"] if d else "")')"
[ -n "$PID" ] || { echo "error: no pipeline found for $REF" >&2; exit 1; }
JID="$(glab api "projects/$PROJ/pipelines/$PID/jobs" | python3 -c 'import sys,json;print(next((j["id"] for j in json.load(sys.stdin) if j["name"]=="build"),""))')"
[ -n "$JID" ] || { echo "error: no build job in pipeline $PID" >&2; exit 1; }
glab api "projects/$PROJ/jobs/$JID/artifacts" > "$WORK/art.zip"
unzip -q "$WORK/art.zip" -d "$WORK/art"

LX="$(find "$WORK/art" -type f -name wc3-launcher | head -1)"
WN="$(find "$WORK/art" -type f -name 'wc3-launcher.exe' | head -1)"
SUMS="$(find "$WORK/art" -type f -name SHA256SUMS | head -1)"
[ -n "$LX" ] && [ -n "$WN" ] && [ -n "$SUMS" ] \
  || { echo "error: artifact is missing a file" >&2; find "$WORK/art" -type f >&2; exit 1; }

echo "== 2. verify the checksums CI published =="
( cd "$(dirname "$SUMS")" && sha256sum -c "$(basename "$SUMS")" ) \
  || { echo "error: checksum mismatch" >&2; exit 1; }

echo "== 3. read the connection settings from CI variables (values never printed) =="
WC3_SERVER="$(glab variable get SERVER_HOST -R games/wc3-launcher 2>/dev/null)"; export WC3_SERVER
WC3_RELAY_TOKEN="$(glab variable get RELAY_TOKEN -R games/wc3-launcher 2>/dev/null)"; export WC3_RELAY_TOKEN
WC3_RELAY_CERT_PIN="$(glab variable get RELAY_CERT_PIN -R games/wc3-launcher 2>/dev/null)"; export WC3_RELAY_CERT_PIN
export WC3_GATEWAY="${WC3_GATEWAY:-Salt Lair}"
# A missing cert pin is NOT a permissive fallback: the relay certificate is
# self-signed, so with an empty pin the launcher falls back to CA verification
# that can never succeed, silently dropping every player to a direct connection
# and skipping map sync forever. Treat all three as mandatory.
[ -n "$WC3_SERVER" ] && [ -n "$WC3_RELAY_TOKEN" ] && [ -n "$WC3_RELAY_CERT_PIN" ] \
  || { echo "error: a required CI variable is empty (SERVER_HOST / RELAY_TOKEN / RELAY_CERT_PIN)" >&2; exit 1; }

export LX WN DIST VER="$REF"

echo "== 4. assemble the zip =="
python3 <<'PY'
import os, json, zipfile, hashlib

lx, wn, dist, ver = os.environ["LX"], os.environ["WN"], os.environ["DIST"], os.environ["VER"]

cfg = {
    "server":  os.environ["WC3_SERVER"],
    "token":   os.environ["WC3_RELAY_TOKEN"],
    "certPin": os.environ["WC3_RELAY_CERT_PIN"],
    "gateway": os.environ["WC3_GATEWAY"],
}
cfg_bytes = (json.dumps(cfg, indent=2) + "\n").encode()

readme = f"""    Salt Lair - Warcraft III        (Launcher {ver})

  WHAT THIS IS
    Warcraft III: The Frozen Throne on our own private realm. One download,
    no setup, no Battle.net account.

  HOW TO PLAY
    Windows   double-click  SaltWC3-Launcher.exe
    Linux     run           ./SaltWC3-Launcher-linux

    The first run installs Warcraft III from Blizzard's own free download,
    syncs our map library, and starts the game. Nothing to type or configure.
    After that, use the "Warcraft III (Online)" icon it puts on your desktop.

  IF IT ASKS TO INSTALL SOMETHING
    On Linux it may offer to install Wine, a Vulkan driver, or video codecs.
    It shows you the exact command first and asks before running anything.
    Say yes: on a fresh machine the game will not start without them.

  GETTING AN ACCOUNT
    Create it in the game: on the login screen choose "Create Account".
    Your account lives only on our realm.

  IF SOMETHING GOES WRONG
    Press Ctrl-C to back out of any prompt. If the game will not start, send
    me the text from the launcher window and I can tell what happened.

  CHECKSUMS
    SHA256SUMS.txt next to this zip lists the hashes of both binaries, if you
    want to verify what you downloaded.
"""
readme_bytes = readme.encode()

def add(z, arcname, data, mode):
    # Set the mode explicitly. zipfile.write() would copy the source file's mode,
    # and a downloaded file is 0644, which ships a Linux binary that will not run.
    zi = zipfile.ZipInfo(arcname)
    zi.external_attr = (mode & 0o7777) << 16
    zi.compress_type = zipfile.ZIP_DEFLATED
    z.writestr(zi, data)

with open(lx, "rb") as f: lx_data = f.read()
with open(wn, "rb") as f: wn_data = f.read()

out = os.path.join(dist, "SaltWC3-Launcher.zip")
with zipfile.ZipFile(out, "w") as z:
    add(z, "SaltWC3-Launcher/README.txt", readme_bytes, 0o644)
    add(z, "SaltWC3-Launcher/wc3-launcher.json", cfg_bytes, 0o644)
    add(z, "SaltWC3-Launcher/SaltWC3-Launcher.exe", wn_data, 0o755)
    add(z, "SaltWC3-Launcher/SaltWC3-Launcher-linux", lx_data, 0o755)

sums = os.path.join(dist, "SHA256SUMS.txt")
with open(sums, "w") as f:
    f.write("%s  SaltWC3-Launcher.exe\n"   % hashlib.sha256(wn_data).hexdigest())
    f.write("%s  SaltWC3-Launcher-linux\n" % hashlib.sha256(lx_data).hexdigest())

# Report shape only, never secret values.
print("  config keys:", sorted(cfg))
print("  server set:", bool(cfg["server"]),
      "| token len:", len(cfg["token"]),
      "| certPin len:", len(cfg["certPin"]),
      "| gateway:", cfg["gateway"])
print("  zip:", out)
print("  sums:", sums)
PY

echo "== 5. final contents =="
python3 - "$DIST/SaltWC3-Launcher.zip" <<'PY'
import sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
for i in z.infolist():
    print("  %s  %9d  %s" % (oct((i.external_attr >> 16) & 0o777), i.file_size, i.filename))
PY
echo "== done =="
