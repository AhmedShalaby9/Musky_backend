package server

import (
	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
)

type chinaInput struct {
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	Notes       string `json:"notes"`
}

func (in *chinaInput) valid() bool {
	return in.AmountMinor >= 1 && in.AmountMinor <= 1000000000000 &&
		(in.Kind == "opening" || in.Kind == "purchase" || in.Kind == "payment" || in.Kind == "adjustment") &&
		validText(in.Notes, 0, 500)
}

func chinaBalance(entries []model.ChinaEntry) int64 {
	var bal int64
	for _, e := range entries {
		if e.Kind == "payment" {
			bal -= e.AmountMinor
		} else {
			bal += e.AmountMinor
		}
	}
	return bal
}

func (a *API) listChinaEntries(c *gin.Context) {
	var entries []model.ChinaEntry
	if err := a.orm.WithContext(c.Request.Context()).
		Where("tenant_id = ?", tenantID(c)).
		Order("created_at, id").
		Find(&entries).Error; err != nil {
		databaseError(c, err)
		return
	}
	bal := chinaBalance(entries)
	c.JSON(200, gin.H{"entries": entries, "balance_minor": bal})
}

func (a *API) createChinaEntry(c *gin.Context) {
	var in chinaInput
	if !decode(c, &in) {
		return
	}
	if !in.valid() {
		fail(c, 400, "invalid china entry fields")
		return
	}

	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()

	// If kind is opening, check that no opening entry exists yet.
	if in.Kind == "opening" {
		var count int64
		if err := tx.Model(&model.ChinaEntry{}).
			Where("tenant_id = ? AND kind = 'opening'", tenantID(c)).
			Count(&count).Error; err != nil {
			databaseError(c, err)
			return
		}
		if count > 0 {
			fail(c, 409, "تم تسجيل الرصيد الافتتاحي مسبقاً")
			return
		}
	}

	// For payments, verify balance is sufficient.
	if in.Kind == "payment" {
		var all []model.ChinaEntry
		if err := tx.Where("tenant_id = ?", tenantID(c)).Find(&all).Error; err != nil {
			databaseError(c, err)
			return
		}
		bal := chinaBalance(all)
		if bal <= 0 {
			fail(c, 422, "لا يوجد رصيد مستحق للصين")
			return
		}
		if in.AmountMinor > bal {
			fail(c, 422, "المبلغ أكبر من الرصيد المستحق")
			return
		}
	}

	entry := model.ChinaEntry{
		TenantID:        tenantID(c),
		CreatedByUserID: actor(c).ID,
		Kind:            in.Kind,
		AmountMinor:     in.AmountMinor,
		Notes:           in.Notes,
	}
	if err := tx.Create(&entry).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Where("tenant_id = ? AND id = ?", tenantID(c), entry.ID).Take(&entry).Error; err != nil {
		databaseError(c, err)
		return
	}

	var all []model.ChinaEntry
	if err := tx.Where("tenant_id = ?", tenantID(c)).Find(&all).Error; err != nil {
		databaseError(c, err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}

	c.JSON(201, gin.H{"entry": entry, "balance_minor": chinaBalance(all)})
}

func (a *API) deleteChinaEntry(c *gin.Context) {
	entryID, ok := pathID(c, "id")
	if !ok {
		return
	}

	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()

	var entry model.ChinaEntry
	if err := tx.Where("tenant_id = ? AND id = ?", tenantID(c), entryID).Take(&entry).Error; err != nil {
		databaseError(c, err)
		return
	}

	// Opening entries cannot be deleted if other entries exist.
	if entry.Kind == "opening" {
		var count int64
		if err := tx.Model(&model.ChinaEntry{}).
			Where("tenant_id = ? AND id <> ?", tenantID(c), entryID).
			Count(&count).Error; err != nil {
			databaseError(c, err)
			return
		}
		if count > 0 {
			fail(c, 422, "لا يمكن حذف الرصيد الافتتاحي بعد وجود حركات أخرى")
			return
		}
	}

	if err := tx.Delete(&entry).Error; err != nil {
		databaseError(c, err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}

	c.Status(204)
}
