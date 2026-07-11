#!/bin/sh
set -eu

repo="${PAXM_REPO:-pax-beehive/memory-adaptor}"
url="https://github.com/${repo}/releases/latest/download/install.sh"
export PAXM_VERSION="${PAXM_VERSION:-v0.1.12}"

printf '%s\n' "This installs the paxm release binary from ${repo}."
printf '%s\n' "Pinned paxm version: ${PAXM_VERSION}"
printf '%s\n' "Provider credentials and Codex hooks are configured separately by paxm setup."
curl -fsSL "$url" | bash
