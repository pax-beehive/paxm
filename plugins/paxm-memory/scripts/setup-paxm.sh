#!/bin/sh
set -eu

paxm_bin="${PAXM_BINARY:-paxm}"
if ! command -v "$paxm_bin" >/dev/null 2>&1 && [ -x "${HOME:-}/.local/bin/paxm" ]; then
  paxm_bin="${HOME:-}/.local/bin/paxm"
fi
if ! command -v "$paxm_bin" >/dev/null 2>&1 && [ ! -x "$paxm_bin" ]; then
  printf '%s\n' "paxm is not installed. Run the plugin installer first." >&2
  exit 1
fi

exec "$paxm_bin" setup --integration codex-plugin "$@"
