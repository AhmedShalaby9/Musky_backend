package server

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"musky/backend/internal/model"
)

const userContactColumns = "id,tenant_id,user_id,title,value,visible_on_invoice,sort_order,created_at"

type userContactRecord struct {
	ID               uint64
	TenantID         uint64
	UserID           uint64
	Title            string
	Value            string
	VisibleOnInvoice bool
	SortOrder        int
	CreatedAt        time.Time
}

func (userContactRecord) TableName() string { return "user_contacts" }

// canManageUserContacts allows only the account owner or the super admin to
// add, edit or remove contact rows. Setting visible_on_invoice is further
// restricted to the super admin alone (see the input handlers below), so a
// tenant's own admin account can never suppress or promote the trader
// owner's invoice branding, or vice versa.
func canManageUserContacts(actorUser model.User, ownerID uint64) bool {
	return actorUser.Role == model.SuperAdmin || actorUser.ID == ownerID
}

func contactOwnerExists(ctx context.Context, db *gorm.DB, tenant, userID uint64) bool {
	var count int64
	db.WithContext(ctx).Table("users").Where("tenant_id = ? AND id = ?", tenant, userID).Count(&count)
	return count > 0
}

func (a *API) listUserContacts(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	if !contactOwnerExists(c.Request.Context(), a.orm, tenantID(c), userID) {
		fail(c, 404, "not found")
		return
	}
	var data []model.UserContact
	result := a.orm.WithContext(c.Request.Context()).Table("user_contacts").Select(userContactColumns).
		Where("tenant_id = ? AND user_id = ?", tenantID(c), userID).Order("sort_order, id").Scan(&data)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, gin.H{"data": data})
}

type contactInput struct {
	Title            *string `json:"title"`
	Value            *string `json:"value"`
	VisibleOnInvoice *bool   `json:"visible_on_invoice"`
	SortOrder        *int    `json:"sort_order"`
}

func (a *API) createUserContact(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	actorUser := actor(c)
	if !canManageUserContacts(actorUser, userID) {
		fail(c, 403, "only the account owner or super admin can add contacts")
		return
	}
	var in contactInput
	if !decode(c, &in) {
		return
	}
	if in.Title == nil || in.Value == nil {
		fail(c, 400, "title and value are required")
		return
	}
	title := strings.TrimSpace(*in.Title)
	value := strings.TrimSpace(*in.Value)
	if !validText(title, 1, 60) || !validText(value, 1, 160) {
		fail(c, 400, "title must be 1-60 characters and value 1-160 characters")
		return
	}
	if in.SortOrder != nil {
		fail(c, 400, "sort_order cannot be set on create")
		return
	}
	visible := false
	if in.VisibleOnInvoice != nil {
		if actorUser.Role != model.SuperAdmin {
			fail(c, 403, "only super admin can set invoice visibility")
			return
		}
		visible = *in.VisibleOnInvoice
	}
	var id uint64
	err := a.orm.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if !contactOwnerExists(c.Request.Context(), tx, tenantID(c), userID) {
			return gorm.ErrRecordNotFound
		}
		var next struct{ Max int }
		if err := tx.Table("user_contacts").Select("COALESCE(MAX(sort_order),0) AS max").
			Where("tenant_id = ? AND user_id = ?", tenantID(c), userID).Scan(&next).Error; err != nil {
			return err
		}
		record := userContactRecord{TenantID: tenantID(c), UserID: userID, Title: title, Value: value, VisibleOnInvoice: visible, SortOrder: next.Max + 1}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		id = record.ID
		return nil
	})
	if err != nil {
		databaseError(c, err)
		return
	}
	var out model.UserContact
	result := a.orm.WithContext(c.Request.Context()).Table("user_contacts").Select(userContactColumns).
		Where("tenant_id = ? AND id = ?", tenantID(c), id).Scan(&out)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(201, out)
}

func (a *API) updateUserContact(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	contactID, ok := pathID(c, "contactID")
	if !ok {
		return
	}
	actorUser := actor(c)
	if !canManageUserContacts(actorUser, userID) {
		fail(c, 403, "only the account owner or super admin can edit contacts")
		return
	}
	var in contactInput
	if !decode(c, &in) {
		return
	}
	if in.Title == nil && in.Value == nil && in.VisibleOnInvoice == nil && in.SortOrder == nil {
		fail(c, 400, "no changes provided")
		return
	}
	if in.VisibleOnInvoice != nil && actorUser.Role != model.SuperAdmin {
		fail(c, 403, "only super admin can set invoice visibility")
		return
	}
	if in.Title != nil {
		*in.Title = strings.TrimSpace(*in.Title)
		if !validText(*in.Title, 1, 60) {
			fail(c, 400, "invalid title")
			return
		}
	}
	if in.Value != nil {
		*in.Value = strings.TrimSpace(*in.Value)
		if !validText(*in.Value, 1, 160) {
			fail(c, 400, "invalid value")
			return
		}
	}
	err := a.orm.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var stored userContactRecord
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID(c), userID, contactID).First(&stored)
		if result.Error != nil {
			return result.Error
		}
		if in.Title != nil {
			stored.Title = *in.Title
		}
		if in.Value != nil {
			stored.Value = *in.Value
		}
		if in.VisibleOnInvoice != nil {
			stored.VisibleOnInvoice = *in.VisibleOnInvoice
		}
		if in.SortOrder != nil {
			stored.SortOrder = *in.SortOrder
		}
		return tx.Model(&userContactRecord{}).Where("tenant_id = ? AND id = ?", tenantID(c), contactID).Updates(map[string]any{
			"title": stored.Title, "value": stored.Value, "visible_on_invoice": stored.VisibleOnInvoice, "sort_order": stored.SortOrder,
		}).Error
	})
	if err != nil {
		databaseError(c, err)
		return
	}
	var out model.UserContact
	result := a.orm.WithContext(c.Request.Context()).Table("user_contacts").Select(userContactColumns).
		Where("tenant_id = ? AND id = ?", tenantID(c), contactID).Scan(&out)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	c.JSON(200, out)
}

func (a *API) deleteUserContact(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	contactID, ok := pathID(c, "contactID")
	if !ok {
		return
	}
	actorUser := actor(c)
	if !canManageUserContacts(actorUser, userID) {
		fail(c, 403, "only the account owner or super admin can delete contacts")
		return
	}
	result := a.orm.WithContext(c.Request.Context()).
		Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID(c), userID, contactID).Delete(&userContactRecord{})
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
