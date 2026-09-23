CREATE TABLE IF NOT EXISTS invoice_returns (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 invoice_id BIGINT UNSIGNED NOT NULL,
 client_id BIGINT UNSIGNED NOT NULL,
 created_by_user_id BIGINT UNSIGNED NOT NULL,
 amount_minor BIGINT NOT NULL,
 reason VARCHAR(500) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_tenant_return (tenant_id,id),
 INDEX idx_return_invoice (tenant_id,invoice_id),
 CONSTRAINT fk_return_invoice FOREIGN KEY (tenant_id,invoice_id) REFERENCES invoices(tenant_id,id),
 CONSTRAINT fk_return_client FOREIGN KEY (tenant_id,client_id) REFERENCES clients(tenant_id,id),
 CONSTRAINT fk_return_actor FOREIGN KEY (created_by_user_id) REFERENCES users(id),
 CONSTRAINT ck_return_amount CHECK (amount_minor BETWEEN 1 AND 100000000000000)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS invoice_return_items (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 return_id BIGINT UNSIGNED NOT NULL,
 invoice_id BIGINT UNSIGNED NOT NULL,
 product_id BIGINT UNSIGNED NOT NULL,
 title VARCHAR(150) NOT NULL,
 code VARCHAR(80) NOT NULL,
 pieces_per_unit BIGINT NOT NULL,
 quantity BIGINT NOT NULL,
 units_per_package BIGINT NOT NULL,
 package_count BIGINT NOT NULL,
 unit_price_minor BIGINT NOT NULL,
 total_minor BIGINT NOT NULL,
 CONSTRAINT fk_return_item_return FOREIGN KEY (tenant_id,return_id) REFERENCES invoice_returns(tenant_id,id),
 CONSTRAINT fk_return_item_invoice FOREIGN KEY (tenant_id,invoice_id) REFERENCES invoices(tenant_id,id),
 CONSTRAINT fk_return_item_product FOREIGN KEY (tenant_id,product_id) REFERENCES products(tenant_id,id),
 CONSTRAINT ck_return_item_numbers CHECK (quantity BETWEEN 1 AND 1000000000 AND pieces_per_unit BETWEEN 1 AND 1000000 AND units_per_package BETWEEN 1 AND 1000000 AND package_count BETWEEN 1 AND 1000000000 AND unit_price_minor BETWEEN 0 AND 1000000000000 AND total_minor BETWEEN 0 AND 100000000000000)
) ENGINE=InnoDB;

ALTER TABLE stock_movements ADD COLUMN return_id BIGINT UNSIGNED NULL AFTER invoice_id;
ALTER TABLE stock_movements DROP INDEX uq_stock_invoice;
ALTER TABLE stock_movements ADD INDEX idx_stock_return_ref (tenant_id,return_id);
ALTER TABLE stock_movements ADD UNIQUE KEY uq_stock_return (return_id,product_id);
ALTER TABLE stock_movements ADD CONSTRAINT fk_stock_return FOREIGN KEY (tenant_id,return_id) REFERENCES invoice_returns(tenant_id,id);
ALTER TABLE stock_movements DROP CHECK ck_stock_kind;
ALTER TABLE stock_movements ADD CONSTRAINT ck_stock_kind CHECK (kind IN ('opening','adjustment','sale','void','purchase','reactivate','purchase_reactivate','return'));

ALTER TABLE client_ledger ADD COLUMN return_id BIGINT UNSIGNED NULL AFTER invoice_id;
ALTER TABLE client_ledger DROP INDEX uq_ledger_invoice;
ALTER TABLE client_ledger ADD INDEX idx_ledger_return_ref (tenant_id,return_id);
ALTER TABLE client_ledger ADD UNIQUE KEY uq_ledger_return (return_id);
ALTER TABLE client_ledger ADD CONSTRAINT fk_ledger_return FOREIGN KEY (tenant_id,return_id) REFERENCES invoice_returns(tenant_id,id);
ALTER TABLE client_ledger DROP CHECK ck_ledger_kind;
ALTER TABLE client_ledger ADD CONSTRAINT ck_ledger_kind CHECK (kind IN ('invoice','void','purchase','purchase_void','reactivate','purchase_reactivate','return'));
