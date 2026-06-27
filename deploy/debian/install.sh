#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${HOME}/.valinor/config.toml"
BIN_DIR="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
VALINOR_CONFIG_DIR="${HOME}/.config/valinor"
VALINOR_SHARE_DIR="${HOME}/.local/share/valinor"

die() {
	echo "ERROR: $*" >&2
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

echo "Valinor Debian install"
echo "======================"

require_cmd curl
require_cmd jq
require_cmd podman
require_cmd systemctl
require_cmd loginctl

if [[ ! -f "${CONFIG_FILE}" ]]; then
	die "config not found at ${CONFIG_FILE}; run 'valinor init' or copy config from another machine first"
fi

mkdir -p "${BIN_DIR}" "${SYSTEMD_USER_DIR}" "${VALINOR_CONFIG_DIR}" "${VALINOR_SHARE_DIR}" "${HOME}/.valinor/logs"

install -m 0755 "${SCRIPT_DIR}/update.sh" "${VALINOR_SHARE_DIR}/update.sh"
ln -sf "${VALINOR_SHARE_DIR}/update.sh" "${BIN_DIR}/valinor-update"

install -m 0644 "${SCRIPT_DIR}/valinor.service" "${SYSTEMD_USER_DIR}/valinor.service"
install -m 0644 "${SCRIPT_DIR}/valinor-update.service" "${SYSTEMD_USER_DIR}/valinor-update.service"
install -m 0644 "${SCRIPT_DIR}/valinor-update.timer" "${SYSTEMD_USER_DIR}/valinor-update.timer"

if [[ ! -f "${VALINOR_CONFIG_DIR}/valinor.env" && -f "${SCRIPT_DIR}/valinor.env.example" ]]; then
	install -m 0600 "${SCRIPT_DIR}/valinor.env.example" "${VALINOR_CONFIG_DIR}/valinor.env.example"
fi

echo "Fetching latest release binary..."
"${VALINOR_SHARE_DIR}/update.sh"

systemctl --user daemon-reload
systemctl --user enable --now valinor.service
systemctl --user enable --now valinor-update.timer

if ! loginctl show-user "${USER}" -p Linger --value | grep -q yes; then
	loginctl enable-linger "${USER}"
	echo "Enabled systemd linger for ${USER} (service runs when logged out)"
fi

echo ""
echo "Install complete."
echo ""
echo "Useful commands:"
echo "  systemctl --user status valinor"
echo "  journalctl --user -u valinor -f"
echo "  systemctl --user status valinor-update.timer"
echo "  valinor-update"
echo "  valinor -version"