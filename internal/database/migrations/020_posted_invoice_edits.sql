-- Posted invoices can be edited in place. Each edit records the stock
-- difference per product ('edit') and the total difference on the client's
-- account ('adjust' / 'purchase_adjust'), linked to the invoice.
ALTER TABLE stock_movements DROP CHECK ck_stock_kind;
ALTER TABLE stock_movements ADD CONSTRAINT ck_stock_kind CHECK (kind IN ('opening','adjustment','sale','void','purchase','reactivate','purchase_reactivate','return','edit'));
ALTER TABLE client_ledger DROP CHECK ck_ledger_kind;
ALTER TABLE client_ledger ADD CONSTRAINT ck_ledger_kind CHECK (kind IN ('invoice','void','purchase','purchase_void','reactivate','purchase_reactivate','return','adjust','purchase_adjust'));
