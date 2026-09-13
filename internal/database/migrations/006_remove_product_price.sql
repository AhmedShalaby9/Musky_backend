ALTER TABLE products DROP CHECK ck_product_numbers;
ALTER TABLE products DROP COLUMN unit_price_minor;
ALTER TABLE products ADD CONSTRAINT ck_product_numbers CHECK (quantity BETWEEN 0 AND 1000000000 AND pieces_per_unit BETWEEN 1 AND 1000000);
