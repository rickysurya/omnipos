# 0003: Kitchen channel and webhook security

Status: accepted, 2026-11-12

## Context

Tailscale Funnel exposes the server to the internet. Before this change, `/ws` had no authentication and accepted any origin, so anyone who found the URL could read every customer address and set any order to any status. The hub's broadcast blocked on a single stalled client, which would also freeze every Telegram webhook, because the webhook handler broadcasts while it holds the request.

## Decisions

Authenticate the kitchen socket.
- A shared token in `KDS_TOKEN` is required on every connection, sent as `Authorization: Bearer <token>` or, for browsers, as a subprotocol (`Sec-WebSocket-Protocol: kds.v1, kds-token.<token>`). Browsers cannot set headers on a WebSocket.
- The token is never accepted in the URL. The request logger records the URL, so a query parameter would put the secret in logs.
- The server selects `kds.v1` only, so the token is not echoed back in the handshake response.
- Comparison hashes both sides and compares in constant time. An unset token authorizes nobody.
- The token must differ from the Telegram secret, so leaking one does not open the other channel.

Restrict browser origins.
- A browser must send an `Origin` on the `KDS_ALLOWED_ORIGINS` list. The default list is empty, which rejects all browser origins.
- A connection with no `Origin` header is allowed through the origin check. Only browsers send one, and browsers are the case where another site could ride on a user's session. Non-browser clients still need the token.

Keep the hub from becoming a denial of service lever.
- Each display has a buffered queue. `Broadcast` never blocks. A display whose queue fills is dropped and can reconnect.
- Reads are limited to 1 KiB. A 60 second ping and pong deadline closes dead connections. Writes carry a 10 second deadline. At most 32 displays can connect.

Enforce the order lifecycle where the data lives.
- The kitchen can set `preparing`, `ready` and `completed`, each only from the previous status. The check is a single `UPDATE ... WHERE status = <previous>`, so it is atomic and a double click or a replay changes nothing and sends no second notification.
- The domain rejects any other status before touching the database, which stops a display from cancelling an order or reopening a finished one.

Harden the HTTP surface.
- The server sets header, read, write and idle timeouts, a header size limit and a 1 MiB cap on webhook bodies. The webhook secret is compared in constant time and must be 32 to 256 characters.
- Webhook updates with no message, such as edited messages, are acknowledged and ignored. Previously they created a customer with chat id 0.
- Startup fails on missing or weak configuration instead of running with an empty secret.
- Shutdown drains in-flight requests and disconnects the displays.

Deduplicate Telegram updates.
- Telegram retries a webhook that returns an error, and adding an item is not idempotent, so a retry could add an item twice. The handler checks `processed_updates` for the `update_id` before processing and records it after processing succeeds.
- A failure leaves the update unrecorded, so the retry is processed. A crash between processing and recording can still repeat one update. Closing that gap needs the record written in the same transaction as the work, which the current port boundaries do not allow.
- The table grows without bound. Prune it as the owner: `DELETE FROM processed_updates WHERE received_at < now() - interval '30 days'`.

Neutralize customer text in the kitchen message.
- Addresses and item names are placed in a pipe delimited line. `|` and control characters are replaced with a space, so a customer cannot forge extra fields such as `Items:`. The wire format is unchanged so existing display clients keep working.
- Item names are limited to 100 characters (matching the column), quantities to 99 and addresses to 300.

## Not done, and why

- Per chat rate limiting. A single customer can currently send messages as fast as Telegram allows.
- Per station kitchen credentials. One shared token means rotation restarts every display. If stations need separate identities, move to per station tokens.
- Transport security is assumed to be provided by Tailscale Funnel in front of the plain HTTP listener on localhost. If the app is ever exposed directly, add TLS.

## Revisit when

- Telegram retries are observed in production logs.
- More than one kitchen station needs distinct access, or a customer facing web channel is added.
