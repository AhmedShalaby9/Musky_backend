ALTER TABLE products ADD COLUMN loose_pieces BIGINT NOT NULL DEFAULT 0 AFTER quantity;
ALTER TABLE products ADD CONSTRAINT ck_product_loose_pieces CHECK (loose_pieces >= 0 AND loose_pieces < pieces_per_unit);
ALTER TABLE stock_movements ADD COLUMN pieces_delta BIGINT NULL AFTER quantity_delta;
UPDATE stock_movements sm JOIN products p ON p.tenant_id = sm.tenant_id AND p.id = sm.product_id SET sm.pieces_delta = sm.quantity_delta * p.pieces_per_unit WHERE sm.pieces_delta IS NULL;
