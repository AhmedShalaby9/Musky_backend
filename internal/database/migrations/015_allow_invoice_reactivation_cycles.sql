-- An invoice may be voided and later reactivated more than once.
-- Keep every stock and client-ledger event instead of blocking the second cycle
-- with the original one-row-per-invoice uniqueness constraints.
ALTER TABLE stock_movements
  DROP INDEX uq_stock_invoice;

ALTER TABLE client_ledger
  DROP INDEX uq_ledger_invoice;
