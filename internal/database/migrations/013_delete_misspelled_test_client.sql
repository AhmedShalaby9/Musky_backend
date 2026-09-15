-- The live test record uses "احمد" without the hamza.
CREATE TEMPORARY TABLE purge_clients_013 AS
SELECT id AS client_id, tenant_id
FROM clients
WHERE TRIM(name) = 'احمد جمال شلبي';

CREATE TEMPORARY TABLE purge_invoices_013 AS
SELECT i.id AS invoice_id, i.tenant_id
FROM invoices i
JOIN purge_clients_013 c
  ON c.tenant_id = i.tenant_id
 AND c.client_id = i.client_id;

CREATE TEMPORARY TABLE purge_receipts_013 AS
SELECT r.id AS receipt_id
FROM client_receipts r
JOIN purge_clients_013 c
  ON c.tenant_id = r.tenant_id
 AND c.client_id = r.client_id;

UPDATE products p
JOIN (
  SELECT sm.tenant_id, sm.product_id, SUM(sm.quantity_delta) AS delta
  FROM stock_movements sm
  JOIN purge_invoices_013 i
    ON i.tenant_id = sm.tenant_id
   AND i.invoice_id = sm.invoice_id
  GROUP BY sm.tenant_id, sm.product_id
) movements
  ON movements.tenant_id = p.tenant_id
 AND movements.product_id = p.id
SET p.quantity = p.quantity - movements.delta;

UPDATE client_receipts r
JOIN purge_receipts_013 removed ON removed.receipt_id = r.reversal_of_id
SET r.reversal_of_id = NULL;

DELETE f
FROM file_objects f
JOIN purge_invoices_013 i
  ON f.object_key = CONCAT('tenants/', i.tenant_id, '/invoices/', i.invoice_id, '/invoice.pdf');

DELETE p
FROM invoice_payments p
JOIN purge_invoices_013 i
  ON i.tenant_id = p.tenant_id AND i.invoice_id = p.invoice_id;

DELETE l
FROM client_ledger l
JOIN purge_invoices_013 i
  ON i.tenant_id = l.tenant_id AND i.invoice_id = l.invoice_id;

DELETE sm
FROM stock_movements sm
JOIN purge_invoices_013 i
  ON i.tenant_id = sm.tenant_id AND i.invoice_id = sm.invoice_id;

DELETE ii
FROM invoice_items ii
JOIN purge_invoices_013 i
  ON i.tenant_id = ii.tenant_id AND i.invoice_id = ii.invoice_id;

DELETE r
FROM client_receipts r
JOIN purge_clients_013 c
  ON c.tenant_id = r.tenant_id AND c.client_id = r.client_id;

DELETE i
FROM invoices i
JOIN purge_invoices_013 removed
  ON removed.tenant_id = i.tenant_id AND removed.invoice_id = i.id;

DELETE c
FROM clients c
JOIN purge_clients_013 removed
  ON removed.tenant_id = c.tenant_id AND removed.client_id = c.id;

DROP TEMPORARY TABLE purge_receipts_013;
DROP TEMPORARY TABLE purge_invoices_013;
DROP TEMPORARY TABLE purge_clients_013;
