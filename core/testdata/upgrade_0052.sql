-- A 1.2 single-store install, at migration 0052, with a little of everything
-- the upgrade to shared tenancy has to carry across unchanged.
UPDATE store_profile SET name = 'Old Shop' WHERE id = 1;
INSERT INTO superusers (id, email, password_hash, role)
VALUES (1, 'owner@example.com', 'pbkdf2-sha256$1$c2FsdA$a2V5', 'owner');
INSERT INTO products (id, slug, title, status, currency) VALUES (1, 'tee', 'Tee', 'active', 'USD');
INSERT INTO variants (id, product_id, sku, price_minor) VALUES (1, 1, 'TEE-01', 1500);
INSERT INTO orders (id, number, access_token, payment_provider, currency, subtotal_minor, total_minor, email)
VALUES (3, 'GC-000003', 'tok3', 'cod', 'USD', 1500, 1500, 'a@example.com'),
       (7, 'GC-000007', 'tok7', 'cod', 'USD', 1500, 1500, 'b@example.com');
SELECT setval(pg_get_serial_sequence('orders', 'id'), 7);
SELECT setval(pg_get_serial_sequence('products', 'id'), 1);
SELECT setval(pg_get_serial_sequence('variants', 'id'), 1);
SELECT setval(pg_get_serial_sequence('superusers', 'id'), 1);
INSERT INTO api_keys (name, prefix, token_hash, role) VALUES ('ci', 'gck_old1', 'hash', 'owner');
