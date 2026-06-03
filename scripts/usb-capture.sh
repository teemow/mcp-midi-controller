#!/usr/bin/env bash
# usb-capture.sh — shared USB readback-research capture tooling.
#
# One entry point for the four capture channels used to reverse-engineer how
# each USB-connected pedal exposes real device state (see docs/research/usb.md):
#
#   descriptors  lsusb -v + sysfs dump per rig device (the gold reference)
#   setup        privileged prep: load usbmon, install alsa-utils (needs sudo)
#   usbmon       capture raw USB traffic on a bus to a .pcapng (Wireshark)
#   midi         snoop USB-MIDI SysEx on an ALSA rawmidi port (amidi)
#   serial       inspect the ML10X CDC-ACM serial line (/dev/ttyACM*)
#
# Read-only by design: nothing here writes to a device. Rig-specific dumps
# (serials, bus topology) are written under docs/private/ (gitignored).
set -euo pipefail

# vid:pid -> friendly device name for the pedals on this rig.
RIG_DEVICES=(
  "0483:a334:opus"   # Two Notes Opus      — HID only
  "1b12:0041:h90"    # Eventide H90        — USB-MIDI + Mass Storage + USB-Audio
  "331b:0008:ml10x"  # Morningstar ML10X   — USB-MIDI + CDC-ACM serial
  "29a4:0400:eq2"    # Source Audio EQ2    — USB-MIDI + HID
  "0582:02af:sl-2"   # Boss SL-2           — USB-MIDI only
)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${USB_CAPTURE_OUT:-$REPO_ROOT/docs/private/usb-descriptors}"

