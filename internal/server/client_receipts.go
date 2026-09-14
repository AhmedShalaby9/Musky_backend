package server

import (
	"database/sql"

	"github.com/gin-gonic/gin"
	"musky/backend/internal/model"
)

type receiptInput struct {
	AmountMinor int64  `json:"amount_minor"`
	Method      string `json:"method"`
	Notes       string `json:"notes"`
}

func (in *receiptInput) valid() bool {
	return in.AmountMinor >= 1 && in.AmountMinor <= 1000000000000 &&
		(in.Method == "cash" || in.Method == "online") &&
		validText(in.Notes, 0, 500)
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

	// Read current balance for the client.
	var balanceMinor int64
	err := a.db.QueryRowContext(c.Request.Context(),
		"SELECT "+clientDisplayCols+" FROM clients c WHERE c.tenant_id=? AND c.id=?",
		tenantID(c), clientID,
	).Scan(
		new(uint64), new(uint64), new(uint64), new(string), new(interface{}),
		new(string), new(bool), new(int64), new(interface{}),
		&balanceMinor, new(interface{}),
	)
	if err != nil {
		databaseError(c, err)
		return
	}
	if balanceMinor <= 0 {
		fail(c, 422, "رصيد العميل صفر أو دائن، لا يمكن تسجيل دفعة")
		return
	}
	if in.AmountMinor > balanceMinor {
		fail(c, 422, "المبلغ أكبر من رصيد العميل")
		return
	}

	res, err := a.db.ExecContext(c.Request.Context(),
		"INSERT INTO client_receipts(tenant_id,client_id,received_by_user_id,amount_minor,method,notes) VALUES (?,?,?,?,?,?)",
		tenantID(c), clientID, actor(c).ID, in.AmountMinor, in.Method, in.Notes,
	)
	if err != nil {
		databaseError(c, err)
		return
	}
	id, _ := res.LastInsertId()

	var receipt model.ClientReceipt
	err = a.db.QueryRowContext(c.Request.Context(),
		"SELECT id,tenant_id,client_id,received_by_user_id,amount_minor,method,notes,reversal_of_id,received_at,created_at FROM client_receipts WHERE id=?",
		id,
	).Scan(&receipt.ID, &receipt.TenantID, &receipt.ClientID, &receipt.ReceivedByUserID,
		&receipt.AmountMinor, &receipt.Method, &receipt.Notes, &receipt.ReversalOfID,
		&receipt.ReceivedAt, &receipt.CreatedAt)
	if err != nil {
		databaseError(c, err)
		return
	}

	// Re-read updated balance.
	_ = a.db.QueryRowContext(c.Request.Context(),
		"SELECT "+clientDisplayCols+" FROM clients c WHERE c.tenant_id=? AND c.id=?",
		tenantID(c), clientID,
	).Scan(
		new(uint64), new(uint64), new(uint64), new(string), new(interface{}),
		new(string), new(bool), new(int64), new(interface{}),
		&balanceMinor, new(interface{}),
	)

	c.JSON(201, gin.H{"receipt": receipt, "balance_minor": balanceMinor})
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

	// Read original receipt; must belong to same tenant/client and not be a reversal itself.
	var original model.ClientReceipt
	err := a.db.QueryRowContext(c.Request.Context(),
		"SELECT id,tenant_id,client_id,received_by_user_id,amount_minor,method,notes,reversal_of_id,received_at,created_at FROM client_receipts WHERE tenant_id=? AND client_id=? AND id=?",
		tenantID(c), clientID, rid,
	).Scan(&original.ID, &original.TenantID, &original.ClientID, &original.ReceivedByUserID,
		&original.AmountMinor, &original.Method, &original.Notes, &original.ReversalOfID,
		&original.ReceivedAt, &original.CreatedAt)
	if err == sql.ErrNoRows {
		fail(c, 404, "not found")
		return
	}
	if err != nil {
		databaseError(c, err)
		return
	}
	if original.ReversalOfID != nil {
		fail(c, 422, "لا يمكن عكس سجل عكس")
		return
	}

	// Check no existing reversal already exists for this receipt.
	var existingReversal uint64
	err = a.db.QueryRowContext(c.Request.Context(),
		"SELECT id FROM client_receipts WHERE reversal_of_id=?", rid,
	).Scan(&existingReversal)
	if err == nil {
		fail(c, 409, "تم عكس هذه الدفعة مسبقاً")
		return
	}
	if err != sql.ErrNoRows {
		databaseError(c, err)
		return
	}

	res, err := a.db.ExecContext(c.Request.Context(),
		"INSERT INTO client_receipts(tenant_id,client_id,received_by_user_id,amount_minor,method,notes,reversal_of_id) VALUES (?,?,?,?,?,?,?)",
		tenantID(c), clientID, actor(c).ID, original.AmountMinor, original.Method, original.Notes, rid,
	)
	if err != nil {
		databaseError(c, err)
		return
	}
	newID, _ := res.LastInsertId()

	var reversal model.ClientReceipt
	err = a.db.QueryRowContext(c.Request.Context(),
		"SELECT id,tenant_id,client_id,received_by_user_id,amount_minor,method,notes,reversal_of_id,received_at,created_at FROM client_receipts WHERE id=?",
		newID,
	).Scan(&reversal.ID, &reversal.TenantID, &reversal.ClientID, &reversal.ReceivedByUserID,
		&reversal.AmountMinor, &reversal.Method, &reversal.Notes, &reversal.ReversalOfID,
		&reversal.ReceivedAt, &reversal.CreatedAt)
	if err != nil {
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

	// Read client name and balance.
	var clientName string
	var balanceMinor int64
	err := a.db.QueryRowContext(c.Request.Context(),
		"SELECT "+clientDisplayCols+" FROM clients c WHERE c.tenant_id=? AND c.id=?",
		tenantID(c), clientID,
	).Scan(
		new(uint64), new(uint64), new(uint64), &clientName, new(interface{}),
		new(string), new(bool), new(int64), new(interface{}),
		&balanceMinor, new(interface{}),
	)
	if err != nil {
		databaseError(c, err)
		return
	}

	const q = `
SELECT 'opening' AS kind, NULL AS ref_id, NULL AS invoice_number,
       c.opening_balance_minor AS delta_minor,
       NULL AS method, '' AS notes, c.created_at AS at
FROM clients c WHERE c.tenant_id=? AND c.id=?

UNION ALL

SELECT l.kind, i.id, i.number, l.amount_minor,
       NULL, COALESCE(i.void_reason,''),
       COALESCE(i.posted_at, i.voided_at)
FROM client_ledger l JOIN invoices i ON i.id=l.invoice_id
WHERE l.tenant_id=? AND l.client_id=?

UNION ALL

SELECT 'invoice_payment', p.invoice_id, i.number, -p.amount_minor,
       p.method, p.notes, p.paid_at
FROM invoice_payments p JOIN invoices i ON i.id=p.invoice_id
WHERE p.tenant_id=? AND p.client_id=?

UNION ALL

SELECT IF(r.reversal_of_id IS NULL,'receipt','reversal'),
       r.id, NULL,
       IF(r.reversal_of_id IS NULL,-r.amount_minor,r.amount_minor),
       r.method, r.notes, r.received_at
FROM client_receipts r WHERE r.tenant_id=? AND r.client_id=?

ORDER BY at ASC
`
	rows, err := a.db.QueryContext(c.Request.Context(), q,
		tenantID(c), clientID,
		tenantID(c), clientID,
		tenantID(c), clientID,
		tenantID(c), clientID,
	)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer rows.Close()

	var running int64
	entries := []model.LedgerEntry{}
	for rows.Next() {
		var e model.LedgerEntry
		if err = rows.Scan(&e.Kind, &e.RefID, &e.InvoiceNumber, &e.DeltaMinor, &e.Method, &e.Notes, &e.At); err != nil {
			databaseError(c, err)
			return
		}
		running += e.DeltaMinor
		e.RunningBalance = running
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		databaseError(c, err)
		return
	}

	c.JSON(200, gin.H{
		"client": gin.H{
			"id":            clientID,
			"name":          clientName,
			"balance_minor": balanceMinor,
		},
		"entries": entries,
	})
}
