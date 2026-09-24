package server

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/gvanbeck/nautilus/pdf/rtl"
	"github.com/phpdave11/gofpdf"
	"gorm.io/gorm"
	"musky/backend/internal/model"
)

//go:embed fonts/DejaVuSansCondensed-Bold.ttf
var statementBoldFont []byte

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
	periodEntries, openingBalance, _ := statementRows(entries, from, to)
	items, err := loadStatementItems(tx, tenantID(c), periodEntries)
	if err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	lines := statementLines(periodEntries, items, openingBalance, from)
	pdf := buildClientStatementPDF(client, lines, from, to)
	var out bytes.Buffer
	if err = pdf.Output(&out); err != nil {
		fail(c, 500, "could not generate statement PDF")
		return
	}
	c.Header("Content-Disposition", statementDisposition(clientID, client.Name))
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

// statementItem is one product line of an invoice or a return.
type statementItem struct {
	Title           string
	Code            string
	UnitsPerPackage int64
	PackageCount    int64
	UnitPriceMinor  int64
	TotalMinor      int64
}

type statementItems struct {
	invoices map[uint64][]statementItem
	returns  map[uint64][]statementItem
}

// loadStatementItems fetches the product lines of every invoice and return
// that appears in the statement period, so each can be printed per item.
func loadStatementItems(tx *gorm.DB, tenant uint64, entries []model.LedgerEntry) (statementItems, error) {
	out := statementItems{invoices: map[uint64][]statementItem{}, returns: map[uint64][]statementItem{}}
	invoiceIDs, returnIDs := []uint64{}, []uint64{}
	for _, entry := range entries {
		switch {
		case entry.Kind == "return" && entry.ReturnID != nil:
			returnIDs = append(returnIDs, *entry.ReturnID)
		case isInvoiceMovement(entry.Kind) && entry.RefID != nil:
			invoiceIDs = append(invoiceIDs, *entry.RefID)
		}
	}
	type row struct {
		OwnerID uint64
		statementItem
	}
	const columns = "title, code, units_per_package, package_count, unit_price_minor, total_minor"
	if len(invoiceIDs) > 0 {
		rows := []row{}
		if err := tx.Table("invoice_items").Select("invoice_id AS owner_id, "+columns).Where("tenant_id = ? AND invoice_id IN ?", tenant, invoiceIDs).Order("id").Scan(&rows).Error; err != nil {
			return out, err
		}
		for _, r := range rows {
			out.invoices[r.OwnerID] = append(out.invoices[r.OwnerID], r.statementItem)
		}
	}
	if len(returnIDs) > 0 {
		rows := []row{}
		if err := tx.Table("invoice_return_items").Select("return_id AS owner_id, "+columns).Where("tenant_id = ? AND return_id IN ?", tenant, returnIDs).Order("id").Scan(&rows).Error; err != nil {
			return out, err
		}
		for _, r := range rows {
			out.returns[r.OwnerID] = append(out.returns[r.OwnerID], r.statementItem)
		}
	}
	return out, nil
}

func isInvoiceMovement(kind string) bool {
	switch kind {
	case "invoice", "purchase", "reactivate", "purchase_reactivate", "void", "purchase_void":
		return true
	}
	return false
}

// statementLine is one printed row of the item-level statement.
type statementLine struct {
	At          time.Time
	Code        string
	Description string
	DocNumber   string
	Packages    int64
	PerPackage  int64
	UnitPrice   int64
	HasItem     bool
	Debit       int64
	Credit      int64
	Balance     int64
	// Opening marks the "رصيد سابق" row, which always shows debit and balance.
	Opening bool
}

