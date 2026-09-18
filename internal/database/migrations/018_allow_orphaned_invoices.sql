-- Invoices keep their snapshot fields when a client is permanently deleted.
ALTER TABLE invoices DROP FOREIGN KEY fk_invoice_client;
ALTER TABLE invoices MODIFY client_id BIGINT UNSIGNED NULL;
ALTER TABLE invoices ADD CONSTRAINT fk_invoice_client FOREIGN KEY (tenant_id, client_id) REFERENCES clients(tenant_id, id) ON DELETE SET NULL;

-- Payments are removable during client cleanup, but existing rows must also
-- tolerate an orphaned invoice owner if cleanup is performed in stages.
ALTER TABLE invoice_payments DROP FOREIGN KEY fk_payment_client;
ALTER TABLE invoice_payments MODIFY client_id BIGINT UNSIGNED NULL;
ALTER TABLE invoice_payments ADD CONSTRAINT fk_payment_client FOREIGN KEY (tenant_id, client_id) REFERENCES clients(tenant_id, id) ON DELETE SET NULL;
