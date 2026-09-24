package server

import (
	"fmt"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
)

func invoiceHasProduct(invoice model.Invoice, productID uint64) bool {
	for _, item := range invoice.Items {
		if item.ProductID == productID {
			return true
		}
	}
	return false
}

// applyPostedInvoiceEdit books the difference between a posted invoice and its
// edited lines: stock moves only by the changed quantities, and the client's
// account gets one adjustment for the change in total. The caller then
// replaces the invoice lines and total. A non-empty message means the edit is
// rejected with that HTTP status and nothing has been written.
func applyPostedInvoiceEdit(tx *gorm.DB, tenant, userID uint64, existing model.Invoice, items []model.InvoiceItem, newTotal int64) (int, string, error) {
	returned, err := returnedQuantities(tx, tenant, existing.ID)
	if err != nil {
		return 0, "", err
	}
	oldQty := map[uint64]int64{}
	titles := map[uint64]string{}
	for _, item := range existing.Items {
		oldQty[item.ProductID] += item.Quantity
		titles[item.ProductID] = item.Title
	}
	newQty := map[uint64]int64{}
	for _, item := range items {
		newQty[item.ProductID] += item.Quantity
		titles[item.ProductID] = item.Title
	}
	for productID, qty := range returned {
		if newQty[productID] < qty {
			return 409, fmt.Sprintf("لا يمكن تقليل كمية %s إلى أقل من المرتجع منها (%d)", titles[productID], qty), nil
		}
	}
	if newTotal < existing.PaidMinor+existing.ReturnedMinor {
		return 409, fmt.Sprintf("الإجمالي الجديد (%s) أقل من المدفوع والمرتجع لهذه الفاتورة (%s)", moneyPDF(newTotal), moneyPDF(existing.PaidMinor+existing.ReturnedMinor)), nil
	}

	// Stock direction: a sale takes stock out, a purchase brings it in.
	type change struct {
		productID uint64
		delta     int64
	}
	changes := []change{}
	for _, productID := range sortedProductIDs(oldQty, newQty) {
		diff := newQty[productID] - oldQty[productID]
		if diff == 0 {
			continue
		}
		delta := diff
		if existing.DocumentType == "sale" {
			delta = -diff
		}
		changes = append(changes, change{productID, delta})
	}
	for _, ch := range changes {
		var p model.Product
		result := tx.Select(productColumns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenant, ch.productID).Take(&p)
		if err := result.Error; err != nil || result.RowsAffected == 0 {
			if err == nil {
				err = gorm.ErrRecordNotFound
			}
			return 0, "", err
		}
		if ch.delta < 0 && p.Quantity < -ch.delta {
			return 409, fmt.Sprintf("المخزون غير كافٍ للصنف %s (المتاح %d)", titles[ch.productID], p.Quantity), nil
		}
		if ch.delta > 0 && p.Quantity > maxQuantity-ch.delta {
			return 409, "stock limit would be exceeded", nil
		}
		if err := tx.Model(&model.Product{}).Where("tenant_id = ? AND id = ?", tenant, p.ID).Updates(map[string]any{"quantity": gorm.Expr("quantity + ?", ch.delta), "version": gorm.Expr("version + 1")}).Error; err != nil {
			return 0, "", err
		}
		invoiceID := existing.ID
		if err := tx.Create(&stockMovementRecord{TenantID: tenant, ProductID: p.ID, InvoiceID: &invoiceID, CreatedByUserID: userID, Kind: "edit", QuantityDelta: ch.delta}).Error; err != nil {
			return 0, "", err
		}
	}

	if diff := newTotal - existing.TotalMinor; diff != 0 {
		kind, amount := "adjust", diff
		if existing.DocumentType == "purchase" {
			kind, amount = "purchase_adjust", -diff
		}
		if err := tx.Create(&clientLedgerRecord{TenantID: tenant, ClientID: existing.ClientID, InvoiceID: existing.ID, Kind: kind, AmountMinor: amount}).Error; err != nil {
			return 0, "", err
		}
	}
	return 0, "", nil
}

// sortedProductIDs returns every product on either side in a stable order, so
// row locks are always taken in the same sequence.
func sortedProductIDs(a, b map[uint64]int64) []uint64 {
	ids := []uint64{}
	for id := range a {
		ids = append(ids, id)
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// foldInvoiceAdjustments merges each edit adjustment into the latest posting
// of its invoice, so the account shows the invoice at its edited total on its
// own date, then recomputes the running balances.
func foldInvoiceAdjustments(entries []model.LedgerEntry) []model.LedgerEntry {
	lastPosting := map[uint64]int{}
	for i, entry := range entries {
		switch entry.Kind {
		case "invoice", "purchase", "reactivate", "purchase_reactivate":
			if entry.RefID != nil {
				lastPosting[*entry.RefID] = i
			}
		}
	}
	extra := map[int]int64{}
	folded := map[int]bool{}
	for i, entry := range entries {
		if (entry.Kind == "adjust" || entry.Kind == "purchase_adjust") && entry.RefID != nil {
			if target, ok := lastPosting[*entry.RefID]; ok {
				extra[target] += entry.DeltaMinor
				folded[i] = true
			}
		}
	}
	out := make([]model.LedgerEntry, 0, len(entries))
	var running int64
	for i, entry := range entries {
		if folded[i] {
			continue
		}
		entry.DeltaMinor += extra[i]
		running += entry.DeltaMinor
		entry.RunningBalance = running
		out = append(out, entry)
	}
	return out
}
