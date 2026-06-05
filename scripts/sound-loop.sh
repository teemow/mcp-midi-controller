#!/usr/bin/env bash
# sound-loop.sh — the LIVE TEST LOOP and acceptance gate for the sound-engineer
# iteration features (Phase 1: richer server-side analysis + the one-call
# probe_sound loop). See docs/research/sound-engineer-test-loop.md.
#
# This is NOT a unit test. The synthetic Go tests in internal/audiotap are the
# fast inner loop for DSP correctness; THIS is the outer loop that proves a real
# sound engineer can actually work with a feature, against the running daemon +
# iPad/AUM/synth over the LAN. A feature is "done" only when its live-loop
# acceptance passes here.
#
# It mirrors the manual run the plan is built on: deploy -> confirm the brain
# (hands) and tap (ears) are connected -> play a known note -> read the analysis
# back -> assert ground truth (A4 = MIDI 69 = 440 Hz -> note A4 within a few
# cents). Because some links are physical (iPad on the LAN, a synth loaded and
# routed, the firewall allowing :7800), preflight prints a clear DIAGNOSIS when a
# precondition fails so you never mistake an environment problem for a feature
# regression.
#
# Usage:
#   scripts/sound-loop.sh preflight              # check service + brain + tap; diagnose
#   scripts/sound-loop.sh tap                    # dump the current get_audio_tap snapshot
#   scripts/sound-loop.sh probe [note] [vel] [ms] [ch]
#                                                # play a note, then dump the snapshot
#   scripts/sound-loop.sh probe-sound [note] [cc] [low] [high] [ms] [ch]
#                                                # ONE probe_sound call: set a CC + play + analyze;
#                                                # then compare CC low vs high (brightness)
#   scripts/sound-loop.sh compare [note] [cc] [low] [high] [ms] [ch]
#                                                # A/B: capture_audio_snapshot + compare_audio;
#                                                # assert louder vel -> +dBFS, brighter CC -> +centroid
#   scripts/sound-loop.sh assert-a4             # play A4 and assert the ground truth
#   scripts/sound-loop.sh run                    # preflight + assert-a4 (the gate)
#   scripts/sound-loop.sh deploy                 # make deploy (build + restart daemon)
#
# Environment overrides:
#   MCP_ADDR          loopback MCP endpoint (default 127.0.0.1:7799)
#   CENTS_TOL         pitch tolerance in cents for A4 (default 15)
#   PROBE_VELOCITY    note-on velocity for probes (default 100)
#   PROBE_DURATION_MS how long to hold a probe note (default 800)
#   PROBE_CHANNEL     MIDI channel 1-16 for probes (default 1)
#   SILENCE_RMS       window-RMS floor that counts as "signal present" (default 0.005)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MCP_ADDR="${MCP_ADDR:-127.0.0.1:7799}"
MCP_URL="http://${MCP_ADDR}/"
UNIT="mcp-midi-controller.service"
LAN_PORT="${LAN_PORT:-7800}"

# Ground-truth anchor: A4 is MIDI note 69 and 440 Hz.
A4_NOTE=69
A4_HZ=440
CENTS_TOL="${CENTS_TOL:-15}"
PROBE_VELOCITY="${PROBE_VELOCITY:-100}"
PROBE_DURATION_MS="${PROBE_DURATION_MS:-800}"
PROBE_CHANNEL="${PROBE_CHANNEL:-1}"
SILENCE_RMS="${SILENCE_RMS:-0.005}"

c_reset=$'\033[0m'; c_cyan=$'\033[36m'; c_green=$'\033[32m'; c_yellow=$'\033[33m'; c_red=$'\033[31m'
log()  { printf '%s==>%s %s\n' "$c_cyan" "$c_reset" "$*"; }
ok()   { printf '%s  ok%s %s\n' "$c_green" "$c_reset" "$*"; }
warn() { printf '%swarn%s %s\n' "$c_yellow" "$c_reset" "$*" >&2; }
fail() { printf '%sfail%s %s\n' "$c_red" "$c_reset" "$*" >&2; }
die()  { fail "$*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

have curl || die "curl is required"
have jq   || die "jq is required (for asserting structuredContent)"

# mcp_call <tool> <json-args> — full streamable-HTTP dance (initialize -> session
# id -> initialized -> tools/call). Prints the raw JSON-RPC response payload so
# callers can jq into .result.structuredContent / .result.content. Mirrors the
# dance in scripts/validate.sh.
mcp_call() {
  local tool="$1" args="$2"
  local hdrs body sid json
  hdrs="$(mktemp)"
  trap 'rm -f "$hdrs"' RETURN

  curl -fsS -D "$hdrs" -o /dev/null -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sound-loop.sh","version":"0"}}}' \
    || die "MCP initialize failed (is the daemon running on $MCP_ADDR? try: scripts/sound-loop.sh deploy)"
  sid="$(grep -i '^Mcp-Session-Id:' "$hdrs" | tr -d '\r' | awk '{print $2}')"
  [ -n "$sid" ] || die "no Mcp-Session-Id returned by the daemon on $MCP_ADDR"

  curl -fsS -o /dev/null -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" \
    --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' || true

  body="$(curl -fsS -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $sid" \
    --data "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}")" \
    || die "tools/call $tool failed"

  json="$(printf '%s\n' "$body" | sed -n 's/^data: //p')"
  [ -n "$json" ] || json="$body"
  printf '%s\n' "$json"
}

