package server

import (
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
	"strings"
)

type createUserInput struct {
	Name     string     `json:"name"`
	Email    string     `json:"email"`
	Password string     `json:"password"`
	Role     model.Role `json:"role"`
}

func (in *createUserInput) validate() bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = normalizeEmail(in.Email)
	return validText(in.Name, 1, 150) && validEmail(in.Email) && len(in.Password) >= 12 && len(in.Password) <= 72 && (in.Role == model.Admin || in.Role == model.Trader)
}
func (a *API) createTenant(c *gin.Context) {
	var in struct {
		Name   string          `json:"name"`
		Trader createUserInput `json:"trader"`
	}
	if !decode(c, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Trader.Role != "" && in.Trader.Role != model.Trader {
		fail(c, 400, "initial user must be a trader")
		return
	}
	in.Trader.Role = model.Trader
	if !validText(in.Name, 1, 150) || !in.Trader.validate() {
		fail(c, 400, "valid tenant name, trader name, email and 12-72 byte password required")
		return
	}
	hash, err := HashPassword(in.Trader.Password)
	if err != nil {
		fail(c, 500, "internal server error")
		return
	}
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(c.Request.Context(), "INSERT INTO tenants(name) VALUES (?)", in.Name)
	if err != nil {
		databaseError(c, err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		databaseError(c, err)
		return
	}
	res, err = tx.ExecContext(c.Request.Context(), "INSERT INTO users(tenant_id,name,email,password_hash,role) VALUES (?,?,?,?,'trader')", id, in.Trader.Name, in.Trader.Email, hash)
	if err != nil {
		databaseError(c, err)
		return
	}
	userID, err := res.LastInsertId()
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": id, "name": in.Name, "active": true, "trader_id": userID})
}
func (a *API) listTenants(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	rows, err := a.db.QueryContext(c.Request.Context(), "SELECT id,name,active,logo_url,created_at FROM tenants ORDER BY id LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer rows.Close()
	data := []model.Tenant{}
	for rows.Next() {
		var t model.Tenant
		if err = rows.Scan(&t.ID, &t.Name, &t.Active, &t.LogoURL, &t.CreatedAt); err != nil {
			databaseError(c, err)
			return
		}
		data = append(data, t)
	}
	if err = rows.Err(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}
func (a *API) updateTenant(c *gin.Context) {
	id, ok := pathID(c, "tenantID")
	if !ok {
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Active *bool   `json:"active"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Name == nil && in.Active == nil {
		fail(c, 400, "provide name or active")
		return
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !validText(*in.Name, 1, 150) {
			fail(c, 400, "invalid name")
			return
		}
	}
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	var t model.Tenant
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT id,name,active,logo_url,created_at FROM tenants WHERE id=? FOR UPDATE", id).Scan(&t.ID, &t.Name, &t.Active, &t.LogoURL, &t.CreatedAt); err != nil {
		databaseError(c, err)
		return
	}
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Active != nil {
		t.Active = *in.Active
	}
	if _, err = tx.ExecContext(c.Request.Context(), "UPDATE tenants SET name=?,active=? WHERE id=?", t.Name, t.Active, id); err != nil {
		databaseError(c, err)
		return
	}
	if !t.Active {
		if _, err = tx.ExecContext(c.Request.Context(), "DELETE s FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.tenant_id=?", id); err != nil {
			databaseError(c, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, t)
}
