package server

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
	"strconv"
	"strings"
	"time"
)

const invoiceColumns = "id,tenant_id,client_id,created_by_user_id,number,status,document_type,currency,DATE_FORMAT(issue_date,'%Y-%m-%d') AS issue_date,client_name,client_address,notes,void_reason,total_minor,version,created_at,posted_at,voided_at,COALESCE(pdf_url,'') AS pdf_url"
const maxPrice int64 = 1000000000000

func readInvoice(ctx context.Context, tx *gorm.DB, tenant, id uint64, lock bool) (model.Invoice, error) {
	tx = tx.WithContext(ctx)
	query := tx.Model(&model.Invoice{}).Select(invoiceColumns).Where("tenant_id = ? AND id = ?", tenant, id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var v model.Invoice
	if err := query.Take(&v).Error; err != nil {
		return v, err
	}
	v.Items = []model.InvoiceItem{}
	if err := tx.Model(&invoiceItemRecord{}).Where("tenant_id = ? AND invoice_id = ?", tenant, id).Order("id").Scan(&v.Items).Error; err != nil {
		return v, err
	}
	v.Payments = []model.Payment{}
	if err := tx.Model(&paymentRecord{}).Where("tenant_id = ? AND invoice_id = ?", tenant, id).Order("paid_at,id").Scan(&v.Payments).Error; err != nil {
		return v, err
	}
	v.Returns = []model.InvoiceReturn{}
	if err := tx.Model(&invoiceReturnRecord{}).Where("tenant_id = ? AND invoice_id = ?", tenant, id).Order("created_at,id").Scan(&v.Returns).Error; err != nil {
		return v, err
	}
	for i := range v.Returns {
		v.Returns[i].Items = []model.InvoiceReturnItem{}
		if err := tx.Model(&invoiceReturnItemRecord{}).Where("tenant_id = ? AND return_id = ?", tenant, v.Returns[i].ID).Order("id").Scan(&v.Returns[i].Items).Error; err != nil {
			return v, err
		}
		v.ReturnedMinor += v.Returns[i].AmountMinor
	}
	for _, payment := range v.Payments {
		v.PaidMinor += payment.AmountMinor
	}
	v.RemainingMinor = v.TotalMinor - v.ReturnedMinor - v.PaidMinor
	if v.RemainingMinor < 0 {
		v.RemainingMinor = 0
	}
	v.PaymentStatus = "unpaid"
	if v.PaidMinor > 0 {
		v.PaymentStatus = "partially_paid"
	}
	if v.RemainingMinor == 0 && v.TotalMinor-v.ReturnedMinor > 0 {
		v.PaymentStatus = "paid"
	}
	return v, nil
}
func (a *API) listInvoices(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := a.orm.WithContext(c.Request.Context()).Model(&model.Invoice{}).Select(invoiceColumns).Where("tenant_id = ?", tenantID(c))
	if status := c.Query("status"); status != "" {
		if status != "draft" && status != "posted" && status != "void" && status != "cancelled" {
			fail(c, 400, "invalid invoice status")
			return
		}
		query = query.Where("status = ?", status)
	}
	if documentType := c.Query("document_type"); documentType != "" {
		if documentType != "sale" && documentType != "purchase" {
			fail(c, 400, "document_type must be sale or purchase")
			return
		}
		query = query.Where("document_type = ?", documentType)
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		if !validText(q, 1, 150) {
			fail(c, 400, "invalid invoice search")
			return
		}
		query = query.Where("client_name LIKE ?", "%"+q+"%")
	}
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		date, err := time.Parse("2006-01-02", from)
		if err != nil || date.Year() < 1000 {
			fail(c, 400, "from must be YYYY-MM-DD")
			return
		}
		query = query.Where("issue_date >= ?", from)
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		date, err := time.Parse("2006-01-02", to)
		if err != nil || date.Year() < 1000 {
			fail(c, 400, "to must be YYYY-MM-DD")
			return
		}
		query = query.Where("issue_date <= ?", to)
	}

	data := []model.Invoice{}
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&data).Error; err != nil {
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
	tx := a.orm.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil {
		databaseError(c, tx.Error)
		return
	}
	defer tx.Rollback()
	v, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}

