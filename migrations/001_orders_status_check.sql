-- Allow the full order lifecycle in orders.status.
-- Safe to run more than once: the constraint is dropped if present, then recreated.
--
-- Apply with:
--   docker exec -i omnipos-db psql -U omnipos_owner -d omnipos < migrations/001_orders_status_check.sql
-- Run it as the owner role. The application role cannot alter tables.

BEGIN;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;

ALTER TABLE orders
    ADD CONSTRAINT orders_status_check
    CHECK (status IN ('pending', 'confirmed', 'preparing', 'ready', 'completed', 'cancelled'));

COMMIT;
