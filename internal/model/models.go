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
	ID                  uint64  `json:"id"`
	TenantID            uint64  `json:"tenant_id"`
	UserID              uint64  `json:"user_id"`
	Name                string  `json:"name"`
	Phone               *string `json:"phone"`
	Address             string  `json:"address"`
	Active              bool    `json:"active"`
	OpeningBalanceMinor int64   `json:"opening_balance_minor"`
	// BalanceMinor is computed: opening_balance_minor + posted invoices − payments.
	// Positive = client owes us; negative = we owe client.
	BalanceMinor int64 `json:"balance_minor"`
	// LastPaymentAt is the most recent invoice payment or general receipt date.
	LastPaymentAt *time.Time `json:"last_payment_at"`
	CreatedAt     time.Time  `json:"created_at"`
}
