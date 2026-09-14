package server

import (
	"database/sql"
	"time"

	"github.com/gin-gonic/gin"
)

type dailyJournalPayment struct {
	ID            uint64    `json:"id"`
	Kind          string    `json:"kind"`
	InvoiceID     *uint64   `json:"invoice_id"`
	InvoiceNumber *int64    `json:"invoice_number"`
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
	rows, err := a.db.QueryContext(c.Request.Context(), `
		SELECT id, kind, invoice_id, invoice_number, client_name, amount_minor, method, notes, paid_at
		FROM (
		  SELECT p.id, 'invoice_payment' AS kind, p.invoice_id, i.number AS invoice_number, c.name AS client_name,
		         p.amount_minor, p.method, COALESCE(p.notes,''), p.paid_at
		FROM invoice_payments p
		JOIN invoices i ON i.id=p.invoice_id AND i.tenant_id=p.tenant_id
		JOIN clients c ON c.id=p.client_id AND c.tenant_id=p.tenant_id
		WHERE p.tenant_id=? AND p.paid_at>=? AND p.paid_at<?
		  UNION ALL
		  SELECT r.id, 'client_receipt', NULL, NULL, c.name, r.amount_minor, r.method, COALESCE(r.notes,''), r.received_at
		  FROM client_receipts r JOIN clients c ON c.id=r.client_id AND c.tenant_id=r.tenant_id
		  WHERE r.tenant_id=? AND r.received_at>=? AND r.received_at<? AND r.reversal_of_id IS NULL
		) journal ORDER BY paid_at, id`, tenantID(c), start, end, tenantID(c), start, end)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer rows.Close()
	items := make([]dailyJournalPayment, 0)
	var all, cash, online int64
	for rows.Next() {
		var item dailyJournalPayment
		var invoiceID, number sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Kind, &invoiceID, &number, &item.ClientName, &item.AmountMinor, &item.Method, &item.Notes, &item.PaidAt); err != nil {
			databaseError(c, err)
			return
		}
		if number.Valid {
			item.InvoiceNumber = &number.Int64
		}
		if invoiceID.Valid {
			id := uint64(invoiceID.Int64)
			item.InvoiceID = &id
		}
		items = append(items, item)
		all += item.AmountMinor
		if item.Method == "cash" {
			cash += item.AmountMinor
		} else if item.Method == "online" {
			online += item.AmountMinor
		}
	}
	if err := rows.Err(); err != nil {
		databaseError(c, err)
		return
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
