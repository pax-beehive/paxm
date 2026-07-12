#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin"

cat >"$tmp/bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' 'printf "installer-version=%s\\n" "${PAXM_VERSION:-latest}"'
EOF
chmod +x "$tmp/bin/curl"

latest=$(env -u PAXM_VERSION PATH="$tmp/bin:/bin:/usr/bin" sh "$root/install-paxm.sh")
case "$latest" in
  *"paxm version: latest GitHub release"*"installer-version=latest"*) ;;
  *)
    printf '%s\n' "latest install behavior failed:" "$latest" >&2
    exit 1
    ;;
esac

pinned=$(PATH="$tmp/bin:/bin:/usr/bin" PAXM_VERSION=v0.1.19 sh "$root/install-paxm.sh")
case "$pinned" in
  *"Requested paxm version: v0.1.19"*"installer-version=v0.1.19"*) ;;
  *)
    printf '%s\n' "pinned install behavior failed:" "$pinned" >&2
    exit 1
    ;;
esac
