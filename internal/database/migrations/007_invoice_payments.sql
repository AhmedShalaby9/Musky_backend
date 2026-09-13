CREATE TABLE IF NOT EXISTS invoice_payments (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 invoice_id BIGINT UNSIGNED NOT NULL,
 client_id BIGINT UNSIGNED NOT NULL,
 received_by_user_id BIGINT UNSIGNED NOT NULL,
 amount_minor BIGINT NOT NULL,
 method VARCHAR(20) NOT NULL,
 notes VARCHAR(500) NOT NULL DEFAULT '',
 paid_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 CONSTRAINT fk_payment_invoice FOREIGN KEY (tenant_id,invoice_id) REFERENCES invoices(tenant_id,id),
 CONSTRAINT fk_payment_client FOREIGN KEY (tenant_id,client_id) REFERENCES clients(tenant_id,id),
 CONSTRAINT fk_payment_user FOREIGN KEY (received_by_user_id) REFERENCES users(id),
 CONSTRAINT ck_payment_method CHECK (method IN ('cash','online')),
 CONSTRAINT ck_payment_amount CHECK (amount_minor BETWEEN 1 AND 1000000000000),
 INDEX idx_payment_invoice (tenant_id,invoice_id),
 INDEX idx_payment_client (tenant_id,client_id)
) ENGINE=InnoDB;
