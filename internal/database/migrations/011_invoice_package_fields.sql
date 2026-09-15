ALTER TABLE invoice_items
 ADD COLUMN units_per_package BIGINT NOT NULL DEFAULT 1 AFTER pieces_per_unit,
 ADD COLUMN package_count BIGINT NOT NULL DEFAULT 1 AFTER quantity;

UPDATE invoice_items
 SET units_per_package = pieces_per_unit,
     package_count = quantity;

ALTER TABLE invoice_items
 ADD CONSTRAINT ck_item_package_numbers CHECK (units_per_package BETWEEN 1 AND 1000000 AND package_count BETWEEN 1 AND 1000000000);
