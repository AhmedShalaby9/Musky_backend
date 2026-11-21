package server

import (
	"time"

	"github.com/gin-gonic/gin"
)

type productReport struct {
	ProductID   uint64 `json:"product_id"`
	Title       string `json:"title"`
	Code        string `json:"code"`
	CostMinor   int64  `json:"cost_minor"`
	QtySold     int64  `json:"qty_sold"`
	Revenue     int64  `json:"revenue_minor"`
	COGS        int64  `json:"cogs_minor"`
	Profit      int64  `json:"profit_minor"`
	ReturnedQty int64  `json:"returned_qty"`
	ReturnedAmt int64  `json:"returned_minor"`
}

type clientReport struct {
	ClientID uint64 `json:"client_id"`
	Name     string `json:"name"`
	Revenue  int64  `json:"revenue_minor"`
	Paid     int64  `json:"paid_minor"`
	Count    int64  `json:"invoice_count"`
}

func (a *API) reports(c *gin.Context) {
	from := c.Query("from")
	to := c.Query("to")
	if from != "" {
		if _, err := time.Parse("2006-01-02", from); err != nil {
			fail(c, 400, "from must use YYYY-MM-DD")
			return
		}
	}
	if to != "" {
		if _, err := time.Parse("2006-01-02", to); err != nil {
			fail(c, 400, "to must use YYYY-MM-DD")
			return
		}
	}

	db := a.orm.WithContext(c.Request.Context())
	tid := tenantID(c)

	// ── Product-level report (sales only, posted invoices) ──
	q := db.Table("invoice_items ii").
		Select(`
			ii.product_id,
			MAX(ii.title) AS title,
			MAX(ii.code) AS code,
			COALESCE(MAX(p.cost_minor),0) AS cost_minor,
			SUM(ii.units_per_package * ii.package_count) AS qty_sold,
			SUM(ii.total_minor) AS revenue,
			SUM(COALESCE(p.cost_minor,0) * ii.units_per_package * ii.package_count) AS cogs,
			SUM(ii.total_minor) - SUM(COALESCE(p.cost_minor,0) * ii.units_per_package * ii.package_count) AS profit
		`).
		Joins("JOIN invoices i ON i.id=ii.invoice_id AND i.tenant_id=ii.tenant_id").
		Joins("LEFT JOIN products p ON p.id=ii.product_id AND p.tenant_id=ii.tenant_id").
		Where("ii.tenant_id = ? AND i.status = 'posted' AND i.document_type = 'sale'", tid)

	if from != "" {
		q = q.Where("i.issue_date >= ?", from)
	}
	if to != "" {
		q = q.Where("i.issue_date <= ?", to)
	}
	q = q.Group("ii.product_id")

	var products []productReport
	if err := q.Order("qty_sold DESC").Scan(&products).Error; err != nil {
		databaseError(c, err)
		return
	}

	// ── Returns per product in the same period ──
	rq := db.Table("invoice_return_items ri").
		Select(`
			ri.product_id,
			SUM(ri.units_per_package * ri.package_count) AS returned_qty,
			SUM(ri.total_minor) AS returned_amt
		`).
		Joins("JOIN invoice_returns r ON r.id=ri.return_id AND r.tenant_id=ri.tenant_id").
		Joins("JOIN invoices i ON i.id=r.invoice_id AND i.tenant_id=r.tenant_id").
		Where("ri.tenant_id = ? AND i.status = 'posted' AND i.document_type = 'sale'", tid)
	if from != "" {
		rq = rq.Where("i.issue_date >= ?", from)
	}
	if to != "" {
		rq = rq.Where("i.issue_date <= ?", to)
	}
	rq = rq.Group("ri.product_id")

	type returnRow struct {
		ProductID   uint64 `json:"product_id"`
		ReturnedQty int64  `json:"returned_qty"`
		ReturnedAmt int64  `json:"returned_amt"`
	}
	var returns []returnRow
	if err := rq.Scan(&returns).Error; err != nil {
		databaseError(c, err)
		return
	}
	returnMap := map[uint64]returnRow{}
	for _, r := range returns {
		returnMap[r.ProductID] = r
	}
	for i := range products {
		if r, ok := returnMap[products[i].ProductID]; ok {
			products[i].ReturnedQty = r.ReturnedQty
			products[i].ReturnedAmt = r.ReturnedAmt
		}
	}

	// ── Totals ──
	var totalRevenue, totalCOGS, totalProfit, totalReturned int64
	for _, p := range products {
		totalRevenue += p.Revenue
		totalCOGS += p.COGS
		totalProfit += p.Profit
		totalReturned += p.ReturnedAmt
	}

	// ── Top clients by revenue ──
	cq := db.Table("invoices i").
		Select(`
			i.client_id,
			MAX(i.client_name) AS name,
			SUM(i.total_minor) AS revenue,
			COALESCE(SUM(paid.paid),0) AS paid,
			COUNT(i.id) AS count
		`).
		Joins("LEFT JOIN (SELECT invoice_id, tenant_id, SUM(amount_minor) AS paid FROM invoice_payments GROUP BY invoice_id, tenant_id) paid ON paid.invoice_id=i.id AND paid.tenant_id=i.tenant_id").
		Where("i.tenant_id = ? AND i.status = 'posted' AND i.document_type = 'sale'", tid)
	if from != "" {
		cq = cq.Where("i.issue_date >= ?", from)
	}
	if to != "" {
		cq = cq.Where("i.issue_date <= ?", to)
	}
	var clients []clientReport
	if err := cq.Group("i.client_id").Order("revenue DESC").Limit(20).Scan(&clients).Error; err != nil {
		databaseError(c, err)
		return
	}

	// ── Expenses in the same period ──
	eq := db.Table("expenses").Select("COALESCE(SUM(amount_minor),0)").Where("tenant_id = ?", tid)
	if from != "" {
		eq = eq.Where("spent_at >= ?", from)
	}
	if to != "" {
		eq = eq.Where("spent_at <= ?", to)
	}
	var totalExpenses int64
	if err := eq.Scan(&totalExpenses).Error; err != nil {
		databaseError(c, err)
		return
	}

	// ── Purchase invoices total ──
	pq := db.Table("invoices").Select("COALESCE(SUM(total_minor),0)").
		Where("tenant_id = ? AND status = 'posted' AND document_type = 'purchase'", tid)
	if from != "" {
		pq = pq.Where("issue_date >= ?", from)
	}
	if to != "" {
		pq = pq.Where("issue_date <= ?", to)
	}
	var totalPurchases int64
	if err := pq.Scan(&totalPurchases).Error; err != nil {
		databaseError(c, err)
		return
	}

	// ── Sort helpers: by profit, by loss ──
	topProfit := make([]productReport, 0, len(products))
	topLoss := make([]productReport, 0, len(products))
	for _, p := range products {
		if p.CostMinor > 0 {
			if p.Profit > 0 {
				topProfit = append(topProfit, p)
			} else if p.Profit < 0 {
				topLoss = append(topLoss, p)
			}
		}
	}
	// Sort topProfit descending by profit
	for i := 0; i < len(topProfit); i++ {
		for j := i + 1; j < len(topProfit); j++ {
			if topProfit[j].Profit > topProfit[i].Profit {
				topProfit[i], topProfit[j] = topProfit[j], topProfit[i]
			}
		}
	}
	// Sort topLoss ascending by profit (most negative first)
	for i := 0; i < len(topLoss); i++ {
		for j := i + 1; j < len(topLoss); j++ {
			if topLoss[j].Profit < topLoss[i].Profit {
				topLoss[i], topLoss[j] = topLoss[j], topLoss[i]
			}
		}
	}

	netRevenue := totalRevenue - totalReturned
	grossProfit := netRevenue - totalCOGS
	netProfit := grossProfit - totalExpenses

	c.JSON(200, gin.H{
		"totals": gin.H{
			"revenue_minor":     totalRevenue,
			"returned_minor":    totalReturned,
			"net_revenue_minor": netRevenue,
			"cogs_minor":        totalCOGS,
			"gross_profit_minor": grossProfit,
			"expenses_minor":    totalExpenses,
			"purchases_minor":   totalPurchases,
			"net_profit_minor":  netProfit,
		},
		"top_selling": products,
		"top_profit":  topProfit,
		"top_loss":    topLoss,
		"top_clients": clients,
	})
}
