-- Baseline schema for a new database. Runs once, on first start of an empty data directory.
-- Existing databases reach the same schema through the numbered files in migrations/.
-- Who may touch what is decided in db/grants.psql, not here.

CREATE TABLE IF NOT EXISTS customers (
    chat_id VARCHAR(50) PRIMARY KEY,
    current_state VARCHAR(50) NOT NULL DEFAULT 'idle'
);

-- The catalog. Prices are integer minor units, so no floating point ever touches money.
-- The application can only read this table; the owner role maintains it.
CREATE TABLE IF NOT EXISTS menu_items (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    price_cents BIGINT NOT NULL,
    is_available BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT menu_items_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT menu_items_price_check CHECK (price_cents >= 0)
);

-- Names are matched case-insensitively with runs of spaces collapsed, so uniqueness uses the same rule.
CREATE UNIQUE INDEX IF NOT EXISTS menu_items_name_unique
    ON menu_items (lower(regexp_replace(btrim(name), '\s+', ' ', 'g')));

CREATE TABLE IF NOT EXISTS orders (
    id SERIAL PRIMARY KEY,
    customer_id VARCHAR(50) REFERENCES customers(chat_id),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    address TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    total_cents BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT orders_status_check CHECK (status IN ('pending', 'confirmed', 'preparing', 'ready', 'completed', 'cancelled')),
    CONSTRAINT orders_total_check CHECK (total_cents >= 0)
);

-- A customer has at most one open cart. This also makes concurrent first items safe.
CREATE UNIQUE INDEX IF NOT EXISTS orders_one_pending_per_customer
    ON orders (customer_id) WHERE status = 'pending';

-- item_name and unit_price_cents are copies taken when the item is added, so later menu edits
-- never change an existing order. The application can insert these rows but never update them.
CREATE TABLE IF NOT EXISTS order_items (
    id SERIAL PRIMARY KEY,
    order_id INT REFERENCES orders(id) ON DELETE CASCADE,
    item_name VARCHAR(100) NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    menu_item_id INT REFERENCES menu_items(id),
    unit_price_cents BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT order_items_quantity_check CHECK (quantity BETWEEN 1 AND 99),
    CONSTRAINT order_items_price_check CHECK (unit_price_cents >= 0)
);

-- Telegram update ids that were handled, so a retried webhook delivery does nothing twice.
CREATE TABLE IF NOT EXISTS processed_updates (
    update_id BIGINT PRIMARY KEY,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