// statementLines expands ledger entries into printed rows: every invoice or
// return becomes one row per product, and the running balance moves per row.
func statementLines(entries []model.LedgerEntry, items statementItems, opening int64, from time.Time) []statementLine {
	lines := []statementLine{{At: from, Description: "رصيد سابق", Debit: opening, Balance: opening, Opening: true}}
	balance := opening
	for _, entry := range entries {
		doc := ""
		if entry.InvoiceNumber != nil {
			doc = fmt.Sprint(*entry.InvoiceNumber)
		}
		var products []statementItem
		switch {
		case entry.Kind == "return" && entry.ReturnID != nil:
			products = items.returns[*entry.ReturnID]
		case isInvoiceMovement(entry.Kind) && entry.RefID != nil:
			products = items.invoices[*entry.RefID]
		}
		sum := int64(0)
		for _, p := range products {
			sum += p.TotalMinor
		}
		// Fall back to a single summary row if the items do not account for the
		// whole movement, so the running balance can never drift from the ledger.
		if len(products) == 0 || sum != abs64(entry.DeltaMinor) {
			balance = entry.RunningBalance
			lines = append(lines, withAmount(statementLine{At: entry.At, Description: statementDescription(entry), DocNumber: doc, Balance: balance}, entry.DeltaMinor))
			continue
		}
		sign := int64(1)
		if entry.DeltaMinor < 0 {
			sign = -1
		}
		prefix := itemMovementPrefix(entry)
		for _, p := range products {
			balance += sign * p.TotalMinor
			line := statementLine{
				At: entry.At, Code: p.Code, Description: prefix + ": " + p.Title, DocNumber: doc,
				Packages: p.PackageCount, PerPackage: p.UnitsPerPackage, UnitPrice: p.UnitPriceMinor, HasItem: true,
				Balance: balance,
			}
			lines = append(lines, withAmount(line, sign*p.TotalMinor))
		}
		balance = entry.RunningBalance
	}
	return lines
}

