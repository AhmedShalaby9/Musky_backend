package server

import (
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
	"strings"
)

// clientStoredCols selects the persisted columns only (no alias required).
const clientStoredCols = "id,tenant_id,user_id,name,phone,address,active,opening_balance_minor,created_at"

// clientDisplayCols selects all persisted columns (aliased to c.) plus a computed
// balance_minor that sums the opening balance, posted-invoice amounts, and payments.
const clientDisplayCols = "c.id,c.tenant_id,c.user_id,c.name,c.phone,c.address,c.active,c.opening_balance_minor,c.created_at," +
	"(c.opening_balance_minor" +
	"+COALESCE((SELECT SUM(l.amount_minor) FROM client_ledger l WHERE l.tenant_id=c.tenant_id AND l.client_id=c.id),0)" +
	"-COALESCE((SELECT SUM(p.amount_minor) FROM invoice_payments p WHERE p.tenant_id=c.tenant_id AND p.client_id=c.id),0)" +
	"-COALESCE((SELECT SUM(IF(r.reversal_of_id IS NULL,r.amount_minor,-r.amount_minor)) FROM client_receipts r WHERE r.tenant_id=c.tenant_id AND r.client_id=c.id),0)" +
	") AS balance_minor"

// scanClientStored reads the 9 persisted columns. Used inside transactions where
// the full balance expression is not needed (e.g. FOR UPDATE row lock).
func scanClientStored(row scanner) (model.Client, error) {
	var v model.Client
	err := row.Scan(&v.ID, &v.TenantID, &v.UserID, &v.Name, &v.Phone, &v.Address, &v.Active, &v.OpeningBalanceMinor, &v.CreatedAt)
	return v, err
}

// scanClient reads 10 columns: the 9 persisted plus the computed balance_minor.
func scanClient(row scanner) (model.Client, error) {
	var v model.Client
	err := row.Scan(&v.ID, &v.TenantID, &v.UserID, &v.Name, &v.Phone, &v.Address, &v.Active, &v.OpeningBalanceMinor, &v.CreatedAt, &v.BalanceMinor)
	return v, err
}

type clientInput struct {
	UserID              *uint64 `json:"user_id"`
	Name                *string `json:"name"`
	Phone               *string `json:"phone"`
	Address             *string `json:"address"`
	Active              *bool   `json:"active"`
	OpeningBalanceMinor *int64  `json:"opening_balance_minor"`
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
		if *in.Phone == "" {
			v.Phone = nil
		} else {
			v.Phone = in.Phone
		}
	}
	if in.Address != nil {
		v.Address = *in.Address
	}
	if in.Active != nil {
		v.Active = *in.Active
	}
	if in.OpeningBalanceMinor != nil {
		v.OpeningBalanceMinor = *in.OpeningBalanceMinor
	}
}

func (a *API) listClients(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := "SELECT " + clientDisplayCols + " FROM clients c WHERE c.tenant_id=?"
	args := []any{tenantID(c)}
	if c.Query("active") == "true" {
		query += " AND c.active=TRUE"
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query += " AND (c.name LIKE ? OR c.phone LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	query += " ORDER BY c.id LIMIT ? OFFSET ?"
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
	v, err := scanClient(a.db.QueryRowContext(c.Request.Context(),
		"SELECT "+clientDisplayCols+" FROM clients c WHERE c.tenant_id=? AND c.id=?",
		tenantID(c), id))
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, v)
}

func (a *API) createClient(c *gin.Context) { a.saveClient(c, true) }
func (a *API) updateClient(c *gin.Context) { a.saveClient(c, false) }
func (a *API) deleteClient(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	res, err := a.db.ExecContext(c.Request.Context(), "UPDATE clients SET active=FALSE WHERE tenant_id=? AND id=?", tenantID(c), id)
	if err != nil {
		databaseError(c, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(c, 404, "not found")
		return
	}
	c.Status(204)
}

func (a *API) saveClient(c *gin.Context, create bool) {
	var in clientInput
	if !decode(c, &in) {
		return
	}
	if !in.valid() || (create && in.Name == nil) {
		fail(c, 400, "invalid client fields; name is required on creation")
		return
	}
	if !create && in.UserID == nil && in.Name == nil && in.Phone == nil && in.Address == nil && in.Active == nil && in.OpeningBalanceMinor == nil {
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
		v, err = scanClientStored(tx.QueryRowContext(c.Request.Context(),
			"SELECT "+clientStoredCols+" FROM clients WHERE tenant_id=? AND id=? FOR UPDATE",
			tenantID(c), id))
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
		if err = tx.QueryRowContext(c.Request.Context(),
			"SELECT id FROM users WHERE tenant_id=? AND id=? AND active=TRUE FOR SHARE",
			tenantID(c), v.UserID).Scan(&owner); err != nil {
			databaseError(c, err)
			return
		}
	}
	if create {
		result, err := tx.ExecContext(c.Request.Context(),
			"INSERT INTO clients(tenant_id,user_id,name,phone,address,active,opening_balance_minor) VALUES (?,?,?,?,?,?,?)",
			v.TenantID, v.UserID, v.Name, v.Phone, v.Address, v.Active, v.OpeningBalanceMinor)
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
		if _, err = tx.ExecContext(c.Request.Context(),
			"UPDATE clients SET user_id=?,name=?,phone=?,address=?,active=?,opening_balance_minor=? WHERE tenant_id=? AND id=?",
			v.UserID, v.Name, v.Phone, v.Address, v.Active, v.OpeningBalanceMinor, tenantID(c), id); err != nil {
			databaseError(c, err)
			return
		}
	}
	v, err = scanClient(tx.QueryRowContext(c.Request.Context(),
		"SELECT "+clientDisplayCols+" FROM clients c WHERE c.tenant_id=? AND c.id=?",
		tenantID(c), id))
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
