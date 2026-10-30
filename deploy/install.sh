#!/usr/bin/env bash
# Installs or updates Mandate on a Linux server (amd64) as a systemd service,
# without Docker and without compiling anything. Re-run it to update.
#
#   curl -fsSL https://raw.githubusercontent.com/sadrax4/mandate/main/deploy/install.sh | bash
#
# Configuration lives in /opt/mandate/.env (created from .env.example on the
# first run; edit it and run `systemctl restart mandate`). The service runs as
# the unprivileged user "mandate"; the .env file and the data directory are
# readable by that user only.
set -euo pipefail
REPO="${MANDATE_REPO:-sadrax4/mandate}"
DIR="${MANDATE_DIR:-/opt/mandate}"
SVC_USER="${MANDATE_USER:-mandate}"
URL="https://github.com/${REPO}/releases/latest/download/mandate-linux-amd64.tar.gz"

if ! id "$SVC_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$DIR" --shell /usr/sbin/nologin "$SVC_USER"
fi
mkdir -p "$DIR/data"
DIR=$(readlink -f "$DIR")
cd "$DIR"
echo "Downloading $URL"
curl -fsSL -o mandate.tar.gz "$URL"
# The release publishes a checksum next to the archive: a download that cannot
# be verified is not installed (set MANDATE_SKIP_CHECKSUM=1 to override).
if [ "${MANDATE_SKIP_CHECKSUM:-0}" != "1" ]; then
  curl -fsSL -o mandate.tar.gz.sha256 "$URL.sha256" || { echo "Could not download the checksum ($URL.sha256); refusing to install an unverified build." >&2; exit 1; }
  command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is missing; install coreutils or set MANDATE_SKIP_CHECKSUM=1." >&2; exit 1; }
  echo "$(awk '{print $1}' mandate.tar.gz.sha256)  mandate.tar.gz" | sha256sum -c - >/dev/null || { echo "Checksum mismatch: the download does not match the release." >&2; exit 1; }
  echo "Checksum verified"
  rm -f mandate.tar.gz.sha256
fi
tar xzf mandate.tar.gz
rm -f mandate.tar.gz
chmod +x mandate

if [ ! -f .env ]; then
  cp .env.example .env
  echo
  echo "Created $DIR/.env from the example. Put your keys in it (nano $DIR/.env), then: systemctl restart mandate"
  echo "Without keys the app runs in offline demo mode."
  sed -i 's/^PAYPAL_MODE=.*/PAYPAL_MODE=mock/; s/^LLM_PROVIDER=.*/LLM_PROVIDER=fake/; s/^CATALOG=.*/CATALOG=mock/' .env
fi
if ! grep -q '^PUBLIC_URL=' .env || grep -q '^PUBLIC_URL=http://localhost' .env; then
  IP=$(curl -fsSL https://api.ipify.org || hostname -I | awk '{print $1}')
  sed -i '/^PUBLIC_URL=/d' .env
  echo "PUBLIC_URL=http://${IP}:8080" >> .env
  echo "Set PUBLIC_URL=http://${IP}:8080 (put a domain with HTTPS in front of it for PayPal webhooks)"
fi
chown -R "$SVC_USER:$SVC_USER" "$DIR"
chmod 700 "$DIR/data"
chmod 600 "$DIR/.env"

cat > /etc/systemd/system/mandate.service <<UNIT
[Unit]
Description=Mandate - AI shopping agent with spending mandates
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=$DIR
# .env is read by the binary itself (it tolerates comments and quotes)
ExecStart=$DIR/mandate
Restart=always
RestartSec=3
User=$SVC_USER
Group=$SVC_USER
NoNewPrivileges=yes
ProtectSystem=strict
$( case "$DIR" in /home/*|/root/*) ;; *) echo "ProtectHome=yes";; esac )
PrivateTmp=yes
ReadWritePaths=$DIR/data
AmbientCapabilities=CAP_NET_BIND_SERVICE

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
