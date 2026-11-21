package server

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"musky/backend/internal/model"
)

type expenseInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Description string `json:"description"`
	SpentAt     string `json:"spent_at"`
}

func (in *expenseInput) valid() bool {
	if in.AmountMinor < 1 || in.AmountMinor > 1000000000000 {
		return false
	}
	if !validText(in.Description, 1, 500) {
		return false
	}
	if _, err := time.Parse("2006-01-02", in.SpentAt); err != nil {
		return false
	}
	return true
}

func (a *API) listExpenses(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}

	db := a.orm.WithContext(c.Request.Context())
	query := db.Table("expenses").Where("tenant_id = ?", tenantID(c))

	from := c.Query("from")
	to := c.Query("to")
	if from != "" {
		if _, err := time.Parse("2006-01-02", from); err != nil {
			fail(c, 400, "from must use YYYY-MM-DD")
			return
		}
		query = query.Where("spent_at >= ?", from)
	}
	if to != "" {
		if _, err := time.Parse("2006-01-02", to); err != nil {
			fail(c, 400, "to must use YYYY-MM-DD")
			return
		}
		query = query.Where("spent_at <= ?", to)
	}

	// Total for the filtered period.
	var totalMinor int64
	if err := query.Session(&gorm.Session{}).Select("COALESCE(SUM(amount_minor),0)").Scan(&totalMinor).Error; err != nil {
		databaseError(c, err)
		return
	}

	var expenses []model.Expense
	if err := query.Order("spent_at DESC, id DESC").Limit(limit).Offset(offset).Find(&expenses).Error; err != nil {
		databaseError(c, err)
		return
	}

	c.JSON(200, gin.H{"data": expenses, "total_minor": totalMinor, "limit": limit, "offset": offset})
}

func (a *API) createExpense(c *gin.Context) {
	var in expenseInput
	if !decode(c, &in) {
		return
	}
	if !in.valid() {
		fail(c, 400, "invalid expense fields")
		return
	}

	expense := model.Expense{
		TenantID:        tenantID(c),
		CreatedByUserID: actor(c).ID,
		AmountMinor:     in.AmountMinor,
		Description:     in.Description,
		SpentAt:         in.SpentAt,
	}
	if err := a.orm.WithContext(c.Request.Context()).Create(&expense).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := a.orm.WithContext(c.Request.Context()).Where("tenant_id = ? AND id = ?", tenantID(c), expense.ID).Take(&expense).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"expense": expense})
}

func (a *API) deleteExpense(c *gin.Context) {
	entryID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var expense model.Expense
	if err := a.orm.WithContext(c.Request.Context()).Where("tenant_id = ? AND id = ?", tenantID(c), entryID).Take(&expense).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := a.orm.WithContext(c.Request.Context()).Delete(&expense).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}
