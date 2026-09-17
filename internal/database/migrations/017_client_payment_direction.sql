ALTER TABLE client_receipts ADD COLUMN direction VARCHAR(10) NOT NULL DEFAULT 'in' AFTER amount_minor;
ALTER TABLE client_receipts ADD CONSTRAINT ck_receipt_direction CHECK (direction IN ('in','out'));
