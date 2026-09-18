-- Keep invoices after a client is permanently deleted. The cleanup endpoint
-- explicitly sets client_id to NULL before deleting the client, so the
-- existing composite foreign keys can remain in place. This is restart-safe
-- if an earlier deployment stopped after partially applying this migration.
ALTER TABLE invoices MODIFY client_id BIGINT UNSIGNED NULL;
ALTER TABLE invoice_payments MODIFY client_id BIGINT UNSIGNED NULL;
