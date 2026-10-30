# Deploying Mandate on your own server

## Smallest servers: prebuilt binary + systemd (no Docker, no compiling)

Every push to `main` publishes a Linux build as the `latest` GitHub release. On the server:

```bash
curl -fsSL https://raw.githubusercontent.com/sadrax4/mandate/main/deploy/install.sh | bash
nano /opt/mandate/.env            # keys, CATALOG=channel3, PUBLIC_URL=http://SERVER_IP:8080
systemctl restart mandate
journalctl -u mandate -f          # logs
```

Re-run the same `curl ... | bash` line to update. The service uses about 60 MB of RAM.

## Docker

Tested on Ubuntu 22.04/24.04 with Docker. Building the image needs about 2 GB of RAM (or swap). All commands run on the server.

```bash
# 1. Docker
curl -fsSL https://get.docker.com | sh

# 2. Code and configuration
git clone https://github.com/sadrax4/mandate && cd mandate
cp .env.example .env
nano .env        # PAYPAL_*, ANTHROPIC_API_KEY, CHANNEL3_API_KEY, CATALOG=channel3, PUBLIC_URL

# 3a. No domain: serve on port 8080 (PUBLIC_URL=http://SERVER_IP:8080)
docker compose up -d --build

# 3b. Domain pointing at this server: automatic HTTPS (PUBLIC_URL=https://your.domain)
DOMAIN=your.domain docker compose --profile https up -d --build
```

Useful commands:

```bash
docker compose logs -f mandate          # live logs
docker compose pull && git pull && docker compose up -d --build   # update
docker compose down                     # stop (data stays in the mandate-data volume)
```

Open the firewall for 8080 (no domain) or 80 and 443 (domain). PayPal redirects the buyer back to `PUBLIC_URL/paypal/return`, so it must be the address you open in the browser.

To enable PayPal webhooks, create a webhook in the Developer Dashboard for `PUBLIC_URL/paypal/webhook` with the events `CHECKOUT.ORDER.APPROVED` and `PAYMENT.CAPTURE.COMPLETED`, then put its ID in `PAYPAL_WEBHOOK_ID` and restart. Webhooks require HTTPS, so use the domain setup.
