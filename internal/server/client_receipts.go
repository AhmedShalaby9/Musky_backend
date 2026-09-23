package server

import (
	"context"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"musky/backend/internal/model"
)

type receiptInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Direction   string `json:"direction"`
	Method      string `json:"method"`
	Notes       string `json:"notes"`
}

func (in *receiptInput) valid() bool {
	return in.AmountMinor >= 1 && in.AmountMinor <= 1000000000000 && (in.Method == "cash" || in.Method == "online") && (in.Direction == "in" || in.Direction == "out") && validText(in.Notes, 0, 500)
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

func loadClientLedger(ctx context.Context, db *gorm.DB, tenant, clientID uint64) (model.Client, []model.LedgerEntry, error) {
	tx := db.WithContext(ctx)
	client, err := clientBalance(tx, tenant, clientID)
	if err != nil {
		return client, nil, err
	}
	opening := tx.Table("clients c").Select("'opening' AS kind, NULL AS ref_id, NULL AS invoice_number, '' AS document_type, CASE WHEN c.opening_balance_type='payable' THEN -c.opening_balance_minor ELSE c.opening_balance_minor END AS delta_minor, NULL AS method, '' AS notes, c.created_at AS at").Where("c.tenant_id = ? AND c.id = ?", tenant, clientID)
	ledger := tx.Table("client_ledger l").Select("l.kind, i.id AS ref_id, i.number AS invoice_number, i.document_type, l.amount_minor AS delta_minor, NULL AS method, COALESCE(i.void_reason,'') AS notes, CASE WHEN l.kind IN ('void','purchase_void') THEN COALESCE(i.voided_at,i.posted_at,i.created_at) ELSE COALESCE(i.posted_at,i.created_at) END AS at").Joins("JOIN invoices i ON i.id=l.invoice_id AND i.tenant_id=l.tenant_id").Where("l.tenant_id = ? AND l.client_id = ?", tenant, clientID)
	payments := tx.Table("invoice_payments p").Select("'invoice_payment' AS kind, p.invoice_id AS ref_id, i.number AS invoice_number, i.document_type, CASE WHEN i.document_type='purchase' THEN p.amount_minor ELSE -p.amount_minor END AS delta_minor, p.method, p.notes, p.paid_at AS at").Joins("JOIN invoices i ON i.id=p.invoice_id AND i.tenant_id=p.tenant_id").Where("p.tenant_id = ? AND p.client_id = ?", tenant, clientID)
	receipts := tx.Model(&model.ClientReceipt{}).Select("IF(reversal_of_id IS NULL,IF(direction='out','client_payment','receipt'), 'reversal') AS kind, id AS ref_id, NULL AS invoice_number, '' AS document_type, IF(reversal_of_id IS NULL,IF(direction='out',amount_minor,-amount_minor),IF(direction='out',-amount_minor,amount_minor)) AS delta_minor, method, notes, received_at AS at").Where("tenant_id = ? AND client_id = ?", tenant, clientID)
	entries := []model.LedgerEntry{}
	if err := tx.Table("(? UNION ALL ? UNION ALL ? UNION ALL ?) entries", opening, ledger, payments, receipts).Order("at,kind,ref_id").Scan(&entries).Error; err != nil {
		return client, nil, err
	}
	var running int64
	for i := range entries {
		running += entries[i].DeltaMinor
		entries[i].RunningBalance = running
	}
	return client, entries, nil
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
	if in.Direction == "" {
		in.Direction = "in"
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
	if in.Direction == "in" && client.BalanceMinor <= 0 {
		fail(c, 422, "لا يوجد رصيد مستحق من العميل")
		return
	}
	if in.Direction == "in" && in.AmountMinor > client.BalanceMinor {
		fail(c, 422, "المبلغ أكبر من رصيد العميل")
		return
	}
	if in.Direction == "out" && client.BalanceMinor >= 0 {
		fail(c, 422, "لا يوجد رصيد مستحق للعميل")
		return
	}
	if in.Direction == "out" && in.AmountMinor > -client.BalanceMinor {
		fail(c, 422, "المبلغ أكبر من رصيد العميل الدائن")
		return
	}
	receipt := model.ClientReceipt{TenantID: tenantID(c), ClientID: clientID, ReceivedByUserID: actor(c).ID, AmountMinor: in.AmountMinor, Direction: in.Direction, Method: in.Method, Notes: in.Notes}
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

func (a *API) updateClientReceipt(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	receiptID, ok := pathID(c, "receiptID")
	if !ok {
		return
	}
	var in receiptInput
	if !decode(c, &in) || in.Direction == "" || !in.valid() {
		fail(c, 400, "invalid receipt fields")
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	var receipt model.ClientReceipt
	if err := tx.Where("tenant_id = ? AND id = ? AND client_id = ? AND reversal_of_id IS NULL", tenantID(c), receiptID, clientID).Take(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	client, err := clientBalance(tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	oldEffect := receipt.AmountMinor
	if receipt.Direction == "in" {
		oldEffect = -oldEffect
	}
	base := client.BalanceMinor - oldEffect
	newEffect := in.AmountMinor
	if in.Direction == "in" {
		newEffect = -newEffect
	}
	resulting := base + newEffect
	if resulting < 0 && in.Direction != "out" {
		fail(c, 422, "المبلغ أكبر من رصيد العميل")
		return
	}
	if resulting > 0 && in.Direction != "in" {
		fail(c, 422, "المبلغ أكبر من رصيد العميل الدائن")
		return
	}
	if err = tx.Model(&receipt).Updates(map[string]any{"amount_minor": in.AmountMinor, "direction": in.Direction, "method": in.Method, "notes": in.Notes}).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Where("tenant_id = ? AND id = ?", tenantID(c), receiptID).Take(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	client, err = clientBalance(tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"receipt": receipt, "balance_minor": client.BalanceMinor})
}

func (a *API) deleteClientReceipt(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	receiptID, ok := pathID(c, "receiptID")
	if !ok {
		return
	}
	tx, ok := a.commerceTx(c)
	if !ok {
		return
	}
	defer tx.Rollback()
	var receipt model.ClientReceipt
	if err := tx.Where("tenant_id = ? AND id = ? AND client_id = ?", tenantID(c), receiptID, clientID).Take(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Where("tenant_id = ? AND client_id = ? AND reversal_of_id = ?", tenantID(c), clientID, receiptID).Delete(&model.ClientReceipt{}).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Delete(&receipt).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}

func (a *API) clientLedger(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx := a.orm.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil {
		databaseError(c, tx.Error)
		return
	}
	defer tx.Rollback()
	client, entries, err := loadClientLedger(c.Request.Context(), tx, tenantID(c), clientID)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"client": gin.H{"id": clientID, "name": client.Name, "balance_minor": client.BalanceMinor}, "entries": entries})
}
