# Security

Mandate moves (sandbox) money on a human's behalf, so it is built around a few hard rules:

- The model has no payment tool. It can search, check a cart against the mandate, propose, and suggest a mandate change. Payment happens server-side after a human approval, or through a PayPal account the human chose to save, and only for carts the policy engine accepts at that moment.
- Secrets (PayPal client secret, Anthropic key, Channel3 key) come from the environment or `.env` and are never logged, stored or sent to the model.
- A saved PayPal vault token is stored with the mandate; it is only usable with the merchant's own API credentials, and every charge made with it still passes the policy engine.
- Webhook deliveries are verified with PayPal (`verify-webhook-signature`) before anything is updated.
- The public demo caps agent runs per hour and runs against PayPal's sandbox only.

## Reporting a vulnerability

Please do not open a public issue for security problems. Email the maintainer (see the GitHub profile of the repository owner) with a description and reproduction steps. You will get a reply within a few days.

## Scope notes

This is a hackathon project: there is no user authentication and persistence is a JSON file. Do not run it against PayPal live credentials without adding authentication, a database and secret management appropriate for real money.
