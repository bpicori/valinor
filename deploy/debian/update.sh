#!/usr/bin/env bash
set -euo pipefail

REPO="bpicori/valinor"
INSTALL_DIR="${HOME}/.local/bin"
BINARY="${INSTALL_DIR}/valinor"
PREV_BINARY="${INSTALL_DIR}/valinor.prev"
LOG_DIR="${HOME}/.valinor/logs"
LOG_FILE="${LOG_DIR}/deploy.log"
ENV_FILE="${HOME}/.config/valinor/valinor.env"

log() {
	local msg="[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
	echo "${msg}"
	mkdir -p "${LOG_DIR}"
	echo "${msg}" >> "${LOG_DIR}/deploy.log"
}

die() {
	log "ERROR: $*"
	exit 1
}

if [[ -f "${ENV_FILE}" ]]; then
	# shellcheck disable=SC1090
	source "${ENV_FILE}"
fi

detect_arch() {
	case "$(uname -m)" in
	x86_64) echo "amd64" ;;
	aarch64 | arm64) echo "arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

installed_version() {
	if [[ ! -x "${BINARY}" ]]; then
		echo ""
		return 0
	fi
	"${BINARY}" -version 2>/dev/null | sed -n 's/^version=\(v[^ ]*\).*/\1/p'
}

github_api() {
	local url="https://api.github.com/repos/${REPO}/$1"
	local args=(-fsSL)
	if [[ -n "${GITHUB_TOKEN:-}" ]]; then
		args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
	fi
	curl "${args[@]}" "${url}"
}

ARCH="$(detect_arch)"
LATEST_JSON="$(github_api releases/latest)" || die "failed to fetch latest release"
LATEST_TAG="$(echo "${LATEST_JSON}" | jq -r '.tag_name')"
ASSET_NAME="valinor_${LATEST_TAG#v}_linux_${ARCH}.tar.gz"
ASSET_ID="$(echo "${LATEST_JSON}" | jq -r --arg name "${ASSET_NAME}" '.assets[] | select(.name == $name) | .id')"

if [[ -z "${LATEST_TAG}" || "${LATEST_TAG}" == "null" ]]; then
	die "could not parse latest release tag"
fi

if [[ -z "${ASSET_ID}" || "${ASSET_ID}" == "null" ]]; then
	die "release asset not found: ${ASSET_NAME}"
fi

CURRENT="$(installed_version)"
if [[ "${CURRENT}" == "${LATEST_TAG}" ]]; then
	log "already up to date (${LATEST_TAG})"
	exit 0
fi

log "updating ${CURRENT:-<none>} -> ${LATEST_TAG} (${ASSET_NAME})"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

ARCHIVE="${TMPDIR}/${ASSET_NAME}"
ASSET_API_URL="https://api.github.com/repos/${REPO}/releases/assets/${ASSET_ID}"
DOWNLOAD_ARGS=(-fsSL -o "${ARCHIVE}" -H "Accept: application/octet-stream")
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
	DOWNLOAD_ARGS+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi
if ! curl "${DOWNLOAD_ARGS[@]}" "${ASSET_API_URL}"; then
	if [[ -z "${GITHUB_TOKEN:-}" ]]; then
		die "failed to download ${ASSET_NAME}; set GITHUB_TOKEN in ${ENV_FILE} for private repositories"
	fi
	die "failed to download ${ASSET_NAME}"
fi

tar -xzf "${ARCHIVE}" -C "${TMPDIR}"
NEW_BINARY="${TMPDIR}/valinor"
[[ -f "${NEW_BINARY}" ]] || die "archive did not contain valinor binary"

chmod +x "${NEW_BINARY}"
NEW_VERSION="$("${NEW_BINARY}" -version 2>/dev/null | sed -n 's/^version=\(v[^ ]*\).*/\1/p')"
if [[ "${NEW_VERSION}" != "${LATEST_TAG}" ]]; then
	die "downloaded binary version mismatch: expected ${LATEST_TAG}, got ${NEW_VERSION:-<unknown>}"
fi

mkdir -p "${INSTALL_DIR}"
if [[ -x "${BINARY}" ]]; then
	cp -f "${BINARY}" "${PREV_BINARY}"
fi
install -m 0755 "${NEW_BINARY}" "${BINARY}.new"
mv -f "${BINARY}.new" "${BINARY}"

log "installed ${LATEST_TAG} to ${BINARY}"

if systemctl --user is-active --quiet valinor.service 2>/dev/null; then
	systemctl --user restart valinor.service
	log "restarted valinor.service"
elif systemctl --user is-enabled --quiet valinor.service 2>/dev/null; then
	systemctl --user start valinor.service
	log "started valinor.service"
else
	log "valinor.service not enabled; binary updated but service not restarted"
fi