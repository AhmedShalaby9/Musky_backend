package server

import (
	"github.com/gin-gonic/gin"
	"time"
)

type dailyJournalPayment struct {
	ID            uint64    `json:"id"`
	Kind          string    `json:"kind"`
	InvoiceID     *uint64   `json:"invoice_id"`
	InvoiceNumber *int64    `json:"invoice_number"`
	DocumentType  string    `json:"document_type,omitempty"`
	ClientID      uint64    `json:"client_id"`
	ClientName    string    `json:"client_name"`
	AmountMinor   int64     `json:"amount_minor"`
	Method        string    `json:"method"`
	Notes         string    `json:"notes"`
	PaidAt        time.Time `json:"paid_at"`
}

func (a *API) dailyJournal(c *gin.Context) {
	dateText := c.DefaultQuery("date", time.Now().In(cairoLocation()).Format("2006-01-02"))
	day, err := time.ParseInLocation("2006-01-02", dateText, cairoLocation())
	if err != nil {
		fail(c, 400, "date must use YYYY-MM-DD")
		return
	}
	start, end := day.UTC(), day.AddDate(0, 0, 1).UTC()
	db := a.orm.WithContext(c.Request.Context())
	payments := db.Table("invoice_payments p").Select("p.id, 'invoice_payment' AS kind, p.invoice_id, i.number AS invoice_number, i.document_type, c.id AS client_id, c.name AS client_name, p.amount_minor, p.method, COALESCE(p.notes,'') AS notes, p.paid_at").
		Joins("JOIN invoices i ON i.id=p.invoice_id AND i.tenant_id=p.tenant_id").
		Joins("JOIN clients c ON c.id=p.client_id AND c.tenant_id=p.tenant_id").
		Where("p.tenant_id = ? AND p.paid_at >= ? AND p.paid_at < ?", tenantID(c), start, end)
	receipts := db.Table("client_receipts r").Select("r.id, 'client_receipt' AS kind, NULL AS invoice_id, NULL AS invoice_number, '' AS document_type, c.id AS client_id, c.name AS client_name, r.amount_minor, r.method, COALESCE(r.notes,'') AS notes, r.received_at AS paid_at").
		Joins("JOIN clients c ON c.id=r.client_id AND c.tenant_id=r.tenant_id").
		Where("r.tenant_id = ? AND r.received_at >= ? AND r.received_at < ? AND r.reversal_of_id IS NULL", tenantID(c), start, end)
	var rows []dailyJournalPayment
	err = db.Table("(? UNION ALL ?) journal", payments, receipts).Order("paid_at,id").Scan(&rows).Error
	if err != nil {
		databaseError(c, err)
		return
	}
	items := make([]dailyJournalPayment, 0, len(rows))
	var all, cash, online int64
	for _, item := range rows {
		items = append(items, item)
		all += item.AmountMinor
		if item.Method == "cash" {
			cash += item.AmountMinor
		} else if item.Method == "online" {
			online += item.AmountMinor
		}
	}
	c.JSON(200, gin.H{"date": dateText, "currency": "EGP", "totals": gin.H{"all_minor": all, "cash_minor": cash, "online_minor": online}, "data": items})
}

func cairoLocation() *time.Location {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		return time.UTC
	}
	return loc
}
