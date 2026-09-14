package server

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
	"strconv"
	"strings"
	"time"
)

const invoiceColumns = "id,tenant_id,client_id,created_by_user_id,number,status,currency,DATE_FORMAT(issue_date,'%Y-%m-%d'),client_name,client_address,notes,void_reason,total_minor,version,created_at,posted_at,voided_at,COALESCE(pdf_url,'')"
const maxPrice int64 = 1000000000000

func scanInvoice(row scanner) (model.Invoice, error) {
	var v model.Invoice
	err := row.Scan(&v.ID, &v.TenantID, &v.ClientID, &v.CreatedByUserID, &v.Number, &v.Status, &v.Currency, &v.IssueDate, &v.ClientName, &v.ClientAddress, &v.Notes, &v.VoidReason, &v.TotalMinor, &v.Version, &v.CreatedAt, &v.PostedAt, &v.VoidedAt, &v.PdfURL)
	return v, err
}
func readInvoice(ctx context.Context, tx *sql.Tx, tenant, id uint64, lock bool) (model.Invoice, error) {
	query := "SELECT " + invoiceColumns + " FROM invoices WHERE tenant_id=? AND id=?"
	if lock {
		query += " FOR UPDATE"
	}
	v, err := scanInvoice(tx.QueryRowContext(ctx, query, tenant, id))
	if err != nil {
		return v, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT product_id,title,code,pieces_per_unit,quantity,unit_price_minor,total_minor FROM invoice_items WHERE tenant_id=? AND invoice_id=? ORDER BY id", tenant, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	v.Items = []model.InvoiceItem{}
	for rows.Next() {
		var item model.InvoiceItem
		if err = rows.Scan(&item.ProductID, &item.Title, &item.Code, &item.PiecesPerUnit, &item.Quantity, &item.UnitPriceMinor, &item.TotalMinor); err != nil {
			return v, err
		}
		v.Items = append(v.Items, item)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	v.Payments = []model.Payment{}
	rows, err = tx.QueryContext(ctx, "SELECT id,amount_minor,method,notes,paid_at FROM invoice_payments WHERE tenant_id=? AND invoice_id=? ORDER BY paid_at,id", tenant, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var payment model.Payment
		if err = rows.Scan(&payment.ID, &payment.AmountMinor, &payment.Method, &payment.Notes, &payment.PaidAt); err != nil {
			return v, err
		}
		v.PaidMinor += payment.AmountMinor
		v.Payments = append(v.Payments, payment)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	v.RemainingMinor = v.TotalMinor - v.PaidMinor
	v.PaymentStatus = "unpaid"
	if v.PaidMinor > 0 {
		v.PaymentStatus = "partially_paid"
	}
	if v.RemainingMinor == 0 && v.TotalMinor > 0 {
		v.PaymentStatus = "paid"
	}
	return v, nil
}
func (a *API) listInvoices(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := "SELECT " + invoiceColumns + " FROM invoices WHERE tenant_id=?"
	args := []any{tenantID(c)}
	if status := c.Query("status"); status != "" {
		if status != "draft" && status != "posted" && status != "void" && status != "cancelled" {
			fail(c, 400, "invalid invoice status")
			return
		}
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := a.db.QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer rows.Close()
	data := []model.Invoice{}
	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			databaseError(c, err)
			return
		}
		data = append(data, v)
	}
	if err = rows.Err(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}
func (a *API) getInvoice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(c.Request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	v, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}

type invoiceLineInput struct {
	ProductID      uint64 `json:"product_id"`
	Quantity       int64  `json:"quantity"`
	UnitPriceMinor *int64 `json:"unit_price_minor"`
}
type invoiceInput struct {
	ClientID  uint64             `json:"client_id"`
	IssueDate string             `json:"issue_date"`
	Notes     string             `json:"notes"`
	Items     []invoiceLineInput `json:"items"`
	Version   int64              `json:"version"`
}

func lineTotal(quantity, price int64) (int64, bool) {
	if quantity < 1 || quantity > maxQuantity || price < 0 || price > maxPrice || (price > 0 && quantity > maxTotal/price) {
		return 0, false
	}
	return quantity * price, true
}
func (a *API) createInvoice(c *gin.Context) { a.saveInvoice(c, true) }
func (a *API) updateInvoice(c *gin.Context) { a.saveInvoice(c, false) }
func (a *API) saveInvoice(c *gin.Context, create bool) {
	var in invoiceInput
	if !decode(c, &in) {
		return
	}
	date, err := time.Parse("2006-01-02", in.IssueDate)
	if err != nil || date.Year() < 1000 || in.ClientID == 0 || !validText(in.Notes, 0, 2000) || len(in.Items) < 1 || len(in.Items) > 100 {
		fail(c, 400, "valid client, YYYY-MM-DD issue_date and 1-100 invoice items required")
		return
	}
	var id uint64
	if !create {
		var ok bool
		id, ok = pathID(c, "id")
		if !ok {
			return
		}
		if in.Version <= 0 {
			fail(c, 400, "current invoice version is required")
			return
		}
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !create {
		existing, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, true)
		if err != nil {
			databaseError(c, err)
			return
		}
		if existing.Status != "draft" {
			fail(c, 409, "only draft invoices can be edited")
			return
		}
		if existing.Version != in.Version {
			fail(c, 409, "invoice changed; refresh before saving")
			return
		}
	}
	var name, address string
	var active bool
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT name,address,active FROM clients WHERE tenant_id=? AND id=? FOR SHARE", tenantID(c), in.ClientID).Scan(&name, &address, &active); err != nil {
		databaseError(c, err)
		return
	}
	if !active {
		fail(c, 409, "cannot invoice an archived client")
		return
	}
	items := make([]model.InvoiceItem, 0, len(in.Items))
	seen := map[uint64]bool{}
	var total int64
	for _, line := range in.Items {
		if line.ProductID == 0 || seen[line.ProductID] {
			fail(c, 400, "each product must appear once; combine its pack quantities")
			return
		}
		seen[line.ProductID] = true
		p, err := scanProduct(tx.QueryRowContext(c.Request.Context(), "SELECT "+productColumns+" FROM products WHERE tenant_id=? AND id=? FOR SHARE", tenantID(c), line.ProductID))
		if err != nil {
			databaseError(c, err)
			return
		}
		if !p.Active {
			fail(c, 409, "cannot invoice an archived product")
			return
		}
		if line.UnitPriceMinor == nil {
			fail(c, 400, "unit_price_minor is required for every invoice item")
			return
		}
		price := *line.UnitPriceMinor
		amount, valid := lineTotal(line.Quantity, price)
		if !valid || total > maxTotal-amount {
			fail(c, 400, "invalid quantities, prices or invoice total limit exceeded")
			return
		}
		total += amount
		items = append(items, model.InvoiceItem{ProductID: p.ID, Title: p.Title, Code: p.Code, PiecesPerUnit: p.PiecesPerUnit, Quantity: line.Quantity, UnitPriceMinor: price, TotalMinor: amount})
	}
	if create {
		result, err := tx.ExecContext(c.Request.Context(), "INSERT INTO invoices(tenant_id,client_id,created_by_user_id,issue_date,client_name,client_address,notes,total_minor) VALUES (?,?,?,?,?,?,?,?)", tenantID(c), in.ClientID, actor(c).ID, in.IssueDate, name, address, in.Notes, total)
		if err != nil {
			databaseError(c, err)
			return
		}
		inserted, err := result.LastInsertId()
		if err != nil {
			databaseError(c, err)
			return
		}
		id = uint64(inserted)
	} else {
		if _, err = tx.ExecContext(c.Request.Context(), "UPDATE invoices SET client_id=?,issue_date=?,client_name=?,client_address=?,notes=?,total_minor=?,version=version+1 WHERE tenant_id=? AND id=?", in.ClientID, in.IssueDate, name, address, in.Notes, total, tenantID(c), id); err != nil {
			databaseError(c, err)
			return
		}
		if _, err = tx.ExecContext(c.Request.Context(), "DELETE FROM invoice_items WHERE tenant_id=? AND invoice_id=?", tenantID(c), id); err != nil {
			databaseError(c, err)
			return
		}
	}
	for _, item := range items {
		if _, err = tx.ExecContext(c.Request.Context(), "INSERT INTO invoice_items(tenant_id,invoice_id,product_id,title,code,pieces_per_unit,quantity,unit_price_minor,total_minor) VALUES (?,?,?,?,?,?,?,?,?)", tenantID(c), id, item.ProductID, item.Title, item.Code, item.PiecesPerUnit, item.Quantity, item.UnitPriceMinor, item.TotalMinor); err != nil {
			databaseError(c, err)
			return
		}
	}
	v, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	if create {
		c.JSON(201, v)
	} else {
		c.JSON(200, v)
	}
}
func (a *API) postInvoice(c *gin.Context)   { a.transitionInvoice(c, "posted") }
func (a *API) voidInvoice(c *gin.Context)   { a.transitionInvoice(c, "void") }
func (a *API) cancelInvoice(c *gin.Context) { a.transitionInvoice(c, "cancelled") }
func (a *API) transitionInvoice(c *gin.Context, target string) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if target == "cancelled" {
		in.Version, _ = strconv.ParseInt(c.Query("version"), 10, 64)
	} else if !decode(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Version <= 0 || (target == "void" && !validText(in.Reason, 1, 500)) {
		fail(c, 400, "current version and a reason for voiding are required")
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	v, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, true)
	if err != nil {
		databaseError(c, err)
		return
	}
	if v.Status == target {
		c.JSON(200, v)
		return
	} // Retrying a completed transition never repeats stock/ledger effects.
	if v.Version != in.Version {
		fail(c, 409, "invoice changed; refresh before continuing")
		return
	}
	if (target == "void" && v.Status != "posted") || (target != "void" && v.Status != "draft") {
		fail(c, 409, "invoice cannot make this status transition")
		return
	}
	if target == "posted" {
		var active bool
		if err = tx.QueryRowContext(c.Request.Context(), "SELECT active FROM clients WHERE tenant_id=? AND id=? FOR SHARE", tenantID(c), v.ClientID).Scan(&active); err != nil {
			databaseError(c, err)
			return
		}
		if !active {
			fail(c, 409, "cannot post for an archived client")
			return
		}
	}
	if target != "cancelled" {
		for _, item := range v.Items {
			p, err := scanProduct(tx.QueryRowContext(c.Request.Context(), "SELECT "+productColumns+" FROM products WHERE tenant_id=? AND id=? FOR UPDATE", tenantID(c), item.ProductID))
			if err != nil {
				databaseError(c, err)
				return
			}
			delta := item.Quantity
			kind := "void"
			if target == "posted" {
				if !p.Active || p.Quantity < item.Quantity {
					fail(c, 409, fmt.Sprintf("insufficient stock or archived product: %s", item.Title))
					return
				}
				if p.PiecesPerUnit != item.PiecesPerUnit {
					fail(c, 409, "pack size changed; edit and save the draft before posting")
					return
				}
				delta = -item.Quantity
				kind = "sale"
			} else if p.Quantity > maxQuantity-item.Quantity {
				fail(c, 409, "stock limit would be exceeded by reversal")
				return
			}
			if _, err = tx.ExecContext(c.Request.Context(), "UPDATE products SET quantity=quantity+?,version=version+1 WHERE tenant_id=? AND id=?", delta, tenantID(c), p.ID); err != nil {
				databaseError(c, err)
				return
			}
			if _, err = tx.ExecContext(c.Request.Context(), "INSERT INTO stock_movements(tenant_id,product_id,invoice_id,created_by_user_id,kind,quantity_delta) VALUES (?,?,?,?,?,?)", tenantID(c), p.ID, id, actor(c).ID, kind, delta); err != nil {
				databaseError(c, err)
				return
			}
		}
		amount := v.TotalMinor
		kind := "invoice"
		if target == "void" {
			amount = -amount
			kind = "void"
		}
		if _, err = tx.ExecContext(c.Request.Context(), "INSERT INTO client_ledger(tenant_id,client_id,invoice_id,kind,amount_minor) VALUES (?,?,?,?,?)", tenantID(c), v.ClientID, id, kind, amount); err != nil {
			databaseError(c, err)
			return
		}
	}
	switch target {
	case "posted":
		if _, err = tx.ExecContext(c.Request.Context(), "INSERT IGNORE INTO invoice_counters(tenant_id) VALUES (?)", tenantID(c)); err != nil {
			databaseError(c, err)
			return
		}
		var number int64
		if err = tx.QueryRowContext(c.Request.Context(), "SELECT next_number FROM invoice_counters WHERE tenant_id=? FOR UPDATE", tenantID(c)).Scan(&number); err != nil {
			databaseError(c, err)
			return
		}
		if _, err = tx.ExecContext(c.Request.Context(), "UPDATE invoice_counters SET next_number=next_number+1 WHERE tenant_id=?", tenantID(c)); err != nil {
			databaseError(c, err)
			return
		}
		_, err = tx.ExecContext(c.Request.Context(), "UPDATE invoices SET status='posted',number=?,posted_at=UTC_TIMESTAMP(6),version=version+1 WHERE tenant_id=? AND id=?", number, tenantID(c), id)
	case "void":
		_, err = tx.ExecContext(c.Request.Context(), "UPDATE invoices SET status='void',void_reason=?,voided_at=UTC_TIMESTAMP(6),version=version+1 WHERE tenant_id=? AND id=?", in.Reason, tenantID(c), id)
	default:
		_, err = tx.ExecContext(c.Request.Context(), "UPDATE invoices SET status='cancelled',version=version+1 WHERE tenant_id=? AND id=?", tenantID(c), id)
	}
	if err != nil {
		databaseError(c, err)
		return
	}
	v, err = readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}
func (a *API) financialSummary(c *gin.Context) {
	var receivables, payables, net int64
	err := a.db.QueryRowContext(c.Request.Context(), `SELECT COALESCE(SUM(GREATEST(balance,0)),0),COALESCE(SUM(GREATEST(-balance,0)),0),COALESCE(SUM(balance),0)
 FROM (SELECT c.opening_balance_minor+COALESCE((SELECT SUM(l.amount_minor) FROM client_ledger l WHERE l.tenant_id=c.tenant_id AND l.client_id=c.id),0)-COALESCE((SELECT SUM(p.amount_minor) FROM invoice_payments p WHERE p.tenant_id=c.tenant_id AND p.client_id=c.id),0) AS balance FROM clients c WHERE c.tenant_id=?) balances`, tenantID(c)).Scan(&receivables, &payables, &net)
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"currency": "EGP", "receivables_minor": receivables, "payables_minor": payables, "net_minor": net, "scope": "posted_invoices_and_voids"})
}

type paymentInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Method      string `json:"method"`
	Notes       string `json:"notes"`
}

func (a *API) createPayment(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in paymentInput
	if !decode(c, &in) {
		return
	}
	in.Method = strings.ToLower(strings.TrimSpace(in.Method))
	in.Notes = strings.TrimSpace(in.Notes)
	if in.AmountMinor < 1 || in.AmountMinor > maxPrice || (in.Method != "cash" && in.Method != "online") || !validText(in.Notes, 0, 500) {
		fail(c, 400, "amount_minor, method (cash or online), and valid notes are required")
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	var clientID uint64
	var status string
	var total, paid int64
	if err := tx.QueryRowContext(c.Request.Context(), "SELECT client_id,status,total_minor FROM invoices WHERE tenant_id=? AND id=? FOR UPDATE", tenantID(c), id).Scan(&clientID, &status, &total); err != nil {
		databaseError(c, err)
		return
	}
	if status != "posted" {
		fail(c, 409, "payments can only be recorded for posted invoices")
		return
	}
	if err := tx.QueryRowContext(c.Request.Context(), "SELECT COALESCE(SUM(amount_minor),0) FROM invoice_payments WHERE tenant_id=? AND invoice_id=?", tenantID(c), id).Scan(&paid); err != nil {
		databaseError(c, err)
		return
	}
	if paid > total-in.AmountMinor {
		fail(c, 409, "payment exceeds the invoice remaining balance")
		return
	}
	res, err := tx.ExecContext(c.Request.Context(), "INSERT INTO invoice_payments(tenant_id,invoice_id,client_id,received_by_user_id,amount_minor,method,notes) VALUES (?,?,?,?,?,?,?)", tenantID(c), id, clientID, actor(c).ID, in.AmountMinor, in.Method, in.Notes)
	if err != nil {
		databaseError(c, err)
		return
	}
	paymentID, err := res.LastInsertId()
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": paymentID, "invoice_id": id, "client_id": clientID, "amount_minor": in.AmountMinor, "method": in.Method, "notes": in.Notes, "paid_minor": paid + in.AmountMinor, "remaining_minor": total - paid - in.AmountMinor})
}
