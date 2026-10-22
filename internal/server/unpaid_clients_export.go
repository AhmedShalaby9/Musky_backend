package server

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"musky/backend/internal/model"
)

const maxUnpaidPeriodDays = 3650

// parseUnpaidPeriods accepts "1,3,7" or "1d,3d,7d" and returns sorted, unique day counts.
func parseUnpaidPeriods(raw string) ([]int, bool) {
	seen := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(part)), "d")
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > maxUnpaidPeriodDays {
			return nil, false
		}
		seen[n] = true
	}
	if len(seen) == 0 {
		return nil, false
	}
	days := make([]int, 0, len(seen))
	for n := range seen {
		days = append(days, n)
	}
	sort.Ints(days)
	return days, true
}

// unpaidBucket returns the largest selected period the client has gone without paying.
func unpaidBucket(periods []int, daysUnpaid int, neverPaid bool) int {
	bucket := periods[0]
	for _, p := range periods {
		if neverPaid || daysUnpaid >= p {
			bucket = p
		}
	}
	return bucket
}

// exportUnpaidClients streams an .xlsx of clients who owe money and have not
// paid for at least the smallest selected period (periods=1,3,7 or 1d,3d,7d).
func (a *API) exportUnpaidClients(c *gin.Context) {
	periods, ok := parseUnpaidPeriods(c.Query("periods"))
	if !ok {
		fail(c, 400, "periods must be a comma-separated list of positive day counts, e.g. 1,3,7 or 1d,3d,7d")
		return
	}
	var clients []model.Client
	result := clientDisplayQuery(a.orm.WithContext(c.Request.Context()), tenantID(c)).
		Where("c.active = TRUE").
		Having("balance_minor > 0 AND (last_payment_at IS NULL OR last_payment_at < UTC_TIMESTAMP() - INTERVAL ? DAY)", periods[0]).
		Order("balance_minor DESC").Scan(&clients)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}

	f := excelize.NewFile()
	defer f.Close()
	sheet := "Unpaid clients"
	f.SetSheetName("Sheet1", sheet)
	headers := []string{"Client", "Phone", "Address", "Balance (EGP)", "Last payment", "Days unpaid", "Period"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetCellStyle(sheet, "A1", "G1", bold)
	money, _ := f.NewStyle(&excelize.Style{NumFmt: 4})
	now := time.Now().UTC()
	for i, cl := range clients {
		row := i + 2
		phone := ""
		if cl.Phone != nil {
			phone = *cl.Phone
		}
		last, days, never := "Never", "", cl.LastPaymentAt == nil
		daysUnpaid := 0
		if !never {
			last = cl.LastPaymentAt.UTC().Format("2006-01-02")
			daysUnpaid = int(now.Sub(*cl.LastPaymentAt).Hours() / 24)
			days = strconv.Itoa(daysUnpaid)
		}
		f.SetSheetRow(sheet, fmt.Sprintf("A%d", row), &[]any{cl.Name, phone, cl.Address, float64(cl.BalanceMinor) / 100, last, days, fmt.Sprintf("%dd+", unpaidBucket(periods, daysUnpaid, never))})
		f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), money)
	}
	for col, w := range []float64{28, 16, 36, 16, 14, 12, 10} {
		name, _ := excelize.ColumnNumberToName(col + 1)
		f.SetColWidth(sheet, name, name, w)
	}
	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		fail(c, 500, "could not generate spreadsheet")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="unpaid-clients-%s.xlsx"`, now.Format("2006-01-02")))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", out.Bytes())
}
