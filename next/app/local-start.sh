#!/bin/sh
# Starts the local build (next/app/README.md, "The local build") with its coding-tool add-ons
# trusted. bun package.ts local copies this file into the build's folder as `start`.
#
#   start                 the app in a home folder of its own, home/ in the build's folder
#   start --real-home     the app on your own home folder and coding tools; it says first what it
#                         writes there and how to remove it
#
# Other arguments go to the app. The app is built with the artifact-test-trust feature, which
# accepts publisher roots only from a trust profile in the process HOME: a folder owned by you that
# no one else may use (mode 0700 here), holding a marker that names that folder and a nonce the
# app is given as PLUXY_TEST_TRUST_NONCE, and the roots file. This writes that profile: one root,
# the key this build's add-ons were signed with, for this build's add-on index only. It also names
# that index in the app's device-registries.json. Signatures are checked as in any build.
set -eu

refuse() {
  printf 'start: %s\n' "$*" >&2
  exit 1
}

here=$(cd "$(dirname "$0")" && pwd -P)
program="$here/bin/pluxy-app-local"
tools="$here/lib/pluxy Local/app-tools"
index="$tools/index.json"
identifier=dev.pluxy.desktop.local
marker=.pluxy-test-trust-profile.json
roots=.pluxy-test-trust-roots.json

[ -x "$program" ] || refuse "$program is missing: build again with make app-local"
[ -f "$index" ] && [ -f "$tools/publisher.json" ] || refuse "$tools is incomplete: build again with make app-local"

# The key this build's add-ons are signed with, as package.ts wrote it.
key_id=$(sed -n 's/^  "key_id": "\([a-z0-9-]*\)",\{0,1\}$/\1/p' "$tools/publisher.json")
public_key=$(sed -n 's/^  "public_key": "\([0-9a-f]\{64\}\)",\{0,1\}$/\1/p' "$tools/publisher.json")
[ -n "$key_id" ] && [ -n "$public_key" ] || refuse "$tools/publisher.json does not name a key"

# A path written into JSON as it is: one that would need escaping is refused.
plain() {
  case $1 in *'"'* | *'\'*) return 1 ;; esac
  [ "$(printf '%s' "$1" | tr -d '\001-\037\177')" = "$1" ]
}

real=
if [ "${1-}" = --real-home ]; then
  real=1
  shift
fi

if [ -n "$real" ]; then
  home=${HOME-}
  case $home in /*) ;; *) refuse "HOME is not an absolute path: not starting" ;; esac
  [ -d "$home" ] && [ ! -L "$home" ] || refuse "HOME ($home) is not a folder (a link to one is not enough): not starting"
  [ "$(stat -c %u "$home")" = "$(id -u)" ] || refuse "HOME ($home) is not owned by you: not starting"
  mode=$(stat -c %a "$home")
  case $mode in
    *00) ;;
    *) refuse "HOME ($home) has mode $mode, so other users may use it. The trust profile is accepted only in a home no one else may use (chmod 700 \"$home\"): not starting" ;;
  esac
else
  home="$here/home"
  mkdir -p "$home"
  chmod 700 "$home"
  # Everything the app and the tools it starts keep goes under that home, not yours.
  XDG_CONFIG_HOME="$home/.config"
  XDG_DATA_HOME="$home/.local/share"
  XDG_CACHE_HOME="$home/.cache"
  XDG_STATE_HOME="$home/.local/state"
  export XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME
fi
home=$(cd "$home" && pwd -P)
plain "$home" || refuse "the home folder's path ($home) holds a quote, backslash or control character: not starting"
plain "$index" || refuse "the build's path ($here) holds a quote, backslash or control character: not starting"
config="${XDG_CONFIG_HOME:-$home/.config}/$identifier"
data="${XDG_DATA_HOME:-$home/.local/share}/$identifier"
for f in "$home/$marker" "$home/$roots"; do
  if [ -e "$f" ] || [ -L "$f" ]; then
    [ -f "$f" ] && [ ! -L "$f" ] || refuse "$f is not a plain file: not starting"
  fi
done

if [ -n "$real" ]; then
  cat >&2 <<EOF
start: starting pluxy Local on your own home folder, $home.
It writes:
  $home/$marker
  $home/$roots
      the trust profile: this build's add-on signing key, for its add-on index only
  $config/
      the app's settings, with device-registries.json naming this build's add-ons
  $data/
      the app's data: its cores, providers, installed add-ons, and the backups the tool pages keep
and, only when you confirm Apply (or Clear, Restore backup) on a tool's page, that tool's own
settings, such as ~/.claude/settings.json or ~/.codex/config.toml.
To remove it: first press Clear on each tool page you applied (its backups are in the app's data),
quit the app, then:
  rm -f "$home/$marker" "$home/$roots"
  rm -rf "$config" "$data"
EOF
fi

umask 077
# The roots file names this build's key and index, written each start, so a home other builds
# also used trusts only the build started last.
tmp="$home/$roots.$$"
printf '{"roots":[{"kind":"test","publisher":"pluxy","source":"%s","key_id":"%s","public_key":"%s"}],"updater":null}\n' \
  "$index" "$key_id" "$public_key" >"$tmp"
mv -f "$tmp" "$home/$roots"
# The marker is kept while it names this home: an app already running holds its nonce.
nonce=
if [ -f "$home/$marker" ]; then
  nonce=$(sed -n 's/^{"format":1,"home":"\(.*\)","purpose":"pluxy-artifact-test-trust","nonce":"\([0-9a-f]\{64\}\)"}$/\1 \2/p' "$home/$marker")
  case $nonce in "$home "*) nonce=${nonce##* } ;; *) nonce= ;; esac
fi
if [ -z "$nonce" ]; then
  nonce=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
  tmp="$home/$marker.$$"
  printf '{"format":1,"home":"%s","purpose":"pluxy-artifact-test-trust","nonce":"%s"}\n' "$home" "$nonce" >"$tmp"
  mv -f "$tmp" "$home/$marker"
fi

# The app reads its registries from its config folder. A file already there that does not name
# this build's index is left as it is.
mkdir -p "$config"
if [ ! -e "$config/device-registries.json" ]; then
  printf '{"version":0,"registries":[{"name":"local-build","url":"%s"}]}\n' "$index" >"$config/device-registries.json"
elif ! grep -qF "\"$index\"" "$config/device-registries.json"; then
  printf 'start: %s does not name this build'"'"'s add-ons (%s): add it there to install them\n' \
    "$config/device-registries.json" "$index" >&2
fi

HOME=$home
PLUXY_TEST_TRUST_NONCE=$nonce
export HOME PLUXY_TEST_TRUST_NONCE
exec "$program" "$@"
