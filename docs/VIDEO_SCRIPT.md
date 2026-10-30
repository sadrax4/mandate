# Demo video script (target 2:40, hard limit 3:00)

Record on the live server (http://23.27.20.150:8080) so the badges read `PayPal: sandbox`, `Agent: claude-opus-5-5`, `Catalog: channel3`. Say (or caption) the words "PayPal" and "Claude" explicitly: judges look for both. Keep retailer logos and product photos out of frame (third-party trademark rule); product names in a grid are fine. 1920×1080, browser zoomed to 110%, no music. Narrate calmly; the product does the talking. Open the PayPal sandbox buyer login in a second tab beforehand so the login step takes seconds.

| Time | Screen | Say |
|---|---|---|
| 0:00–0:20 | Title card, then dashboard | "AI agents can shop for you, but nobody wants to hand an AI their wallet. Mandate gives an agent a spending authority it cannot exceed, and PayPal checkout is the only way money moves." |
| 0:20–0:45 | New mandate form, fill it in | "A mandate is a budget, a per-item cap, allowed categories, blocked merchants, a minimum rating, and whether I want to approve every cart." Create it. |
| 0:45–1:25 | Start mission, watch activity stream | "I give it a goal in plain language." Type *a mechanical keyboard with brown switches under $120*. "Claude searches real products through Channel3, compares them, and runs every cart through the policy engine. Everything it looked at lands in this grid." Pause on the check event with the green checks and on the agent's reasons for rejecting alternatives. |
| 1:25–1:50 | Proposal card | "Here's the proposal: each item, the agent's reason, the total, what's left of the budget, and which rules passed. The agent cannot pay. I can." |
| 1:50–2:15 | Click Approve → PayPal sandbox checkout → return | "Approval creates a PayPal order for this exact cart and reserves the budget. I approve on PayPal as the buyer. Back in Mandate the order is captured, with the capture ID, and the budget updates." Point at the *PayPal account saved on this mandate* line. |
| 2:15–2:35 | Second mission on the same mandate (prepared earlier), click Approve | "That first checkout saved my PayPal account to this mandate. This cart is paid with one click, no checkout, and anything under the auto-approve threshold pays with no click at all. The mandate still decides every cent." |
| 2:35–2:45 | Reject flow (prepared earlier) | "If I reject with feedback, the agent revises in the same conversation." |
| 2:45–2:55 | Ledger | "Every order, every capture, checkout or saved account, filterable, auditable." |
| 2:55–3:00 | Closing | "Mandate: delegate the shopping, keep the authority. Built on PayPal Orders and vault, Claude, AG Grid and Channel3." |

Checklist before recording:
- Clean dashboard: either a fresh server data file or simply create a new mandate on camera.
- Sandbox mode with real PayPal checkout, buyer logged out so the login is visible (credentials in the README).
- The mandate used on camera has *Save my PayPal account* ticked and the Vault feature is enabled on the sandbox app, so the second approval pays with one click.
- Pre-warm one mission so you know the agent's timing; Claude usually needs 20–60 seconds.
- Keep third-party logos out of the frame except PayPal's own checkout.
- Upload to YouTube as public or unlisted; paste the link in the Devpost form.
