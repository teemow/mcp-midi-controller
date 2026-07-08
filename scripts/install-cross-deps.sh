#!/usr/bin/env bash
# Installs the CGO build dependencies GoReleaser needs on a GitHub-hosted
# amd64 runner: ALSA headers for the rtmidi USB-MIDI driver plus the arm64
# cross toolchain and arm64 ALSA headers. Invoked as a goreleaser `before`
# hook; no-ops outside CI so local `goreleaser release` runs are unaffected.
set -euo pipefail

if [ -z "${CI:-}" ]; then
  echo "Not running in CI, skipping cross-compile dependency install."
  exit 0
fi

if command -v aarch64-linux-gnu-g++ >/dev/null && [ -f /usr/lib/aarch64-linux-gnu/pkgconfig/alsa.pc ]; then
  echo "Cross-compile dependencies already installed."
  exit 0
fi

sudo dpkg --add-architecture arm64
# Restrict the default repos to amd64 and add arm64 from ports.ubuntu.com.
# A plain sed on "Architectures:" is not enough: stanzas without that field
# (the security stanza on current runner images) default to every dpkg
# architecture and then 404 on arm64 indexes.
sudo python3 - <<'PYEOF'
from pathlib import Path

path = Path('/etc/apt/sources.list.d/ubuntu.sources')
fixed = []
# Deb822 stanzas are separated by blank lines. Only stanzas containing a
# Types: field are real sources; comment-only blocks must stay untouched or
# apt rejects the file as malformed.
for stanza in path.read_text().split('\n\n'):
    lines = [l for l in stanza.splitlines() if not l.startswith('Architectures:')]
    if any(l.startswith('Types:') for l in lines):
        lines.append('Architectures: amd64')
    fixed.append('\n'.join(lines))
path.write_text('\n\n'.join(fixed) + '\n')
PYEOF
CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"
sudo tee /etc/apt/sources.list.d/arm64-ports.sources >/dev/null <<EOF
Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports/
Suites: ${CODENAME} ${CODENAME}-updates ${CODENAME}-security
Components: main universe
Architectures: arm64
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
sudo apt-get update
sudo apt-get install -y gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libasound2-dev libasound2-dev:arm64
