#!/usr/bin/env bash
set -euo pipefail

readonly WTERM_VERSION="0.3.2"
readonly FONTS_VERSION="5.3.0"
readonly WTERM_DOM_SHA256="7f72a0697cdada18a94ce79ba95d9a90b995d0f50e01f7d34fb70be63015f568"
readonly WTERM_CORE_SHA256="7805eeb951b9b10aceec0a2a05350f1790af84b79710cb8d6d5f83dfd765de18"
readonly DOTGOTHIC16_SHA256="5977ee64b27320f4cc22017b956c5271f2152d307e294771970d6b19cdf6b3dd"
readonly IBM_PLEX_MONO_SHA256="60d3c0cfa549cb06fcb8867ee83f994d78e212abfac3bef3275436acb5c4d029"

readonly PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly VENDOR_ROOT="${PROJECT_ROOT}/internal/zotui/web/assets/vendor"
readonly WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

download() {
	local url="$1"
	local output="$2"
	local expected="$3"
	local actual

	curl --fail --location --silent --show-error "$url" --output "$output"
	if command -v sha256sum >/dev/null 2>&1; then
		actual="$(sha256sum "$output" | awk '{print $1}')"
	else
		actual="$(shasum -a 256 "$output" | awk '{print $1}')"
	fi
	if [[ "$actual" != "$expected" ]]; then
		echo "checksum mismatch for $url" >&2
		echo "expected $expected" >&2
		echo "actual   $actual" >&2
		return 1
	fi
}

download \
	"https://registry.npmjs.org/@wterm/dom/-/dom-${WTERM_VERSION}.tgz" \
	"${WORK_DIR}/wterm-dom.tgz" \
	"${WTERM_DOM_SHA256}"
download \
	"https://registry.npmjs.org/@wterm/core/-/core-${WTERM_VERSION}.tgz" \
	"${WORK_DIR}/wterm-core.tgz" \
	"${WTERM_CORE_SHA256}"
download \
	"https://registry.npmjs.org/@fontsource/dotgothic16/-/dotgothic16-${FONTS_VERSION}.tgz" \
	"${WORK_DIR}/dotgothic16.tgz" \
	"${DOTGOTHIC16_SHA256}"
download \
	"https://registry.npmjs.org/@fontsource/ibm-plex-mono/-/ibm-plex-mono-${FONTS_VERSION}.tgz" \
	"${WORK_DIR}/ibm-plex-mono.tgz" \
	"${IBM_PLEX_MONO_SHA256}"

for package in wterm-dom wterm-core dotgothic16 ibm-plex-mono; do
	mkdir -p "${WORK_DIR}/${package}"
	tar -xzf "${WORK_DIR}/${package}.tgz" -C "${WORK_DIR}/${package}"
done

mkdir -p \
	"${VENDOR_ROOT}/wterm/dom" \
	"${VENDOR_ROOT}/wterm/core" \
	"${VENDOR_ROOT}/fonts" \
	"${VENDOR_ROOT}/licenses"

for module in index wterm renderer input debug; do
	install -m 0644 \
		"${WORK_DIR}/wterm-dom/package/dist/${module}.js" \
		"${VENDOR_ROOT}/wterm/dom/${module}.js"
done
for module in index terminal-core transport wasm-bridge wasm-inline; do
	install -m 0644 \
		"${WORK_DIR}/wterm-core/package/dist/${module}.js" \
		"${VENDOR_ROOT}/wterm/core/${module}.js"
done
install -m 0644 \
	"${WORK_DIR}/wterm-dom/package/src/terminal.css" \
	"${VENDOR_ROOT}/wterm/terminal.css"

install -m 0644 \
	"${WORK_DIR}/dotgothic16/package/files/dotgothic16-latin-400-normal.woff2" \
	"${VENDOR_ROOT}/fonts/dotgothic16-latin-400-normal.woff2"
for font in \
	ibm-plex-mono-latin-300-normal \
	ibm-plex-mono-latin-400-normal \
	ibm-plex-mono-latin-400-italic \
	ibm-plex-mono-latin-500-normal \
	ibm-plex-mono-latin-600-normal; do
	install -m 0644 \
		"${WORK_DIR}/ibm-plex-mono/package/files/${font}.woff2" \
		"${VENDOR_ROOT}/fonts/${font}.woff2"
done

install -m 0644 "${WORK_DIR}/wterm-dom/package/LICENSE" "${VENDOR_ROOT}/licenses/wterm-dom-APACHE-2.0.txt"
install -m 0644 "${WORK_DIR}/wterm-core/package/LICENSE" "${VENDOR_ROOT}/licenses/wterm-core-APACHE-2.0.txt"
install -m 0644 "${WORK_DIR}/dotgothic16/package/LICENSE" "${VENDOR_ROOT}/licenses/dotgothic16-OFL-1.1.txt"
install -m 0644 "${WORK_DIR}/ibm-plex-mono/package/LICENSE" "${VENDOR_ROOT}/licenses/ibm-plex-mono-OFL-1.1.txt"

echo "refreshed zotui assets: wterm ${WTERM_VERSION}, Fontsource ${FONTS_VERSION}"
