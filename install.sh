#!/usr/bin/env bash
# zot installer - https://zot.im/install.sh
#
#   curl -fsSL https://zot.im/install.sh | bash
#
# Installs the latest zot release for this machine. Downloads the platform
# tarball from the openzot/openzot GitHub release, verifies it against the
# release's checksums.txt, and puts the binary in ~/.local/bin.
#
# This script is the source of truth: it ships in the zot repository and is
# published as an asset on every release. https://zot.im/install.sh proxies to
# it, so the website never carries its own copy to drift out of sync.
#
# Options (environment variables, or a version as the first argument):
#   ZOT_VERSION      release to install - "latest" (default) or a tag such as
#                    vX.Y.Z / X.Y.Z. Also: curl ... | bash -s -- vX.Y.Z
#   ZOT_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#
# Linux and macOS, amd64 and arm64. On Windows, download the archive from
# https://github.com/openzot/openzot/releases instead.
set -euo pipefail

main() {
  local repo="openzot/openzot"
  local requested="${1:-${ZOT_VERSION:-latest}}"
  local install_dir="${ZOT_INSTALL_DIR:-${HOME}/.local/bin}"

  log() { echo "zot: $*" >&2; }
  fail() { log "$*"; exit 1; }

  command -v curl >/dev/null 2>&1 || fail "curl is required"
  command -v tar >/dev/null 2>&1 || fail "tar is required"

  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW* | MSYS* | CYGWIN*)
      fail "Windows is not supported by this script - download zot from https://github.com/${repo}/releases" ;;
    *) fail "unsupported OS: $(uname -s)" ;;
  esac

  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac

  # Resolve "latest" through the release redirect - no API call, no rate limit.
  local version
  if [[ "$requested" == "latest" ]]; then
    local location
    location="$(curl -fsSI -o /dev/null -w '%{redirect_url}' "https://github.com/${repo}/releases/latest")"
    version="${location##*/}"
    [[ "$version" =~ ^v[0-9] ]] || fail "could not resolve the latest release from ${location:-<no redirect>}"
  else
    version="v${requested#v}"
  fi

  local root="zot-${version}-${os}-${arch}"
  local archive="${root}.tar.gz"
  local base="https://github.com/${repo}/releases/download/${version}"

  # not local: the EXIT trap runs after main has returned
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT

  log "downloading ${archive}"
  curl -fsSL --retry 3 -o "${work}/${archive}" "${base}/${archive}" \
    || fail "no release asset ${archive} - is ${version} a real release, and ${os}/${arch} a published platform?"
  curl -fsSL --retry 3 -o "${work}/checksums.txt" "${base}/checksums.txt" \
    || fail "could not download checksums.txt for ${version}"

  local expected actual
  expected="$(grep -E "[[:space:]]\*?${archive}\$" "${work}/checksums.txt" | awk '{print $1}' | head -n1)"
  [[ -n "$expected" ]] || fail "checksums.txt has no entry for ${archive}"

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${work}/${archive}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${work}/${archive}" | awk '{print $1}')"
  else
    fail "need sha256sum or shasum to verify the download"
  fi
  [[ "$expected" == "$actual" ]] || fail "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"

  tar -xzf "${work}/${archive}" -C "$work"
  mkdir -p "$install_dir"
  install -m 0755 "${work}/${root}/zot" "${install_dir}/zot"

  log "installed zot ${version} (${os}/${arch}) to ${install_dir}/zot"
  "${install_dir}/zot" --version >&2

  case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *)
      log ""
      log "${install_dir} is not on your PATH. Add it, e.g.:"
      log "  export PATH=\"${install_dir}:\$PATH\""
      ;;
  esac

  log ""
  log "next: export a provider key and hand zot an order -"
  log "  export ZAI_API_KEY=\"...\""
  log "  zot new \"add input validation to the signup handler and a test\""
  log "  zot"
}

main "$@"