type invoiceLineInput struct {
	ProductID       uint64 `json:"product_id"`
	Quantity        int64  `json:"quantity"`
	UnitsPerPackage *int64 `json:"units_per_package"`
	PackageCount    *int64 `json:"package_count"`
	UnitPriceMinor  *int64 `json:"unit_price_minor"`
}
type invoiceInput struct {
	DocumentType string             `json:"document_type"`
	ClientID     uint64             `json:"client_id"`
	IssueDate    string             `json:"issue_date"`
	Notes        string             `json:"notes"`
	Items        []invoiceLineInput `json:"items"`
	Version      int64              `json:"version"`
}

func lineTotal(unitPrice, unitsPerPackage, packageCount int64) (int64, bool) {
	if unitPrice < 0 || unitPrice > maxPrice || unitsPerPackage < 1 || unitsPerPackage > maxQuantity || packageCount < 1 || packageCount > maxQuantity {
		return 0, false
	}
	if unitsPerPackage > maxTotal/packageCount || (unitPrice > 0 && unitPrice > maxTotal/(unitsPerPackage*packageCount)) {
		return 0, false
	}
	return unitPrice * unitsPerPackage * packageCount, true
}
func (a *API) createInvoice(c *gin.Context) { a.saveInvoice(c, true) }
func (a *API) updateInvoice(c *gin.Context) { a.saveInvoice(c, false) }
func (a *API) saveInvoice(c *gin.Context, create bool) {
	var in invoiceInput
	if !decode(c, &in) {
		return
	}
	if in.DocumentType == "" {
		in.DocumentType = "sale"
	}
	if in.DocumentType != "sale" && in.DocumentType != "purchase" {
		fail(c, 400, "document_type must be sale or purchase")
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
	var client struct {
		Name    string
		Address string
		Active  bool
	}
	if err = tx.Model(&clientRecord{}).Select("name,address,active").Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id = ? AND id = ?", tenantID(c), in.ClientID).Take(&client).Error; err != nil {
		databaseError(c, err)
		return
	}
	name, address, active = client.Name, client.Address, client.Active
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
		var p model.Product
		result := tx.Select(productColumns).Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id = ? AND id = ?", tenantID(c), line.ProductID).Take(&p)
		if err := result.Error; err != nil || result.RowsAffected == 0 {
			if err == nil {
				err = gorm.ErrRecordNotFound
			}
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
		// New clients send explicit carton semantics. Older clients are kept
		// compatible by treating quantity as the carton count and one unit per carton.
		unitsPerPackage := int64(1)
		packageCount := line.Quantity
		if line.UnitsPerPackage != nil {
			unitsPerPackage = *line.UnitsPerPackage
		}
		if line.PackageCount != nil {
			packageCount = *line.PackageCount
		}
		amount, valid := lineTotal(price, unitsPerPackage, packageCount)
		if !valid || total > maxTotal-amount {
			fail(c, 400, "invalid quantities, prices or invoice total limit exceeded")
			return
		}
		total += amount
		items = append(items, model.InvoiceItem{ProductID: p.ID, Title: p.Title, Code: p.Code, PiecesPerUnit: p.PiecesPerUnit, Quantity: packageCount, UnitsPerPackage: unitsPerPackage, PackageCount: packageCount, UnitPriceMinor: price, TotalMinor: amount})
	}
	if create {
		row := model.Invoice{TenantID: tenantID(c), ClientID: in.ClientID, CreatedByUserID: actor(c).ID, IssueDate: in.IssueDate, ClientName: name, ClientAddress: address, Notes: in.Notes, TotalMinor: total, DocumentType: in.DocumentType, Status: "draft", Currency: "EGP", Version: 1}
		rowTable := tx.Create(&row)
		if rowTable.Error != nil {
			databaseError(c, rowTable.Error)
			return
		}
		id = row.ID
	} else {
		if err = tx.Model(&model.Invoice{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(map[string]any{"client_id": in.ClientID, "issue_date": in.IssueDate, "client_name": name, "client_address": address, "notes": in.Notes, "total_minor": total, "document_type": in.DocumentType, "version": gorm.Expr("version + 1")}).Error; err != nil {
			databaseError(c, err)
			return
		}
		if err = tx.Where("tenant_id = ? AND invoice_id = ?", tenantID(c), id).Delete(&invoiceItemRecord{}).Error; err != nil {
			databaseError(c, err)
			return
		}
	}
	for _, item := range items {
		if err = tx.Create(&invoiceItemRecord{TenantID: tenantID(c), InvoiceID: id, InvoiceItem: item}).Error; err != nil {
			databaseError(c, err)
			return
		}
	}
	v, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
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
func (a *API) deleteCancelledInvoice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	var invoice model.Invoice
	if err := tx.Model(&model.Invoice{}).Select("status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Take(&invoice).Error; err != nil {
		databaseError(c, err)
		return
	}
	if invoice.Status != "cancelled" {
		fail(c, 409, "only cancelled invoices can be permanently deleted")
		return
	}
	if err := tx.Where("tenant_id = ? AND invoice_id = ?", tenantID(c), id).Delete(&invoiceItemRecord{}).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Where("tenant_id = ? AND id = ? AND status = 'cancelled'", tenantID(c), id).Delete(&model.Invoice{}).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}
func (a *API) reactivateInvoice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var invoice model.Invoice
	if err := a.orm.WithContext(c.Request.Context()).Model(&model.Invoice{}).
		Select("status").Where("tenant_id = ? AND id = ?", tenantID(c), id).Take(&invoice).Error; err != nil {
		databaseError(c, err)
		return
	}
	if invoice.Status == "void" {
		a.transitionInvoice(c, "reactivated")
		return
	}
	if invoice.Status != "cancelled" {
		fail(c, 409, "only cancelled invoices can be reactivated")
		return
	}
	version, err := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || version <= 0 {
		var in struct {
			Version int64 `json:"version"`
		}
		if !decode(c, &in) {
			return
		}
		version = in.Version
	}
	if version <= 0 {
		fail(c, 400, "current version is required")
		return
	}
	result := a.orm.WithContext(c.Request.Context()).Model(&model.Invoice{}).
		Where("tenant_id = ? AND id = ? AND status = 'cancelled' AND version = ?", tenantID(c), id, version).
		Updates(map[string]any{"status": "draft", "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 409, "invoice changed; refresh before continuing")
		return
	}
	v, err := readInvoice(c.Request.Context(), a.orm, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}
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
	reactivate := target == "reactivated"
	if (target == "void" && v.Status != "posted") ||
		(target == "reactivated" && v.Status != "void") ||
		(target != "void" && target != "reactivated" && v.Status != "draft") {
		fail(c, 409, "invoice cannot make this status transition")
		return
	}
	if target == "posted" || reactivate {
		var active bool
		var client struct{ Active bool }
		if err = tx.Model(&clientRecord{}).Select("active").Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id = ? AND id = ?", tenantID(c), v.ClientID).Take(&client).Error; err != nil {
			databaseError(c, err)
			return
		}
		active = client.Active
		if !active {
			fail(c, 409, "cannot post for an archived client")
			return
		}
	}
	if target != "cancelled" {
		for _, item := range v.Items {
			var p model.Product
			result := tx.Select(productColumns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID(c), item.ProductID).Take(&p)
			if err := result.Error; err != nil || result.RowsAffected == 0 {
				if err == nil {
					err = gorm.ErrRecordNotFound
				}
				databaseError(c, err)
				return
			}
			delta := item.Quantity
			kind := "void"
			if target == "posted" || reactivate {
				if !p.Active || (v.DocumentType == "sale" && p.Quantity < item.Quantity) {
					fail(c, 409, fmt.Sprintf("insufficient stock or archived product: %s", item.Title))
					return
				}
				if p.PiecesPerUnit != item.PiecesPerUnit {
					fail(c, 409, "pack size changed; edit and save the draft before posting")
					return
				}
				if v.DocumentType == "sale" {
					delta = -item.Quantity
				}
				kind = "sale"
				if v.DocumentType == "purchase" {
					delta = item.Quantity
					kind = "purchase"
				}
				if reactivate {
					if v.DocumentType == "purchase" {
						kind = "purchase_reactivate"
					} else {
						kind = "reactivate"
					}
				}
			} else if v.DocumentType == "sale" && p.Quantity > maxQuantity-item.Quantity {
				fail(c, 409, "stock limit would be exceeded by reversal")
				return
			} else if v.DocumentType == "purchase" && p.Quantity < item.Quantity {
				fail(c, 409, "insufficient stock to reverse this purchase")
				return
			} else if v.DocumentType == "purchase" {
				delta = -item.Quantity
			}
			if err = tx.Model(&model.Product{}).Where("tenant_id = ? AND id = ?", tenantID(c), p.ID).Updates(map[string]any{"quantity": gorm.Expr("quantity + ?", delta), "version": gorm.Expr("version + 1")}).Error; err != nil {
				databaseError(c, err)
				return
			}
			if err = tx.Create(&stockMovementRecord{TenantID: tenantID(c), ProductID: p.ID, InvoiceID: &id, CreatedByUserID: actor(c).ID, Kind: kind, QuantityDelta: delta}).Error; err != nil {
				databaseError(c, err)
				return
			}
		}
		amount := v.TotalMinor
		if v.DocumentType == "purchase" {
			amount = -amount
		}
		kind := "invoice"
		if v.DocumentType == "purchase" {
			kind = "purchase"
		}
		if reactivate {
			if v.DocumentType == "purchase" {
				kind = "purchase_reactivate"
			} else {
				kind = "reactivate"
			}
		}
		if target == "void" {
			amount = -amount
			if v.DocumentType == "purchase" {
				kind = "purchase_void"
			} else {
				kind = "void"
			}
		}
		if err = tx.Create(&clientLedgerRecord{TenantID: tenantID(c), ClientID: v.ClientID, InvoiceID: id, Kind: kind, AmountMinor: amount}).Error; err != nil {
			databaseError(c, err)
			return
		}
	}
	switch target {
	case "posted", "reactivated":
		var number int64
		if target == "posted" {
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&invoiceCounterRecord{TenantID: tenantID(c), NextNumber: 1}).Error; err != nil {
				databaseError(c, err)
				return
			}
			var counter struct{ NextNumber int64 }
			if err = tx.Model(&invoiceCounterRecord{}).Select("next_number").Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenantID(c)).Take(&counter).Error; err != nil {
				databaseError(c, err)
				return
			}
			number = counter.NextNumber
			if err = tx.Model(&invoiceCounterRecord{}).Where("tenant_id = ?", tenantID(c)).Update("next_number", gorm.Expr("next_number + 1")).Error; err != nil {
				databaseError(c, err)
				return
			}
		}
		updates := map[string]any{"status": "posted", "posted_at": gorm.Expr("UTC_TIMESTAMP(6)"), "version": gorm.Expr("version + 1")}
		if target == "posted" {
			updates["number"] = number
		} else {
			updates["void_reason"] = ""
			updates["voided_at"] = nil
		}
		err = tx.Model(&model.Invoice{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(updates).Error
	case "void":
		err = tx.Model(&model.Invoice{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(map[string]any{"status": "void", "void_reason": in.Reason, "voided_at": gorm.Expr("UTC_TIMESTAMP(6)"), "version": gorm.Expr("version + 1")}).Error
	default:
		err = tx.Model(&model.Invoice{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version + 1")}).Error
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
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}
func (a *API) financialSummary(c *gin.Context) {
	var receivables, payables, net int64
	var summary struct {
		Receivables int64
		Payables    int64
		Net         int64
	}
	db := a.orm.WithContext(c.Request.Context())
	balances := clientDisplayQuery(db, tenantID(c))
	err := db.Table("(?) balances", balances).
		Select("COALESCE(SUM(GREATEST(balance_minor,0)),0) AS receivables,COALESCE(SUM(GREATEST(-balance_minor,0)),0) AS payables,COALESCE(SUM(balance_minor),0) AS net").Scan(&summary).Error
	if err != nil {
		databaseError(c, err)
		return
	}
	receivables, payables, net = summary.Receivables, summary.Payables, summary.Net
	// Return the contributing balances as well as the totals. This makes the
	// summary auditable when a deleted or newly-created client appears to affect
	// the numbers unexpectedly.
	var clients []model.Client
	if err = balances.Order("ABS(balance_minor) DESC, c.id").Scan(&clients).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{
		"currency":          "EGP",
		"receivables_minor": receivables,
		"payables_minor":    payables,
		"net_minor":         net,
		"scope":             "opening_balances_posted_invoices_payments_and_receipts",
		"clients":           clients,
	})
}

type paymentInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Method      string `json:"method"`
	Notes       string `json:"notes"`
}

type returnItemInput struct {
	ProductID    uint64 `json:"product_id"`
	PackageCount int64  `json:"package_count"`
}

type returnInput struct {
	Reason string            `json:"reason"`
	Items  []returnItemInput `json:"items"`
}

func returnedQuantities(tx *gorm.DB, tenant, invoiceID uint64) (map[uint64]int64, error) {
	rows := []struct {
		ProductID uint64
		Quantity  int64
	}{}
	if err := tx.Model(&invoiceReturnItemRecord{}).Select("product_id, COALESCE(SUM(package_count),0) AS quantity").Where("tenant_id = ? AND invoice_id = ?", tenant, invoiceID).Group("product_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[uint64]int64{}
	for _, row := range rows {
		out[row.ProductID] = row.Quantity
	}
	return out, nil
}

func (a *API) createInvoiceReturn(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in returnInput
	if !decode(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Items) < 1 || len(in.Items) > 100 || !validText(in.Reason, 0, 500) {
		fail(c, 400, "invalid return fields")
		return
	}
	requested := map[uint64]int64{}
	for _, item := range in.Items {
		if item.ProductID == 0 || item.PackageCount < 1 || item.PackageCount > maxQuantity {
			fail(c, 400, "invalid return item")
			return
		}
		requested[item.ProductID] += item.PackageCount
		if requested[item.ProductID] > maxQuantity {
			fail(c, 400, "invalid return item quantity")
			return
		}
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
	if v.Status != "posted" || v.DocumentType != "sale" {
		fail(c, 409, "returns can only be created for posted sale invoices")
		return
	}
	alreadyReturned, err := returnedQuantities(tx, tenantID(c), id)
	if err != nil {
		databaseError(c, err)
		return
	}
	itemsByProduct := map[uint64]model.InvoiceItem{}
	for _, item := range v.Items {
		itemsByProduct[item.ProductID] = item
	}
	returnRow := invoiceReturnRecord{InvoiceReturn: model.InvoiceReturn{TenantID: tenantID(c), InvoiceID: id, ClientID: v.ClientID, CreatedByUserID: actor(c).ID, Reason: in.Reason}}
	returnItems := []invoiceReturnItemRecord{}
	var total int64
	for productID, packageCount := range requested {
		item, exists := itemsByProduct[productID]
		if !exists {
			fail(c, 400, "returned product is not on this invoice")
			return
		}
		remaining := item.PackageCount - alreadyReturned[productID]
		if remaining <= 0 || packageCount > remaining {
			fail(c, 409, "return quantity exceeds invoice remaining quantity")
			return
		}
		quantity := packageCount
		line, valid := lineTotal(item.UnitPriceMinor, item.UnitsPerPackage, packageCount)
		if !valid || total > 100000000000000-line {
			fail(c, 400, "invalid return total")
			return
		}
		total += line
		returnItems = append(returnItems, invoiceReturnItemRecord{
			TenantID:  tenantID(c),
			InvoiceID: id,
			InvoiceReturnItem: model.InvoiceReturnItem{
				ProductID: productID, Title: item.Title, Code: item.Code, PiecesPerUnit: item.PiecesPerUnit, Quantity: quantity,
				UnitsPerPackage: item.UnitsPerPackage, PackageCount: packageCount, UnitPriceMinor: item.UnitPriceMinor, TotalMinor: line,
			},
		})
	}
	returnRow.AmountMinor = total
	if err = tx.Create(&returnRow).Error; err != nil {
		databaseError(c, err)
		return
	}
	returnID := returnRow.ID
	for i := range returnItems {
		returnItems[i].ReturnID = returnID
		if err = tx.Create(&returnItems[i]).Error; err != nil {
			databaseError(c, err)
			return
		}
		var p model.Product
		if err = tx.Select(productColumns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID(c), returnItems[i].ProductID).Take(&p).Error; err != nil {
			databaseError(c, err)
			return
		}
		if p.Quantity > maxQuantity-returnItems[i].InvoiceReturnItem.Quantity {
			fail(c, 409, "stock limit would be exceeded by return")
			return
		}
		if err = tx.Model(&model.Product{}).Where("tenant_id = ? AND id = ?", tenantID(c), p.ID).Updates(map[string]any{"quantity": gorm.Expr("quantity + ?", returnItems[i].InvoiceReturnItem.Quantity), "version": gorm.Expr("version + 1")}).Error; err != nil {
			databaseError(c, err)
			return
		}
		if err = tx.Create(&stockMovementRecord{TenantID: tenantID(c), ProductID: p.ID, InvoiceID: &id, ReturnID: &returnID, CreatedByUserID: actor(c).ID, Kind: "return", QuantityDelta: returnItems[i].InvoiceReturnItem.Quantity}).Error; err != nil {
			databaseError(c, err)
			return
		}
	}
	if err = tx.Create(&clientLedgerRecord{TenantID: tenantID(c), ClientID: v.ClientID, InvoiceID: id, ReturnID: &returnID, Kind: "return", AmountMinor: -total}).Error; err != nil {
		databaseError(c, err)
		return
	}
	v, err = readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, v)
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
	var total, returned, paid int64
	var invoiceRow struct {
		ClientID   uint64
		Status     string
		TotalMinor int64
	}
	if err := tx.Model(&model.Invoice{}).Select("client_id,status,total_minor").Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Take(&invoiceRow).Error; err != nil {
		databaseError(c, err)
		return
	}
	clientID, status, total = invoiceRow.ClientID, invoiceRow.Status, invoiceRow.TotalMinor
	if status != "posted" {
		fail(c, 409, "payments can only be recorded for posted invoices")
		return
	}
	var paidRow struct{ Paid int64 }
	if err := tx.Model(&paymentRecord{}).Select("COALESCE(SUM(amount_minor),0) AS paid").Where("tenant_id = ? AND invoice_id = ?", tenantID(c), id).Scan(&paidRow).Error; err != nil {
		databaseError(c, err)
		return
	}
	paid = paidRow.Paid
	var returnRow struct{ Returned int64 }
	if err := tx.Model(&invoiceReturnRecord{}).Select("COALESCE(SUM(amount_minor),0) AS returned").Where("tenant_id = ? AND invoice_id = ?", tenantID(c), id).Scan(&returnRow).Error; err != nil {
		databaseError(c, err)
		return
	}
	returned = returnRow.Returned
	due := total - returned
	if due < 0 {
		due = 0
	}
	if paid > due-in.AmountMinor {
		fail(c, 409, "payment exceeds the invoice remaining balance")
		return
	}
	row := paymentRecord{TenantID: tenantID(c), InvoiceID: id, ClientID: clientID, ReceivedByUserID: actor(c).ID, Payment: model.Payment{AmountMinor: in.AmountMinor, Method: in.Method, Notes: in.Notes}}

	if err := tx.Omit("paid_at").Create(&row).Error; err != nil {
		databaseError(c, err)
		return
	}
	paymentID := row.ID
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": paymentID, "invoice_id": id, "client_id": clientID, "amount_minor": in.AmountMinor, "method": in.Method, "notes": in.Notes, "paid_minor": paid + in.AmountMinor, "returned_minor": returned, "remaining_minor": due - paid - in.AmountMinor})
}
