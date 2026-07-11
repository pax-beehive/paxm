#!/bin/sh

# Plugin hooks are deliberately fail-open: a missing binary, incomplete setup,
# provider timeout, or untrusted configuration must not block a Codex task.
set -u

event="${1:-}"
case "$event" in
  session_start|user_input|turn_end) ;;
  *) exit 0 ;;
esac

find_paxm() {
  if [ -n "${PAXM_BINARY:-}" ] && [ -x "${PAXM_BINARY}" ]; then
    printf '%s\n' "$PAXM_BINARY"
    return 0
  fi
  if command -v paxm >/dev/null 2>&1; then
    command -v paxm
    return 0
  fi
  home="${HOME:-}"
  if [ -n "$home" ]; then
    for candidate in "$home/.local/bin/paxm" "$home/go/bin/paxm"; do
      if [ -x "$candidate" ]; then
        printf '%s\n' "$candidate"
        return 0
      fi
    done
  fi
  return 1
}

paxm_bin="$(find_paxm 2>/dev/null || true)"
[ -n "$paxm_bin" ] || exit 0

tmp="$(mktemp "${TMPDIR:-/tmp}/paxm-memory-hook.XXXXXX" 2>/dev/null || true)"
[ -n "$tmp" ] || exit 0
trap 'rm -f "$tmp"' 0 1 2 3 15

PAXM_INTEGRATION_OWNER=codex-plugin \
  "$paxm_bin" __hook --target codex --event "$event" --json >"$tmp" 2>/dev/null
status=$?
[ "$status" -eq 0 ] || exit 0
cat "$tmp"
