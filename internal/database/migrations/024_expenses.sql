CREATE TABLE IF NOT EXISTS expenses (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 created_by_user_id BIGINT UNSIGNED NOT NULL,
 amount_minor BIGINT NOT NULL,
 description VARCHAR(500) NOT NULL,
 spent_at DATE NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 CONSTRAINT fk_expense_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id),
 CONSTRAINT fk_expense_user FOREIGN KEY (tenant_id, created_by_user_id) REFERENCES users(tenant_id, id),
 CONSTRAINT chk_expense_amount CHECK (amount_minor >= 1 AND amount_minor <= 1000000000000),
 INDEX idx_expenses_tenant_date (tenant_id, spent_at)
) ENGINE=InnoDB;
