# Demo video script (target 2:40, hard limit 3:00)

Record on the live server (http://23.27.20.150:8080) so the badges read `PayPal: sandbox`, `Agent: claude-opus-5-5`, `Catalog: channel3`. Say (or caption) the words "PayPal" and "Claude" explicitly: judges look for both. Keep retailer logos and product photos out of frame (third-party trademark rule); product names in a grid are fine. 1920×1080, browser zoomed to 110%, no music. Narrate calmly; the product does the talking. Open the PayPal sandbox buyer login in a second tab beforehand so the login step takes seconds.

| Time | Screen | Say |
|---|---|---|
| 0:00–0:15 | Dashboard | "Mandate is an AI shopping agent that can only spend inside the limits you give it, and pays with PayPal. AI agents can shop for you, but nobody wants to hand an AI their wallet; this is the middle ground." |
| 0:15–0:35 | New mandate form, fill it in | "A mandate is a budget, a per-item cap, allowed categories, blocked merchants, a minimum rating, and whether I approve every cart. I also let it save my PayPal account at the first checkout." Save. |
| 0:35–1:15 | Start mission, watch activity stream | "I give it a goal in plain language." Type *a mechanical keyboard with brown switches under $120*. "Claude searches real products through Channel3, compares them, and runs every cart through the policy engine. Everything it looked at lands in this grid; every product links to the merchant." Pause on the green checks and the agent's reasons for rejecting alternatives. |
| 1:15–1:30 | Proposal card | "Here is the proposal: the item, the agent's reason, the total, what is left of the budget, and which rules passed. The agent cannot pay. I can." |
| 1:30–1:55 | Click Approve → PayPal sandbox checkout → return | "Approval creates a PayPal order for this exact cart and reserves the budget. I pay on PayPal as the buyer. Back in Mandate the order is captured, with the capture ID, and the budget updates. PayPal also saved my account to this mandate." |
| 1:55–2:10 | Second mission on the same mandate (prepared earlier), click Approve | "This cart is paid with one click and no checkout, and anything under the auto-approve threshold pays with no click at all. The mandate still decides every cent." |
| 2:10–2:25 | Mission with a goal outside the mandate (prepared earlier) | "If a goal cannot fit, the agent explains why and suggests the smallest change. It cannot change its own mandate; I click Amend and retry." Click it, show the retry starting. |
| 2:25–2:40 | Negotiation mission (prepared earlier), then Insights | "Two agents can even negotiate: my buyer agent haggles for a service under my limit, the seller concedes, and I approve the deal. Insights shows where the money went." |
| 2:40–2:50 | Ledger, then the Ask panel | "Every order and capture is in the ledger, and the assistant can check them against PayPal itself through PayPal's MCP server, read-only." |
| 2:50–3:00 | Closing | "Mandate: delegate the shopping, keep the authority. Sandbox only today, open source, built on PayPal Orders, vault and MCP, Claude, AG Grid and Channel3." |

Checklist before recording:
- Clean dashboard: either a fresh server data file or simply create a new mandate on camera.
- Sandbox mode with real PayPal checkout, buyer logged out so the login is visible (credentials in the README).
- The mandate used on camera has *Save my PayPal account* ticked and the Vault feature is enabled on the sandbox app, so the second approval pays with one click.
- Prepare in advance on the same mandate: a second mission with a pending proposal (for the one-click step), a mission whose goal fails the mandate (for the suggestion step), a finished negotiation mission (*Hire a service*), and one standing order so the dashboard card is there.
- Pre-warm one mission so you know the agent's timing; Claude usually needs 20–60 seconds.
- Keep third-party logos out of the frame except PayPal's own checkout.
- Upload to YouTube as public or unlisted; paste the link in the Devpost form.
