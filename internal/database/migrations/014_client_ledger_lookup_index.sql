-- Client list/detail queries aggregate ledger rows by tenant and client.
-- Without this index, MySQL may scan the entire ledger once per client.
ALTER TABLE client_ledger
  ADD INDEX idx_ledger_client (tenant_id, client_id);
