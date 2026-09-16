package server

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/phpdave11/gofpdf"
	"musky/backend/internal/model"
)

func (a *API) clientStatementPDF(c *gin.Context) {
	clientID, ok := pathID(c, "id")
	if !ok {
		return
	}
	from, to, ok := statementPeriod(c)
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
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	periodEntries, openingBalance, closingBalance := statementRows(entries, from, to)
	pdf := buildClientStatementPDF(client, periodEntries, openingBalance, closingBalance, from, to)
	var out bytes.Buffer
	if err = pdf.Output(&out); err != nil {
		fail(c, 500, "could not generate statement PDF")
		return
	}
	filename := fmt.Sprintf("client-%d-statement-%s-%s.pdf", clientID, from.Format("20060102"), to.Format("20060102"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/pdf", out.Bytes())
}

func statementPeriod(c *gin.Context) (time.Time, time.Time, bool) {
	fromText, toText := c.Query("from"), c.Query("to")
	if fromText == "" || toText == "" {
		fail(c, 400, "from and to are required")
		return time.Time{}, time.Time{}, false
	}
	from, err1 := time.Parse("2006-01-02", fromText)
	to, err2 := time.Parse("2006-01-02", toText)
	if err1 != nil || err2 != nil || to.Before(from) {
		fail(c, 400, "invalid statement period")
		return time.Time{}, time.Time{}, false
	}
	if to.Sub(from) > 370*24*time.Hour {
		fail(c, 400, "statement period is too large")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func statementRows(entries []model.LedgerEntry, from, to time.Time) ([]model.LedgerEntry, int64, int64) {
	toEnd := to.Add(24*time.Hour - time.Nanosecond)
	rows := []model.LedgerEntry{}
	var opening, closing int64
	for _, entry := range entries {
		if entry.At.Before(from) {
			opening = entry.RunningBalance
			closing = entry.RunningBalance
			continue
		}
		if entry.At.After(toEnd) {
			break
		}
		rows = append(rows, entry)
		closing = entry.RunningBalance
	}
	if len(rows) == 0 {
		closing = opening
	}
	return rows, opening, closing
}

func buildClientStatementPDF(client model.Client, entries []model.LedgerEntry, openingBalance, closingBalance int64, from, to time.Time) *gofpdf.Fpdf {
	pdf := gofpdf.New("P", "mm", "A4", "")
	fontPath := ""
	font := "musky"
	if fontPath != "" {
		pdf.AddUTF8Font(font, "", fontPath)
		pdf.AddUTF8Font(font, "B", fontPath)
	} else {
		pdf.AddUTF8FontFromBytes(font, "", invoiceFont)
		pdf.AddUTF8FontFromBytes(font, "B", invoiceFont)
	}
	pdf.SetMargins(12, 12, 12)
	pdf.AddPage()
	pdf.SetTextColor(15, 79, 79)
	pdf.SetFont(font, "B", 18)
	pdf.CellFormat(0, 9, arabic("شركة بكار لاين"), "", 1, "R", false, 0, "")
	pdf.SetFont(font, "", 11)
	pdf.CellFormat(0, 7, arabic("للاستيراد والتصدير"), "", 1, "R", false, 0, "")
	pdf.Ln(3)
	pdf.SetTextColor(30, 50, 60)
	pdf.SetFont(font, "B", 16)
	pdf.CellFormat(0, 10, arabic("كشف حساب عميل"), "", 1, "C", false, 0, "")
	pdf.SetFont(font, "", 11)
	pdf.CellFormat(120, 7, arabic(client.Name), "", 0, "L", false, 0, "")
	pdf.CellFormat(60, 7, arabic("العميل"), "", 1, "R", false, 0, "")
	if client.Address != "" {
		pdf.CellFormat(120, 7, arabic(truncatePDF(client.Address, 60)), "", 0, "L", false, 0, "")
		pdf.CellFormat(60, 7, arabic("العنوان"), "", 1, "R", false, 0, "")
	}
	period := formatStatementDate(from) + " - " + formatStatementDate(to)
	pdf.CellFormat(120, 7, period, "", 0, "L", false, 0, "")
	pdf.CellFormat(60, 7, arabic("الفترة"), "", 1, "R", false, 0, "")
	pdf.CellFormat(120, 7, statementMoney(openingBalance), "", 0, "L", false, 0, "")
	pdf.CellFormat(60, 7, arabic("رصيد سابق"), "", 1, "R", false, 0, "")
	pdf.Ln(4)

	headers := []string{arabic("الرصيد"), arabic("دائن"), arabic("مدين"), arabic("البيان"), arabic("التاريخ")}
	widths := []float64{32, 30, 30, 68, 25}
	pdf.SetFillColor(15, 79, 79)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(font, "B", 9)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 8, h, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetTextColor(30, 50, 60)
	pdf.SetFont(font, "", 9)
	if len(entries) == 0 {
		pdf.CellFormat(185, 8, arabic("لا توجد حركات في هذه الفترة"), "1", 1, "C", false, 0, "")
	}
	for _, entry := range entries {
		debit, credit := "", ""
		if entry.DeltaMinor >= 0 {
			debit = statementMoney(entry.DeltaMinor)
		} else {
			credit = statementMoney(-entry.DeltaMinor)
		}
		values := []string{
			statementMoney(entry.RunningBalance),
			credit,
			debit,
			arabic(truncatePDF(statementDescription(entry), 42)),
			formatStatementDate(entry.At),
		}
		for i, value := range values {
			align := "C"
			if i == 3 {
				align = "R"
			}
			pdf.CellFormat(widths[i], 8, value, "1", 0, align, false, 0, "")
		}
		pdf.Ln(-1)
		if pdf.GetY() > 275 {
			pdf.AddPage()
		}
	}
	pdf.Ln(5)
	pdf.SetFont(font, "B", 12)
	pdf.CellFormat(120, 8, statementMoney(closingBalance), "", 0, "L", false, 0, "")
	pdf.CellFormat(60, 8, arabic("الرصيد الختامي"), "", 1, "R", false, 0, "")
	return pdf
}

func statementDescription(entry model.LedgerEntry) string {
	number := ""
	if entry.InvoiceNumber != nil {
		number = fmt.Sprintf(" %06d", *entry.InvoiceNumber)
	}
	switch entry.Kind {
	case "opening":
		return "رصيد افتتاحي"
	case "invoice", "purchase", "reactivate", "purchase_reactivate":
		return invoiceDocumentTypeAr(entry.DocumentType) + number
	case "void", "purchase_void":
		return "إلغاء " + invoiceDocumentTypeAr(entry.DocumentType) + number
	case "invoice_payment":
		if entry.DocumentType == "purchase" {
			return "دفعة للمورد" + number
		}
		return "دفعة من العميل" + number
	case "receipt":
		return "دفعة عامة"
	case "reversal":
		return "عكس دفعة"
	default:
		return entry.Kind
	}
}

func formatStatementDate(value time.Time) string {
	return value.Format("02/01/2006")
}

func statementMoney(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}
