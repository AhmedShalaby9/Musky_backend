CREATE TABLE IF NOT EXISTS china_entries (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 created_by_user_id BIGINT UNSIGNED NOT NULL,
 kind VARCHAR(20) NOT NULL,
 amount_minor BIGINT NOT NULL,
 notes VARCHAR(500) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 CONSTRAINT fk_china_entry_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id),
 CONSTRAINT fk_china_entry_user FOREIGN KEY (tenant_id, created_by_user_id) REFERENCES users(tenant_id, id),
 CONSTRAINT chk_china_kind CHECK (kind IN ('opening', 'purchase', 'payment', 'adjustment')),
 CONSTRAINT chk_china_amount CHECK (amount_minor >= 1 AND amount_minor <= 1000000000000),
 INDEX idx_china_entries_tenant (tenant_id, created_at)
) ENGINE=InnoDB;
