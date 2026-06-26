#!/bin/bash
set -euo pipefail

SETTINGS=/etc/searxng/settings.yml

if [ -f "$SETTINGS" ] && grep -q 'ultrasecretkey' "$SETTINGS"; then
	secret=$(head -c 24 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)
	sed -i "s/ultrasecretkey/${secret}/" "$SETTINGS"
fi

export SEARXNG_SETTINGS_PATH="$SETTINGS"
export GRANIAN_HOST=0.0.0.0
export GRANIAN_PORT=8080
export GRANIAN_INTERFACE=wsgi
export GRANIAN_WEBSOCKETS=false

cd /usr/local/searxng
/usr/local/searxng/.venv/bin/granian searx.webapp:app &

exec sleep infinity