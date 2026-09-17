ALTER TABLE clients ADD COLUMN opening_balance_type VARCHAR(20) NOT NULL DEFAULT 'receivable' AFTER opening_balance_minor;
UPDATE clients SET opening_balance_type = 'payable', opening_balance_minor = -opening_balance_minor WHERE opening_balance_minor < 0;
ALTER TABLE clients ADD CONSTRAINT ck_client_opening_balance_type CHECK (opening_balance_type IN ('receivable','payable'));