# mcp_text <tool> <args> — the human-readable text content of a tool result.
mcp_text() { mcp_call "$1" "$2" | jq -r '.result.content[]?.text // .error.message // empty'; }
# tap_struct — the get_audio_tap Snapshot as compact JSON (structuredContent).
tap_struct() { mcp_call get_audio_tap '{}' | jq -c '.result.structuredContent // {}'; }

# ---- preconditions -------------------------------------------------------

service_active() { systemctl --user is-active --quiet "$UNIT"; }

# brain_connected — there is no MCP read for the brain, so use the journal: the
# last midi-control connect/disconnect event wins. Returns 0 when connected.
brain_connected() {
  local last
  last="$(journalctl --user -u "$UNIT" --no-pager -o cat 2>/dev/null \
    | grep -E 'midi-control (connected|disconnected)' | tail -n1 || true)"
  [[ "$last" == *"midi-control connected"* ]]
}

preflight() {
  local rc=0

  log "1/4 systemd user service ($UNIT)"
  if service_active; then
    ok "service active"
  else
    fail "service not active — this is an ENV issue, not a feature failure"
    warn "  fix: scripts/sound-loop.sh deploy   (or: make deploy)"
    return 1
  fi

  log "2/4 MCP endpoint ($MCP_URL)"
  local snap
  if snap="$(tap_struct 2>/dev/null)"; then
    ok "MCP endpoint reachable"
  else
    fail "MCP endpoint unreachable on $MCP_ADDR"
    warn "  fix: check listen_addr in config.yaml and that the daemon is up (make status)"
    return 1
  fi

  log "3/4 audio tap (the ears: ProbeAudioTap streaming)"
  if [ "$(printf '%s' "$snap" | jq -r '.connected // false')" = "true" ]; then
    local rate src
    rate="$(printf '%s' "$snap" | jq -r '.sample_rate // 0')"
    src="$(printf '%s' "$snap" | jq -r '.source // "tap"')"
    ok "tap connected ($src @ ${rate} Hz, $(printf '%s' "$snap" | jq -r '.window_samples // 0') samples in window)"
  else
    fail "no audio tap streaming — ENV issue"
    warn "  fix: load the agent-loop session in AUM (author_loop_session -> push & open),"
    warn "       insert ProbeAudioTap on the synth channel with streaming enabled,"
    warn "       ensure the iPad is on the LAN and the daemon host allows :$LAN_PORT (firewall)."
    rc=1
  fi

  log "4/4 midi brain (the hands: ProbeMidiBrain on /midi-control)"
  if brain_connected; then
    ok "brain connected (per journal)"
  else
    fail "no brain connected — ENV issue"
    warn "  fix: the agent-loop session embeds ProbeMidiBrain; load it in AUM so the"
    warn "       brain dials in to ws://<host>:$LAN_PORT/midi-control. Check: make logs"
    rc=1
  fi

  if [ "$rc" -eq 0 ]; then
    ok "preflight passed — the loop is live"
  else
    fail "preflight failed — resolve the ENV issues above before asserting features"
  fi
  return "$rc"
}

# ---- driving + reading ---------------------------------------------------

# play_note <note> <velocity> <duration_ms> <channel> — excite the synth via the
# brain. Fails hard if the brain reports it is not connected.
play_note() {
  local note="$1" vel="$2" ms="$3" ch="$4" out
  out="$(mcp_text play_notes "{\"note\":$note,\"velocity\":$vel,\"duration_ms\":$ms,\"channel\":$ch}")"
  if [[ "$out" == *"no ProbeMidiBrain connected"* || "$out" == *"failed"* ]]; then
    die "play_notes failed: $out (brain not connected — ENV issue)"
  fi
  printf '%s\n' "$out"
}

