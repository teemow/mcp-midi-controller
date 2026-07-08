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

if command -v aarch64-linux-gnu-gcc >/dev/null && [ -f /usr/lib/aarch64-linux-gnu/pkgconfig/alsa.pc ]; then
  echo "Cross-compile dependencies already installed."
  exit 0
fi

sudo dpkg --add-architecture arm64
# Restrict the default (amd64) repos and add arm64 from ports.ubuntu.com.
sudo sed -i 's/^Architectures:.*/Architectures: amd64/' /etc/apt/sources.list.d/ubuntu.sources
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
sudo apt-get install -y gcc-aarch64-linux-gnu libasound2-dev libasound2-dev:arm64
