package model

import "time"

type ClientReceipt struct {
	ID               uint64    `json:"id"`
	TenantID         uint64    `json:"tenant_id"`
	ClientID         uint64    `json:"client_id"`
	ReceivedByUserID uint64    `json:"received_by_user_id"`
	AmountMinor      int64     `json:"amount_minor"`
	Direction        string    `json:"direction"`
	Method           string    `json:"method"`
	Notes            string    `json:"notes"`
	ReversalOfID     *uint64   `json:"reversal_of_id"`
	ReceivedAt       time.Time `json:"received_at" gorm:"default:CURRENT_TIMESTAMP(6)"`
	CreatedAt        time.Time `json:"created_at"`
}

type LedgerEntry struct {
	Kind           string    `json:"kind"`
	RefID          *uint64   `json:"ref_id"`
	ReturnID       *uint64   `json:"-"`
	InvoiceNumber  *int64    `json:"invoice_number"`
	DocumentType   string    `json:"document_type,omitempty"`
	DeltaMinor     int64     `json:"delta_minor"`
	RunningBalance int64     `json:"running_balance"`
	Method         *string   `json:"method"`
	Notes          string    `json:"notes"`
	At             time.Time `json:"at"`
}

// Product quantities are measured in whole boxes/packs, not pieces.
type Product struct {
	ID            uint64    `json:"id"`
	TenantID      uint64    `json:"tenant_id"`
	Title         string    `json:"title"`
	Code          string    `json:"code"`
	Quantity      int64     `json:"quantity"`
	PiecesPerUnit int64     `json:"pieces_per_unit"`
	Active        bool      `json:"active"`
	Version       int64     `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
}
type Invoice struct {
	ID              uint64          `json:"id"`
	TenantID        uint64          `json:"tenant_id"`
	ClientID        uint64          `json:"client_id"`
	CreatedByUserID uint64          `json:"created_by_user_id"`
	Number          *int64          `json:"number"`
	Status          string          `json:"status"`
	DocumentType    string          `json:"document_type"`
	Currency        string          `json:"currency"`
	IssueDate       string          `json:"issue_date"`
	ClientName      string          `json:"client_name"`
	ClientAddress   string          `json:"client_address"`
	Notes           string          `json:"notes"`
	VoidReason      string          `json:"void_reason"`
	TotalMinor      int64           `json:"total_minor"`
	ReturnedMinor   int64           `json:"returned_minor" gorm:"->;-:migration"`
	PaidMinor       int64           `json:"paid_minor" gorm:"->;-:migration"`
	RemainingMinor  int64           `json:"remaining_minor" gorm:"->;-:migration"`
	PaymentStatus   string          `json:"payment_status" gorm:"->;-:migration"`
	Version         int64           `json:"version"`
	CreatedAt       time.Time       `json:"created_at"`
	PostedAt        *time.Time      `json:"posted_at"`
	VoidedAt        *time.Time      `json:"voided_at"`
	PdfURL          string          `json:"pdf_url"`
	Items           []InvoiceItem   `json:"items,omitempty" gorm:"-"`
	Payments        []Payment       `json:"payments,omitempty" gorm:"-"`
	Returns         []InvoiceReturn `json:"returns,omitempty" gorm:"-"`
}
type Payment struct {
	ID          uint64    `json:"id"`
	AmountMinor int64     `json:"amount_minor"`
	Method      string    `json:"method"`
	Notes       string    `json:"notes"`
	PaidAt      time.Time `json:"paid_at"`
}
type InvoiceItem struct {
	ProductID       uint64 `json:"product_id"`
	Title           string `json:"title"`
	Code            string `json:"code"`
	PiecesPerUnit   int64  `json:"pieces_per_unit"`
	Quantity        int64  `json:"quantity"`
	UnitsPerPackage int64  `json:"units_per_package"`
	PackageCount    int64  `json:"package_count"`
	UnitPriceMinor  int64  `json:"unit_price_minor"`
	TotalMinor      int64  `json:"total_minor"`
}

type InvoiceReturn struct {
	ID              uint64              `json:"id"`
	TenantID        uint64              `json:"tenant_id"`
	InvoiceID       uint64              `json:"invoice_id"`
	ClientID        uint64              `json:"client_id"`
	CreatedByUserID uint64              `json:"created_by_user_id"`
	AmountMinor     int64               `json:"amount_minor"`
	Reason          string              `json:"reason"`
	CreatedAt       time.Time           `json:"created_at"`
	Items           []InvoiceReturnItem `json:"items,omitempty" gorm:"-"`
}

type InvoiceReturnItem struct {
	ProductID       uint64 `json:"product_id"`
	Title           string `json:"title"`
	Code            string `json:"code"`
	PiecesPerUnit   int64  `json:"pieces_per_unit"`
	Quantity        int64  `json:"quantity"`
	UnitsPerPackage int64  `json:"units_per_package"`
	PackageCount    int64  `json:"package_count"`
	UnitPriceMinor  int64  `json:"unit_price_minor"`
	TotalMinor      int64  `json:"total_minor"`
}