func withAmount(line statementLine, delta int64) statementLine {
	if delta >= 0 {
		line.Debit = delta
	} else {
		line.Credit = -delta
	}
	return line
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func itemMovementPrefix(entry model.LedgerEntry) string {
	kind := "مبيعات صنف"
	if entry.DocumentType == "purchase" {
		kind = "مشتريات صنف"
	}
	switch entry.Kind {
	case "void", "purchase_void":
		return "إلغاء " + kind
	case "return":
		return "مرتجع " + kind
	}
	return kind
}

func statementDescription(entry model.LedgerEntry) string {
	number := ""
	if entry.InvoiceNumber != nil {
		number = fmt.Sprintf(" %d", *entry.InvoiceNumber)
	}
	switch entry.Kind {
	case "opening":
		return "رصيد افتتاحي"
	case "invoice", "purchase", "reactivate", "purchase_reactivate":
		return invoiceDocumentTypeAr(entry.DocumentType) + number
	case "void", "purchase_void":
		return "إلغاء " + invoiceDocumentTypeAr(entry.DocumentType) + number
	case "return":
		return "مرتجع " + invoiceDocumentTypeAr(entry.DocumentType) + number
	case "adjust", "purchase_adjust":
		return "تعديل " + invoiceDocumentTypeAr(entry.DocumentType) + number
	case "invoice_payment":
		if entry.DocumentType == "purchase" {
			return paymentDescription("مدفوعات فاتورة", entry)
		}
		return paymentDescription("متحصلات فاتورة", entry)
	case "receipt":
		return paymentDescription("متحصلات مباشرة", entry)
	case "client_payment":
		return paymentDescription("مدفوعات مباشرة", entry)
	case "reversal":
		return paymentDescription("عكس دفعة", entry)
	default:
		return entry.Kind
	}
}

// paymentDescription follows the "متحصلات مباشرة: الدرج، ملحوظة: ..." style.
func paymentDescription(label string, entry model.LedgerEntry) string {
	text := label
	if entry.Method != nil {
		text += ": " + paymentMethodAr(*entry.Method)
	}
	notes := strings.TrimSpace(entry.Notes)
	if notes == "" {
		notes = "-"
	}
	return text + "، ملحوظة: " + notes
}

func paymentMethodAr(method string) string {
	switch method {
	case "cash":
		return "نقدي"
	case "online":
		return "تحويل"
	}
	return method
}

// Column layout, left to right on the page (the table reads right to left).
var statementColumns = []struct {
	title string
	width float64
}{
	{"رصيد", 31}, {"دائن", 28}, {"مدين", 28}, {"س.وحدة", 20}, {"قطع/ وحدات", 20},
	{"العبوة", 16}, {"عبوات/\nكراتين", 14}, {"رقم مستند", 22}, {"بيـــــان", 60}, {"كود الصنف", 24}, {"تاريخ", 22},
}

const (
	statementDescColumn = 8
	statementLineHeight = 5.5
	statementPageBottom = 200
)

func buildClientStatementPDF(client model.Client, lines []statementLine, from, to time.Time) *gofpdf.Fpdf {
	const font = "musky"
	pdf := gofpdf.New("L", "mm", "A4", "")
	pdf.AddUTF8FontFromBytes(font, "", invoiceFont)
	pdf.AddUTF8FontFromBytes(font, "B", statementBoldFont)
	pdf.SetMargins(6, 10, 6)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetTextColor(0, 0, 0)
	pdf.SetDrawColor(0, 0, 0)
	pdf.AddPage()

	pdf.SetFont(font, "B", 12)
	title := visualRTL("تقرير بحركة متعامل بالصنف")
	pdf.CellFormat(0, 7, title, "", 1, "C", false, 0, "")
	// Underline the title like the reference report.
	w := pdf.GetStringWidth(title)
	pageW, _ := pdf.GetPageSize()
	y := pdf.GetY() - 1
	pdf.Line((pageW-w)/2, y, (pageW+w)/2, y)
	pdf.Ln(6)

	pdf.SetFont(font, "B", 10)
	header := []string{
		fmt.Sprintf("المتعامل: %d - %s", client.ID, client.Name),
		"من تاريخ: " + formatHeaderDate(from),
		"إلى تاريخ: " + formatHeaderDate(to),
	}
	for _, text := range header {
		pdf.CellFormat(0, 5, visualRTL(text), "", 1, "R", false, 0, "")
	}

	drawStatementHeader(pdf, font)
	pdf.SetFont(font, "B", 9)
	for _, line := range lines {
		desc := wrapArabic(pdf, line.Description, statementColumns[statementDescColumn].width-2)
		h := statementLineHeight * float64(len(desc))
		if h < 7 {
			h = 7
		}
		if pdf.GetY()+h > statementPageBottom {
			pdf.AddPage()
			drawStatementHeader(pdf, font)
			pdf.SetFont(font, "B", 9)
		}
		values := statementLineValues(line)
		x, y := pdf.GetX(), pdf.GetY()
		for i, col := range statementColumns {
			pdf.Rect(x, y, col.width, h, "D")
			if i == statementDescColumn {
				top := y + (h-statementLineHeight*float64(len(desc)))/2
				for j, part := range desc {
					pdf.SetXY(x+1, top+statementLineHeight*float64(j))
					pdf.CellFormat(col.width-2, statementLineHeight, part, "", 0, "R", false, 0, "")
				}
			} else {
				pdf.SetXY(x, y)
				pdf.CellFormat(col.width, h, values[i], "", 0, "C", false, 0, "")
			}
			x += col.width
		}
		pdf.SetXY(pdf.GetX(), y+h)
		pdf.SetX(6)
	}
	return pdf
}

func drawStatementHeader(pdf *gofpdf.Fpdf, font string) {
	const h = 10
	x, y := pdf.GetX(), pdf.GetY()
	for _, col := range statementColumns {
		pdf.Rect(x, y, col.width, h, "D")
		parts := strings.Split(col.title, "\n")
		size := 9.0
		if len(parts) > 1 {
			size = 7
		}
		pdf.SetFont(font, "B", size)
		lineH := 4.0
		top := y + (h-lineH*float64(len(parts)))/2
		for i, part := range parts {
			pdf.SetXY(x, top+lineH*float64(i))
			pdf.CellFormat(col.width, lineH, visualRTL(part), "", 0, "C", false, 0, "")
		}
		x += col.width
	}
	pdf.SetXY(6, y+h)
	// Thin gap under the header, matching the reference's double rule.
	pdf.Line(6, y+h+1, x, y+h+1)
	pdf.SetXY(6, y+h+1)
}

func statementLineValues(line statementLine) []string {
	values := make([]string, len(statementColumns))
	values[0] = statementMoney(line.Balance)
	if line.Credit != 0 {
		values[1] = statementMoney(line.Credit)
	}
	if line.Debit != 0 || line.Opening {
		values[2] = statementMoney(line.Debit)
	}
	if line.HasItem {
		values[3] = statementMoney(line.UnitPrice)
		values[5] = fmt.Sprint(line.PerPackage)
		values[6] = fmt.Sprint(line.Packages)
	}
	values[7] = line.DocNumber
	if line.Code != "" {
		values[9] = "[" + line.Code + "]"
	}
	values[10] = line.At.Format("02-01-2006")
	return values
}

// wrapArabic splits text into lines that fit width, wrapping on words in
// logical order and shaping each line afterwards so RTL order stays correct.
func wrapArabic(pdf *gofpdf.Fpdf, text string, width float64) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if current != "" && pdf.GetStringWidth(visualRTL(candidate)) > width {
			lines = append(lines, visualRTL(current))
			current = word
			continue
		}
		current = candidate
	}
	return append(lines, visualRTL(current))
}

