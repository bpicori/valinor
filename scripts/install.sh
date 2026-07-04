#!/usr/bin/env bash
# Valinor installer — download binary, configure, register service.
#
#   curl -fsSL https://raw.githubusercontent.com/bpicori/valinor/main/scripts/install.sh | bash
#
# Non-interactive (set secrets before piping):
#   export VALINOR_OPENAI_API_KEY=sk-...
#   export VALINOR_TELEGRAM_BOT_TOKEN=123:ABC
#   curl -fsSL https://raw.githubusercontent.com/bpicori/valinor/main/scripts/install.sh | bash
set -euo pipefail

REPO="${VALINOR_INSTALL_REPO:-bpicori/valinor}"
BIN_DIR="${HOME}/.local/bin"
VALINOR="${BIN_DIR}/valinor"
CONFIG_FILE="${HOME}/.valinor/config.toml"

die() {
	echo "ERROR: $*" >&2
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

detect_platform() {
	local os arch
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	arch="$(uname -m)"
	case "$os" in
	linux) os="linux" ;;
	darwin) os="darwin" ;;
	*) die "unsupported OS: $(uname -s) (linux and darwin only)" ;;
	esac
	case "$arch" in
	x86_64 | amd64) arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
	PLATFORM_OS="$os"
	PLATFORM_ARCH="$arch"
}

preflight() {
	require_cmd curl
	require_cmd python3
	require_cmd tar
	require_cmd podman

	case "$PLATFORM_OS" in
	linux)
		require_cmd systemctl
		require_cmd loginctl
		;;
	darwin)
		require_cmd launchctl
		;;
	esac
}

latest_asset_url() {
	local tag version asset_name release_json
	release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"
	tag="$(printf '%s' "$release_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])')"
	version="${tag#v}"
	asset_name="valinor_${version}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
	printf '%s' "$release_json" | python3 -c '
import json, sys
data = json.load(sys.stdin)
name = sys.argv[1]
for asset in data.get("assets", []):
    if asset.get("name") == name:
        print(asset["browser_download_url"])
        break
else:
    raise SystemExit(f"release asset not found: {name}")
' "$asset_name"
}

install_binary() {
	local url tmp archive binary
	url="$(latest_asset_url)"
	tmp="$(mktemp -d)"
	archive="${tmp}/valinor.tar.gz"
	binary="${tmp}/valinor"
	trap 'rm -rf "${tmp}"' EXIT

	echo "==> Downloading ${url##*/}"
	curl -fsSL "$url" -o "$archive"
	tar -xzf "$archive" -C "$tmp" valinor
	chmod 0755 "$binary"

	mkdir -p "$BIN_DIR"
	install -m 0755 "$binary" "$VALINOR"
	echo "==> Installed ${VALINOR}"
}

path_hint() {
	case ":${PATH}:" in
	*":${BIN_DIR}:"*) ;;
	*)
		echo ""
		echo "Add ${BIN_DIR} to your PATH:"
		echo "  export PATH=\"${BIN_DIR}:\$PATH\""
		;;
	esac
}

write_config_from_env() {
	local api_key bot_token model provider
	api_key="${VALINOR_OPENAI_API_KEY:-}"
	bot_token="${VALINOR_TELEGRAM_BOT_TOKEN:-}"
	model="${VALINOR_DEFAULT_MODEL:-gpt-4o}"
	provider="${VALINOR_DEFAULT_PROVIDER:-openai}"

	[[ -n "$api_key" ]] || die "VALINOR_OPENAI_API_KEY is required to create config"
	[[ -n "$bot_token" ]] || die "VALINOR_TELEGRAM_BOT_TOKEN is required to create config"

	mkdir -p "$(dirname "$CONFIG_FILE")"
	cat >"$CONFIG_FILE" <<EOF
default_provider = "${provider}"

[providers.${provider}]
type = "openai"
api_key = "${api_key}"
default_model = "${model}"

[telegram]
bot_token = "${bot_token}"
EOF
	chmod 0600 "$CONFIG_FILE"
	echo "==> Wrote ${CONFIG_FILE}"
}

ensure_config() {
	if [[ -f "$CONFIG_FILE" ]]; then
		return 0
	fi

	if [[ -n "${VALINOR_OPENAI_API_KEY:-}" || -n "${VALINOR_TELEGRAM_BOT_TOKEN:-}" ]]; then
		write_config_from_env
		return 0
	fi

	echo "==> No config found. Starting setup wizard..."
	"$VALINOR" init
}

install_service() {
	echo "==> Registering service"
	"$VALINOR" install
}

main() {
	echo "Valinor install"
	echo "=============="

	detect_platform
	preflight
	install_binary
	path_hint
	ensure_config
	install_service

	echo ""
	echo "Done. Valinor is running as a user service with auto-updates."
	echo ""
	echo "  valinor status"
	echo "  valinor logs -f"
}

main "$@"