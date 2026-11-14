# 0004: Menu, pricing and order totals

Status: accepted, 2026-11-13

## Context

Items were free text with no price, so an order had no total and a customer could order anything. The bot now sells from a menu stored in the database.

## Decisions

Menu in `menu_items`.
- Customers order by item name (case and extra spaces ignored) or by name with a quantity. Free text that is not on the menu is rejected.
- Names are unique after normalization, so a name always resolves to one item.
- The application role has read only access. The menu is managed by the owner role with plain SQL (see the README), which keeps price changes out of reach of a compromised bot.
- Hiding an item sets `is_available = false`. Rows are never deleted, so past orders keep their reference.

Prices in integer minor units.
- `price_cents` is an integer, never a float. `CURRENCY_DECIMALS` says how many digits sit after the decimal point. The default is 0 with `CURRENCY=IDR`, so the column holds whole rupiah. For a currency with cents, set `CURRENCY_DECIMALS=2` and store 1999 for 19.99.
- Changing `CURRENCY_DECIMALS` after orders exist changes how stored amounts display, so decide it before the first order.

Price snapshots.
- `order_items` copies the name and unit price when the item is added, in the same statement that checks availability. A later price change does not alter an existing order, and an item that sold out a moment ago inserts nothing.
- The order total is computed and stored when the order is confirmed, so the kitchen and the customer see the same number afterwards.

One pending cart per customer.
- A partial unique index on `orders (customer_id) WHERE status = 'pending'` enforces this in the database. Two messages arriving at once cannot create two carts.
- The insert is attempted only when no cart exists. Even an insert that does nothing consumes an order number, and the first version did this on every item, which made order numbers jump. This was found by running the statements against a real PostgreSQL server.

Quantity limits.
- 1 to 99 per line (also a check constraint) and at most 25 lines per order. The menu message is truncated to fit Telegram's 4096 character limit.

## Consequences

- Existing databases run `migrations/002_menu_pricing_and_idempotency.sql`, then `db/grants.psql`. The migration cancels older duplicate pending carts, keeping the newest, before it adds the unique index.
- Orders created before the migration have `total_cents` of 0 and lines with price 0.
- `db/seed/sample-menu.sql` holds example items.

## Revisit when

- Modifiers, per item notes or multiple branches are needed. Those need their own tables rather than more columns here.
- Menu edits should happen from a staff interface. That needs a separate authenticated path with its own role.
