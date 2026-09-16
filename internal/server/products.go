package server

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
	"strings"
)

const maxQuantity int64 = 1000000000
const maxTotal int64 = 100000000000000
const productColumns = "id,tenant_id,title,code,quantity,pieces_per_unit,active,version,created_at"

// All commerce mutations acquire the tenant row first. This serializes stock
// corrections, invoice posting and numbering within a tenant, not across tenants.
func (a *API) commerceTx(c *gin.Context) (*gorm.DB, bool) {
	tx := a.orm.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil {
		databaseError(c, tx.Error)
		return nil, false
	}
	var tenant struct{ Active bool }
	result := tx.Table("tenants").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tenantID(c)).Select("active").Scan(&tenant)
	if result.Error != nil {
		tx.Rollback()
		databaseError(c, result.Error)
		return nil, false
	}
	if result.RowsAffected == 0 {
		tx.Rollback()
		fail(c, 404, "not found")
		return nil, false
	}
	if !tenant.Active {
		tx.Rollback()
		fail(c, 403, "tenant is inactive")
		return nil, false
	}
	return tx, true
}
func (a *API) listProducts(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := a.orm.WithContext(c.Request.Context()).Table("products").Select(productColumns).Where("tenant_id = ?", tenantID(c))
	if c.Query("active") == "true" {
		query = query.Where("active = TRUE")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query = query.Where("(title LIKE ? OR code LIKE ?)", "%"+q+"%", "%"+q+"%")
	}
	var data []model.Product
	result := query.Order("id").Limit(limit).Offset(offset).Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}
func (a *API) getProduct(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var p model.Product
	result := a.orm.WithContext(c.Request.Context()).Table("products").Select(productColumns).
		Where("tenant_id = ? AND id = ?", tenantID(c), id).Limit(1).Scan(&p)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	c.JSON(200, p)
}

type productInput struct {
	Title         *string `json:"title"`
	Code          *string `json:"code"`
	Quantity      *int64  `json:"quantity"`
	PiecesPerUnit *int64  `json:"pieces_per_unit"`
	Active        *bool   `json:"active"`
	Version       int64   `json:"version"`
}

type productBuyerRow struct {
	InvoiceID       uint64 `json:"invoice_id"`
	InvoiceNumber   *int64 `json:"invoice_number"`
	IssueDate       string `json:"issue_date"`
	ClientID        uint64 `json:"client_id"`
	ClientName      string `json:"client_name"`
	ClientAddress   string `json:"client_address"`
	PackageCount    int64  `json:"package_count"`
	UnitsPerPackage int64  `json:"units_per_package"`
	UnitPriceMinor  int64  `json:"unit_price_minor"`
	TotalMinor      int64  `json:"total_minor"`
}

func (a *API) createProduct(c *gin.Context) { a.saveProduct(c, true) }
func (a *API) updateProduct(c *gin.Context) { a.saveProduct(c, false) }
func (a *API) productBuyers(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	var product model.Product
	result := a.orm.WithContext(c.Request.Context()).Table("products").Select(productColumns).
		Where("tenant_id = ? AND id = ?", tenantID(c), id).Limit(1).Scan(&product)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	var data []productBuyerRow
	result = a.orm.WithContext(c.Request.Context()).
		Table("invoice_items AS ii").
		Select(`
			i.id AS invoice_id,
			i.number AS invoice_number,
			DATE_FORMAT(i.issue_date, '%Y-%m-%d') AS issue_date,
			i.client_id AS client_id,
			i.client_name AS client_name,
			i.client_address AS client_address,
			ii.package_count AS package_count,
			ii.units_per_package AS units_per_package,
			ii.unit_price_minor AS unit_price_minor,
			ii.total_minor AS total_minor`).
		Joins("JOIN invoices AS i ON i.tenant_id = ii.tenant_id AND i.id = ii.invoice_id").
		Where("ii.tenant_id = ? AND ii.product_id = ? AND i.status = 'posted'", tenantID(c), id).
		Order("i.issue_date DESC, i.id DESC").
		Limit(limit).Offset(offset).
		Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, gin.H{"product": product, "data": data, "limit": limit, "offset": offset})
}
func (a *API) deleteProduct(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	if err := tx.Where("tenant_id = ? AND product_id = ?", tenantID(c), id).Delete(&stockMovementRecord{}).Error; err != nil {
		databaseError(c, err)
		return
	}
	res := tx.Table("products").Where("tenant_id = ? AND id = ?", tenantID(c), id).Delete(&model.Product{})
	if res.Error != nil {
		databaseError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}
func (a *API) saveProduct(c *gin.Context, create bool) {
	var in productInput
	if !decode(c, &in) {
		return
	}
	if create && (in.Title == nil || in.Code == nil || in.Quantity == nil || in.PiecesPerUnit == nil) {
		fail(c, 400, "title, code, quantity and pieces_per_unit are required")
		return
	}
	if !create && in.Version <= 0 {
		fail(c, 400, "current product version is required")
		return
	}
	if !create && in.Title == nil && in.Code == nil && in.Quantity == nil && in.PiecesPerUnit == nil && in.Active == nil {
		fail(c, 400, "no changes provided")
		return
	}
	var id uint64
	if !create {
		var ok bool
		id, ok = pathID(c, "id")
		if !ok {
			return
		}
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	p := model.Product{TenantID: tenantID(c), Active: true, Version: 1}
	if !create {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("products").Select(productColumns).Where("tenant_id = ? AND id = ?", tenantID(c), id).Scan(&p)
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			fail(c, 404, "not found")
			return
		}
		if p.Version != in.Version {
			fail(c, 409, "product changed; refresh before saving")
			return
		}
	}
	oldQuantity := p.Quantity
	if in.Title != nil {
		p.Title = strings.TrimSpace(*in.Title)
	}
	if in.Code != nil {
		p.Code = strings.TrimSpace(*in.Code)
	}
	if in.Quantity != nil {
		p.Quantity = *in.Quantity
	}
	if in.PiecesPerUnit != nil {
		p.PiecesPerUnit = *in.PiecesPerUnit
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	if !validText(p.Title, 1, 150) || !validText(p.Code, 1, 80) || p.Quantity < 0 || p.Quantity > maxQuantity || p.PiecesPerUnit < 1 || p.PiecesPerUnit > 1000000 {
		fail(c, 400, "invalid product fields or numeric limits")
		return
	}
	if create {
		result := tx.Table("products").Create(&p)
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
		id = p.ID
	} else {
		result := tx.Table("products").Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(map[string]any{"title": p.Title, "code": p.Code, "quantity": p.Quantity, "pieces_per_unit": p.PiecesPerUnit, "active": p.Active, "version": gorm.Expr("version + 1")})
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
	}
	if p.Quantity != oldQuantity {
		kind := "adjustment"
		if create {
			kind = "opening"
		}
		if err := tx.Create(&stockMovementRecord{TenantID: tenantID(c), ProductID: id, CreatedByUserID: actor(c).ID, Kind: kind, QuantityDelta: p.Quantity - oldQuantity}).Error; err != nil {
			databaseError(c, err)
			return
		}
	}
	result := tx.Table("products").Select(productColumns).Where("tenant_id = ? AND id = ?", tenantID(c), id).Scan(&p)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	if create {
		c.JSON(201, p)
	} else {
		c.JSON(200, p)
	}
}
