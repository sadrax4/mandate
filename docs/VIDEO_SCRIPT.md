# Demo video (2:52, English captions, no narration, no music)

Recorded live on http://23.27.20.150:8080 (PayPal sandbox, Claude, Channel3 catalog) by `scripts/video`:
a real browser at 1920×1080 with a visible cursor, human-paced typing, burned-in captions and an `.srt`
with the same text. Waits on the agent are sped up and captioned "(sped up)". Nothing is faked: the
PayPal sandbox login, the capture and the receipt IDs on screen are real sandbox objects (no real money).

| Time | Scene | What happens |
|---|---|---|
| 0:00 | Title card | "Give an AI agent a budget. Keep the final say." |
| 0:04 | Intro | The live app and its status pill (PayPal sandbox, Claude, Channel3) |
| 0:13 | Mandate | New mandate "Desk setup": $300 budget, $150 per item, auto-pay under $40, save the PayPal account |
| 0:29 | Buy | Goal in plain language, Claude researches (sped up), proposal with passed mandate checks, Approve, PayPal sandbox login, capture, receipt |
| 1:23 | Auto-pay | A cart under $40 is paid by the saved PayPal account with no click |
| 1:39 | Reject | A cart above the limit waits; reject with a reason; the agent revises in the same conversation |
| 2:06 | Negotiate | Buyer agent haggles with a seller agent; the agreed price becomes an ordinary proposal |
| 2:26 | Ledger | Every order and capture; click a row to open its mission; CSV export |
| 2:38 | Insights | Spend by category, merchant and day (AG Charts) |
| 2:47 | End card | Repository and live demo URL |

Known gap: the order assistant (PayPal's hosted MCP server) is not shown. On the day of recording PayPal's
sandbox MCP answered every tool call with `Unsupported cache mode: default`, so the video shows the ledger
and makes no claim about the assistant.

## Recording it again

```bash
cd scripts/video && npm i && npx playwright install chromium
export BASE_URL=http://23.27.20.150:8080 MODE=live MANDATE_NAME="Desk setup"   # a fresh mandate name each time,
for s in intro mandate buy autopay reject negotiate ledger insights; do node scenes.cjs $s; done   # so the PayPal login shows again
node mkorder.cjs scenes intro,mandate,buy,autopay,reject,negotiate,ledger,insights > order.json
node build.cjs order.json out        # out/mandate-demo.mp4 and out/mandate-demo.srt
```

`MODE=live` reads the sandbox buyer from the app's public `demo_buyer_hint` (or `PP_EMAIL` / `PP_PASS`) and masks
the password wherever the app prints it. `LEDGER_GRID_ONLY=1` records the ledger scene without the assistant.
`MODE=mock` records the offline demo with an "OFFLINE DEMO MODE" badge. Devpost rules: under 3:00, public on YouTube,
no third-party music or logos beyond PayPal's own login page.