cmd_tap() {
  log "get_audio_tap (text):"
  mcp_text get_audio_tap '{}'
  log "get_audio_tap (structuredContent):"
  tap_struct | jq '.'
}

cmd_probe() {
  local note="${1:-$A4_NOTE}" vel="${2:-$PROBE_VELOCITY}" ms="${3:-$PROBE_DURATION_MS}" ch="${4:-$PROBE_CHANNEL}"
  log "probe: play note $note vel $vel for ${ms}ms on ch$ch, then read the tap"
  play_note "$note" "$vel" "$ms" "$ch" | sed 's/^/  /'
  cmd_tap
}

# probe_sound_call <settings-json> <note> <ms> <ch> — one probe_sound call (set a
# CC + play + analyze in a single round trip); prints the raw JSON-RPC response.
probe_sound_call() {
  local settings="$1" note="$2" ms="$3" ch="$4"
  mcp_call probe_sound \
    "{\"settings\":$settings,\"note\":$note,\"velocity\":$PROBE_VELOCITY,\"duration_ms\":$ms,\"channel\":$ch}"
}

# probe_sound_snap <settings-json> <note> <ms> <ch> — the analysis snapshot
# (structuredContent.snapshot) of one probe_sound call as compact JSON.
probe_sound_snap() {
  probe_sound_call "$@" | jq -c '.result.structuredContent.snapshot // {}'
}

