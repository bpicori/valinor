#!/usr/bin/env bash
# Valinor installer — download binary, configure, register service.
#
#   curl -fsSL https://raw.githubusercontent.com/bpicori/valinor/main/scripts/install.sh | bash
set -euo pipefail

REPO="${VALINOR_INSTALL_REPO:-bpicori/valinor}"
BRANCH="${VALINOR_INSTALL_BRANCH:-main}"
BIN_DIR="${HOME}/.local/bin"
VALINOR="${BIN_DIR}/valinor"
CONFIG_FILE="${HOME}/.valinor/config.toml"
LOG_DIR="${HOME}/.valinor/logs"
RUN_MODE="${VALINOR_RUN_MODE:-all}"

SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
LAUNCH_AGENTS_DIR="${HOME}/Library/LaunchAgents"

die() {
	echo "ERROR: $*" >&2
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

scripts_base() {
	if [[ "${BASH_SOURCE[0]}" == */install.sh ]]; then
		cd "$(dirname "${BASH_SOURCE[0]}")" && pwd
		return
	fi
	echo "https://raw.githubusercontent.com/${REPO}/${BRANCH}/scripts"
}

fetch_script_file() {
	local rel="$1" dest="$2" base
	base="$(scripts_base)"
	if [[ "$base" != http* ]]; then
		cp "${base}/${rel}" "$dest"
		return
	fi
	curl -fsSL "${base}/${rel}" -o "$dest"
}

render_template() {
	local src="$1" dest="$2"
	sed \
		-e "s|@VALINOR_BINARY@|${VALINOR}|g" \
		-e "s|@VALINOR_MODE@|${RUN_MODE}|g" \
		-e "s|@VALINOR_STDOUT_LOG@|${LOG_DIR}/valinor.stdout.log|g" \
		-e "s|@VALINOR_STDERR_LOG@|${LOG_DIR}/valinor.stderr.log|g" \
		"$src" >"$dest"
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
	require_cmd sed

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

latest_release_asset() {
	local tag version asset_name release_json asset_url
	release_json="$(curl -fsSL -H "Accept: application/vnd.github+json" \
		"https://api.github.com/repos/${REPO}/releases/latest")"
	tag="$(printf '%s' "$release_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])')"
	version="${tag#v}"
	asset_name="valinor_${version}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
	asset_url="$(printf '%s' "$release_json" | python3 -c '
import json, sys
data = json.load(sys.stdin)
name = sys.argv[1]
for asset in data.get("assets", []):
    if asset.get("name") == name:
        print(asset["browser_download_url"])
        break
else:
    raise SystemExit(f"release asset not found: {name}")
' "$asset_name")"
	RELEASE_ASSET_NAME="$asset_name"
	RELEASE_ASSET_URL="$asset_url"
}

install_binary() {
	local archive_dir archive binary extract_dir
	latest_release_asset
	archive_dir="$(mktemp -d)"
	archive="${archive_dir}/valinor.tar.gz"
	extract_dir="${RELEASE_ASSET_NAME%.tar.gz}"
	binary="${archive_dir}/${extract_dir}/valinor"
	trap 'rm -rf "${archive_dir}"' RETURN

	echo "==> Downloading ${RELEASE_ASSET_NAME}"
	curl -fsSL "$RELEASE_ASSET_URL" -o "$archive"
	tar -xzf "$archive" -C "$archive_dir"
	[[ -f "$binary" ]] || die "archive did not contain valinor binary at ${extract_dir}/valinor"
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

enable_systemd_linger() {
	local user linger
	user="${USER:-}"
	[[ -n "$user" ]] || die "USER environment variable is empty"

	linger="$(loginctl show-user "$user" -p Linger --value 2>/dev/null || true)"
	linger="$(printf '%s' "$linger" | tr '[:upper:]' '[:lower:]')"
	if [[ "$linger" == "yes" ]]; then
		return 0
	fi

	loginctl enable-linger "$user"
	echo "==> Enabled systemd linger for ${user}"
}

install_systemd_service() {
	local tmp unit
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp}"' RETURN

	mkdir -p "$SYSTEMD_USER_DIR" "$LOG_DIR"

	for unit in valinor.service valinor-update.service valinor-update.timer; do
		fetch_script_file "systemd/${unit}" "${tmp}/${unit}"
		render_template "${tmp}/${unit}" "${SYSTEMD_USER_DIR}/${unit}"
		chmod 0644 "${SYSTEMD_USER_DIR}/${unit}"
	done

	echo "==> Enabling systemd user service"
	systemctl --user daemon-reload
	systemctl --user enable --now valinor.service
	systemctl --user enable --now valinor-update.timer
	enable_systemd_linger
}

launchd_gui_domain() {
	echo "gui/$(id -u)"
}

launchd_bootout() {
	local plist="$1" domain
	domain="$(launchd_gui_domain)"
	launchctl bootout "$domain" "$plist" 2>/dev/null || true
}

launchd_bootstrap() {
	local label="$1" plist="$2" domain
	domain="$(launchd_gui_domain)"
	launchd_bootout "$plist"
	launchctl bootstrap "$domain" "$plist"
}

install_launchd_service() {
	local tmp agent_plist update_plist
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp}"' RETURN

	mkdir -p "$LAUNCH_AGENTS_DIR" "$LOG_DIR"

	agent_plist="${LAUNCH_AGENTS_DIR}/com.valinor.agent.plist"
	update_plist="${LAUNCH_AGENTS_DIR}/com.valinor.update.plist"

	fetch_script_file "launchd/com.valinor.agent.plist" "${tmp}/com.valinor.agent.plist"
	fetch_script_file "launchd/com.valinor.update.plist" "${tmp}/com.valinor.update.plist"
	render_template "${tmp}/com.valinor.agent.plist" "$agent_plist"
	render_template "${tmp}/com.valinor.update.plist" "$update_plist"
	chmod 0644 "$agent_plist" "$update_plist"

	echo "==> Enabling launchd agents"
	launchd_bootstrap "com.valinor.agent" "$agent_plist"
	launchd_bootstrap "com.valinor.update" "$update_plist"
}

install_service() {
	echo "==> Registering service"
	case "$PLATFORM_OS" in
	linux) install_systemd_service ;;
	darwin) install_launchd_service ;;
	esac
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