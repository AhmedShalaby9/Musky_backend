package server

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
	"strings"
)

type userRecord struct {
	ID           uint64
	TenantID     *uint64
	Name         string
	Email        string
	PasswordHash string
	Role         model.Role
	Active       bool
	CreatedAt    time.Time
}

func (userRecord) TableName() string { return "users" }

func userFromRecord(v userRecord) model.User {
	return model.User{ID: v.ID, TenantID: v.TenantID, Name: v.Name, Email: v.Email, PasswordHash: v.PasswordHash, Role: v.Role, Active: v.Active, CreatedAt: v.CreatedAt}
}

type apiError struct {
	status  int
	message string
}

func (e apiError) Error() string { return e.message }

func ptr[T any](v T) *T { return &v }

func (a *API) listUsers(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	var data []model.User
	result := a.orm.WithContext(c.Request.Context()).Table("users").Select(userColumns).
		Where("tenant_id = ?", tenantID(c)).Order("id").Limit(limit).Offset(offset).Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, gin.H{"data": data, "limit": limit, "offset": offset})
}
func (a *API) getUser(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var u model.User
	result := a.orm.WithContext(c.Request.Context()).Table("users").Select(userColumns).
		Where("tenant_id = ? AND id = ?", tenantID(c), id).Limit(1).Scan(&u)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	c.JSON(200, u)
}
func (a *API) createUser(c *gin.Context) {
	var in createUserInput
	if !decode(c, &in) {
		return
	}
	if !in.validate() {
		fail(c, 400, "valid name, email, admin/trader role and 12-72 byte password required")
		return
	}
	if in.Role != model.Admin {
		fail(c, 400, "create a new tenant to register a trader; only admins can be added to an existing tenant")
		return
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		fail(c, 500, "internal server error")
		return
	}
	u := userRecord{TenantID: ptr(tenantID(c)), Name: in.Name, Email: in.Email, PasswordHash: hash, Role: in.Role, Active: true}
	if err := a.orm.WithContext(c.Request.Context()).Create(&u).Error; err != nil {
		databaseError(c, err)
		return
	}
	var out model.User
	result := a.orm.WithContext(c.Request.Context()).Table("users").Select(userColumns).Where("tenant_id = ? AND id = ?", tenantID(c), u.ID).Scan(&out)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(201, out)
}

type updateUserInput struct {
	Name     *string     `json:"name"`
	Email    *string     `json:"email"`
	Role     *model.Role `json:"role"`
	Active   *bool       `json:"active"`
	Password *string     `json:"password"`
}

func (a *API) updateUser(c *gin.Context) {
	var in updateUserInput
	if !decode(c, &in) {
		return
	}
	if in.Name == nil && in.Email == nil && in.Role == nil && in.Active == nil && in.Password == nil {
		fail(c, 400, "no changes provided")
		return
	}
	a.saveUser(c, in, false)
}
func (a *API) deactivateUser(c *gin.Context) {
	active := false
	a.saveUser(c, updateUserInput{Active: &active}, true)
}
func (a *API) saveUser(c *gin.Context, in updateUserInput, deleted bool) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !validText(*in.Name, 1, 150) {
			fail(c, 400, "invalid name")
			return
		}
	}
	if in.Email != nil {
		*in.Email = normalizeEmail(*in.Email)
		if !validEmail(*in.Email) {
			fail(c, 400, "invalid email")
			return
		}
	}
	if in.Role != nil && *in.Role != model.Admin && *in.Role != model.Trader {
		fail(c, 400, "role must be admin or trader")
		return
	}
	var hash string
	if in.Password != nil {
		var err error
		hash, err = HashPassword(*in.Password)
		if err != nil {
			fail(c, 400, err.Error())
			return
		}
	}
	var u model.User
	err := a.orm.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var tenant struct{ ID uint64 }
		if err := tx.Table("tenants").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tenantID(c)).Select("id").Scan(&tenant).Error; err != nil {
			return err
		}
		if tenant.ID == 0 {
			return gorm.ErrRecordNotFound
		}
		var stored userRecord
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenant.ID, id).First(&stored)
		if result.Error != nil {
			return result.Error
		}
		u = userFromRecord(stored)
		manager := actor(c)
		if u.Role == model.Trader && manager.Role != model.SuperAdmin && manager.Role != model.Admin {
			return apiError{status: 403, message: "only admins can change the trader password"}
		}
		if manager.Role == model.Admin && (u.Role != model.Trader || in.Password == nil || in.Name != nil || in.Email != nil || in.Role != nil || in.Active != nil) {
			return apiError{status: 403, message: "admins may only change the trader password"}
		}
		if in.Role != nil && *in.Role != u.Role {
			return apiError{status: 409, message: "roles cannot be changed between trader owners and tenant admins"}
		}
		if u.Role == model.Trader && in.Active != nil && !*in.Active {
			return apiError{status: 409, message: "suspend the trader's tenant instead of deactivating its owner"}
		}
		if in.Name != nil {
			u.Name = *in.Name
		}
		if in.Email != nil {
			u.Email = *in.Email
		}
		if in.Role != nil {
			u.Role = *in.Role
		}
		if in.Active != nil {
			u.Active = *in.Active
		}
		if in.Password != nil {
			u.PasswordHash = hash
		}
		updates := map[string]any{"name": u.Name, "email": u.Email, "role": u.Role, "active": u.Active, "password_hash": u.PasswordHash}
		if err := tx.Model(&userRecord{}).Where("tenant_id = ? AND id = ?", tenant.ID, id).Updates(updates).Error; err != nil {
			return err
		}
		if in.Password != nil || in.Role != nil || in.Active != nil || in.Email != nil {
			if err := tx.Where("user_id = ?", id).Delete(&sessionRecord{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var e apiError
		if errors.As(err, &e) {
			fail(c, e.status, e.message)
		} else {
			databaseError(c, err)
		}
		return
	}
	if deleted {
		c.Status(204)
	} else {
		c.JSON(200, u)
	}
}
