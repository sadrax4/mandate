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
- A **ledger** lists every order with PayPal order and capture IDs, exportable to CSV.

### How we built it
- **Go** backend: policy engine, agent tool loop on the Claude Messages API (official Go SDK, refusal fallback, prompt caching), PayPal Orders v2 client (OAuth2, create with items and breakdown, approve redirect, capture, reconcile, cancel, idempotency via `PayPal-Request-Id`), verified PayPal webhooks, JSON persistence, server-sent events.
- **React + TypeScript + AG Grid** UI: dashboard with live budget bars, mission page with activity stream, proposal grid and approval, ledger grid with CSV export.
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

### What we learned
- Spending controls belong outside the model. Prompts steer; policy engines enforce.
- PayPal's Orders v2 flow maps naturally onto "agent proposes, human approves": the approve link *is* the human gate.

### What's next for Mandate
- Vaulted payment methods so sub-threshold purchases complete without a checkout hop, still inside the mandate.
- Recurring mandates (a monthly budget the agent can keep spending against) and shared mandates for teams.
- Multi-currency mandates, Pay Later at checkout, and the PayPal Agent Toolkit for invoices and disputes.

## Built with
go, react, typescript, ag-grid, paypal-orders-api, claude, anthropic-sdk, channel3, vite, docker, render, playwright

## Try it out links
- GitHub repo: https://github.com/sadrax4/mandate
- Live demo: http://23.27.20.150:8080 (PayPal sandbox, Claude Opus 5.5, Channel3 catalog; sandbox buyer login is in the README)

## AI tools disclosure (Devpost asks for this)
Claude Code (Anthropic) was used as a pair programmer for the implementation, directed and reviewed by the author. Inside the product, the shopping agent runs on Claude through the Anthropic API.
