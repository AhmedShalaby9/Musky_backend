package server

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"musky/backend/internal/model"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gvanbeck/nautilus/pdf/rtl"
	"github.com/phpdave11/gofpdf"
)

//go:embed fonts/DejaVuSansCondensed.ttf
var invoiceFont []byte

// invoiceContactLine is one of a trader's printable contact rows (see
// user_contacts.go), already filtered to visible_on_invoice = true.
type invoiceContactLine struct {
	Title string
	Value string
}

func (a *API) invoicePDF(c *gin.Context) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx := a.orm.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil {
		databaseError(c, tx.Error)
		return
	}
	defer tx.Rollback()
	invoice, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	var tenantRow struct {
		Name      string
		ObjectKey string
	}
	if err = tx.Table("tenants t").Select("t.name, COALESCE(f.object_key,'') AS object_key").Joins("LEFT JOIN file_objects f ON f.id=t.logo_file_id AND f.tenant_id=t.id").Where("t.id = ?", tenantID(c)).Scan(&tenantRow).Error; err != nil {
		databaseError(c, err)
		return
	}
	logoKey := tenantRow.ObjectKey
	var creator struct{ Name string }
	if err = tx.Table("users").Select("name").Where("tenant_id = ? AND id = ?", tenantID(c), invoice.CreatedByUserID).Scan(&creator).Error; err != nil {
		databaseError(c, err)
		return
	}
	var contacts []invoiceContactLine
	if err = tx.Table("user_contacts").Select("title, value").
		Where("tenant_id = ? AND user_id = ? AND visible_on_invoice = TRUE", tenantID(c), invoice.CreatedByUserID).
		Order("sort_order, id").Scan(&contacts).Error; err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit().Error; err != nil {
		databaseError(c, err)
		return
	}
	var logoData []byte
	var logoExt string
	if logoKey != "" {
		if rc, e := a.files.Get(c.Request.Context(), logoKey); e == nil {
			defer rc.Close()
			if data, e := io.ReadAll(rc); e == nil {
				logoData = data
				logoExt = strings.TrimPrefix(filepath.Ext(logoKey), ".")
				if logoExt == "jpg" {
					logoExt = "jpeg"
				}
			}
		}
	}
	pdf := buildInvoicePDF(invoice, tenantRow.Name, creator.Name, contacts, logoData, logoExt)
	var out bytes.Buffer
	if err = pdf.Output(&out); err != nil {
		fail(c, 500, "could not generate invoice PDF")
		return
	}
	key := fmt.Sprintf("tenants/%d/invoices/%d/invoice.pdf", tenantID(c), id)
	if err = a.files.Put(c.Request.Context(), key, "application/pdf", int64(out.Len()), bytes.NewReader(out.Bytes())); err != nil {
		fail(c, 502, "file storage upload failed")
		return
	}
	url := a.files.URL(key)
	// Finish the metadata transaction even if the download request is cancelled.
	metadataCtx, cancelMetadata := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelMetadata()
	err = a.orm.WithContext(metadataCtx).Transaction(func(tx *gorm.DB) error {
		file := fileRecord{TenantID: tenantID(c), UploadedByUserID: actor(c).ID, ObjectKey: key, OriginalName: fmt.Sprintf("invoice-%d.pdf", id), ContentType: "application/pdf", SizeBytes: int64(out.Len()), PublicURL: url}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "object_key"}}, DoUpdates: clause.AssignmentColumns([]string{"size_bytes", "public_url"})}).Create(&file).Error; err != nil {
			return err
		}
		return tx.Model(&model.Invoice{}).Where("tenant_id = ? AND id = ?", tenantID(c), id).Update("pdf_url", url).Error
	})
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"invoice_id": id, "url": url, "key": key})
}

