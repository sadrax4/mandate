# Security

Mandate moves (sandbox) money on a human's behalf, so it is built around a few hard rules:

- The model has no payment tool. It can search, check a cart against the mandate, propose, and suggest a mandate change. Payment happens server-side after a human approval, or through a PayPal account the human chose to save, and only for carts the policy engine accepts at that moment.
- Secrets (PayPal client secret, Anthropic key, Channel3 key) come from the environment or `.env` and are never logged, stored or sent to the model.
- A saved PayPal vault token is stored with the mandate; it is only usable with the merchant's own API credentials, and every charge made with it still passes the policy engine.
- Webhook deliveries are verified with PayPal (`verify-webhook-signature`) before anything is updated.
- The public demo caps agent runs per hour (globally and per address), bounds every input (amounts, names, lists, goals, feedback, body size, number of mandates), limits searches, PayPal calls and open event streams per address, refuses cross-site POSTs, and runs against PayPal's sandbox only.
- The saved PayPal token id is never returned by the API or written into events; the API only reports that a saved account exists.
- The order assistant passes a short-lived PayPal access token to Anthropic's MCP connector so the model can read orders through PayPal's hosted MCP server; only read-only tools are enabled in the toolset the model receives, and the server verifies that tool list itself.
- A model can propose one live cart per mission and nothing after a cart was paid; quantities and proposals per mission are capped in code, not in the prompt. A product's category is taken from the retailer's category path, never from its title.

## Reporting a vulnerability

Please do not open a public issue for security problems. Email the maintainer (see the GitHub profile of the repository owner) with a description and reproduction steps. You will get a reply within a few days.

## Scope notes

This is a hackathon project: there is no user authentication and persistence is a JSON file. Do not run it against PayPal live credentials without adding authentication, a database and secret management appropriate for real money.
