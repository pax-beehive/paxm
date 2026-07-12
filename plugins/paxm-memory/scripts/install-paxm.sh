#!/bin/sh
set -eu

repo="${PAXM_REPO:-pax-beehive/paxm}"
url="https://github.com/${repo}/releases/latest/download/install.sh"

printf '%s\n' "This installs the paxm release binary from ${repo}."
if [ -n "${PAXM_VERSION:-}" ]; then
  printf '%s\n' "Requested paxm version: ${PAXM_VERSION}"
else
  printf '%s\n' "paxm version: latest GitHub release"
fi
printf '%s\n' "Provider credentials and Codex hooks are configured separately by paxm setup."
curl -fsSL "$url" | bash
