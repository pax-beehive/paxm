#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

PAXM_REPO="${PAXM_REPO:-pax-beehive/paxm}"
PAXM_VERSION="${PAXM_VERSION:-latest}"
PAXM_INSTALL_DIR="${PAXM_INSTALL_DIR:-}"
PAXM_BINARY_NAME="${PAXM_BINARY_NAME:-paxm}"
PAXM_GITHUB_BASE_URL="${PAXM_GITHUB_BASE_URL:-https://github.com}"
PAXM_GITHUB_API_BASE_URL="${PAXM_GITHUB_API_BASE_URL:-https://api.github.com}"
PAXM_DOWNLOAD_BASE_URL="${PAXM_DOWNLOAD_BASE_URL:-}"
paxm_installer_tmpdir=""

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  bold="$(tput bold)"
  reset="$(tput sgr0)"
  red="$(tput setaf 1)"
  green="$(tput setaf 2)"
  yellow="$(tput setaf 3)"
  magenta="$(tput setaf 5)"
  cyan="$(tput setaf 6)"
else
  bold=""
  reset=""
  red=""
  green=""
  yellow=""
  magenta=""
  cyan=""
fi

log() {
  printf '%b\n' "${cyan}==>${reset} $*"
}

warn() {
  printf '%b\n' "${yellow}warning:${reset} $*" >&2
}

fail() {
  printf '%b\n' "${red}error:${reset} $*" >&2
  exit 1
}

print_banner() {
  local width=52
  banner_line() {
    local color="$1"
    local text="$2"
    printf '%b|%b %b%-*s%b %b|%b\n' \
      "${cyan}${bold}" "${reset}" "$color" $((width - 4)) "$text" "${reset}" "${cyan}${bold}" "${reset}"
  }

  printf '%b\n' "${cyan}${bold}+--------------------------------------------------+${reset}"
  banner_line "${magenta}${bold}" "    ____  ___   _  __"
  banner_line "${magenta}${bold}" "   / __ \\/   | | |/ /"
  banner_line "${magenta}${bold}" "  / /_/ / /| | |   /"
  banner_line "${yellow}${bold}" " / ____/ ___ |/   |"
  banner_line "${yellow}${bold}" "/_/   /_/  |_/_/|_|"
  banner_line "${green}${bold}" "                 installer for agent memory"
  printf '%b\n' "${cyan}${bold}+--------------------------------------------------+${reset}"
  printf '\n'
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

cleanup() {
  if [[ -n "${paxm_installer_tmpdir:-}" ]]; then
    rm -rf "$paxm_installer_tmpdir"
  fi
}

detect_platform() {
  local os arch

  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
  esac

  case "$(uname -m)" in
    arm64 | aarch64) arch="arm64" ;;
    x86_64 | amd64) arch="amd64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac

  printf '%s/%s' "$os" "$arch"
}

normalize_tag() {
  local value="$1"

  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  if [[ -z "$value" || "$value" == "latest" ]]; then
    printf '%s' "$value"
    return
  fi
  if [[ "$value" == v* ]]; then
    printf '%s' "$value"
    return
  fi
  printf 'v%s' "$value"
}

resolve_tag() {
  local version api response tag

  version="$(normalize_tag "$PAXM_VERSION")"
  if [[ -n "$version" && "$version" != "latest" ]]; then
    printf '%s' "$version"
    return
  fi

  api="${PAXM_GITHUB_API_BASE_URL%/}/repos/${PAXM_REPO}/releases/latest"
  response="$(curl -fsSL \
    -H "Accept: application/vnd.github+json" \
    -H "User-Agent: paxm-installer" \
    "$api")" || fail "failed to fetch latest release from $api"
  tag="$(printf '%s' "$response" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    awk 'NR == 1 { print; exit }')"
  [[ -n "$tag" ]] || fail "latest release response did not include tag_name"
  printf '%s' "$tag"
}

release_base_url() {
  local tag="$1"

  if [[ -n "$PAXM_DOWNLOAD_BASE_URL" ]]; then
    printf '%s' "${PAXM_DOWNLOAD_BASE_URL%/}"
    return
  fi
  printf '%s/%s/releases/download/%s' "${PAXM_GITHUB_BASE_URL%/}" "$PAXM_REPO" "$tag"
}

path_has_dir() {
  [[ ":${PATH:-}:" == *":$1:"* ]]
}

expand_path() {
  case "$1" in
    "~") printf '%s' "$HOME" ;;
    "~/"*) printf '%s/%s' "$HOME" "${1#~/}" ;;
    *) printf '%s' "$1" ;;
  esac
}

