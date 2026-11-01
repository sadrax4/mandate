# Devpost submission text

Paste into the Devpost project form. Keep the headings; judges skim, and the rules say they may judge from the text, images and video alone.

## Project name
Mandate

## Elevator pitch (≤ 200 chars)
An AI shopping agent that researches, compares and checks out with PayPal, but only inside a budget and rules you set, with human approval before every payment.

## About the project

### Inspiration
Agentic commerce has a trust problem. Either the agent holds your wallet, or you do every checkout yourself. We wanted the middle: delegate a purchase the way you would to an assistant, with a *mandate* ("$250, nothing over $120, only computers and office gear, nothing from that merchant, rating 4.3+, ask me before paying") that is enforced in code rather than in a prompt. PayPal's own guidance for agent builders says to keep human oversight where judgment matters most; Mandate is that oversight, made into a product. It is also PayPal's Agent Payments Protocol in product form: the mandate is the *intent mandate* (human not present: budget, categories, timing), every cart you approve is a *cart mandate* (human present), and the PayPal order or vault payment is the payment credential tied to one proposal.

### What it does
- You create a **mandate**: budget, per-item cap, auto-approve threshold, allowed categories, blocked merchants, minimum rating.
- You give the agent a **mission** in plain language.
- The agent (Claude, with five tools, none of which can pay) searches a real product catalog through Channel3, compares candidates, runs every cart through the policy engine, and **proposes**. You see its whole process live: searches, candidates in an AG Grid, mandate checks, the proposal with a reason per item and the alternatives it rejected.
- **Approve** creates a PayPal order for the exact cart and sends you to PayPal checkout. On return the order is captured and the budget is updated. **Reject with feedback** and the agent revises in the same conversation.
- When the goal cannot be met inside the mandate, the agent explains why and **suggests the smallest change** (a higher cap, an extra category, an unblocked merchant). It cannot change its own mandate; you click one button (for example *Allow kitchen & retry*).
- **Save the account, pay with one click.** A mandate can ask PayPal to save your PayPal account at the first checkout (PayPal vault). After that, carts you approve are paid with one click, and carts under the auto-approve threshold are paid with no click at all, with the mandate still deciding every cent.
- **Agent-to-agent commerce.** Choose *Hire a service* and a buyer agent, bound by your mandate, negotiates the price with a seller agent (PayPal's own inspiration list: "two agents negotiating and settling a payment"). The buyer never reveals or exceeds its hard limit; the transcript and the price curve are on the mission page; the agreed price becomes an ordinary proposal that you approve and PayPal settles. Neither agent can pay.
- **Standing orders:** start a mission with "repeat monthly" and the agent re-runs the goal on schedule. Each run is checked against the mandate; with a saved PayPal account and a cart under the threshold it completes with nobody in the loop, and it stops when the budget runs out. Run now and Stop are one click.
- A **ledger** lists every order with PayPal order and capture IDs, exportable to CSV, and an **Insights** page charts budget use per mandate, spend by category, merchant and day (AG Charts) over an AG Grid of every item bought.
- **Ask about your orders.** An order assistant answers questions like "did my last order get captured?" or "any refunds or disputes?" from the ledger and from PayPal itself, through PayPal's hosted MCP server (the PayPal Agent Toolkit). Only PayPal's read-only tools are enabled for it: it can look, never pay.

### How we built it
- **Go** backend: policy engine, agent tool loop on the Claude Messages API (official Go SDK, prompt caching, server-side model fallback), PayPal Orders v2 client (OAuth2, create with items and breakdown, approve redirect, capture, reconcile, cancel, idempotency via `PayPal-Request-Id`), PayPal vault (save the account at checkout, pay later with `vault_id`, fall back to checkout when a token dies), verified PayPal webhooks, PayPal's hosted MCP server through the Anthropic MCP connector with a read-only `mcp_toolset` (the server does the MCP handshake itself at startup to verify credentials and discover tools), a minute scheduler for standing orders, JSON persistence, server-sent events.
- **React + TypeScript + AG Grid + AG Charts** UI: dashboard with live budget bars and standing orders, mission page with activity stream, proposal grid and approval, ledger grid with CSV export, a spending insights page (four charts plus a grouped, filterable grid of every purchased item), and the order assistant panel.
- **Channel3** product search API as the live catalog (search with price, availability and condition filters; product detail for verification). Every candidate in the UI links to the merchant's page, and the Channel3 product IDs travel into the PayPal order items.
- **Mock PayPal** simulator and a **scripted agent** make the entire flow testable without network: `go test ./...` runs the end-to-end flow through real HTTP (checkout, saved-account payments, webhooks, suggestions, standing orders, insights, the MCP assistant), and Playwright scripts drive the UI.
- Deployed on a small Linux server from a GitHub Actions release with a one-line install script; Docker Compose and a Render blueprint are included.

### Challenges we ran into
- Making the agent *safe by construction*: the agent has no payment tool at all; payment is a server-side consequence of a human decision, and the policy engine re-runs at approval time because budgets move.
- Money accounting that survives real-world mess: budget reservations on order creation, release exactly once on capture, cancel, void or decline, capture-on-return with an `ORDER_ALREADY_CAPTURED` guard, serialised money transitions, and a reconciliation endpoint for buyers who approve but never come back. PayPal reports an order COMPLETED after any capture attempt, so the ledger books money on the capture's own status: a pending capture (eCheck, review) stays reserved until PayPal completes it, a declined one hands the cart back for a fresh approval, and a refund that arrives before its capture is parked and applied when the capture is booked. Every one of these paths has a test, and the suite runs under Go's race detector in CI.
- Treating the catalog as hostile: product titles and descriptions are third-party text, so the agent is told to treat instructions inside them as noise, a product's category comes from the retailer's category path and never from its title, and the guards are code rather than prompt: one live cart per mission, none after a cart was paid, capped quantities and proposals. A hijacked agent can only propose, and with a saved PayPal account the auto-approve threshold the human chose is the most it could ever spend in one mission. The README has the full threat model.
- Leaving a demo with no logins on the public internet: every input is bounded (amounts, names, lists, goals, feedback, body size, number of mandates), there are per-address limits on agent runs, searches, PayPal calls and open event streams next to the global hourly cap, cross-site POSTs are refused, responses carry security headers, and the saved PayPal token id never leaves the server.
- Giving the agent *more* PayPal reach without more power: the order assistant reads PayPal through its MCP server, but the mutating tools (create, pay, refund, accept dispute) are disabled in the toolset, and the assistant falls back to the ledger when PayPal's sandbox MCP has a bad day.
- Keeping the demo honest: everything judges see in mock mode is the same code path as sandbox mode behind an interface.

### Accomplishments that we're proud of
- A complete product, not a proof of concept: create → research → propose → approve → PayPal → ledger, with tests covering the whole chain (over a hundred end-to-end tests through real HTTP, run with the race detector in CI), eleven review passes and a security audit before submission.
- It runs live against the PayPal sandbox, Claude Opus 5.5 and Channel3's real catalog; the README shows a real run with a captured sandbox order.
- Two PayPal surfaces for the AI, with the money line drawn in code: the shopping agent proposes and the server pays through Orders v2 after approval; the order assistant reads PayPal through its MCP server with mutating tools disabled.
- Agent-to-agent negotiation that stays inside the human's mandate: the buyer agent cannot exceed its limit, the seller agent cannot go below its floor, and the settlement is a normal PayPal approval, so "two agents negotiating" never becomes "two agents spending".
- Agentic payments without giving the agent a wallet: the saved PayPal account turns a mandate into a real spending authority (one-click approvals, no-click auto-pay under the threshold, standing orders), and the policy engine still bounds every charge.
- The agent explains *why* a goal cannot be met inside a mandate and proposes the smallest fix; the human applies it with one click. The policy stays human-owned, but the loop is fast.
- Rejection with feedback continues the same conversation; the agent genuinely revises.

### What we learned
- Spending controls belong outside the model. Prompts steer; policy engines enforce.
- PayPal's Orders v2 flow maps naturally onto "agent proposes, human approves": the approve link *is* the human gate, and the vault turns that one consent into a durable, bounded authority.
- Supervised and autonomous agentic commerce are the same product with one threshold: everything above it waits for a human, everything below it just happens, and the mandate bounds both.

### What's next for Mandate
- Shared mandates for teams, with per-person limits inside one budget.
- Multi-currency mandates, Pay Later at checkout, and PayPal's Agent Ready checkout once it opens up to sandbox developers.
- Let the order assistant draft (never send) PayPal invoices and dispute responses for a human to approve, using the MCP tools that are disabled today.

## Built with
go, react, typescript, ag-grid, ag-charts, paypal, paypal-orders-api, paypal-vault, paypal-mcp, paypal-webhooks, claude, anthropic-sdk, channel3, vite, docker, render, playwright

## Try it out links
- GitHub repo: https://github.com/sadrax4/mandate
- Live demo: http://23.27.20.150:8080 (PayPal sandbox, Claude Opus 5.5, Channel3 catalog; sandbox buyer login is in the README)

## AI tools disclosure (Devpost asks for this)
Claude Code (Anthropic) was used as a pair programmer for the implementation, directed and reviewed by the author. Inside the product, the shopping agent and the order assistant run on Claude through the Anthropic API.

## Additional info answers

### Explain how your project meets the main technology requirement
PayPal tools (sandbox):
- PayPal REST Orders v2 API: when a human approves the agent's proposal, the server obtains an OAuth2 client-credentials token and creates an order (intent CAPTURE) for the exact cart, with line items, an amount breakdown and return/cancel URLs in payment_source.paypal.experience_context. The buyer is redirected to PayPal's approval link; on return the server captures the order, reconciles it with GET /v2/checkout/orders/{id}, and updates the mandate's budget. PayPal-Request-Id (set to the proposal ID) makes order creation idempotent, and ORDER_ALREADY_CAPTURED is tolerated so a double return cannot double-spend.
- PayPal vault (saved payment methods): a mandate can ask to save the buyer's PayPal account at the first checkout (payment_source.paypal.attributes.vault, store_in_vault ON_SUCCESS). The capture response's vault token is stored on the mandate; later orders are created with payment_source.paypal.vault_id and captured server-side, so approved carts pay with one click and carts under the auto-approve threshold pay with none. Dead tokens are forgotten and the flow falls back to checkout. VAULT.PAYMENT-TOKEN.CREATED webhooks and order reconciliation cover the asynchronous case.
- PayPal Webhooks: CHECKOUT.ORDER.APPROVED, PAYMENT.CAPTURE.COMPLETED, PAYMENT.CAPTURE.REFUNDED/REVERSED/DENIED, CHECKOUT.ORDER.VOIDED and VAULT.PAYMENT-TOKEN.CREATED are handled, each verified through /v1/notifications/verify-webhook-signature before the ledger is touched. The hosted demo runs over plain HTTP, so webhooks are not registered there; it relies on capture-on-return plus the reconciliation endpoint, and the webhook path is covered by tests.
- PayPal MCP server (PayPal Agent Toolkit): the order assistant connects Claude to https://mcp.sandbox.paypal.com through the Anthropic MCP connector, authenticated with the same client-credentials token. The server performs the MCP handshake itself at startup to verify credentials and discover the tool catalogue, then enables only read-only tools (get_order, list_transactions, get_shipment_tracking, get_refund, list_disputes, get_invoice, ...) in an mcp_toolset; create, pay, refund and dispute-accepting tools are never offered to the model.
- PayPal sandbox business and personal (buyer) accounts for end-to-end testing.

AI tools:
- Claude (claude-opus-5-5) through the Anthropic Messages API and the official Go SDK is the shopping agent. It runs an agentic tool loop with five tools: search_products and get_product (catalog), check_mandate (runs the cart through the policy engine and returns the exact violations), propose_cart, and suggest_mandate_change (records the smallest mandate change that would unblock an impossible goal; only a human can apply it). It uses prompt caching and server-side model fallback. By design it has no payment tool: it can only research and propose, and every payment is a server-side consequence of a human approval (or of a saved PayPal account under a human-set threshold) that re-checks the mandate.
- A second Claude session is the order assistant, with the ledger as context and PayPal's MCP tools attached read-only.

Sponsor tools:
- Channel3 product search API (POST /v1/search, GET /v1/products/{id}) is the live catalog: real products, offers, prices, merchants and ratings; every candidate in the UI links to the merchant's product page.
- AG Grid Community (column groups, column filters, quick filter, custom cell renderers, keyboard row opening, CSV export, Theming API) and AG Charts Community (budget use per mandate, spend by category, merchant and day) power the grids and the Insights page.

### Testing instructions or credentials
Open http://23.27.20.150:8080 (plain HTTP, not HTTPS). It runs with the PayPal sandbox, Claude Opus 5.5 and the live Channel3 catalog; the status at the top right reads "PayPal sandbox · Claude · Channel3 · PayPal MCP" when PayPal's MCP server is reachable (hover it for the list of read-only tools).

1. On the dashboard click "Create demo mandate" (it becomes "Show demo mandate" once the demo mandate exists), or "New mandate" to set your own budget, per-item cap, categories, blocked merchants, minimum rating, approval threshold and whether to save your PayPal account at the first checkout.
2. Type a goal in the box at the top of the dashboard, such as "a mechanical keyboard with brown switches under $120" or "a USB-C hub with HDMI under $40", and click "Start mission". The line under the box shows which mandate it runs under; there you can also choose "Repeat monthly" to make it a standing order, or "Hire a service" for an agent-to-agent negotiation.
3. The mission page opens. The tracker at the top moves from Search to Propose while "How the agent got here" fills with searches, mandate checks and the agent's reasoning, and "Products compared" lists every product it looked at (each linking to the merchant page via Channel3). Usually 20-40 seconds.
4. Click "Approve & pay with PayPal". Log in at PayPal with the sandbox buyer: sb-f6dqu53622575@personal.example.com / Udb;8n)b, then click Pay. You are returned to the mission page showing "Paid through PayPal" with the PayPal order and capture IDs in the receipt. The Ledger page lists the order and can export CSV; the Insights page charts it.
5. If the mandate saved your PayPal account (its card on the dashboard shows "PayPal saved"), approve a second mission: it is paid with one click and no checkout; a cart under the auto-approve threshold is paid before the agent finishes.
6. Try "Reject with feedback" on a proposal (for example "cheaper" or "wired only") to see the agent revise in the same conversation. Try a goal outside the mandate (a kettle under a computers-only mandate) to see it explain, suggest the smallest change, and a one-click button such as "Allow kitchen & retry".
7. On the Ledger page, ask the order assistant "Did my most recent order get captured? Confirm it with PayPal." to watch it read PayPal through the MCP server (as of 1 Nov 2026 PayPal's hosted sandbox MCP answers every tool call with "PAYPAL_API_SETUP_ERROR: Unsupported cache mode: default", also when we call it directly without Claude (scripts/mcpcheck.cjs), so it is an error on PayPal's side; the assistant then answers from the ledger and says it could not check PayPal. The demo video therefore shows the ledger itself, not the assistant).

The hosted demo caps agent runs at 40 per hour in total and 30 per address. If you hit the cap or prefer an offline run, the README's quick start runs everything locally in demo mode with a built-in catalog and a PayPal simulator (no keys needed), and the same code path with your own sandbox keys.

### AG Grid experience
Very good. AG Grid Community gave us dense, sortable comparison grids for the agent's candidates, the proposed cart, the ledger and every purchased line item, with column groups, column filters, a quick filter, custom cell renderers (category chips, merchant links, two-line product cells on phones) and CSV export. The Theming API made dark mode a one-line change (colorSchemeDark), and row class rules highlight the items in the proposed cart. AG Charts Community gave the Insights page its budget-use, category, merchant and daily charts with matching light and dark themes in an afternoon. The main thing we would like is a smaller bundle for apps that only need a couple of grids and charts.
