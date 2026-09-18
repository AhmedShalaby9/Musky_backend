-- Remove the requested test client and every transaction owned by that client.
CREATE TEMPORARY TABLE purge_ahmed_allam_invoices AS
SELECT i.id AS invoice_id, i.tenant_id
FROM invoices i
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE TRIM(c.name) IN ('أحمد علام','احمد علام','Ahmed Allam','ahmed allam');

CREATE TEMPORARY TABLE purge_ahmed_allam_receipts AS
SELECT r.id AS receipt_id
FROM client_receipts r
JOIN clients c ON c.tenant_id = r.tenant_id AND c.id = r.client_id
WHERE TRIM(c.name) IN ('أحمد علام','احمد علام','Ahmed Allam','ahmed allam');

UPDATE products p
JOIN (
  SELECT sm.tenant_id, sm.product_id, SUM(sm.quantity_delta) AS delta
  FROM stock_movements sm
  JOIN purge_ahmed_allam_invoices i ON i.tenant_id = sm.tenant_id AND i.invoice_id = sm.invoice_id
  GROUP BY sm.tenant_id, sm.product_id
) movements ON movements.tenant_id = p.tenant_id AND movements.product_id = p.id
SET p.quantity = p.quantity - movements.delta;

UPDATE client_receipts r
JOIN purge_ahmed_allam_receipts removed ON removed.receipt_id = r.reversal_of_id
SET r.reversal_of_id = NULL;

DELETE f FROM file_objects f
JOIN purge_ahmed_allam_invoices i ON f.object_key = CONCAT('tenants/', i.tenant_id, '/invoices/', i.invoice_id, '/invoice.pdf');
DELETE p FROM invoice_payments p JOIN purge_ahmed_allam_invoices i ON i.tenant_id = p.tenant_id AND i.invoice_id = p.invoice_id;
DELETE l FROM client_ledger l JOIN purge_ahmed_allam_invoices i ON i.tenant_id = l.tenant_id AND i.invoice_id = l.invoice_id;
DELETE sm FROM stock_movements sm JOIN purge_ahmed_allam_invoices i ON i.tenant_id = sm.tenant_id AND i.invoice_id = sm.invoice_id;
DELETE ii FROM invoice_items ii JOIN purge_ahmed_allam_invoices i ON i.tenant_id = ii.tenant_id AND i.invoice_id = ii.invoice_id;
DELETE r FROM client_receipts r JOIN purge_ahmed_allam_receipts removed ON removed.receipt_id = r.id;
DELETE i FROM invoices i JOIN purge_ahmed_allam_invoices removed ON removed.tenant_id = i.tenant_id AND removed.invoice_id = i.id;
DELETE c FROM clients c WHERE TRIM(c.name) IN ('أحمد علام','احمد علام','Ahmed Allam','ahmed allam');

DROP TEMPORARY TABLE purge_ahmed_allam_receipts;
DROP TEMPORARY TABLE purge_ahmed_allam_invoices;
