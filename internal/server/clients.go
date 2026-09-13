package server

import (
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
	"strings"
)

const clientColumns = "id,tenant_id,user_id,name,phone,address,active,created_at"

func scanClient(row scanner) (model.Client, error) {
	var v model.Client
	err := row.Scan(&v.ID, &v.TenantID, &v.UserID, &v.Name, &v.Phone, &v.Address, &v.Active, &v.CreatedAt)
	return v, err
}

type clientInput struct {
	UserID  *uint64 `json:"user_id"`
	Name    *string `json:"name"`
	Phone   *string `json:"phone"`
	Address *string `json:"address"`
	Active  *bool   `json:"active"`
}

func (in *clientInput) valid() bool {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !validText(*in.Name, 1, 150) {
			return false
		}
	}
	if in.Phone != nil && !validText(*in.Phone, 0, 40) {
		return false
	}
	if in.Address != nil && !validText(*in.Address, 0, 500) {
		return false
	}
	return in.UserID == nil || *in.UserID > 0
}
func (in clientInput) apply(v *model.Client) {
	if in.UserID != nil {
		v.UserID = *in.UserID
	}
	if in.Name != nil {
		v.Name = *in.Name
	}
	if in.Phone != nil {
		v.Phone = *in.Phone
	}
	if in.Address != nil {
		v.Address = *in.Address
	}
	if in.Active != nil {
		v.Active = *in.Active
	}
}
func (a *API) listClients(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := "SELECT " + clientColumns + " FROM clients WHERE tenant_id=?"
	args := []any{tenantID(c)}
	if c.Query("active") == "true" {
		query += " AND active=TRUE"
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query += " AND (name LIKE ? OR phone LIKE ?)"
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
	data := []model.Client{}
	for rows.Next() {
		v, err := scanClient(rows)
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
func (a *API) getClient(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	v, err := scanClient(a.db.QueryRowContext(c.Request.Context(), "SELECT "+clientColumns+" FROM clients WHERE tenant_id=? AND id=?", tenantID(c), id))
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}
func (a *API) createClient(c *gin.Context)  { a.saveClient(c, true, false) }
func (a *API) updateClient(c *gin.Context)  { a.saveClient(c, false, false) }
func (a *API) archiveClient(c *gin.Context) { a.saveClient(c, false, true) }
func (a *API) saveClient(c *gin.Context, create, archive bool) {
	var in clientInput
	if archive {
		active := false
		in.Active = &active
	} else if !decode(c, &in) {
		return
	}
	if !in.valid() || (create && in.Name == nil) {
		fail(c, 400, "invalid client fields; name is required on creation")
		return
	}
	if !create && in.UserID == nil && in.Name == nil && in.Phone == nil && in.Address == nil && in.Active == nil {
		fail(c, 400, "no changes provided")
		return
	}
	u := actor(c)
	v := model.Client{TenantID: tenantID(c), Active: true}
	var id uint64
	if !create {
		var ok bool
		id, ok = pathID(c, "id")
		if !ok {
			return
		}
	}
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	if create {
		if u.TenantID != nil {
			v.UserID = u.ID
		}
	} else {
		v, err = scanClient(tx.QueryRowContext(c.Request.Context(), "SELECT "+clientColumns+" FROM clients WHERE tenant_id=? AND id=? FOR UPDATE", tenantID(c), id))
		if err != nil {
			databaseError(c, err)
			return
		}
	}
	in.apply(&v)
	if v.UserID == 0 {
		fail(c, 400, "user_id is required for super_admin")
		return
	}
	if create || in.UserID != nil {
		var owner uint64
		if err = tx.QueryRowContext(c.Request.Context(), "SELECT id FROM users WHERE tenant_id=? AND id=? AND active=TRUE FOR SHARE", tenantID(c), v.UserID).Scan(&owner); err != nil {
			databaseError(c, err)
			return
		}
	}
	if create {
		result, err := tx.ExecContext(c.Request.Context(), "INSERT INTO clients(tenant_id,user_id,name,phone,address,active) VALUES (?,?,?,?,?,?)", v.TenantID, v.UserID, v.Name, v.Phone, v.Address, v.Active)
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
		if _, err = tx.ExecContext(c.Request.Context(), "UPDATE clients SET user_id=?,name=?,phone=?,address=?,active=? WHERE tenant_id=? AND id=?", v.UserID, v.Name, v.Phone, v.Address, v.Active, tenantID(c), id); err != nil {
			databaseError(c, err)
			return
		}
	}
	v, err = scanClient(tx.QueryRowContext(c.Request.Context(), "SELECT "+clientColumns+" FROM clients WHERE tenant_id=? AND id=?", tenantID(c), id))
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
		c.JSON(201, v)
	} else {
		c.JSON(200, v)
	}
}