choose_install_dir() {
  local dir

  if [[ -n "$PAXM_INSTALL_DIR" ]]; then
    expand_path "$PAXM_INSTALL_DIR"
    return
  fi

  if [[ -d /usr/local/bin && -w /usr/local/bin ]]; then
    printf '%s' /usr/local/bin
    return
  fi

  IFS=':' read -r -a path_dirs <<<"${PATH:-}"
  for dir in "${path_dirs[@]}"; do
    if [[ -n "$dir" && -d "$dir" && -w "$dir" ]]; then
      printf '%s' "$dir"
      return
    fi
  done

  printf '%s' "$HOME/.local/bin"
}

download_with_progress() {
  local url="$1"
  local output="$2"

  if curl --help all 2>/dev/null | grep -q -- '--progress-bar'; then
    curl -fL --progress-bar -o "$output" "$url"
  else
    curl -fL -o "$output" "$url"
  fi
}

checksum_file() {
  local path="$1"

  if command -v shasum >/dev/null 2>&1; then
    LC_ALL=C LANG=C shasum -a 256 "$path" | awk '{print $1}'
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
    return
  fi
  fail "missing shasum or sha256sum for checksum verification"
}

install_binary() {
  local source="$1"
  local target="$2"
  local install_dir

  install_dir="$(dirname "$target")"
  if ! mkdir -p "$install_dir" 2>/dev/null; then
    if command -v sudo >/dev/null 2>&1; then
      sudo mkdir -p "$install_dir"
    else
      fail "cannot create $install_dir and sudo is unavailable"
    fi
  fi

  if cp "$source" "$target" 2>/dev/null; then
    chmod 0755 "$target" 2>/dev/null || true
    return
  fi

  if command -v sudo >/dev/null 2>&1; then
    sudo cp "$source" "$target"
    sudo chmod 0755 "$target"
    return
  fi

  fail "cannot write to $install_dir and sudo is unavailable"
}

main() {
  print_banner
  require_cmd awk
  require_cmd curl
  require_cmd dirname
  require_cmd grep
  require_cmd mktemp
  require_cmd sed
  require_cmd tar
  require_cmd uname

  local platform os arch tag asset archive_dir base_url tmpdir archive checksums expected actual install_dir target binary_path
  platform="$(detect_platform)"
  os="${platform%/*}"
  arch="${platform#*/}"
  log "Detected platform: ${bold}${platform}${reset}"

  if [[ -z "$PAXM_VERSION" || "$PAXM_VERSION" == "latest" ]]; then
    log "Resolving latest paxm release"
  fi
  tag="$(resolve_tag)"
  asset="paxm_${tag}_${os}_${arch}.tar.gz"
  archive_dir="paxm_${tag}_${os}_${arch}"
  base_url="$(release_base_url "$tag")"

  tmpdir="$(mktemp -d)"
  paxm_installer_tmpdir="$tmpdir"
  trap cleanup EXIT INT TERM

  archive="$tmpdir/$asset"
  checksums="$tmpdir/SHA256SUMS"

  log "Downloading ${bold}${asset}${reset}"
  download_with_progress "$base_url/$asset" "$archive"
  download_with_progress "$base_url/SHA256SUMS" "$checksums"

  expected="$(awk -v asset="$asset" '$2 == asset { print $1; found = 1; exit } END { if (!found) exit 1 }' "$checksums")" ||
    fail "checksum for $asset was not found"
  actual="$(checksum_file "$archive")"
  [[ "$actual" == "$expected" ]] ||
    fail "sha256 mismatch: got $actual expected $expected"

  tar -xzf "$archive" -C "$tmpdir"
  binary_path="$tmpdir/$archive_dir/$PAXM_BINARY_NAME"
  [[ -f "$binary_path" ]] || fail "paxm binary not found in $asset"
  chmod 0755 "$binary_path"

  install_dir="$(choose_install_dir)"
  target="$install_dir/$PAXM_BINARY_NAME"
  log "Installing paxm to ${bold}${target}${reset}"
  install_binary "$binary_path" "$target"

  if ! path_has_dir "$install_dir"; then
    warn "$install_dir is not currently in PATH"
    warn "add it to your shell profile, or run paxm via: $target"
  fi

  log "Installed: $("${target}" version | awk 'NR == 1 { print; exit }')"
  printf '%b\n' "${green}Done.${reset}"
}

main "$@"
