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

# A refusal of the home folder --real-home was given, with the way around it.
refuse_home() {
  refuse "not starting: $*, or run start without --real-home to use the build's own home folder."
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
  case $home in /*) ;; *) refuse_home "your home folder ($home) is not an absolute path" ;; esac
  [ "$home" = / ] || home=${home%/}
  [ -d "$home" ] && [ ! -L "$home" ] || refuse_home "your home folder $home is a link or not a folder"
  [ "$(stat -c %u "$home")" = "$(id -u)" ] || refuse_home "your home folder $home belongs to another user"
  mode=$(stat -c %a "$home")
  case $mode in
    *00) ;;
    *) refuse_home "other users can open your home folder $home (mode $mode). The add-ons are trusted only from a home only you can open: run chmod 700 \"$home\"" ;;
  esac
else
  home="$here/home"
  [ ! -L "$home" ] || refuse "not starting: $home is a link: move it away, and start makes a new home folder there"
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
if ! plain "$home"; then
  if [ -n "$real" ]; then
    refuse_home "the path of your home folder ($home) holds a quote, backslash or control character"
  fi
  refuse "not starting: the build's path ($here) holds a quote, backslash or control character: build it into another folder with make app-local LOCAL=<folder>"
fi
plain "$index" || refuse "not starting: the build's path ($here) holds a quote, backslash or control character: build it into another folder with make app-local LOCAL=<folder>"
config="${XDG_CONFIG_HOME:-$home/.config}/$identifier"
data="${XDG_DATA_HOME:-$home/.local/share}/$identifier"
for f in "$home/$marker" "$home/$roots"; do
  if [ -e "$f" ] || [ -L "$f" ]; then
    [ -f "$f" ] && [ ! -L "$f" ] || refuse "not starting: $f is not a plain file: move it away, then start again"
  fi
done

if [ -n "$real" ]; then
  cat >&2 <<EOF
start: starting pluxy (local build) on your own home folder, $home.
It writes:
  $home/$marker
  $home/$roots
      the trust profile: this build's add-on signing key, for its add-on index only
  $config/
      the app's settings, with device-registries.json naming this build's add-ons
  $data/
      the app's data: its pluxy, providers, installed add-ons, and the backups the tool pages keep
and, only when you confirm Connect, Disconnect or Restore backup on a tool's page, that tool's own
settings, such as ~/.claude/settings.json or ~/.codex/config.toml. Connect points that tool at
this build's pluxy, away from any other pluxy it used.
To remove it: press Disconnect on each tool page you connected (its backups are in the app's data),
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
  printf 'start: this build'"'"'s add-ons will not show in Add-ons: %s lists other add-on sources. Add {"name":"local-build","url":"%s"} to its "registries", then start again.\n' \
    "$config/device-registries.json" "$index" >&2
fi

HOME=$home
PLUXY_TEST_TRUST_NONCE=$nonce
export HOME PLUXY_TEST_TRUST_NONCE
exec "$program" "$@"
