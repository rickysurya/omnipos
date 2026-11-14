# OmniPOS

Multi-tenant POS engine in Go. Customers order through a Telegram bot, and the kitchen follows orders on a WebSocket display.

## Run locally

1. Copy `.env.example` to `.env` and fill it in. Generate each secret with `openssl rand -hex 32`. `APP_DB_PASSWORD` and the password inside `DATABASE_URL` must match.
2. Start the database: `docker compose up -d`. The first start creates the schema and the `omnipos_app` role from `db/initdb/`.
3. Run the server: `go run .`
4. Point the Telegram webhook at `https://<your-funnel-host>/webhook` with the same value as `TELEGRAM_SECRET_TOKEN` passed as `secret_token` to `setWebhook`.

## Menu

Customers can only order items in `menu_items`. The application role cannot write to that table, so manage it as the owner:

```
docker exec -i omnipos-db psql -U omnipos_owner -d omnipos < db/seed/sample-menu.sql
```

```sql
INSERT INTO menu_items (name, price_cents) VALUES ('Es Teh', 8000);
UPDATE menu_items SET price_cents = 27000 WHERE name = 'Nasi Goreng';
UPDATE menu_items SET is_available = false WHERE name = 'Es Teh';
```

Prices are integers in the unit set by `CURRENCY_DECIMALS` (default 0, whole rupiah). Existing orders keep the price they were placed at.

## Kitchen display

Connect to `/ws` with `Authorization: Bearer <KDS_TOKEN>`. A browser page cannot set that header, so it opens the socket with two subprotocols instead:

```js
new WebSocket("wss://<host>/ws", ["kds.v1", "kds-token." + token]);
```

The page's origin must be listed in `KDS_ALLOWED_ORIGINS`. The server sends `NEW_ORDER|ID:..|Customer:..|Address:..|Items:..` lines. The display replies with `{"order_id": 1, "status": "preparing"}`, then `ready`, then `completed`, in that order.

## Tests

```
go vet ./... && go test -race ./...
TEST_DATABASE_URL=postgres://omnipos_owner:<password>@localhost:5432/omnipos go test -v -run TestKDSEndToEnd ./...
```

The end to end test creates its own throwaway schema and drops it afterwards. Use the owner credentials for it, since it creates tables.

## Existing databases

Bring an older database up to date with the numbered files in `migrations/`, run as the owner role. After migrations that add tables, re-run the grants as the owner: `docker exec -i omnipos-db psql -U omnipos_owner -d omnipos -v app_user=omnipos_app < db/grants.psql`. A database from before the menu feature needs `002_menu_pricing_and_idempotency.sql` and then the grants. Moving data from a PostgreSQL 15 container to 18 needs a `pg_dump` and restore.

## Decisions

See `docs/adr/` for the reasoning behind versions, database access and security choices.
