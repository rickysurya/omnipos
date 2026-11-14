-- Brings an existing database up to the schema in db/initdb/01-schema.sql:
-- the menu catalog, price snapshots on order lines, order totals, one open cart per customer,
-- and the Telegram update ledger. Safe to run more than once.
--
--   docker exec -i omnipos-db psql -U omnipos_owner -d omnipos < migrations/002_menu_pricing_and_idempotency.sql
--   docker exec -i omnipos-db psql -U omnipos_owner -d omnipos -v app_user=omnipos_app -f /opt/omnipos/grants.psql
--
-- Run the second command too: the application role has no access to the new tables until then.
-- Existing order lines keep a price of 0, because no price was recorded when they were created.

BEGIN;

CREATE TABLE IF NOT EXISTS menu_items (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    price_cents BIGINT NOT NULL,
    is_available BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT menu_items_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT menu_items_price_check CHECK (price_cents >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS menu_items_name_unique
    ON menu_items (lower(regexp_replace(btrim(name), '\s+', ' ', 'g')));

ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS menu_item_id INT REFERENCES menu_items(id),
    ADD COLUMN IF NOT EXISTS unit_price_cents BIGINT NOT NULL DEFAULT 0;

ALTER TABLE order_items DROP CONSTRAINT IF EXISTS order_items_price_check;
ALTER TABLE order_items ADD CONSTRAINT order_items_price_check CHECK (unit_price_cents >= 0);

-- NOT VALID applies the rule to new rows without failing on old ones that may predate the limit.
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS order_items_quantity_check;
ALTER TABLE order_items ADD CONSTRAINT order_items_quantity_check CHECK (quantity BETWEEN 1 AND 99) NOT VALID;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS total_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_total_check;
ALTER TABLE orders ADD CONSTRAINT orders_total_check CHECK (total_cents >= 0);

CREATE TABLE IF NOT EXISTS processed_updates (
    update_id BIGINT PRIMARY KEY,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Keep only the newest open cart of any customer who has several, then forbid it from happening again.
-- Older carts are cancelled, not deleted, in line with soft deletion.
UPDATE orders o
SET status = 'cancelled'
WHERE o.status = 'pending'
  AND EXISTS (
      SELECT 1 FROM orders newer
      WHERE newer.customer_id = o.customer_id AND newer.status = 'pending' AND newer.id > o.id
  );

CREATE UNIQUE INDEX IF NOT EXISTS orders_one_pending_per_customer
    ON orders (customer_id) WHERE status = 'pending';

COMMIT;
