# Contributing to Mandate

Thanks for looking under the hood. Mandate is a small Go + React codebase and it stays that way on purpose: every money-moving path is readable in one sitting.

## Ground rules

- **The agent never pays.** Any change that gives the model a tool that creates, captures, refunds or otherwise moves money will not be merged. Payment is a server-side consequence of a human decision (or of a saved PayPal account under a human-set threshold), and the policy engine runs again at that moment.
- **Every money transition is serialised** under `payMu` in `internal/server` and reflected in the mandate's `spent`/`reserved` exactly once. Keep it that way; the tests check it.
- **Offline first.** `go test ./...` must pass with no network: the mock PayPal client, the demo catalog and the scripted agent cover the whole flow.

## Development

```bash
cd web && npm install && npm run build && cd ..   # UI into web/dist
go run ./cmd/mandate                               # http://localhost:8080 in offline demo mode
go test ./...                                      # unit + HTTP end-to-end tests
node scripts/smoke.cjs                             # Playwright walk-through against a running demo server
```

Set `PAYPAL_CLIENT_ID`/`PAYPAL_CLIENT_SECRET` (sandbox) and `ANTHROPIC_API_KEY` in `.env` for the real stack; see `.env.example` and the README.

## Pull requests

- One change per PR, with a test when behaviour changes. `gofmt` and `go vet` run in CI.
- Describe the user-visible effect in the PR title; the README's "How it works" section should still be true after your change (update it if not).
- No secrets, screenshots of dashboards with credentials, or sandbox keys in commits.

## Reporting issues

Use the issue templates. For anything security-related, see [SECURITY.md](SECURITY.md).
