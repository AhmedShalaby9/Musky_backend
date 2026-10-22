package server

import (
	"gorm.io/gorm"
	"musky/backend/internal/model"
)

// stockPieces measures inventory in the smallest sellable unit.
func stockPieces(p model.Product) int64 {
	return p.Quantity*p.PiecesPerUnit + p.LoosePieces
}

// changeStock writes normalized whole-pack and loose-piece balances. The caller
// must hold the tenant transaction and a lock on the product row.
func changeStock(tx *gorm.DB, p model.Product, delta int64) (bool, error) {
	next := stockPieces(p) + delta
	if next < 0 || next > maxQuantity*p.PiecesPerUnit+p.PiecesPerUnit-1 {
		return false, nil
	}
	err := tx.Model(&model.Product{}).Where("tenant_id = ? AND id = ?", p.TenantID, p.ID).
		Updates(map[string]any{
			"quantity":     next / p.PiecesPerUnit,
			"loose_pieces": next % p.PiecesPerUnit,
			"version":      gorm.Expr("version + 1"),
		}).Error
	return err == nil, err
}

func invoicePieces(item model.InvoiceItem) int64 {
	return item.UnitsPerPackage * item.PackageCount
}
