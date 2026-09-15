-- Remove the explicitly requested test clients and all data owned by them.
-- Client user accounts and products are intentionally retained.
-- Uses inline JOINs instead of temporary tables (avoids CREATE TEMPORARY TABLES privilege).

-- Undo posted/voided stock movements before removing the movement history.
UPDATE products p
JOIN (
  SELECT sm.tenant_id, sm.product_id, SUM(sm.quantity_delta) AS delta
  FROM stock_movements sm
  JOIN invoices i ON i.tenant_id = sm.tenant_id AND i.id = sm.invoice_id
  JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
  WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي'
  GROUP BY sm.tenant_id, sm.product_id
) movements ON movements.tenant_id = p.tenant_id AND movements.product_id = p.id
SET p.quantity = p.quantity - movements.delta;

-- Detach any receipt reversals that point at receipts being removed.
UPDATE client_receipts r
JOIN client_receipts orig ON orig.id = r.reversal_of_id
JOIN clients c ON c.tenant_id = orig.tenant_id AND c.id = orig.client_id
SET r.reversal_of_id = NULL
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

-- Generated invoice PDFs are stored as file records keyed by invoice ID.
DELETE f
FROM file_objects f
JOIN invoices i ON f.object_key = CONCAT('tenants/', i.tenant_id, '/invoices/', i.id, '/invoice.pdf')
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE p
FROM invoice_payments p
JOIN invoices i ON i.tenant_id = p.tenant_id AND i.id = p.invoice_id
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE l
FROM client_ledger l
JOIN invoices i ON i.tenant_id = l.tenant_id AND i.id = l.invoice_id
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE sm
FROM stock_movements sm
JOIN invoices i ON i.tenant_id = sm.tenant_id AND i.id = sm.invoice_id
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE ii
FROM invoice_items ii
JOIN invoices i ON i.tenant_id = ii.tenant_id AND i.id = ii.invoice_id
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE r
FROM client_receipts r
JOIN clients c ON c.tenant_id = r.tenant_id AND c.id = r.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE i
FROM invoices i
JOIN clients c ON c.tenant_id = i.tenant_id AND c.id = i.client_id
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';

DELETE c
FROM clients c
WHERE LOWER(TRIM(c.name)) = 'test' OR TRIM(c.name) = 'أحمد جمال شلبي';
