#!/usr/bin/env bash
# deploy-mac.sh — run the daemon as a launchd user agent on macOS and keep it
# on the latest main. The macOS counterpart of deploy.sh (systemd).
#
#   scripts/deploy-mac.sh           install / refresh both launchd agents, then
#                                   build, install and restart the daemon now
#   scripts/deploy-mac.sh --update  what the update agent runs every 5 minutes:
#                                   fetch origin/main; when it moved (or got a
#                                   new release tag), fast-forward this clone,
#                                   build, install and restart. Otherwise silent.
#
# Two agents in ~/Library/LaunchAgents, rendered from init/:
#   com.teemow.mcp-midi-controller         the daemon (KeepAlive on failure)
#   com.teemow.mcp-midi-controller.update  this script with --update
#
# The clone this script lives in is the source: keep it on main and clean. The
# update refuses to touch a clone with local changes or another branch, and it
# skips a commit whose build failed until main moves again, so a broken build
# never replaces the running daemon. The embedded SPA (internal/webui/dist) is
# committed, so no Node is needed. Logs: ~/Library/Logs/mcp-midi-controller*.log
set -euo pipefail

# launchd starts agents with PATH=/usr/bin:/bin:/usr/sbin:/sbin; Go lives in
# /usr/local/bin (Intel, Homebrew or the Go installer) or /opt/homebrew/bin.
export PATH="/usr/local/go/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

LABEL="com.teemow.mcp-midi-controller"
UPDATE_LABEL="${LABEL}.update"
BIN="${MCP_MIDI_BIN:-/usr/local/bin/mcp-midi-controller}"
AGENTS_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/mcp-midi-controller"
DEPLOYED="$STATE_DIR/deployed-version"  # git describe of the installed build
FAILED="$STATE_DIR/failed-version"      # git describe of the last failed build
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

# build_and_restart <version>: build into a temp file next to the binary, swap
# it in atomically, restart the daemon agent.
build_and_restart() {
  local version=$1 tmp
  tmp="$(dirname "$BIN")/.mcp-midi-controller.new.$$"
  log "building $version"
  if ! go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$tmp" ./cmd/mcp-midi-controller; then
    rm -f "$tmp"
    echo "$version" >"$FAILED"
    die "build of $version failed; the running daemon is unchanged (retried when main moves)"
  fi
  mv -f "$tmp" "$BIN"
  echo "$version" >"$DEPLOYED"
  rm -f "$FAILED"
  launchctl kickstart -k "$DOMAIN/$LABEL"
  log "deployed $version ($BIN), restarted $LABEL"
}

update() {
  local force=${1:-} branch target version
  branch=$(git symbolic-ref --quiet --short HEAD || echo DETACHED)
  [ "$branch" = main ] || die "clone is on $branch, not main; not updating"
  [ -z "$(git status --porcelain --untracked-files=no)" ] || die "clone has local changes; not updating"

  git fetch --quiet --tags origin main || die "git fetch failed"
  target=$(git rev-parse origin/main)
  version=$(git describe --tags --always "$target")
  if [ -z "$force" ]; then
    [ "$version" = "$(cat "$DEPLOYED" 2>/dev/null)" ] && [ -x "$BIN" ] && return 0
    [ "$version" = "$(cat "$FAILED" 2>/dev/null)" ] && return 0
  fi
  if [ "$(git rev-parse HEAD)" != "$target" ]; then
    git merge --quiet --ff-only "$target" || die "main has diverged from origin/main; not updating"
    log "fast-forwarded to $(git log --oneline -1)"
  fi
  build_and_restart "$version"
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
    sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
    ;;
esac
