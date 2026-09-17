package server

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
	"strconv"
	"strings"
	"time"
)

// clientStoredCols selects the persisted columns only (no alias required).
const clientStoredCols = "id,tenant_id,user_id,name,phone,address,active,opening_balance_minor,opening_balance_type,created_at"

type clientRecord struct {
	ID                  uint64
	TenantID            uint64
	UserID              uint64
	Name                string
	Phone               *string
	Address             string
	Active              bool
	OpeningBalanceMinor int64
	OpeningBalanceType  string
	CreatedAt           time.Time
}

func (clientRecord) TableName() string { return "clients" }

func clientFromRecord(v clientRecord) model.Client {
	return model.Client{ID: v.ID, TenantID: v.TenantID, UserID: v.UserID, Name: v.Name, Phone: v.Phone, Address: v.Address, Active: v.Active, OpeningBalanceMinor: v.OpeningBalanceMinor, OpeningBalanceType: v.OpeningBalanceType, CreatedAt: v.CreatedAt}
}

// Aggregate once per tenant and share the balance formula across all endpoints.
func clientDisplayQuery(db *gorm.DB, tenant uint64) *gorm.DB {
	ledger := db.Model(&clientLedgerRecord{}).Select("client_id, SUM(amount_minor) AS amount").Where("tenant_id = ?", tenant).Group("client_id")
	payments := db.Table("invoice_payments p").Select("p.client_id, SUM(CASE WHEN i.document_type='purchase' THEN -p.amount_minor ELSE p.amount_minor END) AS amount, MAX(p.paid_at) AS last_at").Joins("JOIN invoices i ON i.tenant_id=p.tenant_id AND i.id=p.invoice_id").Where("p.tenant_id = ?", tenant).Group("p.client_id")
	receipts := db.Model(&model.ClientReceipt{}).Select("client_id, SUM(IF(reversal_of_id IS NULL,IF(direction='out',-amount_minor,amount_minor),IF(direction='out',amount_minor,-amount_minor))) AS amount, MAX(IF(reversal_of_id IS NULL,received_at,NULL)) AS last_at").Where("tenant_id = ?", tenant).Group("client_id")
	return db.Table("clients c").Select("c.id,c.tenant_id,c.user_id,c.name,c.phone,c.address,c.active,c.opening_balance_minor,c.opening_balance_type,c.created_at,"+
		"((CASE WHEN c.opening_balance_type='payable' THEN -c.opening_balance_minor ELSE c.opening_balance_minor END) + COALESCE(l.amount,0) - COALESCE(p.amount,0) - COALESCE(r.amount,0)) AS balance_minor,"+
		"CASE WHEN p.last_at IS NULL THEN r.last_at WHEN r.last_at IS NULL THEN p.last_at ELSE GREATEST(p.last_at,r.last_at) END AS last_payment_at").
		Joins("LEFT JOIN (?) l ON l.client_id = c.id", ledger).
		Joins("LEFT JOIN (?) p ON p.client_id = c.id", payments).
		Joins("LEFT JOIN (?) r ON r.client_id = c.id", receipts).Where("c.tenant_id = ?", tenant)
}

type clientInput struct {
	UserID              *uint64 `json:"user_id"`
	Name                *string `json:"name"`
	Phone               *string `json:"phone"`
	Address             *string `json:"address"`
	Active              *bool   `json:"active"`
	OpeningBalanceMinor *int64  `json:"opening_balance_minor"`
	OpeningBalanceType  *string `json:"opening_balance_type"`
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
	if in.OpeningBalanceType != nil && *in.OpeningBalanceType != "receivable" && *in.OpeningBalanceType != "payable" {
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
		if v.OpeningBalanceMinor < 0 {
			v.OpeningBalanceMinor = -v.OpeningBalanceMinor
			v.OpeningBalanceType = "payable"
		}
	}
	if in.OpeningBalanceType != nil {
		v.OpeningBalanceType = *in.OpeningBalanceType
	}
}

func (a *API) listClients(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	query := clientDisplayQuery(a.orm.WithContext(c.Request.Context()), tenantID(c))
	if c.Query("active") == "true" {
		query = query.Where("c.active = TRUE")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query = query.Where("(c.name LIKE ? OR c.phone LIKE ?)", "%"+q+"%", "%"+q+"%")
	}
	if d := c.Query("days_without_payment"); d != "" {
		days, err := strconv.Atoi(d)
		if err != nil || days < 1 {
			fail(c, 400, "days_without_payment must be a positive integer")
			return
		}
		query = query.Having("last_payment_at IS NULL OR last_payment_at < UTC_TIMESTAMP() - INTERVAL ? DAY", days)
	}
	data := []model.Client{}
	result := query.Order("c.id").Limit(limit).Offset(offset).Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}

func (a *API) getClient(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var v model.Client
	result := clientDisplayQuery(a.orm.WithContext(c.Request.Context()), tenantID(c)).
		Where("c.tenant_id = ? AND c.id = ?", tenantID(c), id).Limit(1).Scan(&v)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
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
	result := a.orm.WithContext(c.Request.Context()).Model(&clientRecord{}).
		Where("tenant_id = ? AND id = ?", tenantID(c), id).
		Update("active", false)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
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
	if !create && in.UserID == nil && in.Name == nil && in.Phone == nil && in.Address == nil && in.Active == nil && in.OpeningBalanceMinor == nil && in.OpeningBalanceType == nil {
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
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	if create {
		if u.TenantID != nil {
			v.UserID = u.ID
		}
	} else {
		var record clientRecord
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", tenantID(c), id).First(&record)
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
		v = clientFromRecord(record)
	}
	in.apply(&v)
	if v.UserID == 0 {
		fail(c, 400, "user_id is required for super_admin")
		return
	}
	if create || in.UserID != nil {
		var owner model.User
		result := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Select("id").Where("tenant_id = ? AND id = ? AND active = TRUE", tenantID(c), v.UserID).First(&owner)
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
	}
	if create {
		record := clientRecord{TenantID: v.TenantID, UserID: v.UserID, Name: v.Name, Phone: v.Phone, Address: v.Address, Active: v.Active, OpeningBalanceMinor: v.OpeningBalanceMinor, OpeningBalanceType: v.OpeningBalanceType}
		result := tx.Create(&record)
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
		id = record.ID
	} else {
		result := tx.Model(&clientRecord{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Updates(map[string]any{
			"user_id": v.UserID, "name": v.Name, "phone": v.Phone, "address": v.Address,
			"active": v.Active, "opening_balance_minor": v.OpeningBalanceMinor, "opening_balance_type": v.OpeningBalanceType,
		})
		if result.Error != nil {
			databaseError(c, result.Error)
			return
		}
	}
	var display model.Client
	result := clientDisplayQuery(tx, tenantID(c)).
		Where("c.tenant_id = ? AND c.id = ?", tenantID(c), id).Limit(1).Scan(&display)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	if create {
		c.JSON(201, display)
	} else {
		c.JSON(200, display)
	}
}
