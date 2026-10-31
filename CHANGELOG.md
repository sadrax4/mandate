# Changelog

Everything here was built during the PayPal AI Hackathon submission period (Oct 1 – Nov 12, 2026). Newest first.

## 2026-10-31

- ui: redesigned around one action and one story. The dashboard opens on a goal bar (mandate, kind and repeat chosen inline, suggested goals as chips, a three-step guide that folds away), followed by what needs approval, the mandates as cards with the remaining amount first, standing orders and recent missions as rows. The mission page is one column: the goal, a storyline tracker (search, compare, check, propose, pay; revised, no proposal, failed), a decision card for every state (working, proposal, approved but unpaid, paid with the PayPal receipt, negotiation, suggestion, failed), the story of how the agent got here with tool calls folded into one line each and a switch to show every event, and the products it compared. No nested scrolling, flat cards, tabular numbers, phone layout without sideways scroll, dark mode throughout
- demo: `FAKE_AGENT_DELAY` slows the scripted agent so an offline mission can be watched while it runs

## 2026-10-30

- ledger: the capture's own status decides whether money moved: pending captures (eCheck, review) stay reserved until PayPal completes them, declined ones hand the proposal back for a fresh approval; refunds that arrive before their capture are parked and applied at booking; a withdrawn order that PayPal captures anyway is booked and flagged, or reconciled by the status check
- ledger: in-flight approvals keep their reservation while other orders settle; a second approval (double click, second tab) waits for the order and returns it; re-approving a paid proposal is harmless
- ui: action errors stay on screen, no sideways scroll on phones, cart grids without empty space, grid status colours, full CSV exports, a demo mandate that is created once; the activity log follows a running mission and shows MCP tool calls live
- security: an audit pass. Amounts must be finite and capped, text and lists are bounded, responses are never blank on an encoding error; one live cart per mission and none after a paid one; categories come from the retailer's category path only; the saved PayPal token id never leaves the server; per-address limits on runs, searches, PayPal calls, mandates and event streams; security headers and cross-site POST refusal; standing orders stop when the budget is used up; the installer runs the service as its own user with a private `.env` and a verified download
- review: eleven review rounds (money path, concurrency, UI) with a test for every fix; `go test -race` in CI
- docs: screenshots for one-click payment and standing orders; video script covers the new features in three minutes
- docs: final Devpost text with PayPal's agentic-commerce framing and the additional-info answers
- standing orders, Channel3 links, assistant fallback
- docs: README reflects five agent tools, the assistant session, the new tests and the judging walkthrough
- agent: suggest the smallest mandate change when a goal cannot be met; humans amend and retry with one click
- docs: architecture diagram; agent hook test follows the new status contract
- insights: spending dashboard with AG Charts and a grouped, filterable AG Grid of purchased items
- agent: set the mission status for auto-approved proposals only after the order exists; insights endpoint
- paypal: order assistant on PayPal's hosted MCP server (read-only tools via the Anthropic MCP connector)
- paypal: saved payment methods (vault): one-click approvals and no-click auto-pay under the threshold, with checkout fallback
- docs: Devpost text reflects webhooks, live deployment and the real sandbox run
- docs: real repo and live demo links in the Devpost draft
- docs: live sandbox screenshots, video script on the live server
- review fixes: serialised money transitions, reservation tracking, void/refund handling, withdrawable orders, per-mission agent lock, session eviction, debounced persistence, Channel3 detail parity, demo buyer hint, docs and cover
- deploy: let the binary read .env (comment-safe), start in demo mode when no keys exist
- deploy: prebuilt Linux release on every push and a systemd install script for small servers
- deploy: do not require DOMAIN unless the https profile is used
- deploy: docker compose with optional Caddy HTTPS, server deployment guide
- deploy: default Render blueprint to Channel3 with safe fallback when the key is missing
- chore: stop tracking tsc build info
- ui: render agent markdown safely; prompt: clarify unrated products and plain-text style
- main: last .env occurrence wins; strip trailing comments
- catalog: implement the Channel3 client against the documented OpenAPI schema (offers, ratings, brands, category path) with tests
- server: cap agent runs per hour for public demos
- paypal: verified webhook handling (approve→capture, capture→reconcile), idempotent markPaid; ledger CSV export
- agent: support Anthropic-compatible gateways (OpenRouter) via ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN
- docs: use cd web for npm steps so the quick start works on Windows
- docs: Devpost submission draft and demo video script
- Mandate: AI shopping agent with spending mandates and PayPal checkout