// buildInvoicePDF renders one invoice as a boxed, RTL-styled A4 page: a
// header naming the trader who created it (with their approved contact
// lines), a boxed document type/number line, a bordered info grid, a
// bordered items table and boxed totals. It touches no database or file
// storage, so it is easy to unit test and reuse.
func buildInvoicePDF(invoice model.Invoice, tenantName, creatorName string, contacts []invoiceContactLine, logoData []byte, logoExt string) *gofpdf.Fpdf {
	pdf := gofpdf.New("P", "mm", "A4", "")
	fontPath := os.Getenv("MUSKY_PDF_FONT_PATH")
	font := "musky"
	if fontPath != "" {
		pdf.AddUTF8Font("musky", "", fontPath)
		pdf.AddUTF8Font("musky", "B", fontPath)
	} else {
		pdf.AddUTF8FontFromBytes("musky", "", invoiceFont)
		pdf.AddUTF8FontFromBytes("musky", "B", invoiceFont)
	}
	pdf.AddPage()
	left, top, right, _ := pdf.GetMargins()
	pageW, _ := pdf.GetPageSize()
	contentWidth := pageW - left - right
	half := contentWidth / 2

	// gridRow draws a bordered two-column row. `rightText` is what an Arabic
	// reader sees first (rightmost); gofpdf lays cells out left-to-right, so
	// the right-hand field is emitted last to land on the right.
	gridRow := func(rightText, leftText string) {
		pdf.CellFormat(half, 8, arabic(truncatePDF(leftText, 42)), "1", 0, "R", false, 0, "")
		pdf.CellFormat(half, 8, arabic(truncatePDF(rightText, 42)), "1", 1, "R", false, 0, "")
	}

	if len(logoData) > 0 {
		pdf.RegisterImageOptionsReader("logo", gofpdf.ImageOptions{ImageType: logoExt}, bytes.NewReader(logoData))
		pdf.ImageOptions("logo", left, top, 28, 0, false, gofpdf.ImageOptions{ImageType: logoExt}, 0, "")
	}

	// Header: the trader who created this invoice, their business, and their
	// approved contact lines. Right-aligned across the full content width so
	// it never overlaps the logo, which sits in the top-left corner.
	heading := strings.TrimSpace(creatorName)
	if tenantName != "" {
		if heading == "" {
			heading = tenantName
		} else {
			heading = heading + " - " + tenantName
		}
	}
	pdf.SetFont(font, "B", 18)
	pdf.SetTextColor(15, 79, 79)
	if heading != "" {
		pdf.CellFormat(0, 9, arabic(truncatePDF(heading, 60)), "", 1, "R", false, 0, "")
	}
	if len(contacts) > 0 {
		pdf.SetFont(font, "", 10)
		pdf.SetTextColor(60, 78, 74)
		for _, ct := range contacts {
			pdf.CellFormat(
				0, 6,
				arabic(fmt.Sprintf("%s: %s", ct.Title, ct.Value)),
				"", 1, "R", false, 0, "",
			)
		}
	}
	pdf.Ln(4)

	// Boxed document type + number line.
	number := "مسودة"
	if invoice.Number != nil {
		number = fmt.Sprintf("%06d", *invoice.Number)
	}
	pdf.SetFont(font, "B", 14)
	pdf.SetTextColor(15, 79, 79)
	pdf.CellFormat(
		contentWidth, 11,
		arabic(fmt.Sprintf("%s رقم: %s", invoiceDocumentTypeAr(invoice.DocumentType), number)),
		"1", 1, "C", false, 0, "",
	)
	pdf.Ln(4)

	// Bordered info grid: type/date, client/status, address/notes.
	pdf.SetFont(font, "", 10)
	pdf.SetTextColor(30, 50, 60)
	gridRow(
		"النوع: "+invoiceTypeShortAr(invoice.DocumentType),
		"التاريخ: "+formatInvoiceDate(invoice.IssueDate),
	)
	gridRow(
		"السيد: "+invoice.ClientName,
		"حالة الفاتورة: "+invoiceStatusAr(invoice.Status),
	)
	if invoice.ClientAddress != "" || invoice.Notes != "" {
		gridRow("العنوان: "+invoice.ClientAddress, "ملاحظات: "+invoice.Notes)
	}
	pdf.Ln(4)

	// Items table, columns in RTL reading order (description first and
	// rightmost, total last and leftmost); emitted in reverse since gofpdf
	// lays cells out left-to-right.
	colWidths := []float64{
		contentWidth * 0.14, // الإجمالي
		contentWidth * 0.12, // العدد
		contentWidth * 0.16, // سعر الوحدة
		contentWidth * 0.12, // العبوة
		contentWidth * 0.16, // الكود
		contentWidth * 0.30, // بيان
	}
	pdf.SetFillColor(15, 79, 79)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(font, "B", 10)
	for i, h := range []string{"الإجمالي", "العدد", "سعر الوحدة", "العبوة", "الكود", "بيان"} {
		pdf.CellFormat(colWidths[i], 9, arabic(h), "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetTextColor(30, 50, 60)
	pdf.SetFont(font, "", 10)
	for _, item := range invoice.Items {
		pdf.CellFormat(colWidths[0], 9, moneyPDF(item.TotalMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colWidths[1], 9, fmt.Sprint(item.PackageCount), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colWidths[2], 9, moneyPDF(item.UnitPriceMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colWidths[3], 9, fmt.Sprint(item.UnitsPerPackage), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colWidths[4], 9, arabic(truncatePDF(item.Code, 14)), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colWidths[5], 9, arabic(truncatePDF(item.Title, 22)), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(6)

	// Boxed totals.
	pdf.SetFont(font, "B", 12)
	pdf.SetTextColor(30, 50, 60)
	pdf.CellFormat(
		contentWidth, 9,
		arabic(fmt.Sprintf("الإجمالي فقط وقدره: %s جنيه", moneyPDF(invoice.TotalMinor))),
		"1", 1, "R", false, 0, "",
	)
	if invoice.PaymentStatus != "" {
		pdf.SetFont(font, "", 10)
		gridRow(
			fmt.Sprintf("المدفوع: %s جنيه", moneyPDF(invoice.PaidMinor)),
			fmt.Sprintf("المتبقي: %s جنيه", moneyPDF(invoice.RemainingMinor)),
		)
		pdf.CellFormat(
			contentWidth, 8,
			arabic("حالة السداد: "+paymentStatusAr(invoice.PaymentStatus)),
			"1", 1, "R", false, 0, "",
		)
	}
	return pdf
}

func arabic(s string) string { return rtl.Shape(s) }

func invoiceDocumentTypeAr(documentType string) string {
	if documentType == "purchase" {
		return "فاتورة شراء"
	}
	return "فاتورة بيع"
}

func invoiceTypeShortAr(documentType string) string {
	if documentType == "purchase" {
		return "شراء"
	}
	return "بيع"
}

func invoiceStatusAr(status string) string {
	switch status {
	case "posted":
		return "مرحّلة"
	case "void":
		return "باطلة"
	case "cancelled":
		return "ملغاة"
	default:
		return "مسودة"
	}
}

func truncatePDF(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-3]) + "..."
}

func formatInvoiceDate(value string) string {
	if len(value) == 10 && value[4] == '-' && value[7] == '-' {
		return value[8:10] + "/" + value[5:7] + "/" + value[:4]
	}
	return value
}

func moneyPDF(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }

func paymentStatusAr(s string) string {
	switch s {
	case "paid":
		return "مدفوع"
	case "partially_paid":
		return "مدفوع جزئياً"
	default:
		return "غير مدفوع"
	}
}
