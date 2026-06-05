#!/usr/bin/env bash
# validate.sh — hardware-validation harness for the live rig.
#
# This is the repeatable runbook tooling behind docs/private/validation.md: it
# drives a control through the LIVE MCP daemon (the same BLE/OSC write path an
# agent uses) and gives you the out-of-band oracle reads to prove the write
# actually landed. It is NOT a unit test — it needs the real rig powered and the
# daemon running. Per-device results (rig-specific) go in docs/private/validation.md.
#
# Two halves:
#   1. MCP drive (control / verify / read / raw / recall) — talks to the daemon
#      over its loopback streamable-HTTP endpoint (default 127.0.0.1:7799).
#   2. Oracle read (usb ...) — reads the device's TRUE state out-of-band via the
#      read-only cmd/usb-probe spike, kept independent of the BLE write path.
#
# Oracles per device (best -> weakest), see docs/private/validation.md:
#   USB readback : sl-2 (RQ1 SYSTEM), ml10x (Morningstar TLV), eq2 (Neuro HID)
#   OSC echo     : x32 (/xremote, already reverse-mapped) -> use `read`/`verify`
#   BLE echo     : verify_control for devices with CC-OUT/PC-OUT enabled
#   audible/app  : md-200, h90, opus (no decoded USB readback)
#
# Usage:
#   scripts/validate.sh control <device> <control> <value-json>
#   scripts/validate.sh verify  <device> <control> <value-json> [timeout_ms]
#   scripts/validate.sh read    [device]
#   scripts/validate.sh recall  <scene> [additive|exact]
#   scripts/validate.sh raw     <tool> <json-args>
#   scripts/validate.sh usb     <sl-2|ml10x|eq2|h90|opus> [extra usb-probe args...]
#
# Examples:
#   scripts/validate.sh control sl2 expression 100      # drive SL-2 EXP (CC16) high
#   scripts/validate.sh usb sl-2                         # read SYSTEM_TEMPO back over USB
#   scripts/validate.sh verify x32 ch01_fader 0.75       # OSC /xremote echo
#   scripts/validate.sh control h90 program 5            # then check the H90 display
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MCP_ADDR="${MCP_ADDR:-127.0.0.1:7799}"
MCP_URL="http://${MCP_ADDR}/"

die() { echo "validate: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

have curl || die "curl is required for the MCP drive half"

# mcp_call <tool-name> <json-args> — performs the full streamable-HTTP dance:
# initialize (capturing the session id), initialized, then tools/call. Prints the
# data payload(s) of the final response (pretty via jq if present).
mcp_call() {
  local tool="$1" args="$2"
  local hdrs body sid

  hdrs="$(mktemp)"
  trap 'rm -f "$hdrs"' RETURN

  # 1. initialize — the session id comes back in the Mcp-Session-Id header.
  curl -fsS -D "$hdrs" -o /dev/null -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"validate.sh","version":"0"}}}' \
    || die "initialize failed (is the daemon running on $MCP_ADDR?)"
  sid="$(grep -i '^Mcp-Session-Id:' "$hdrs" | tr -d '\r' | awk '{print $2}')"
  [ -n "$sid" ] || die "no Mcp-Session-Id returned by the daemon"

  # 2. initialized notification.
  curl -fsS -o /dev/null -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" \
    --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' || true

  # 3. tools/call.
  body="$(curl -fsS -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" \
    --data "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}")" \
    || die "tools/call $tool failed"

  # The response is SSE (event: message / data: {...}) or plain JSON; extract the
  # JSON-RPC payload and surface the human text content.
  local json
  json="$(printf '%s\n' "$body" | sed -n 's/^data: //p')"
  [ -n "$json" ] || json="$body"
  if have jq; then
    printf '%s\n' "$json" | jq -r '.result.content[]?.text // (.error.message) // .' 2>/dev/null || printf '%s\n' "$json"
  else
    printf '%s\n' "$json"
  fi
}

usage() { sed -n '2,40p' "$0"; exit "${1:-1}"; }

cmd="${1:-}"; shift || true
case "$cmd" in
  control)
    [ $# -eq 3 ] || die "usage: control <device> <control> <value-json>"
    mcp_call "control_$1" "{\"settings\":[{\"control\":\"$2\",\"value\":$3}]}"
    ;;
  verify)
    [ $# -ge 3 ] || die "usage: verify <device> <control> <value-json> [timeout_ms]"
    local_to="${4:-}"
    if [ -n "$local_to" ]; then
      mcp_call verify_control "{\"device\":\"$1\",\"control\":\"$2\",\"value\":$3,\"timeout_ms\":$local_to}"
    else
      mcp_call verify_control "{\"device\":\"$1\",\"control\":\"$2\",\"value\":$3}"
    fi
    ;;
  read)
    if [ $# -ge 1 ]; then
      mcp_call read_state "{\"device\":\"$1\"}"
    else
      mcp_call read_state "{}"
    fi
    ;;
  recall)
    [ $# -ge 1 ] || die "usage: recall <scene> [additive|exact]"
    mcp_call recall_scene "{\"name\":\"$1\",\"mode\":\"${2:-additive}\"}"
    ;;
  raw)
    [ $# -eq 2 ] || die "usage: raw <tool> <json-args>"
    mcp_call "$1" "$2"
    ;;
  usb)
    [ $# -ge 1 ] || die "usage: usb <sl-2|ml10x|eq2|h90|opus> [extra args...]"
    dev="$1"; shift
    # Map device -> the usb-probe track + ALSA port substring (rig-specific port
    # names are auto-matched by usb-probe; HID tracks auto-detect by VID:PID).
    exec go run "$REPO_ROOT/cmd/usb-probe" --device "$dev" "$@"
    ;;
  ""|-h|--help|help) usage 0 ;;
  *) die "unknown command $cmd (try --help)" ;;
esac
