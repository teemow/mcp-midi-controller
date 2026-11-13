#!/usr/bin/env bash
# deploy-mac.sh — run the daemon as a launchd user agent on macOS and keep it
# on the latest main. The macOS counterpart of deploy.sh (systemd).
#
#   scripts/deploy-mac.sh           install / refresh both launchd agents, then
#                                   build, install and restart the daemon now
#   scripts/deploy-mac.sh --update  what the update agent runs every 5 minutes
#                                   (and once on wake): fetch origin/main; when
#                                   it moved (or got a new release tag),
#                                   fast-forward this clone, and when the daemon
#                                   it builds differs from the running one and
#                                   the daemon is not busy, build, install and
#                                   restart. Otherwise silent.
#
# Two agents in ~/Library/LaunchAgents, rendered from init/:
#   com.teemow.mcp-midi-controller         the daemon (KeepAlive on failure)
#   com.teemow.mcp-midi-controller.update  this script with --update
#
# An update restarts the daemon only when it has to and when nothing is cut:
#   - A commit that changes nothing the binary is built from (docs, tests, the
#     web sources) does not restart it. The update builds a fingerprint of the
#     checkout (the binary without its version, its VCS stamp and the web UI's
#     copy of the docs) and compares it with the running build's; the same
#     fingerprint leaves the daemon as it is, on its older version.
#   - While the daemon is busy (a song playing, a recording, a stem recording,
#     a mix measurement, a tool call: GET /busy on its loopback address), the
#     update waits and asks again on the next run, logging what it waits for.
#     It builds nothing meanwhile. After MCP_MIDI_UPDATE_MAX_WAIT_MIN minutes
#     (60) of waiting it restarts anyway. A daemon that does not answer (not
#     running, or too old to know /busy) has nothing to wait for.
#
# The clone this script lives in is the source: keep it on main and clean. The
# update refuses to touch a clone with local changes or another branch, and it
# skips a commit whose build failed until main moves again, so a broken build
# never replaces the running daemon. The embedded SPA (internal/webui/dist) is
# committed, so no Node is needed. Logs: ~/Library/Logs/mcp-midi-controller*.log
set -euo pipefail

# launchd starts agents with PATH=/usr/bin:/bin:/usr/sbin:/sbin; Go lives in
# /usr/local/bin (Intel, Homebrew or the Go installer) or /opt/homebrew/bin.
command -v go >/dev/null 2>&1 || export PATH="/usr/local/go/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

LABEL="com.teemow.mcp-midi-controller"
UPDATE_LABEL="${LABEL}.update"
BIN="${MCP_MIDI_BIN:-/usr/local/bin/mcp-midi-controller}"
AGENTS_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/mcp-midi-controller"
DEPLOYED="$STATE_DIR/deployed-version"          # git describe of the installed build
DEPLOYED_FP="$STATE_DIR/deployed-fingerprint"   # fingerprint of the installed build
UNCHANGED="$STATE_DIR/unchanged-version"        # newest main that builds the installed daemon
FAILED="$STATE_DIR/failed-version"              # git describe of the last failed build
WAITING="$STATE_DIR/waiting-since"              # epoch seconds: the update waits for a busy daemon since
MAX_WAIT_MIN="${MCP_MIDI_UPDATE_MAX_WAIT_MIN:-60}"
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/mcp-midi-controller/config.yaml"
DOMAIN="gui/$(id -u)"

log() { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
die() { log "deploy-mac: $*" >&2; exit 1; }

[ "$(uname -s)" = Darwin ] || die "macOS only (use scripts/deploy.sh on Linux)"
command -v go >/dev/null 2>&1 || die "go not found on $PATH"
mkdir -p "$STATE_DIR" "$LOG_DIR" "$AGENTS_DIR"

# One run at a time: the update agent can fire while a manual deploy builds.
LOCK="$STATE_DIR/deploy.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  # A lock older than an hour is left over from a killed run.
  if [ -n "$(find "$LOCK" -maxdepth 0 -mmin +60 2>/dev/null)" ]; then
    rmdir "$LOCK" 2>/dev/null || true
    mkdir "$LOCK" 2>/dev/null || exit 0
  else
    exit 0
  fi
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT

# render <template> -> stdout, filling in this machine's paths.
render() {
  sed -e "s|@BIN@|$BIN|g" -e "s|@REPO@|$REPO_ROOT|g" -e "s|@HOME@|$HOME|g" "$1"
}

# install_agent <label>: write the rendered plist and (re)load it when it
# changed or is not loaded. Returns 0 when the agent was (re)loaded.
install_agent() {
  local label=$1 dst="$AGENTS_DIR/$1.plist" tmp
  tmp=$(mktemp)
  render "init/$label.plist" >"$tmp"
  if cmp -s "$tmp" "$dst" && launchctl print "$DOMAIN/$label" >/dev/null 2>&1; then
    rm -f "$tmp"
    return 1
  fi
  launchctl bootout "$DOMAIN/$label" >/dev/null 2>&1 || true
  install -m 0644 "$tmp" "$dst"
  rm -f "$tmp"
  launchctl bootstrap "$DOMAIN" "$dst"
  log "loaded $label"
  return 0
}

# build <out> <version> [go build flag...]: build the daemon of this checkout.
build() {
  local out=$1 version=$2
  shift 2
  go build -trimpath "$@" -ldflags "-s -w -X main.version=$version" -o "$out" ./cmd/mcp-midi-controller
}

# build_failed <version>: remember the failed build and stop.
build_failed() {
  echo "$1" >"$FAILED"
  die "build of $1 failed; the running daemon is unchanged (retried when main moves)"
}

# sha256 <file>: the file's SHA-256 (sha256sum on macOS 15 and Linux, shasum
# before).
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi | cut -d' ' -f1
}

# The web UI's copy of docs/ (make web bundles it into the embedded SPA). Only
# the UI's docs tab reads it, so a docs-only merge is no reason to restart:
# the fingerprint leaves it out, and the tab shows the docs of the running build.
EMBEDDED_DOCS="internal/webui/dist/docs"

# docs_overlay <file>: write a go build -overlay that hides EMBEDDED_DOCS.
docs_overlay() {
  {
    printf '{"Replace":{'
    if [ -d "$EMBEDDED_DOCS" ]; then
      find "$REPO_ROOT/$EMBEDDED_DOCS" -type f | sed 's/[\\"]/\\&/g; s/.*/"&":""/' | paste -sd, -
    fi
    printf '}}\n'
  } >"$1"
}

# fingerprint: what the daemon of this checkout is, the same for two commits
# that build the same daemon: the hash of its binary built without a version,
# without a VCS stamp (the parts that change with every commit) and without the
# web UI's copy of the docs.
fingerprint() {
  local tmp="$STATE_DIR/.fingerprint.$$" overlay="$STATE_DIR/.fingerprint.$$.json" sum
  docs_overlay "$overlay"
  if ! build "$tmp" fingerprint -buildvcs=false -overlay="$overlay"; then
    rm -f "$tmp" "$overlay"
    return 1
  fi
  sum=$(sha256 "$tmp")
  rm -f "$tmp" "$overlay"
  echo "$sum"
}

# daemon_addr: the daemon's loopback address, listen_addr of its config
# (127.0.0.1:7799 when unset).
daemon_addr() {
  local addr=""
  if [ -r "$CONFIG" ]; then
    addr=$(sed -n -E "s/^listen_addr:[[:space:]]*[\"']?([^\"' #]+).*/\1/p" "$CONFIG" | tail -n 1)
  fi
  echo "${addr:-127.0.0.1:7799}"
}

# busy: ask the daemon what a restart would cut (GET /busy). When it is busy,
# print what it is in the middle of, one line each, and return 0. Return 1 when
# it is idle, not running or gives no answer (a daemon from before /busy).
busy() {
  local answer
  answer=$(curl -sf --max-time 5 "http://$(daemon_addr)/busy" 2>/dev/null) || return 1
  [ "$(printf '%s\n' "$answer" | head -n 1)" = busy ] || return 1
  printf '%s\n' "$answer" | sed 1d
}

# log_busy <activities>: log what the daemon is in the middle of, a line each.
log_busy() {
  printf '%s\n' "$1" | while IFS= read -r line; do log "  busy: $line"; done
}

# wait_for_busy <version>: return 0 when the update to <version> waits for a
# busy daemon, having logged what for; 1 when it goes ahead: the daemon is not
# busy, or the update has waited MAX_WAIT_MIN minutes for it already (then
# WAITED_OUT is set, and the update waits no more in this run).
WAITED_OUT=""
wait_for_busy() {
  local version=$1 activities since waited
  [ -z "$WAITED_OUT" ] || return 1
  if ! activities=$(busy); then
    rm -f "$WAITING"
    return 1
  fi
  since=$(cat "$WAITING" 2>/dev/null || true)
  case $since in
    "" | *[!0-9]*)
      since=$(date +%s)
      echo "$since" >"$WAITING"
      ;;
  esac
  waited=$((($(date +%s) - since) / 60))
  if [ "$waited" -ge "$MAX_WAIT_MIN" ]; then
    log "update to $version goes ahead although the daemon is busy: it has waited $waited min, the most it waits"
    log_busy "$activities"
    rm -f "$WAITING"
    WAITED_OUT=1
    return 1
  fi
  log "update to $version waits: the daemon is busy (waited $waited of at most $MAX_WAIT_MIN min; asks again on the next run)"
  log_busy "$activities"
  return 0
}

