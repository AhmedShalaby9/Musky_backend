CREATE TABLE IF NOT EXISTS client_receipts (
  id                   BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  tenant_id            BIGINT UNSIGNED NOT NULL,
  client_id            BIGINT UNSIGNED NOT NULL,
  received_by_user_id  BIGINT UNSIGNED NOT NULL,
  amount_minor         BIGINT NOT NULL,
  method               VARCHAR(20) NOT NULL DEFAULT 'cash',
  notes                VARCHAR(500) NOT NULL DEFAULT '',
  reversal_of_id       BIGINT UNSIGNED NULL,
  received_at          DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  created_at           DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_receipt_client FOREIGN KEY (tenant_id, client_id)
    REFERENCES clients(tenant_id, id),
  CONSTRAINT fk_receipt_user FOREIGN KEY (received_by_user_id)
    REFERENCES users(id),
  CONSTRAINT fk_receipt_reversal FOREIGN KEY (reversal_of_id)
    REFERENCES client_receipts(id),
  CONSTRAINT ck_receipt_method CHECK (method IN ('cash','online')),
  CONSTRAINT ck_receipt_amount CHECK (amount_minor BETWEEN 1 AND 1000000000000),
  INDEX idx_receipt_client (tenant_id, client_id)
) ENGINE=InnoDB
