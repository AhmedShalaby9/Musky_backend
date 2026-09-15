package server

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	var tenant tenantRecord
	var trader userRecord
	err = a.orm.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		tenant = tenantRecord{Name: in.Name, Active: true}
		if err := tx.Create(&tenant).Error; err != nil {
			return err
		}
		trader = userRecord{TenantID: ptr(tenant.ID), Name: in.Trader.Name, Email: in.Trader.Email, PasswordHash: hash, Role: model.Trader, Active: true}
		return tx.Create(&trader).Error
	})
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": tenant.ID, "name": tenant.Name, "active": true, "trader_id": trader.ID})
}
func (a *API) listTenants(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	var data []model.Tenant
	result := a.orm.WithContext(c.Request.Context()).Table("tenants").Select("id,name,active,logo_url,created_at").
		Order("id").Limit(limit).Offset(offset).Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
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
	var t model.Tenant
	err := a.orm.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&t)
		if result.Error != nil {
			return result.Error
		}
		if in.Name != nil {
			t.Name = *in.Name
		}
		if in.Active != nil {
			t.Active = *in.Active
		}
		if err := tx.Model(&tenantRecord{}).Where("id = ?", id).Updates(map[string]any{"name": t.Name, "active": t.Active}).Error; err != nil {
			return err
		}
		if !t.Active {
			return tx.Where("user_id IN (?)", tx.Model(&userRecord{}).Select("id").Where("tenant_id = ?", id)).Delete(&sessionRecord{}).Error
		}
		return nil
	})
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, t)
}

type tenantRecord struct {
	ID     uint64 `gorm:"primaryKey"`
	Name   string
	Active bool
}

func (tenantRecord) TableName() string { return "tenants" }
