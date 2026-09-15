package server

import (
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"musky/backend/internal/model"
)

type receiptInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Method      string `json:"method"`
	Notes       string `json:"notes"`
}

func (in *receiptInput) valid() bool {
	return in.AmountMinor >= 1 && in.AmountMinor <= 1000000000000 && (in.Method == "cash" || in.Method == "online") && validText(in.Notes, 0, 500)
}

func clientBalance(db *gorm.DB, tenant, id uint64) (model.Client, error) {
	var client model.Client
	result := clientDisplayQuery(db, tenant).Where("c.id = ?", id).Scan(&client)
	if result.Error != nil {
		return client, result.Error
	}
	if result.RowsAffected == 0 {
		return client, gorm.ErrRecordNotFound
	}
	return client, nil
}

func (a *API) createClientReceipt(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in receiptInput
	if !decode(c, &in) {
		return
	}
	if !in.valid() {
		fail(c, 400, "invalid receipt fields")
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	client, err := clientBalance(tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	if client.BalanceMinor <= 0 {
		fail(c, 422, "رصيد العميل صفر أو دائن، لا يمكن تسجيل دفعة")
		return
	}
	if in.AmountMinor > client.BalanceMinor {
		fail(c, 422, "المبلغ أكبر من رصيد العميل")
		return
	}
	receipt := model.ClientReceipt{TenantID: tenantID(c), ClientID: clientID, ReceivedByUserID: actor(c).ID, AmountMinor: in.AmountMinor, Method: in.Method, Notes: in.Notes}
	if err := tx.Create(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Where("tenant_id = ? AND id = ?", tenantID(c), receipt.ID).Take(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	client, err = clientBalance(tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"receipt": receipt, "balance_minor": client.BalanceMinor})
}

func (a *API) reverseClientReceipt(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	rid, ok := pathID(c, "rid")
	if !ok {
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	var original model.ClientReceipt
	result := tx.Where("tenant_id=? AND client_id=? AND id=?", tenantID(c), clientID, rid).First(&original)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		fail(c, 404, "not found")
		return
	}
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if original.ReversalOfID != nil {
		fail(c, 422, "لا يمكن عكس سجل عكس")
		return
	}
	var existing model.ClientReceipt
	check := tx.Where("tenant_id = ? AND client_id = ? AND reversal_of_id = ?", tenantID(c), clientID, rid).First(&existing)
	if check.Error == nil {
		fail(c, 409, "تم عكس هذه الدفعة مسبقاً")
		return
	}
	if !errors.Is(check.Error, gorm.ErrRecordNotFound) {
		databaseError(c, check.Error)
		return
	}
	reversal := model.ClientReceipt{TenantID: tenantID(c), ClientID: clientID, ReceivedByUserID: actor(c).ID, AmountMinor: original.AmountMinor, Method: original.Method, Notes: original.Notes, ReversalOfID: &rid}
	if err := tx.Create(&reversal).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Where("tenant_id = ? AND id = ?", tenantID(c), reversal.ID).Take(&reversal).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, reversal)
}

func (a *API) clientLedger(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx := a.orm.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil { databaseError(c, tx.Error); return }
	defer tx.Rollback()
	client, err := clientBalance(tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	opening := tx.Table("clients c").Select("'opening' AS kind, NULL AS ref_id, NULL AS invoice_number, c.opening_balance_minor AS delta_minor, NULL AS method, '' AS notes, c.created_at AS at").Where("c.tenant_id = ? AND c.id = ?", tenantID(c), clientID)
	ledger := tx.Table("client_ledger l").Select("l.kind, i.id AS ref_id, i.number AS invoice_number, l.amount_minor AS delta_minor, NULL AS method, COALESCE(i.void_reason,'') AS notes, CASE WHEN l.kind='void' THEN i.voided_at ELSE i.posted_at END AS at").Joins("JOIN invoices i ON i.id=l.invoice_id AND i.tenant_id=l.tenant_id").Where("l.tenant_id = ? AND l.client_id = ?", tenantID(c), clientID)
	payments := tx.Table("invoice_payments p").Select("'invoice_payment' AS kind, p.invoice_id AS ref_id, i.number AS invoice_number, -p.amount_minor AS delta_minor, p.method, p.notes, p.paid_at AS at").Joins("JOIN invoices i ON i.id=p.invoice_id AND i.tenant_id=p.tenant_id").Where("p.tenant_id = ? AND p.client_id = ?", tenantID(c), clientID)
	receipts := tx.Model(&model.ClientReceipt{}).Select("IF(reversal_of_id IS NULL,'receipt','reversal') AS kind, id AS ref_id, NULL AS invoice_number, IF(reversal_of_id IS NULL,-amount_minor,amount_minor) AS delta_minor, method, notes, received_at AS at").Where("tenant_id = ? AND client_id = ?", tenantID(c), clientID)
	entries := []model.LedgerEntry{}
	if err := tx.Table("(? UNION ALL ? UNION ALL ? UNION ALL ?) entries", opening, ledger, payments, receipts).Order("at,kind,ref_id").Scan(&entries).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	var running int64
	for i := range entries {
		running += entries[i].DeltaMinor
		entries[i].RunningBalance = running
	}
	c.JSON(200, gin.H{"client": gin.H{"id": clientID, "name": client.Name, "balance_minor": client.BalanceMinor}, "entries": entries})
}
