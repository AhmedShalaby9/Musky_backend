package server

import (
	"database/sql"
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
	"strconv"
	"strings"
)

const maxQuantity int64 = 1000000000
const maxTotal int64 = 100000000000000
const productColumns = "id,tenant_id,title,code,quantity,pieces_per_unit,active,version,created_at"

func scanProduct(row scanner) (model.Product, error) {
	var p model.Product
	err := row.Scan(&p.ID, &p.TenantID, &p.Title, &p.Code, &p.Quantity, &p.PiecesPerUnit, &p.Active, &p.Version, &p.CreatedAt)
	return p, err
}

// All commerce mutations acquire the tenant row first. This serializes stock
// corrections, invoice posting and numbering within a tenant, not across tenants.
func (a *API) commerceTx(c *gin.Context) (*sql.Tx, bool) {
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return nil, false
	}
	var active bool
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT active FROM tenants WHERE id=? FOR UPDATE", tenantID(c)).Scan(&active); err != nil {
		tx.Rollback()
		databaseError(c, err)
		return nil, false
	}
	if !active {
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
	query := "SELECT " + productColumns + " FROM products WHERE tenant_id=?"
	args := []any{tenantID(c)}
	if c.Query("active") == "true" {
		query += " AND active=TRUE"
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query += " AND (title LIKE ? OR code LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	query += " ORDER BY id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := a.db.QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer rows.Close()
	data := []model.Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			databaseError(c, err)
			return
		}
		data = append(data, p)
	}
	if err = rows.Err(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}
func (a *API) getProduct(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	p, err := scanProduct(a.db.QueryRowContext(c.Request.Context(), "SELECT "+productColumns+" FROM products WHERE tenant_id=? AND id=?", tenantID(c), id))
	if err != nil {
		databaseError(c, err)
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

func (a *API) createProduct(c *gin.Context)  { a.saveProduct(c, true, false) }
func (a *API) updateProduct(c *gin.Context)  { a.saveProduct(c, false, false) }
func (a *API) archiveProduct(c *gin.Context) { a.saveProduct(c, false, true) }
func (a *API) saveProduct(c *gin.Context, create, archive bool) {
	var in productInput
	if archive {
		in.Version, _ = strconv.ParseInt(c.Query("version"), 10, 64)
		active := false
		in.Active = &active
	} else if !decode(c, &in) {
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
	var err error
	if !create {
		p, err = scanProduct(tx.QueryRowContext(c.Request.Context(), "SELECT "+productColumns+" FROM products WHERE tenant_id=? AND id=? FOR UPDATE", tenantID(c), id))
		if err != nil {
			databaseError(c, err)
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
		result, err := tx.ExecContext(c.Request.Context(), "INSERT INTO products(tenant_id,title,code,quantity,pieces_per_unit,active) VALUES (?,?,?,?,?,?)", p.TenantID, p.Title, p.Code, p.Quantity, p.PiecesPerUnit, p.Active)
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
		_, err = tx.ExecContext(c.Request.Context(), "UPDATE products SET title=?,code=?,quantity=?,pieces_per_unit=?,active=?,version=version+1 WHERE tenant_id=? AND id=?", p.Title, p.Code, p.Quantity, p.PiecesPerUnit, p.Active, tenantID(c), id)
		if err != nil {
			databaseError(c, err)
			return
		}
	}
	if p.Quantity != oldQuantity {
		kind := "adjustment"
		if create {
			kind = "opening"
		}
		if _, err = tx.ExecContext(c.Request.Context(), "INSERT INTO stock_movements(tenant_id,product_id,created_by_user_id,kind,quantity_delta) VALUES (?,?,?,?,?)", tenantID(c), id, actor(c).ID, kind, p.Quantity-oldQuantity); err != nil {
			databaseError(c, err)
			return
		}
	}
	p, err = scanProduct(tx.QueryRowContext(c.Request.Context(), "SELECT "+productColumns+" FROM products WHERE tenant_id=? AND id=?", tenantID(c), id))
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	if archive {
		c.Status(204)
	} else if create {
		c.JSON(201, p)
	} else {
		c.JSON(200, p)
	}
}
