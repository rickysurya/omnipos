-- Example menu for trying the bot. Run as the owner role. Prices are in the currency's minor
-- units, as configured by CURRENCY_DECIMALS (0 means whole rupiah, so 25000 is Rp 25,000).
--
--   docker exec -i omnipos-db psql -U omnipos_owner -d omnipos < db/seed/sample-menu.sql

INSERT INTO menu_items (name, price_cents) VALUES
    ('Nasi Goreng', 25000),
    ('Mie Goreng', 22000),
    ('Soto Ayam', 20000),
    ('Es Teh', 5000),
    ('Es Jeruk', 8000)
ON CONFLICT DO NOTHING;
