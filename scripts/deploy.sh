#!/usr/bin/env bash
# deploy.sh — build and (re)deploy the daemon as a systemd user service.
#
# This is the single, idempotent rollout path: run it for a first install OR to
# push a fresh build onto a running rig host. It encodes every step that used to
# be a README runbook so deployment is not tribal knowledge:
#
#   1. Build the signalwave SPA into internal/webui/dist (the go:embed input).
#   2. Install the daemon binary into ~/.go/bin — the exact path the systemd unit
#      references in ExecStart, so we pin GOBIN rather than trusting the caller's
#      GOPATH/GOBIN.
#   3. Install / refresh the systemd USER unit and daemon-reload.
#   4. Enable lingering so the daemon survives logout (headless rig host).
#   5. Enable on login and (re)start now to pick up the fresh binary.
#
# Re-running is always safe. No flags required.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

UNIT="mcp-midi-controller.service"
UNIT_SRC="init/${UNIT}"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
UNIT_DST="${UNIT_DIR}/${UNIT}"

# The systemd unit's ExecStart is %h/.go/bin/mcp-midi-controller, so install the
# binary there regardless of the caller's GOPATH/GOBIN. Keep these in sync.
export GOBIN="$HOME/.go/bin"

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31mdeploy: %s\033[0m\n' "$*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "go is required"
command -v npm >/dev/null 2>&1 || die "npm is required (to build the embedded SPA)"
command -v systemctl >/dev/null 2>&1 || die "systemctl is required (systemd user services)"

# 1. Build the embedded SPA. npm ci keeps the build reproducible (matches CI).
log "Building web UI (SPA → internal/webui/dist)…"
( cd web && npm ci && npm run build )

# 2. Install the daemon binary where the unit's ExecStart expects it.
log "Installing daemon binary → ${GOBIN}/mcp-midi-controller"
go install ./cmd/mcp-midi-controller

# 3. Install / refresh the systemd user unit.
log "Installing systemd user unit → ${UNIT_DST}"
mkdir -p "$UNIT_DIR"
install -m 0644 "$UNIT_SRC" "$UNIT_DST"
systemctl --user daemon-reload

# 4. Survive logout on a headless rig host (idempotent).
if command -v loginctl >/dev/null 2>&1; then
  log "Enabling linger for ${USER} (daemon survives logout)…"
  loginctl enable-linger "$USER" >/dev/null 2>&1 || true
fi

# 5. Enable on login + (re)start now to pick up the fresh binary. restart starts
#    the unit if it was not running, so this covers first-install and redeploy.
log "Enabling and (re)starting ${UNIT}…"
systemctl --user enable "$UNIT" >/dev/null
systemctl --user restart "$UNIT"

# 6. Report health and where to look next. The daemon logs its version on the
#    first journal line, so surface that instead of running the binary directly.
sleep 1
if systemctl --user --quiet is-active "$UNIT"; then
  log "Deployed and running:"
  systemctl --user --no-pager --lines=3 status "$UNIT" || true
  log "Follow logs with: journalctl --user -u ${UNIT} -f"
else
  systemctl --user --no-pager --lines=20 status "$UNIT" || true
  die "service did not come up — see status above / journalctl --user -u ${UNIT}"
fi
