-- Remove the explicitly requested test clients and all data owned by them.
-- Client user accounts and products are intentionally retained.

CREATE TEMPORARY TABLE purge_clients AS
SELECT id AS client_id, tenant_id
FROM clients
WHERE LOWER(TRIM(name)) = 'test'
   OR TRIM(name) = 'أحمد جمال شلبي';

CREATE TEMPORARY TABLE purge_invoices AS
SELECT id AS invoice_id, tenant_id
FROM invoices
WHERE EXISTS (
  SELECT 1
  FROM purge_clients c
  WHERE c.tenant_id = invoices.tenant_id
    AND c.client_id = invoices.client_id
);

CREATE TEMPORARY TABLE purge_receipts AS
SELECT r.id AS receipt_id
FROM client_receipts r
JOIN purge_clients c
  ON c.tenant_id = r.tenant_id
 AND c.client_id = r.client_id;

-- Undo posted/voided stock movements before removing the movement history.
UPDATE products p
JOIN (
  SELECT sm.tenant_id, sm.product_id, SUM(sm.quantity_delta) AS delta
  FROM stock_movements sm
  JOIN purge_invoices i
    ON i.tenant_id = sm.tenant_id
   AND i.invoice_id = sm.invoice_id
  GROUP BY sm.tenant_id, sm.product_id
) movements
  ON movements.tenant_id = p.tenant_id
 AND movements.product_id = p.id
SET p.quantity = p.quantity - movements.delta;

-- Detach any receipt reversals that point at receipts being removed.
UPDATE client_receipts r
JOIN purge_receipts removed ON removed.receipt_id = r.reversal_of_id
SET r.reversal_of_id = NULL;

-- Generated invoice PDFs are stored as file records keyed by invoice ID.
DELETE f
FROM file_objects f
JOIN purge_invoices i
  ON f.object_key = CONCAT('tenants/', i.tenant_id, '/invoices/', i.invoice_id, '/invoice.pdf');

DELETE p
FROM invoice_payments p
JOIN purge_invoices i
  ON i.tenant_id = p.tenant_id
 AND i.invoice_id = p.invoice_id;

DELETE l
FROM client_ledger l
JOIN purge_invoices i
  ON i.tenant_id = l.tenant_id
 AND i.invoice_id = l.invoice_id;

DELETE sm
FROM stock_movements sm
JOIN purge_invoices i
  ON i.tenant_id = sm.tenant_id
 AND i.invoice_id = sm.invoice_id;

DELETE ii
FROM invoice_items ii
JOIN purge_invoices i
  ON i.tenant_id = ii.tenant_id
 AND i.invoice_id = ii.invoice_id;

DELETE r
FROM client_receipts r
JOIN purge_clients c
  ON c.tenant_id = r.tenant_id
 AND c.client_id = r.client_id;

DELETE i
FROM invoices i
JOIN purge_invoices removed
  ON removed.tenant_id = i.tenant_id
 AND removed.invoice_id = i.id;

DELETE c
FROM clients c
JOIN purge_clients removed
  ON removed.tenant_id = c.tenant_id
 AND removed.client_id = c.id;

DROP TEMPORARY TABLE purge_receipts;
DROP TEMPORARY TABLE purge_invoices;
DROP TEMPORARY TABLE purge_clients;
