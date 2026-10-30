# Mandate

[![CI](https://github.com/sadrax4/mandate/actions/workflows/ci.yml/badge.svg)](https://github.com/sadrax4/mandate/actions/workflows/ci.yml)
[![Release](https://github.com/sadrax4/mandate/actions/workflows/release.yml/badge.svg)](https://github.com/sadrax4/mandate/releases/tag/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-0d9488.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![PayPal sandbox](https://img.shields.io/badge/PayPal-Orders%20v2%20%C2%B7%20vault%20%C2%B7%20webhooks%20%C2%B7%20MCP-003087?logo=paypal&logoColor=white)](#paypal-integration)

**An AI shopping agent that can only spend inside the mandate you give it, and pays with PayPal.**

You give the agent a *mandate*: a budget, a per-item cap, allowed categories, blocked merchants, a minimum rating and an approval threshold. You give it a *mission*: "a mechanical keyboard with brown switches and a USB-C hub with HDMI, both under $120". The agent researches the catalog, compares candidates, checks every cart against the mandate, and proposes. Nothing moves until a human approves the proposal and completes PayPal checkout. Every order lands in an auditable ledger.

Built for the [PayPal AI Hackathon](https://paypalaihackathon.devpost.com/) (October–November 2026) with Go, Claude, PayPal Orders v2, AG Grid, Channel3 and Render.

**Live demo: http://23.27.20.150:8080** (PayPal sandbox, Claude Opus 5.5, live Channel3 catalog; no real money moves).
To test a payment, log in at PayPal checkout with the sandbox buyer account `sb-f6dqu53622575@personal.example.com` / password `Udb;8n)b`.

![Live sandbox run: real Channel3 product, Claude reasoning, PayPal order captured](docs/screenshots/live-mission-paid.png)

*A real run on the live demo: the agent searched Channel3, picked a Keychron keyboard from bestbuy.com, explained what it rejected and why, and the PayPal sandbox order was captured after human approval.*

## Who it is for

- **People who delegate.** Anyone who would hand a trusted assistant a budget and a list: "replace my broken headphones, under $150, not from that store". Mandate lets an agent do the legwork while the human keeps the decision and the wallet.
- **Households and teams with a shared budget.** One mandate per purpose (office, kitchen, kids' school supplies), each with its own cap and rules, each auditable in one ledger.
- **Small businesses buying supplies.** Policy, not trust, limits what gets bought: categories, caps, blocked vendors, a threshold above which a person signs off. Standing orders cover the recurring stuff.
- **Anyone shipping an agent that touches money.** The pattern (mandate → agent proposes → policy engine checks → human approves or a saved PayPal account pays under a threshold → ledger) is the missing control layer for agentic commerce, and the whole thing is open source.

The worst case is bounded by design: a runaway or manipulated agent can propose, but it cannot exceed the per-item cap, the budget, or the categories, and it cannot pay. On the public demo even the number of agent runs per hour is capped.

## Why

Agentic commerce only works if people can delegate purchases without handing an AI their wallet. Today the choice is binary: either the agent has your payment credentials, or you do every checkout yourself. Mandate adds the missing layer: **a spending authority the agent cannot exceed, enforced in code, plus a human approval gate before every PayPal payment.** The agent is good at research and comparison; the mandate is good at saying no.

## How it works

1. **Create a mandate.** Budget, per-item cap, auto-approve threshold, allowed categories, blocked merchants, minimum rating. The policy engine (`internal/mandate`) is the only thing that decides whether a cart is acceptable.
2. **Start a mission.** A goal in plain language. The agent (Claude via the official Go SDK, `internal/agent`) gets exactly five tools: `search_products`, `get_product`, `check_mandate`, `propose_cart` and `suggest_mandate_change`. None of them can pay.
3. **Watch it work.** Every search, check and decision streams to the UI over server-sent events. The products it considered are shown in an AG Grid, with the proposed ones highlighted.
4. **Approve or reject.** A proposal shows each item, the agent's reason, the total, the remaining budget and which mandate checks passed. Reject with feedback ("too expensive, stay under $80") and the agent revises in the same conversation.
   When a goal cannot be met inside the mandate, the agent explains why and calls `suggest_mandate_change` with the smallest single change that would unblock it (raise the per-item cap to $329, allow the kitchen category, unblock a merchant). The agent cannot change its own mandate: the suggestion appears on the mission page and one click, **Amend mandate & retry**, applies it and retries the goal as a new mission.
5. **Pay with PayPal.** Approval creates a PayPal order (Orders v2, `intent=CAPTURE`) for the exact cart, reserves the budget, and sends the buyer to PayPal checkout. On return the order is captured and the mandate's spent total is updated. Cancelling at PayPal releases the reservation.
6. **Save the account, pay with one click.** A mandate can ask PayPal to save the buyer's PayPal account at the first checkout (PayPal vault). From then on, carts you approve are paid with one click and carts under the auto-approve threshold are paid with no click at all, with the mandate still deciding every cent. The agent still has no payment tool.
7. **Audit.** The ledger lists every order with mission, mandate, amount, status, PayPal order ID, capture ID, payer and whether it went through checkout or the saved account, sortable and filterable.
8. **Standing orders.** Start a mission with *Repeat monthly* and the goal runs again on schedule ("coffee beans every 30 days"): each run is a new mission checked against the mandate like any other, so with a saved PayPal account and a cart under the threshold it completes with nobody in the loop, and it stops the moment the budget runs out. *Run now* and *Stop* are one click.
9. **See where the money went.** The Insights page charts budget use per mandate, spend by category, by merchant and per day (AG Charts), over an AG Grid of every purchased line item with column groups, floating filters, a quick filter and CSV export.
10. **Ask about your orders.** An order assistant on the ledger page answers questions ("did my last order get captured?", "what did I spend this week?", "any refunds or disputes?") from the ledger and from PayPal itself, through **PayPal's hosted MCP server** (the PayPal Agent Toolkit) connected with the Anthropic MCP connector. Only PayPal's read-only tools are enabled for it; it can look, never pay.

Two independent gates protect the money: the policy engine (re-run at approval time, because the budget may have moved) and the buyer's own PayPal consent, given at checkout for every order, or once at the first checkout when the account is saved. Carts under the auto-approve threshold skip the first gate but never the second.

## Screenshots

| | |
|---|---|
| ![Dashboard](docs/screenshots/dashboard.png) Mandates with live budget bars and one-line missions | ![Paid](docs/screenshots/mission-paid.png) Captured payment with PayPal order and capture IDs |
| ![Rejected and revised](docs/screenshots/mission-rejected-and-revised.png) Rejected with feedback, revised by the agent | ![Ledger](docs/screenshots/ledger.png) Ledger across all mandates (AG Grid) |
| ![PayPal sandbox checkout](docs/screenshots/live-paypal-checkout.png) The real PayPal sandbox checkout the buyer lands on | ![Receipt](docs/screenshots/live-payment-receipt.png) Capture ID, payer and the agent's reasoning after payment |
| ![Spending insights](docs/screenshots/insights.png) Spending insights: AG Charts over the ledger, AG Grid of every item bought | ![Suggestion](docs/screenshots/suggestion.png) A goal outside the mandate: the agent explains, suggests the smallest change, the human amends and retries |
| ![One-click payment](docs/screenshots/one-click-paid.png) Second purchase on a mandate with a saved PayPal account: approved and captured with one click, no checkout | ![Standing orders](docs/screenshots/standing-orders.png) Standing orders on the dashboard: the goal re-runs on schedule, inside the mandate, with Run now and Stop |

Dark mode is supported throughout ([screenshot](docs/screenshots/dashboard-dark.png)). The first four screenshots were taken in offline demo mode (note the `mock` badges); the last two on the live sandbox deployment.

## Judging in two minutes

1. Open the live demo, click **New mandate** (or use *Create demo mandate*), give it a budget of 300 and a per-item cap of 150, keep *Save my PayPal account* ticked.
2. Start a mission such as *"a mechanical keyboard with brown switches under $120"* and watch the agent search Channel3, check the mandate and propose.
3. **Approve & pay with PayPal**, log in with the sandbox buyer above, pay. Back in Mandate the capture ID appears and the **Ledger** shows the order. Because the mandate asked to save the account, the activity log now says *PayPal account saved on this mandate*.
4. Start a second mission on the same mandate and approve it: it is paid with one click and no checkout. A cart under the auto-approve threshold is paid before the agent even finishes.
5. Start another mission and **Reject with feedback** to see the agent revise. Try a goal outside the mandate (a kettle under a computers-only mandate): the agent explains, suggests the smallest change, and **Amend mandate & retry** applies it.
6. Open **Insights** for the charts, and on the **Ledger** ask the order assistant *"Did my most recent order get captured? Confirm it with PayPal."* to watch it read PayPal through the MCP server.

The same flow runs locally with no keys at all (offline demo mode), see below.

## Quick start

Requirements: Go 1.25+, Node 22+. Nothing else is required for the offline demo.

```bash
git clone https://github.com/sadrax4/mandate && cd mandate
cd web && npm install && npm run build && cd ..   # builds the UI into web/dist (works on Windows, macOS, Linux)
go run ./cmd/mandate                               # http://localhost:8080
```

With no keys configured the app runs in **offline demo mode**: a scripted agent, the built-in demo catalog and a simulated PayPal checkout page. Click *Create demo mandate*, pick a suggested goal, approve, "pay", and look at the ledger. The whole flow takes about a minute and needs no accounts.

### Real mode (PayPal sandbox + Claude)

```bash
cp .env.example .env
# fill in PAYPAL_CLIENT_ID / PAYPAL_CLIENT_SECRET (sandbox app from https://developer.paypal.com/dashboard/)
# and ANTHROPIC_API_KEY (https://console.anthropic.com/)
go run ./cmd/mandate
```

Approving a proposal now creates a real sandbox order. At checkout, log in with a **sandbox personal (buyer) account** from the PayPal Developer Dashboard (*Testing Tools → Sandbox Accounts*), approve, and you are redirected back; the server captures the order and the capture ID appears on the mission page and in the ledger.

To test saved accounts (one-click and auto-pay) in sandbox, enable the **Vault** feature on your REST app in the Developer Dashboard (*Apps & Credentials → your app → Features → Vault / Save payment methods*). Without it, PayPal refuses to save the account; Mandate then falls back to a regular checkout and says so in the mission log.

To test against a real product catalog, add `CHANNEL3_API_KEY` (free tier at [trychannel3.com](https://trychannel3.com)); the agent then searches live US retail products through Channel3's product search API instead of the demo catalog.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PAYPAL_MODE` | `sandbox` if a client ID is set, else `mock` | `sandbox`, `live` or `mock` (built-in checkout simulator) |
| `PAYPAL_CLIENT_ID`, `PAYPAL_CLIENT_SECRET` | | REST app credentials |
| `PAYPAL_WEBHOOK_ID` | | Optional webhook ID; enables signature verification for `/paypal/webhook` |
| `PAYPAL_MCP` | `auto` | Order assistant on PayPal's MCP server: `auto` (on when PayPal credentials and the Anthropic provider are configured), `on`, `off` |
| `PAYPAL_MCP_URL` | PayPal's SSE endpoint for the environment | Override the MCP endpoint handed to the Anthropic MCP connector (e.g. `https://mcp.sandbox.paypal.com/http`) |
| `LLM_PROVIDER` | `anthropic` if a key is set, else `fake` | `anthropic` or `fake` (scripted agent for tests and demos) |
| `ANTHROPIC_API_KEY` | | Claude API key from the Anthropic Console |
| `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` | | Optional: an Anthropic-compatible gateway and bearer token, e.g. `https://openrouter.ai/api` with an OpenRouter key and `ANTHROPIC_MODEL=anthropic/claude-opus-5.5`. Anthropic-only extras (refusal fallback, effort) are skipped automatically |
| `ANTHROPIC_MODEL` | `claude-opus-5-5` | Any current Claude model ID |
| `ANTHROPIC_EFFORT` | `high` | `low`…`max`, controls reasoning depth |
| `CATALOG` | `channel3` if a key is set, else `mock` | `mock` or `channel3` |
| `CHANNEL3_API_KEY` | | Channel3 product search key |
| `PORT` | `8080` | HTTP port |
| `PUBLIC_URL` | `http://localhost:PORT` | Base URL PayPal redirects back to. Render's `RENDER_EXTERNAL_URL` is used automatically |
| `DATA_FILE` | `data/mandate.json` | JSON persistence; empty keeps everything in memory |
| `STATIC_DIR` | `web/dist` | Built UI |
| `CURRENCY` | `USD` | Default mandate currency |
| `MAX_MISSIONS_PER_HOUR` | `40` | Server-wide cap on agent runs per hour, so a public demo cannot drain the model budget (`0` = unlimited) |
| `DEMO_BUYER_HINT` | | Text shown next to the PayPal button, e.g. the sandbox buyer login testers should use |

The server also reads a `.env` file in the working directory.

## PayPal integration

All PayPal code lives in `internal/paypal` and uses the REST API directly from Go:

- **OAuth2 client-credentials** tokens (`POST /v1/oauth2/token`), cached until they expire.
- **Create order** (`POST /v2/checkout/orders`) with `intent=CAPTURE`, a purchase unit carrying the mandate's currency, the exact cart as `items` with an `item_total` breakdown, `custom_id` set to our proposal ID, and `payment_source.paypal.experience_context` (`PAY_NOW`, no shipping, return and cancel URLs). The proposal ID doubles as the `PayPal-Request-Id` idempotency key, so a double click can never create two orders.
- **Buyer approval** through the `payer-action` link PayPal returns; the buyer logs in and approves on PayPal, not in our app.
- **Capture** (`POST /v2/checkout/orders/{id}/capture`) when PayPal sends the buyer back to `/paypal/return`. `ORDER_ALREADY_CAPTURED` is treated as success so a refreshed return page is harmless.
- **Reconciliation** (`GET /v2/checkout/orders/{id}`) behind *Check status with PayPal*: if the buyer approved but the redirect never arrived, the order is captured; if PayPal already shows it completed, the ledger is updated.
- **Webhooks** (`POST /paypal/webhook`): every delivery is verified with `POST /v1/notifications/verify-webhook-signature` before it is trusted. `CHECKOUT.ORDER.APPROVED` captures the order even if the buyer closed the tab; `PAYMENT.CAPTURE.COMPLETED` reconciles the ledger; denied/refunded/reversed captures update the payment status. Handling is idempotent, so PayPal retries never double-count a spend. Configure a webhook for `<PUBLIC_URL>/paypal/webhook` in the Developer Dashboard and set `PAYPAL_WEBHOOK_ID`.
- **Cancel** (`/paypal/cancel`) releases the budget reservation.
- **PayPal MCP server (Agent Toolkit).** The order assistant talks to `https://mcp.sandbox.paypal.com` (or `mcp.paypal.com` in live) through the Anthropic MCP connector: the Messages API calls PayPal's MCP tools on the model's behalf, authenticated with the same OAuth2 client-credentials token Mandate already mints. At startup Mandate performs the MCP handshake itself (`initialize`, `tools/list` over Streamable HTTP) to check the credentials and discover the tool catalogue, then enables only the read-only tools (`get_order`, `list_transactions`, `get_shipment_tracking`, `get_refund`, `list_disputes`, `get_invoice`, ...) through an `mcp_toolset` with everything else disabled. `create_*`, `pay_*`, `create_refund`, `accept_dispute_claim` and friends are never offered to the model, so even this assistant cannot move money. If the handshake fails the assistant simply stays off and `/api/config` says why.
- **Saved payment methods (vault).** When a mandate asks for it, the first order carries `payment_source.paypal.attributes.vault` (`store_in_vault: ON_SUCCESS`, `usage_type: MERCHANT`); the buyer consents at checkout and the capture response returns the vault token (`payment_source.paypal.attributes.vault.id`, status `VAULTED`), which is stored on the mandate. Later orders are created with `payment_source.paypal.vault_id` and captured server-side straight away: approved carts pay with one click, carts under the auto-approve threshold pay with none. A token that stops working (revoked by the buyer, vault disabled on the app) is forgotten and the flow falls back to a normal checkout, which saves a fresh one.

Budget accounting is explicit: creating an order moves the amount into *reserved*; a capture moves it to *spent*; a cancel releases it. The mandate re-evaluates the cart at approval time, so a proposal that was fine an hour ago cannot be paid if other missions have used the budget since.

`PAYPAL_MODE=mock` swaps in an in-memory simulator with the same interface and a checkout page at `/mock/paypal/approve`, which is what the automated tests and the offline demo use. The real client is tested against an `httptest` server that checks the exact request shapes.

## The agent

`internal/agent` runs a tool loop on the Claude Messages API (beta surface of the official `anthropic-sdk-go` v2):

- Model `claude-opus-5-5` by default, `output_config.effort` configurable, system prompt cached with `cache_control`, and Anthropic's **server-side refusal fallback** enabled so a safety decline is re-served instead of failing the mission.
- Five tools, each with a strict JSON schema. `search_products` and `get_product` read the catalog. `check_mandate` runs the policy engine and returns violations with product IDs so the agent can repair the cart. `propose_cart` runs the policy engine again, stores the proposal and, if the cart is under the auto-approve threshold, triggers PayPal order creation immediately (paid at once when the mandate has a saved PayPal account). `suggest_mandate_change` records the smallest change that would unblock an impossible goal; only a human can apply it.
- The system prompt tells the agent to search per item, verify requirements, prefer value over price, never propose without a passing `check_mandate`, and explain exactly what blocks a goal when the mandate cannot be met (so the human can adjust the mandate rather than guess).
- Conversations are kept per mission, so *Reject with feedback* continues the same conversation with the human's note instead of starting over.
- `LLM_PROVIDER=fake` is a deterministic scripted agent with the same tool interface. It searches, picks by relevance/rating/price, repairs budget violations by downgrading, proposes, and suggests a mandate change when it has to give up. The test suite and the offline demo run on it, so the whole product is testable without network access.
- The **order assistant** (`POST /api/ask`) is a second, separate session: the ledger as context plus PayPal's MCP server through the Anthropic MCP connector, with only read-only tools enabled.

## Mandate rules

| Rule | Violation code |
|---|---|
| Cart total must fit the remaining budget (total − spent − reserved) | `over_budget` |
| Each unit price ≤ max per item | `item_over_cap` |
| Category must be in the allowed list (if set) | `category_not_allowed` |
| Merchant must not be blocked | `merchant_blocked` |
| Rating ≥ minimum (when the product has a rating) | `rating_too_low` |
| Currency must match the mandate | `currency_mismatch` |
| Quantity ≥ 1, cart not empty | `bad_quantity`, `empty_cart` |

A cart requires human approval when the mandate says *always ask*, when no auto-approve threshold is set, or when the total is at or above the threshold.

## Architecture

![Architecture: human, web UI, Go server with policy engine, budget ledger, shopping agent, order assistant and payments; Channel3, Anthropic API and PayPal outside](docs/architecture.png)

```
 browser (React + AG Grid)  ──SSE/JSON──▶  Go HTTP server (internal/server)
                                              │          │            │
                                              ▼          ▼            ▼
                                     internal/agent  internal/mandate  internal/paypal
                                     (Claude tool    (policy engine)   (Orders v2 REST
                                      loop)                            or mock)
                                              │
                                              ▼
                                     internal/catalog (Channel3 or demo catalog)
                                              │
                                     internal/store (JSON file, SSE fan-out)
```

```
cmd/mandate/            entry point, env parsing
internal/agent/         Shopper, tools, Anthropic provider, scripted Fake provider
internal/catalog/       Product model, demo catalog, Channel3 client
internal/mandate/       Mandate, Cart, Decision, Evaluate()
internal/paypal/        REST client, Mock simulator
internal/server/        HTTP API, SSE, PayPal return/cancel, mock checkout page, static UI
internal/store/         in-memory state with JSON persistence and event subscriptions
web/                    Vite + React + TypeScript UI (AG Grid for products, proposal items and the ledger)
scripts/smoke.cjs       Playwright end-to-end smoke test that drives the UI
docs/screenshots/       what the judges see
```

## API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/config` | Which modes are active (PayPal, agent, catalog) |
| `GET/POST` | `/api/mandates` | List / create mandates |
| `GET` | `/api/mandates/{id}` | Mandate with its missions |
| `GET/POST` | `/api/missions` | List / start missions (starts the agent) |
| `GET` | `/api/missions/{id}` | Mission, mandate, proposals, payments, events |
| `GET` | `/api/missions/{id}/events` | Server-sent events stream of agent activity |
| `POST` | `/api/proposals/{id}/approve` | Create the PayPal order, returns `approve_url` |
| `POST` | `/api/proposals/{id}/reject` | `{reason}`; the agent revises |
| `POST` | `/api/payments/{id}/sync` | Reconcile an order with PayPal (capture if approved) |
| `GET` | `/api/ledger` | All orders with mission and mandate context |
| `GET` | `/api/products/search` | Direct catalog search (`q`, `max_price`, `category`) |
| `GET` | `/paypal/return`, `/paypal/cancel` | PayPal redirect targets |
| `POST` | `/paypal/webhook` | Verified PayPal webhook deliveries (approve → capture, capture → reconcile) |

## Testing

```bash
go test ./...                 # policy engine, catalog, PayPal client (httptest), agent (fake provider), HTTP end-to-end
```

The server tests run the full flow through real HTTP: create mandate → start mission → agent proposes → approve → simulated PayPal approval → return → capture → ledger and budget accounting, plus rejection/revision, auto-approval, cancellation, reconciliation, webhooks, saved-account payments (first checkout saves, later carts pay with one click or none, dead tokens fall back to checkout), the suggest-and-amend loop, the insights aggregation and the MCP order assistant (with a scripted MCP server listing and provider).

`scripts/smoke.cjs` and `scripts/smoke-vault.cjs` drive the built UI in a headless browser (Playwright) and produced the demo-mode screenshots in this README. CI runs them too: the `e2e` job builds the UI and the server, starts it in offline demo mode and walks through checkout, saved-account payments, the suggest-and-amend loop, standing orders and the insights page on every push.

## Deploy

The repo ships a multi-stage `Dockerfile` and a `render.yaml`. On [Render](https://render.com), create a Blueprint from the repo, add `PAYPAL_CLIENT_ID`, `PAYPAL_CLIENT_SECRET` and `ANTHROPIC_API_KEY` (and optionally `CHANNEL3_API_KEY`) as secret environment variables; `RENDER_EXTERNAL_URL` is used as the PayPal return URL automatically.

```bash
docker build -t mandate . && docker run --rm -p 8080:8080 --env-file .env mandate
```

On your own server, `docker compose up -d --build` runs it with a persistent data volume, and `--profile https` adds Caddy with automatic certificates for a domain. See [deploy/README.md](deploy/README.md).

## Sponsor tools used

- **PayPal**: Orders v2 (create, capture, get), saved payment methods (vault), verified webhooks, OAuth2, sandbox checkout, and the hosted PayPal MCP server (Agent Toolkit) for the read-only order assistant. Central to the product: it is the only way money moves.
- **AG Grid** (Community) and **AG Charts** (Community): the products-considered grid, the proposal item grid, the ledger and the purchased-items grid (sorting, column groups, floating filters, quick filter, custom cell renderers, pagination, CSV export, Theming API for light and dark mode), plus the Insights charts (stacked horizontal bars for budget use, bars by category and day, a donut by merchant) with tooltips and formatted axes.
- **Channel3**: live product search for AI agents as the catalog backend (`CATALOG=channel3`): `POST /v1/search` with price, availability and condition filters and locale config, `GET /v1/products/{id}` for details. Each product is normalised to its cheapest in-stock offer (merchant domain, buy URL, price), with brand, cross-merchant ratings, category path, structured attributes and key features passed through to the agent. In the UI every candidate links to the merchant's product page and the proposal carries the Channel3 product IDs through to the PayPal order items.
- **Render**: `render.yaml` and Docker image for one-click deployment.

## Security notes and limits

- Secrets only come from the environment; nothing is logged or stored.
- The agent never sees PayPal credentials and has no payment tool. Payment is a server-side consequence of a human decision.
- Orders are idempotent per proposal; budget is reserved before checkout and released on cancel.
- A saved payment token lives on the mandate and is only usable with the merchant's own API credentials; every charge made with it still goes through the policy engine, so the mandate's limits bound what a saved account can ever be charged.
- Persistence is a JSON file: intentional for a hackathon, trivial to swap for Postgres behind `internal/store`.
- The Channel3 client decodes the documented response shapes defensively; if the schema changes, `CATALOG=mock` keeps the demo working.
- Not production-ready for real money without authentication and a database.

## What's next

- Shared mandates for teams, with per-person limits inside one budget.
- Multi-currency mandates and Pay Later options at checkout.
- Let the order assistant draft (never send) PayPal invoices and dispute responses for a human to approve, using the MCP tools that are disabled today.

## Built with AI

The code was written with Claude Code as a pair programmer, directed and reviewed by the author; the Devpost submission discloses this. The agent inside the product runs on Claude through the Anthropic API.

## License

MIT, see [LICENSE](LICENSE).