# cmd_probe_sound — the probe-sound-tool acceptance: prove the compound tool does
# set-CC + play + analyze in ONE call, and that moving a brightness CC is
# reflected in the returned analysis (spectral centroid). Args:
#   [note] [cc] [low] [high] [ms] [ch]
# cc defaults to 74 (the MIDI "brightness"/filter-cutoff CC); low/high are the
# two CC values compared. The synth's CC 74 must be mapped to a tone/brightness
# control for the centroid delta to move — otherwise only the single-call
# analysis (pitch) is asserted.
cmd_probe_sound() {
  local note="${1:-$A4_NOTE}" cc="${2:-74}" low="${3:-0}" high="${4:-127}"
  local ms="${5:-$PROBE_DURATION_MS}" ch="${6:-$PROBE_CHANNEL}"

  log "probe_sound: ONE call sets CC$cc=$high + plays note $note for ${ms}ms on ch$ch + returns analysis"
  local resp hi lo f0 cent_hi cent_lo wav
  resp="$(probe_sound_call "[{\"cc\":$cc,\"value\":$high}]" "$note" "$ms" "$ch")"
  hi="$(printf '%s' "$resp" | jq -c '.result.structuredContent.snapshot // {}')"
  wav="$(printf '%s' "$resp" | jq -r '.result.structuredContent.wav_path // empty')"
  f0="$(printf '%s' "$hi" | jq -r '.analysis.f0_hz // empty')"
  cent_hi="$(printf '%s' "$hi" | jq -r '.spectral.centroid_hz // .spectral.centroidHz // empty')"

  if [ -z "$f0" ] || ! awk -v f="$f0" 'BEGIN{exit !(f>0)}'; then
    fail "probe_sound returned no analysis.f0_hz — the single call did not capture a tone"
    warn "  (is the tap connected and the synth audible? run: scripts/sound-loop.sh preflight)"
    printf '%s\n' "$hi" | jq '.analysis // {}' >&2 || true
    return 1
  fi
  local midi cents name
  read -r midi cents name < <(pitch_of "$f0")
  ok "single round trip OK: set CC$cc + played + analyzed -> f0=${f0} Hz ($name), centroid=${cent_hi:-?} Hz"

  # The probe now writes the captured (isolated) segment to disk and returns its
  # path; the daemon and harness share a host, so the file must exist locally.
  if [ -n "$wav" ] && [ -f "$wav" ]; then
    ok "segment wav written + readable: $wav"
  else
    fail "probe_sound did not return a wav_path that exists (got '${wav:-none}')"
    return 1
  fi

  # Probes are serialized daemon-side and each analyses its own isolated segment
  # (settle -> mark -> note-on -> hold -> mark -> note-off -> extract), so the
  # two captures no longer contaminate each other — no manual window-clear gap.
  log "brightness reflected? compare CC$cc=$low vs CC$cc=$high (same note, isolated segments)"
  lo="$(probe_sound_snap "[{\"cc\":$cc,\"value\":$low}]" "$note" "$ms" "$ch")"
  cent_lo="$(printf '%s' "$lo" | jq -r '.spectral.centroid_hz // .spectral.centroidHz // empty')"
  log "  spectral centroid: CC$low -> ${cent_lo:-?} Hz, CC$high -> ${cent_hi:-?} Hz"
  if [ -n "$cent_lo" ] && [ -n "$cent_hi" ] && awk -v a="$cent_lo" -v b="$cent_hi" 'BEGIN{exit !(b>a)}'; then
    ok "brightness PASS: higher CC$cc raised the spectral centroid (brighter), reflected in one call each"
  else
    warn "centroid did not rise (CC$cc may not map to brightness on this synth, or delta is small)"
    warn "  the single-round-trip acceptance still PASSED; the brightness delta is synth-dependent"
  fi
  return 0
}

# ---- A/B comparison (capture_audio_snapshot + compare_audio) -------------

# send_cc <controller> <value> <channel> — push a CC over the brain channel.
send_cc() {
  local out
  out="$(mcp_text send_midi "{\"kind\":\"cc\",\"controller\":$1,\"value\":$2,\"channel\":$3}")"
  if [[ "$out" == *"failed"* ]]; then
    die "send_midi cc failed: $out (brain not connected — ENV issue)"
  fi
}

# capture_snap <label> — capture the current tap analysis under a label.
capture_snap() { mcp_text capture_audio_snapshot "{\"label\":\"$1\"}" >/dev/null; }

# compare_delta <a> <b> — print the compare_audio delta map as compact JSON.
compare_delta() { mcp_call compare_audio "{\"a\":\"$1\",\"b\":\"$2\"}" | jq -c '.result.structuredContent.delta // {}'; }

# cmd_compare — the ab-compare acceptance: prove capture_audio_snapshot +
# compare_audio report signed deltas with the correct sign. The headline is the
# synth-independent LOUDNESS check (play the same note at a low vs high velocity
# -> compare must report +dBFS). Brightness (a CC up -> +centroid) is a soft
# check because it depends on the synth mapping CC74 to a tone/cutoff control.
# Args: [note] [cc] [low] [high] [ms] [ch].
cmd_compare() {
  local note="${1:-$A4_NOTE}" cc="${2:-74}" low="${3:-0}" high="${4:-127}"
  local ms="${5:-$PROBE_DURATION_MS}" ch="${6:-$PROBE_CHANNEL}"
  local vlo=40 vhi=120
  local rc=0

  log "ab-compare loudness: same note at velocity $vlo vs $vhi (capture + compare)"
  play_note "$note" "$vlo" "$ms" "$ch" >/dev/null
  capture_snap "quiet"
  play_note "$note" "$vhi" "$ms" "$ch" >/dev/null
  capture_snap "loud"
  local drms
  drms="$(compare_delta quiet loud | jq -r '.rms_dbfs_delta // empty')"
  log "  compare quiet -> loud: rms_dbfs_delta=${drms:-?} dB"
  if [ -n "$drms" ] && awk -v x="$drms" 'BEGIN{exit !(x>0)}'; then
    ok "loudness PASS: louder velocity -> +dBFS (correct sign)"
  else
    fail "loudness FAIL: expected +dBFS for a louder note, got ${drms:-none}"
    rc=1
  fi

  log "ab-compare brightness: CC$cc=$low vs =$high on the same note (capture + compare)"
  send_cc "$cc" "$low" "$ch"; play_note "$note" "$PROBE_VELOCITY" "$ms" "$ch" >/dev/null; capture_snap "dark"
  send_cc "$cc" "$high" "$ch"; play_note "$note" "$PROBE_VELOCITY" "$ms" "$ch" >/dev/null; capture_snap "bright"
  local dcent
  dcent="$(compare_delta dark bright | jq -r '.centroid_hz_delta // empty')"
  log "  compare dark -> bright: centroid_hz_delta=${dcent:-?} Hz"
  if [ -n "$dcent" ] && awk -v x="$dcent" 'BEGIN{exit !(x>0)}'; then
    ok "brightness PASS: higher CC$cc -> +centroid (brighter)"
  else
    warn "brightness centroid did not rise (CC$cc may not map to brightness on this synth, or delta is small)"
    warn "  the loudness ab-compare result above is the synth-independent gate"
  fi
  return "$rc"
}

# ---- the acceptance gate: A4 ground truth --------------------------------

# pitch_of <f0_hz> — prints "<midi> <cents-from-nearest> <name>" (e.g. "69 -3.2 A4").
pitch_of() {
  awk -v f="$1" 'BEGIN{
    if (f <= 0) { print "0 0 -"; exit }
    m = 69 + 12*log(f/440)/log(2);
    nm = int(m + (m>=0 ? 0.5 : -0.5));
    cents = 1200*log(f/(440*exp((nm-69)*log(2)/12)))/log(2);
    split("C C# D D# E F F# G G# A A# B", arr, " ");
    idx = nm % 12; if (idx < 0) idx += 12;
    oct = int((nm)/12) - 1;
    printf "%d %.1f %s%d\n", nm, cents, arr[idx+1], oct;
  }'
}

assert_a4() {
  log "acceptance: play A4 (MIDI $A4_NOTE = $A4_HZ Hz) and assert the analysis"

  local before after f0 base_rms post_rms
  before="$(tap_struct)"
  base_rms="$(printf '%s' "$before" | jq -r '.window_rms // 0')"

  play_note "$A4_NOTE" "$PROBE_VELOCITY" "$PROBE_DURATION_MS" "$PROBE_CHANNEL" | sed 's/^/  /'
  after="$(tap_struct)"
  post_rms="$(printf '%s' "$after" | jq -r '.window_rms // 0')"

  # Preferred assertion: a trusted fundamental from the server-side analysis
  # block. The harness only needs analysis.f0_hz; it derives note + cents itself,
  # so it becomes strict the moment analysis-core surfaces f0 (regardless of how
  # the note/cents fields are finally named).
  f0="$(printf '%s' "$after" | jq -r '.analysis.f0_hz // .analysis.f0 // empty')"

  if [ -n "$f0" ] && awk -v f="$f0" 'BEGIN{exit !(f>0)}'; then
    local midi cents name
    read -r midi cents name < <(pitch_of "$f0")
    log "  f0=${f0} Hz -> nearest note $name (MIDI $midi), ${cents} cents off"
    local abscents pass=1
    abscents="$(awk -v c="$cents" 'BEGIN{printf "%.1f", (c<0?-c:c)}')"
    if [ "$midi" != "$A4_NOTE" ]; then
      fail "nearest note is MIDI $midi, want $A4_NOTE (A4)"; pass=0
    fi
    if ! awk -v c="$abscents" -v t="$CENTS_TOL" 'BEGIN{exit !(c<=t)}'; then
      fail "pitch off by $abscents cents, tolerance ±$CENTS_TOL"; pass=0
    fi
    if [ "$pass" -eq 1 ]; then
      ok "A4 ground truth PASS: $name @ ${f0} Hz, ${cents} cents (±$CENTS_TOL)"
      return 0
    fi
    fail "A4 ground truth FAIL"
    return 1
  fi

  # Fallback (before analysis-core/analysis-surface land): there is no f0 yet, so
  # assert the weaker-but-real ground truth that the played note produced signal.
  warn "analysis.f0_hz not present yet (pending analysis-core / analysis-surface)"
  warn "  falling back to level check: did A4 produce audible signal?"
  log "  window_rms: before=$base_rms after=$post_rms (silence floor $SILENCE_RMS)"
  if awk -v a="$post_rms" -v f="$SILENCE_RMS" 'BEGIN{exit !(a>f)}'; then
    ok "level PASS: note produced signal above the silence floor"
    warn "  NOTE: this is the WEAK gate. Re-run once f0 is surfaced to assert pitch."
    return 0
  fi
  fail "level FAIL: window_rms after the note ($post_rms) <= silence floor ($SILENCE_RMS)"
  warn "  is the synth loaded/routed and audible? check the AUM session + matrix"
  return 1
}

cmd_run() {
  preflight || die "preflight failed (see diagnosis above)"
  echo
  assert_a4
}

usage() { sed -n '2,41p' "$0"; exit "${1:-1}"; }

cmd="${1:-}"; shift || true
case "$cmd" in
  preflight) preflight ;;
  tap)       cmd_tap ;;
  probe)     cmd_probe "$@" ;;
  probe-sound) cmd_probe_sound "$@" ;;
  compare)   cmd_compare "$@" ;;
  assert-a4) assert_a4 ;;
  run)       cmd_run ;;
  deploy)    exec make -C "$REPO_ROOT" deploy ;;
  ""|-h|--help|help) usage 0 ;;
  *) die "unknown command $cmd (try --help)" ;;
esac
