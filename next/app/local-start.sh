#!/bin/sh
# Starts the local build (next/app/README.md, "The local build") with its coding-tool add-ons
# trusted. bun package.ts local copies this file into the build's folder as `start`.
#
#   start                 the app in a home folder of its own, home/ in the build's folder
#   start --real-home     the app on your own home folder and coding tools; it says first what it
#                         writes there and how to remove it
#
# Other arguments go to the app. The app is built with the local-build and artifact-test-trust
# features, which together accept publisher roots only from a trust profile in the process HOME
# (src-tauri/src/trust.rs, PROFILE_TRUST): a folder owned by you that no one else may use (mode
# 0700 here), holding a marker that names that folder and a nonce the app is given as
# PLUXY_TEST_TRUST_NONCE, and the roots file. This writes that profile: one root, the key this
# build's add-ons were signed with, for this build's add-on index only. It also names that index in
# the app's device-registries.json. Signatures are checked as in any build.
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

# A path written into JSON as it is: one that would need escaping is refused. Every path start
# uses passes this before anything is written or printed.
plain() {
  case $1 in *'"'* | *'\'*) return 1 ;; esac
  [ "$(printf '%s' "$1" | tr -d '\001-\037\177')" = "$1" ]
}

# A path as one word in single quotes, for the commands start prints for a person to run: the shell
# reads nothing in it, not `$`, a backquote or a space. Only for paths plain() let through (no
# newline, which $() would drop).
quoted() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

build_path() {
  refuse "not starting: the build's path ($here) holds a quote, backslash or control character: build it into another folder with make app-local LOCAL=<folder>"
}
for p in "$here" "$program" "$index"; do
  plain "$p" || build_path
done

real=
if [ "${1-}" = --real-home ]; then
  real=1
  shift
fi

if [ -n "$real" ]; then
  home=${HOME-}
  case $home in /*) ;; *) refuse_home "your home folder ($home) is not an absolute path" ;; esac
  [ "$home" = / ] || home=${home%/}
  plain "$home" || refuse_home "the path of your home folder ($home) holds a quote, backslash or control character"
  # The app's settings and data folders are in XDG_CONFIG_HOME and XDG_DATA_HOME when they are
  # absolute paths (the app ignores a relative one, as the XDG base directory rules say).
  case ${XDG_CONFIG_HOME-} in /*) plain "$XDG_CONFIG_HOME" || refuse_home "the path in XDG_CONFIG_HOME ($XDG_CONFIG_HOME) holds a quote, backslash or control character" ;; esac
  case ${XDG_DATA_HOME-} in /*) plain "$XDG_DATA_HOME" || refuse_home "the path in XDG_DATA_HOME ($XDG_DATA_HOME) holds a quote, backslash or control character" ;; esac
  [ -d "$home" ] && [ ! -L "$home" ] || refuse_home "your home folder $home is a link or not a folder"
  [ "$(stat -c %u "$home")" = "$(id -u)" ] || refuse_home "your home folder $home belongs to another user"
  mode=$(stat -c %a "$home")
  case $mode in
    *00) ;;
    *) refuse_home "other users can open your home folder $home (mode $mode). The add-ons are trusted only from a home only you can open: run chmod 700 $(quoted "$home")" ;;
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
plain "$home" || refuse_home "the path of your home folder ($home) holds a quote, backslash or control character"
case ${XDG_CONFIG_HOME-} in /*) config=$XDG_CONFIG_HOME ;; *) config=$home/.config ;; esac
case ${XDG_DATA_HOME-} in /*) data=$XDG_DATA_HOME ;; *) data=$home/.local/share ;; esac
config=$config/$identifier
data=$data/$identifier
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
  rm -f $(quoted "$home/$marker") $(quoted "$home/$roots")
  rm -rf $(quoted "$config") $(quoted "$data")
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

# The app reads its registries from its config folder. A file already there is left as it is; it
# is read as JSON (by registry_url below) to tell whether one of its registries' url is this
# build's index, and start says what to add when none is.
mkdir -p "$config"
registries=$config/device-registries.json
if [ ! -e "$registries" ] && [ ! -L "$registries" ]; then
  printf '{"version":0,"registries":[{"name":"local-build","url":"%s"}]}\n' "$index" >"$registries"
else
  # Prints yes when a registry in the file's top-level "registries" has url equal to $want, no when
  # none has, and bad when the file is not JSON. A url written with a \u escape never matches.
  registry_url='
function fail() { bad = 1; pos = len + 1 }
function ws() { while (pos <= len && index(" \t\r\n", substr(s, pos, 1))) pos++ }
function str(   out, c) {
  pos++; out = ""
  while (pos <= len) {
    c = substr(s, pos, 1)
    if (c == "\"") { pos++; return out }
    if (c == "\\") {
      c = substr(s, pos + 1, 1)
      if (c == "\"" || c == "\\" || c == "/") { out = out c; pos += 2; continue }
      if (index("bfnrt", c) && c != "") { out = out "\001"; pos += 2; continue }
      if (c == "u" && substr(s, pos + 2, 4) ~ /^[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]$/) { out = out "\001"; pos += 6; continue }
      fail(); return ""
    }
    if (c < " ") { fail(); return "" }
    out = out c; pos++
  }
  fail(); return ""
}
function value(at,   c, k, v) {
  ws(); c = substr(s, pos, 1)
  if (c == "{") {
    pos++; ws()
    if (substr(s, pos, 1) == "}") { pos++; return }
    for (;;) {
      ws()
      if (substr(s, pos, 1) != "\"") { fail(); return }
      k = str(); ws()
      if (substr(s, pos, 1) != ":") { fail(); return }
      pos++; ws()
      if (at == "top" && k == "registries") value("list")
      else if (at == "entry" && k == "url" && substr(s, pos, 1) == "\"") { v = str(); if (v == want) found = 1 }
      else value("")
      ws(); c = substr(s, pos, 1); pos++
      if (c == "}") return
      if (c != ",") { fail(); return }
    }
  }
  if (c == "[") {
    pos++; ws()
    if (substr(s, pos, 1) == "]") { pos++; return }
    for (;;) {
      value(at == "list" ? "entry" : "")
      ws(); c = substr(s, pos, 1); pos++
      if (c == "]") return
      if (c != ",") { fail(); return }
    }
  }
  if (c == "\"") { str(); return }
  if (substr(s, pos, 4) == "true" || substr(s, pos, 4) == "null") { pos += 4; return }
  if (substr(s, pos, 5) == "false") { pos += 5; return }
  if (match(substr(s, pos), /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?/)) { pos += RLENGTH; return }
  fail()
}
{ s = s $0 "\n" }
END {
  want = ENVIRON["want"]; len = length(s); pos = 1
  value("top"); ws()
  if (bad || pos <= len) print "bad"; else if (found) print "yes"; else print "no"
}'
  listed=$(want=$index LC_ALL=C awk "$registry_url" "$registries" 2>/dev/null) || listed=bad
  case $listed in
    yes) ;;
    no)
      printf 'start: this build'"'"'s add-ons will not show in Add-ons: %s lists other add-on sources. Add {"name":"local-build","url":"%s"} to its "registries", then start again.\n' \
        "$registries" "$index" >&2
      ;;
    *)
      printf 'start: this build'"'"'s add-ons will not show in Add-ons: %s cannot be read as JSON. Fix it or move it away, then start again.\n' \
        "$registries" >&2
      ;;
  esac
fi

HOME=$home
PLUXY_TEST_TRUST_NONCE=$nonce
export HOME PLUXY_TEST_TRUST_NONCE
exec "$program" "$@"
