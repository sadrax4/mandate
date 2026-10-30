# Devpost submission text

Paste into the Devpost project form. Keep the headings; judges skim.

## Project name
Mandate

## Elevator pitch (≤ 200 chars)
An AI shopping agent that researches, compares and checks out with PayPal, but only inside a budget and rules you set, with human approval before every payment.

## About the project

### Inspiration
Agentic commerce has a trust problem. Either the agent holds your wallet, or you do every checkout yourself. We wanted the middle: delegate a purchase the way you would to an assistant, with a *mandate* ("$250, nothing over $120, only computers and office gear, nothing from that merchant, rating 4.3+, ask me before paying") that is enforced in code rather than in a prompt.

### What it does
- You create a **mandate**: budget, per-item cap, auto-approve threshold, allowed categories, blocked merchants, minimum rating.
- You give the agent a **mission** in plain language.
- The agent (Claude, with four tools) searches a real product catalog, compares candidates, runs every cart through the policy engine, and **proposes**. It has no payment tool, so it cannot pay.
- You see its whole process live: searches, candidates in an AG Grid, mandate checks, the proposal with a reason per item and the alternatives it rejected.
- **Approve** creates a PayPal order for the exact cart and sends you to PayPal checkout. On return the order is captured and the budget is updated. **Reject with feedback** and the agent revises in the same conversation.
- **Save the account, pay with one click.** A mandate can ask PayPal to save your PayPal account at the first checkout (PayPal vault). After that, carts you approve are paid with one click, and carts under the auto-approve threshold are paid with no click at all, with the mandate still deciding every cent.
- A **ledger** lists every order with PayPal order and capture IDs, exportable to CSV, and an **Insights** page charts budget use per mandate, spend by category, merchant and day (AG Charts) over an AG Grid of every item bought.
- **Ask about your orders.** An order assistant answers questions like "did my last order get captured?" or "any refunds or disputes?" from the ledger and from PayPal itself, through PayPal's hosted MCP server (the PayPal Agent Toolkit). Only PayPal's read-only tools are enabled for it: it can look, never pay.

### How we built it
- **Go** backend: policy engine, agent tool loop on the Claude Messages API (official Go SDK, refusal fallback, prompt caching), PayPal Orders v2 client (OAuth2, create with items and breakdown, approve redirect, capture, reconcile, cancel, idempotency via `PayPal-Request-Id`), PayPal vault (save the account at checkout, pay later with `vault_id`, fall back to checkout when a token dies), verified PayPal webhooks, PayPal's hosted MCP server through the Anthropic MCP connector with a read-only `mcp_toolset` (the server does the MCP handshake itself at startup to verify credentials and discover tools), JSON persistence, server-sent events.
- **React + TypeScript + AG Grid + AG Charts** UI: dashboard with live budget bars, mission page with activity stream, proposal grid and approval, ledger grid with CSV export, and a spending insights page (four charts plus a grouped, filterable grid of every purchased item).
- **Channel3** product search API as the live catalog, with a built-in demo catalog for offline use.
- **Mock PayPal** simulator and a **scripted agent** make the entire flow testable without network: `go test ./...` runs the end-to-end flow through real HTTP, and a Playwright script drives the UI.
- Deployed on a small Linux server from a GitHub Actions release with a one-line install script; Docker Compose and a Render blueprint are included.

### Challenges we ran into
- Making the agent *safe by construction*: the agent has no payment tool at all; payment is a server-side consequence of a human decision, and the policy engine re-runs at approval time because budgets move.
- Money accounting that survives real-world mess: budget reservations on order creation, release on cancel, void or refund, capture-on-return with an `ORDER_ALREADY_CAPTURED` guard, serialised money transitions, and a reconciliation endpoint for buyers who approve but never come back.
- Keeping the demo honest: everything judges see in mock mode is the same code path as sandbox mode behind an interface.

### Accomplishments that we're proud of
- A complete product, not a proof of concept: create → research → propose → approve → PayPal → ledger, with tests covering the whole chain.
- It runs live against the PayPal sandbox, Claude Opus 5.5 and Channel3's real catalog; the README shows a real run with a captured sandbox order.
- The agent explains *why* a goal cannot be met inside a mandate, so the human adjusts the mandate instead of guessing.
- Rejection with feedback continues the same conversation; the agent genuinely revises.
- Two PayPal surfaces for the AI, with the money line drawn in code: the shopping agent proposes and the server pays through Orders v2 after approval; the order assistant reads PayPal through its MCP server with mutating tools disabled.
- Agentic payments without giving the agent a wallet: the saved PayPal account turns a mandate into a real spending authority (one-click approvals, no-click auto-pay under the threshold), and the policy engine still bounds every charge.

### What we learned
- Spending controls belong outside the model. Prompts steer; policy engines enforce.
- PayPal's Orders v2 flow maps naturally onto "agent proposes, human approves": the approve link *is* the human gate.

### What's next for Mandate
- Recurring mandates (a monthly budget the agent can keep spending against) and shared mandates for teams.
- Multi-currency mandates, Pay Later at checkout, and the PayPal Agent Toolkit for invoices and disputes.

## Built with
go, react, typescript, ag-grid, ag-charts, paypal-orders-api, paypal-vault, paypal-mcp, claude, anthropic-sdk, channel3, vite, docker, render, playwright

## Try it out links
- GitHub repo: https://github.com/sadrax4/mandate
- Live demo: http://23.27.20.150:8080 (PayPal sandbox, Claude Opus 5.5, Channel3 catalog; sandbox buyer login is in the README)

## AI tools disclosure (Devpost asks for this)
Claude Code (Anthropic) was used as a pair programmer for the implementation, directed and reviewed by the author. Inside the product, the shopping agent runs on Claude through the Anthropic API.
