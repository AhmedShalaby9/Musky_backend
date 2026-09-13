CREATE TABLE IF NOT EXISTS file_objects (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 tenant_id BIGINT UNSIGNED NOT NULL,
 uploaded_by_user_id BIGINT UNSIGNED NOT NULL,
 object_key VARCHAR(512) NOT NULL,
 original_name VARCHAR(255) NOT NULL,
 content_type VARCHAR(150) NOT NULL,
 size_bytes BIGINT UNSIGNED NOT NULL,
 public_url VARCHAR(1000) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_file_key (object_key),
 CONSTRAINT fk_file_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id),
 CONSTRAINT fk_file_user FOREIGN KEY (uploaded_by_user_id) REFERENCES users(id),
 CONSTRAINT ck_file_size CHECK (size_bytes BETWEEN 1 AND 52428800)
) ENGINE=InnoDB;
