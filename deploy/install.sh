#!/usr/bin/env bash
# Installs or updates Mandate on a Linux server (amd64) as a systemd service,
# without Docker and without compiling anything. Re-run it to update.
#
#   curl -fsSL https://raw.githubusercontent.com/sadrax4/mandate/main/deploy/install.sh | bash
#
# Configuration lives in /opt/mandate/.env (created from .env.example on the
# first run; edit it and run `systemctl restart mandate`).
set -euo pipefail
REPO="${MANDATE_REPO:-sadrax4/mandate}"
DIR="${MANDATE_DIR:-/opt/mandate}"
URL="https://github.com/${REPO}/releases/latest/download/mandate-linux-amd64.tar.gz"

mkdir -p "$DIR/data"
cd "$DIR"
echo "Downloading $URL"
curl -fsSL -o mandate.tar.gz "$URL"
tar xzf mandate.tar.gz
rm -f mandate.tar.gz
chmod +x mandate

if [ ! -f .env ]; then
  cp .env.example .env
  echo "Created $DIR/.env from the example. Edit it, then: systemctl restart mandate"
fi
if ! grep -q '^PUBLIC_URL=' .env || grep -q '^PUBLIC_URL=http://localhost' .env; then
  IP=$(curl -fsSL https://api.ipify.org || hostname -I | awk '{print $1}')
  sed -i '/^PUBLIC_URL=/d' .env
  echo "PUBLIC_URL=http://${IP}:8080" >> .env
  echo "Set PUBLIC_URL=http://${IP}:8080"
fi

cat > /etc/systemd/system/mandate.service <<UNIT
[Unit]
Description=Mandate - AI shopping agent with spending mandates
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
ExecStart=$DIR/mandate
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now mandate
systemctl restart mandate
sleep 2
systemctl --no-pager --lines=5 status mandate || true
echo
echo "Mandate is running: $(grep '^PUBLIC_URL=' .env | cut -d= -f2-)"