# build_and_restart <version> <fingerprint> [force]: build into a temp file
# next to the binary, swap it in atomically, restart the daemon: reloading its
# agent when its definition changed, else a kickstart. Unless forced, a daemon
# that became busy during the build holds the swap until the next run.
build_and_restart() {
  local version=$1 fp=$2 force=${3:-} tmp
  tmp="$(dirname "$BIN")/.mcp-midi-controller.new.$$"
  log "building $version"
  if ! build "$tmp" "$version"; then
    rm -f "$tmp"
    build_failed "$version"
  fi
  if [ -z "$force" ] && wait_for_busy "$version"; then
    rm -f "$tmp"
    return 0
  fi
  mv -f "$tmp" "$BIN"
  echo "$version" >"$DEPLOYED"
  echo "$fp" >"$DEPLOYED_FP"
  rm -f "$FAILED" "$UNCHANGED"
  install_agent "$LABEL" || launchctl kickstart -k "$DOMAIN/$LABEL"
  log "deployed $version ($BIN), restarted $LABEL"
  note_update_agent
}

# agent_current <label>: whether the installed agent is the one init/ renders.
agent_current() {
  render "init/$1.plist" | cmp -s - "$AGENTS_DIR/$1.plist"
}

# note_update_agent: the update agent cannot reload itself (bootout would end
# this very run), so a change to its own definition waits for a full deploy.
note_update_agent() {
  agent_current "$UPDATE_LABEL" ||
    log "init/$UPDATE_LABEL.plist changed: run scripts/deploy-mac.sh once to reload the update agent"
}

update() {
  local force=${1:-} branch target version fp activities
  branch=$(git symbolic-ref --quiet --short HEAD || echo DETACHED)
  [ "$branch" = main ] || die "clone is on $branch, not main; not updating"
  [ -z "$(git status --porcelain --untracked-files=no)" ] || die "clone has local changes; not updating"

  git fetch --quiet --tags origin main || die "git fetch failed"
  target=$(git rev-parse origin/main)
  version=$(git describe --tags --always "$target")
  if [ -z "$force" ]; then
    if [ -x "$BIN" ]; then
      [ "$version" = "$(cat "$DEPLOYED" 2>/dev/null)" ] && return 0
      [ "$version" = "$(cat "$UNCHANGED" 2>/dev/null)" ] && return 0
    fi
    [ "$version" = "$(cat "$FAILED" 2>/dev/null)" ] && return 0
  fi
  if [ "$(git rev-parse HEAD)" != "$target" ]; then
    git merge --quiet --ff-only "$target" || die "main has diverged from origin/main; not updating"
    log "fast-forwarded to $(git log --oneline -1)"
  fi
  if [ -n "$force" ]; then
    # Run by hand: deploy now, but say what the restart cuts.
    if activities=$(busy); then
      log "restarting although the daemon is busy:"
      log_busy "$activities"
    fi
    fp=$(fingerprint) || build_failed "$version"
    build_and_restart "$version" "$fp" force
    return 0
  fi
  # Nothing is built while the daemon is busy: a build takes the host's CPU
  # from the song it plays.
  wait_for_busy "$version" && return 0
  fp=$(fingerprint) || build_failed "$version"
  # The same daemon under the same agent definition: nothing to restart for.
  if [ -x "$BIN" ] && [ "$fp" = "$(cat "$DEPLOYED_FP" 2>/dev/null)" ] && agent_current "$LABEL"; then
    echo "$version" >"$UNCHANGED"
    log "$version builds the same daemon as the running $(cat "$DEPLOYED" 2>/dev/null): not restarting"
    note_update_agent
    return 0
  fi
  build_and_restart "$version" "$fp"
}

case "${1:-}" in
  --update)
    update
    ;;
  "")
    install_agent "$LABEL" || true
    install_agent "$UPDATE_LABEL" || true
    update force
    sleep 2
    launchctl print "$DOMAIN/$LABEL" | grep -E '^[[:space:]]+(state|pid) =' || true
    log "daemon log: $LOG_DIR/mcp-midi-controller.log; updates: $LOG_DIR/mcp-midi-controller-update.log"
    ;;
  *)
    sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
    ;;
esac
