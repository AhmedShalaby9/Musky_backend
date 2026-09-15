package server

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gvanbeck/nautilus/pdf/rtl"
	"github.com/phpdave11/gofpdf"
)

//go:embed fonts/DejaVuSansCondensed.ttf
var invoiceFont []byte

func (a *API) invoicePDF(c *gin.Context) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	invoice, err := readInvoice(c.Request.Context(), tx, tenantID(c), id, false)
	if err != nil {
		databaseError(c, err)
		return
	}
	var tenantName, logoKey string
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT t.name,COALESCE(f.object_key,'') FROM tenants t LEFT JOIN file_objects f ON f.id=t.logo_file_id WHERE t.id=?", tenantID(c)).Scan(&tenantName, &logoKey); err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
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
	pdf.SetFont(font, "B", 20)
	pdf.SetTextColor(15, 79, 79)
	pdf.CellFormat(0, 10, arabic("شركة بكار لاين"), "", 1, "R", false, 0, "")
	pdf.SetFont(font, "", 12)
	pdf.CellFormat(0, 8, arabic("للاستيراد والتصدير"), "", 1, "R", false, 0, "")
	pdf.SetFont(font, "B", 22)
	pdf.CellFormat(0, 13, arabic("إذن صادر"), "", 1, "R", false, 0, "")
	pdf.SetFont(font, "", 10)
	pdf.CellFormat(90, 7, arabic("محمد بكري"), "", 0, "L", false, 0, "")
	pdf.CellFormat(90, 7, arabic("01284550533"), "", 1, "L", false, 0, "")
	pdf.CellFormat(90, 7, arabic("أحمد عماد"), "", 0, "L", false, 0, "")
	pdf.CellFormat(90, 7, arabic("01229722960"), "", 1, "L", false, 0, "")
	pdf.SetFont(font, "", 11)
	pdf.SetTextColor(30, 50, 60)
	number := arabic("مسودة")
	if invoice.Number != nil {
		number = fmt.Sprintf("%06d", *invoice.Number)
	}
	pdf.CellFormat(45, 7, number, "", 0, "L", false, 0, "")
	pdf.CellFormat(45, 7, arabic("رقم"), "", 0, "R", false, 0, "")
	pdf.CellFormat(45, 7, formatInvoiceDate(invoice.IssueDate), "", 0, "L", false, 0, "")
	pdf.CellFormat(45, 7, arabic("تحريراً في"), "", 1, "R", false, 0, "")
	pdf.Ln(5)
	pdf.SetFont(font, "", 11)
	pdf.CellFormat(135, 8, arabic(truncatePDF(invoice.ClientName, 55)), "", 0, "L", false, 0, "")
	pdf.CellFormat(45, 8, arabic("السيد"), "", 1, "R", false, 0, "")
	if invoice.ClientAddress != "" {
		pdf.CellFormat(135, 8, arabic(truncatePDF(invoice.ClientAddress, 70)), "", 0, "L", false, 0, "")
		pdf.CellFormat(45, 8, arabic("العنوان"), "", 1, "R", false, 0, "")
	}
	pdf.Ln(5)
	pdf.SetFillColor(15, 79, 79)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(font, "B", 10)
	for _, h := range []string{arabic("الإجمالي"), arabic("العدد"), arabic("العبوة"), arabic("سعر الوحدة"), arabic("بيان")} {
		pdf.CellFormat(38, 9, h, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetTextColor(30, 50, 60)
	pdf.SetFont(font, "", 10)
	for _, item := range invoice.Items {
		pdf.CellFormat(38, 9, moneyPDF(item.TotalMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, fmt.Sprint(item.PackageCount), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, fmt.Sprint(item.UnitsPerPackage), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, moneyPDF(item.UnitPriceMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, arabic(truncatePDF(item.Title, 24)), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(6)
	pdf.SetFont(font, "B", 13)
	pdf.CellFormat(100, 9, moneyPDF(invoice.TotalMinor), "", 0, "L", false, 0, "")
	pdf.CellFormat(20, 9, arabic("جنيه"), "", 0, "L", false, 0, "")
	pdf.CellFormat(60, 9, arabic("الإجمالي فقط وقدره"), "", 1, "R", false, 0, "")
	if invoice.PaymentStatus != "" {
		pdf.SetFont(font, "", 11)
		pdf.CellFormat(100, 8, moneyPDF(invoice.PaidMinor), "", 0, "L", false, 0, "")
		pdf.CellFormat(20, 8, arabic("جنيه"), "", 0, "L", false, 0, "")
		pdf.CellFormat(60, 8, arabic("المدفوع / المتبقي"), "", 1, "R", false, 0, "")
		pdf.CellFormat(100, 8, moneyPDF(invoice.RemainingMinor), "", 0, "L", false, 0, "")
		pdf.CellFormat(20, 8, arabic("جنيه"), "", 0, "L", false, 0, "")
		pdf.CellFormat(60, 8, arabic(paymentStatusAr(invoice.PaymentStatus)), "", 1, "R", false, 0, "")
	}
	if logoKey != "" {
		if rc, e := a.files.Get(c.Request.Context(), logoKey); e == nil {
			defer rc.Close()
			if data, e := io.ReadAll(rc); e == nil {
				ext := strings.TrimPrefix(filepath.Ext(logoKey), ".")
				if ext == "jpg" {
					ext = "jpeg"
				}
				pdf.RegisterImageOptionsReader("logo", gofpdf.ImageOptions{ImageType: ext}, bytes.NewReader(data))
				pdf.ImageOptions("logo", 15, 15, 28, 0, false, gofpdf.ImageOptions{ImageType: ext}, 0, "")
			}
		}
	}
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
	_, err = a.db.ExecContext(context.Background(), "INSERT INTO file_objects(tenant_id,uploaded_by_user_id,object_key,original_name,content_type,size_bytes,public_url) VALUES (?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE size_bytes=VALUES(size_bytes),public_url=VALUES(public_url)", tenantID(c), actor(c).ID, key, fmt.Sprintf("invoice-%d.pdf", id), "application/pdf", out.Len(), url)
	if err != nil {
		databaseError(c, err)
		return
	}
	_, err = a.db.ExecContext(context.Background(), "UPDATE invoices SET pdf_url=? WHERE tenant_id=? AND id=?", url, tenantID(c), id)
	if err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(201, gin.H{"invoice_id": id, "url": url, "key": key})
}

func arabic(s string) string { return rtl.Shape(s) }

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