func formatHeaderDate(value time.Time) string {
	return fmt.Sprintf("%d-%d-%d", value.Year(), int(value.Month()), value.Day())
}

// statementMoney prints whole pounds without decimals (9360) and keeps only
// the needed fraction digits otherwise (15.5, 15.25).
func statementMoney(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	whole, frac := minor/100, minor%100
	switch {
	case frac == 0:
		return fmt.Sprintf("%s%d", sign, whole)
	case frac%10 == 0:
		return fmt.Sprintf("%s%d.%d", sign, whole, frac/10)
	default:
		return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
	}
}

var unsafeFilename = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

// statementDisposition names the download after the client, e.g.
// كشف_حساب_احمد_جمال.pdf, with an ASCII fallback for old clients.
func statementDisposition(clientID uint64, clientName string) string {
	name := strings.Trim(unsafeFilename.ReplaceAllString(clientName, "_"), "_")
	if name == "" {
		name = fmt.Sprint(clientID)
	}
	filename := "كشف_حساب_" + name + ".pdf"
	return fmt.Sprintf(`attachment; filename="client-%d-statement.pdf"; filename*=UTF-8''%s`, clientID, url.PathEscape(filename))
}

// visualRTL shapes Arabic text and lays it out for a left-to-right renderer
// in a right-to-left paragraph: runs of Latin letters and digits (codes,
// numbers, dates) keep their own order, everything else is reversed, and the
// runs themselves are placed right to left.
func visualRTL(text string) string {
	runes := []rune(rtl.ShapeOnly(text))
	ltr := make([]bool, len(runes))
	for i, r := range runes {
		ltr[i] = r < 0x0590 && (unicode.IsLetter(r) || unicode.IsDigit(r))
	}
	// Separators between two LTR characters belong to them: 15.5, WM66-38, 2026-9-22.
	for i := 1; i < len(runes)-1; i++ {
		if !ltr[i] && strings.ContainsRune(".,-/:", runes[i]) && ltr[i-1] && ltr[i+1] {
			ltr[i] = true
		}
	}
	var runs [][]rune
	for i := 0; i < len(runes); {
		j := i
		for j < len(runes) && ltr[j] == ltr[i] {
			j++
		}
		run := append([]rune(nil), runes[i:j]...)
		if !ltr[i] {
			for l, r := 0, len(run)-1; l < r; l, r = l+1, r-1 {
				run[l], run[r] = mirrorRune(run[r]), mirrorRune(run[l])
			}
			if len(run)%2 == 1 {
				run[len(run)/2] = mirrorRune(run[len(run)/2])
			}
		}
		runs = append(runs, run)
		i = j
	}
	var b strings.Builder
	for i := len(runs) - 1; i >= 0; i-- {
		b.WriteString(string(runs[i]))
	}
	return b.String()
}

func mirrorRune(r rune) rune {
	switch r {
	case '(':
		return ')'
	case ')':
		return '('
	case '[':
		return ']'
	case ']':
		return '['
	}
	return r
}
