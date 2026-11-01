# Demo video tooling

Records the Mandate demo with a real browser (Chromium screencast at 1920×1080), a visible cursor,
human-paced typing, burned-in captions, and an SRT with the same captions.
Needs Node 20+, ffmpeg and `npm i` (Playwright 1.49.1 + `npx playwright install chromium`).

```bash
npm i && npx playwright install chromium
# 1. record scenes (against the offline demo, or the live app with MODE=live)
BASE_URL=http://localhost:8080 MODE=mock node scenes.cjs intro mandate buy autopay reject negotiate ledger insights
# 2. join them with title and end cards, captions burned in
node mkorder.cjs scenes intro,mandate,buy,autopay,reject,negotiate,ledger,insights > order.json
CAPTION_FONT="Liberation Sans" node build.cjs order.json out   # -> out/mandate-demo.mp4 and out/mandate-demo.srt
```

`MODE=live` drives the real PayPal sandbox checkout and needs `PP_EMAIL` / `PP_PASS` (the sandbox buyer).
Use a fresh data file or a throwaway mandate: the scenes create a mandate called "Home office".
Waits on the agent are sped up and captioned "(sped up)". Offline recordings carry an "OFFLINE DEMO MODE" badge.
