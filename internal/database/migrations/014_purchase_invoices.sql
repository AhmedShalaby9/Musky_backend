ALTER TABLE invoices ADD COLUMN document_type VARCHAR(20) NOT NULL DEFAULT 'sale' AFTER status;
ALTER TABLE invoices DROP CHECK ck_invoice_status;
ALTER TABLE invoices ADD CONSTRAINT ck_invoice_status CHECK (status IN ('draft','posted','void','cancelled'));
ALTER TABLE invoices ADD CONSTRAINT ck_invoice_document_type CHECK (document_type IN ('sale','purchase'));
ALTER TABLE stock_movements DROP CHECK ck_stock_kind;
ALTER TABLE stock_movements ADD CONSTRAINT ck_stock_kind CHECK (kind IN ('opening','adjustment','sale','void','purchase','reactivate','purchase_reactivate'));
ALTER TABLE client_ledger DROP CHECK ck_ledger_kind;
ALTER TABLE client_ledger ADD CONSTRAINT ck_ledger_kind CHECK (kind IN ('invoice','void','purchase','purchase_void','reactivate','purchase_reactivate'));
