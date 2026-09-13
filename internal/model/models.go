package model

import "time"

type Role string

const (
	SuperAdmin Role = "super_admin"
	Admin      Role = "admin"
	Trader     Role = "trader"
)

type Tenant struct {
	ID        uint64    `json:"id"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	LogoURL   string    `json:"logo_url"`
	CreatedAt time.Time `json:"created_at"`
}
type User struct {
	ID           uint64    `json:"id"`
	TenantID     *uint64   `json:"tenant_id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	Role         Role      `json:"role"`
	Active       bool      `json:"active"`
	LogoURL      string    `json:"logo_url,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Clients are business contacts, never login accounts.
type Client struct {
	ID        uint64    `json:"id"`
	TenantID  uint64    `json:"tenant_id"`
	UserID    uint64    `json:"user_id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Address   string    `json:"address"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}