die() { echo "usb-capture: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# sysfs_path_for VID PID -> echoes the /sys/bus/usb/devices/<dev> dir, if present.
sysfs_path_for() {
  local vid="$1" pid="$2" d
  for d in /sys/bus/usb/devices/*; do
    [ -f "$d/idVendor" ] || continue
    if [ "$(cat "$d/idVendor")" = "$vid" ] && [ "$(cat "$d/idProduct")" = "$pid" ]; then
      echo "$d"; return 0
    fi
  done
  return 1
}

cmd_descriptors() {
  mkdir -p "$OUT_DIR"
  local entry vid pid name busdev sp out missing=0
  for entry in "${RIG_DEVICES[@]}"; do
    IFS=: read -r vid pid name <<<"$entry"
    sp="$(sysfs_path_for "$vid" "$pid" || true)"
    [ -n "$sp" ] && sp="$(realpath "$sp")"  # resolve symlink; nested dirs are then real
    if [ -z "$sp" ]; then
      echo "[$name] $vid:$pid NOT present — skipping"; missing=1; continue
    fi
    out="$OUT_DIR/$name.txt"
    busdev="$(lsusb -d "$vid:$pid" | sed -E 's/Bus ([0-9]+) Device ([0-9]+):.*/\1:\2/' | head -1)"
    {
      echo "# $name ($vid:$pid) — captured $(date -u +%FT%TZ)"
      echo "# sysfs: $sp   bus:dev = $busdev"
      echo
      echo "## sysfs string descriptors"
      for f in manufacturer product serial bcdDevice version speed; do
        [ -f "$sp/$f" ] && printf "%-14s %s\n" "$f:" "$(cat "$sp/$f")"
      done
      echo
      echo "## interfaces (class/subclass/proto)"
      for i in "$sp"/"$(basename "$sp")":*; do
        [ -d "$i" ] || continue
        printf "  %-10s class=%s sub=%s proto=%s ep=%s name=%q\n" \
          "$(basename "$i")" \
          "$(cat "$i/bInterfaceClass" 2>/dev/null)" \
          "$(cat "$i/bInterfaceSubClass" 2>/dev/null)" \
          "$(cat "$i/bInterfaceProtocol" 2>/dev/null)" \
          "$(cat "$i/bNumEndpoints" 2>/dev/null)" \
          "$(cat "$i/interface" 2>/dev/null)"
      done
      echo
      echo "## HID report descriptors (if any bound)"
      # $sp is realpath-resolved above, so the nested HID report_descriptor under
      # <intf>/<hidbus-id>/ is a real path find can reach without following the
      # cyclic symlinks in sysfs.
      find "$sp" -name report_descriptor 2>/dev/null | while read -r rd; do
        echo "### $rd"
        od -An -tx1 -v "$rd" 2>/dev/null || echo "  (unreadable — needs root)"
      done
      echo
      echo "## lsusb -v (run as root for string + HID report descriptors)"
      if [ "$(id -u)" -ne 0 ]; then
        echo "## NOTE: running non-root; string indices unresolved, report descriptors absent."
      fi
      lsusb -v -d "$vid:$pid" 2>&1
    } >"$out"
    echo "[$name] -> $out ($(wc -l <"$out") lines)"
  done
  echo
  echo "Tip: re-run with 'sudo $0 descriptors' to resolve string + HID report descriptors."
  return $missing
}

cmd_setup() {
  [ "$(id -u)" -eq 0 ] || die "setup needs root: sudo $0 setup"
  echo "Loading usbmon kernel module ..."
  modprobe usbmon && echo "  usbmon loaded"
  if [ -d /sys/kernel/debug/usb/usbmon ]; then
    echo "  usbmon nodes: $(ls /sys/kernel/debug/usb/usbmon)"
  else
    echo "  mounting debugfs ..."; mount -t debugfs none /sys/kernel/debug 2>/dev/null || true
  fi
  if ! have amidi; then
    echo "Installing alsa-utils (amidi/aseqdump) ..."
    if have pacman; then pacman -S --noconfirm alsa-utils
    elif have apt-get; then apt-get update && apt-get install -y alsa-utils
    else echo "  install alsa-utils manually"; fi
  fi
  echo "Adding current invoking user to groups for non-root capture (re-login needed):"
  echo "  usermod -aG wireshark,audio,uucp \$USER   # wireshark=usbmon, uucp=ttyACM"
  echo "Done. Verify: ls /sys/kernel/debug/usb/usbmon ; amidi -l"
}

cmd_usbmon() {
  local bus="${1:-3}" secs="${2:-30}"
  have dumpcap || die "dumpcap (wireshark-cli) not found"
  [ -e "/sys/kernel/debug/usb/usbmon/${bus}u" ] || \
    die "usbmon${bus} missing — run 'sudo $0 setup' first (modprobe usbmon)"
  mkdir -p "$OUT_DIR"
  local out="$OUT_DIR/usbmon-bus${bus}-$(date -u +%Y%m%dT%H%M%SZ).pcapng"
  echo "Capturing USB bus $bus for ${secs}s -> $out"
  echo "  (open the vendor editor and trigger ONE known state change now)"
  dumpcap -i "usbmon${bus}" -a "duration:${secs}" -w "$out"
  echo "Done. Inspect: wireshark '$out'   (filter: usb.transfer_type==URB_BULK)"
}

cmd_midi() {
  have amidi || die "amidi not found — run 'sudo $0 setup' (alsa-utils)"
  if [ $# -eq 0 ]; then
    echo "USB-MIDI ports (use the hw:X,Y id with: $0 midi hw:X,Y):"; amidi -l; return 0
  fi
  local port="$1"
  echo "Snooping SysEx + raw MIDI on $port (Ctrl-C to stop)."
  echo "Trigger a state change on the device or its editor; bytes are dumped hex."
  amidi -p "$port" -d
}

cmd_serial() {
  local tty="${1:-/dev/ttyACM0}"
  [ -e "$tty" ] || die "$tty not present (ML10X CDC-ACM)"
  echo "## $tty line settings (stty)"; stty -F "$tty" -a 2>&1 || true
  echo
  echo "Read-only snoop (Ctrl-C to stop). The Morningstar web editor (Chrome Web"
  echo "Serial) drives this port; to log its frames instead, use Chrome DevTools"
  echo "(see docs/research/usb.md). Raw bytes follow:"
  cat "$tty" | od -An -tx1 -v
}

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
  echo
  echo "Usage: $0 {descriptors|setup|usbmon [bus] [secs]|midi [port]|serial [tty]}"
}

main() {
  local sub="${1:-descriptors}"; shift || true
  case "$sub" in
    descriptors) cmd_descriptors "$@";;
    setup)       cmd_setup "$@";;
    usbmon)      cmd_usbmon "$@";;
    midi)        cmd_midi "$@";;
    serial)      cmd_serial "$@";;
    -h|--help|help) usage;;
    *) usage; exit 2;;
  esac
}

main "$@"
