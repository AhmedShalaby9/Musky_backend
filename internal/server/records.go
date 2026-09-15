package server

import (
	"musky/backend/internal/model"
	"time"
)

// Persistence records explicitly name tables that differ from GORM conventions.
// Existing versioned SQL migrations remain the authority for database constraints.
type sessionRecord struct {
	TokenHash string `gorm:"primaryKey"`
	UserID    uint64
	ExpiresAt time.Time
}

func (sessionRecord) TableName() string { return "sessions" }

type fileRecord struct {
	ID               uint64
	TenantID         uint64
	UploadedByUserID uint64
	ObjectKey        string
	OriginalName     string
	ContentType      string
	SizeBytes        int64
	PublicURL        string
	CreatedAt        time.Time
}

func (fileRecord) TableName() string { return "file_objects" }

type invoiceItemRecord struct {
	ID                uint64
	TenantID          uint64
	InvoiceID         uint64
	model.InvoiceItem `gorm:"embedded"`
}

func (invoiceItemRecord) TableName() string { return "invoice_items" }

type paymentRecord struct {
	model.Payment    `gorm:"embedded"`
	TenantID         uint64
	InvoiceID        uint64
	ClientID         uint64
	ReceivedByUserID uint64
}

func (paymentRecord) TableName() string { return "invoice_payments" }

type stockMovementRecord struct {
	ID              uint64
	TenantID        uint64
	ProductID       uint64
	InvoiceID       *uint64
	CreatedByUserID uint64
	Kind            string
	QuantityDelta   int64
}

func (stockMovementRecord) TableName() string { return "stock_movements" }

type clientLedgerRecord struct {
	ID          uint64
	TenantID    uint64
	ClientID    uint64
	InvoiceID   uint64
	Kind        string
	AmountMinor int64
}

func (clientLedgerRecord) TableName() string { return "client_ledger" }

type invoiceCounterRecord struct {
	TenantID   uint64 `gorm:"primaryKey;autoIncrement:false"`
	NextNumber int64
}

func (invoiceCounterRecord) TableName() string { return "invoice_counters" }
