package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/phpdave11/gofpdf"
)

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
	font := "Helvetica"
	if fontPath != "" {
		pdf.AddUTF8Font("musky", "", fontPath)
		font = "musky"
	}
	pdf.AddPage()
	pdf.SetFont(font, "B", 20)
	pdf.SetTextColor(15, 79, 79)
	pdf.CellFormat(0, 12, tenantName, "", 1, "R", false, 0, "")
	pdf.SetFont(font, "B", 24)
	pdf.CellFormat(0, 16, "فاتورة مبيعات", "", 1, "R", false, 0, "")
	pdf.SetFont(font, "", 11)
	pdf.SetTextColor(30, 50, 60)
	number := "مسودة"
	if invoice.Number != nil {
		number = fmt.Sprintf("INV-%06d", *invoice.Number)
	}
	pdf.CellFormat(0, 7, fmt.Sprintf("رقم الفاتورة: %s    التاريخ: %s", number, invoice.IssueDate), "", 1, "R", false, 0, "")
	pdf.Ln(5)
	pdf.SetFillColor(224, 240, 238)
	pdf.SetFont(font, "B", 12)
	pdf.CellFormat(0, 9, "بيانات العميل", "", 1, "R", true, 0, "")
	pdf.SetFont(font, "", 11)
	pdf.CellFormat(0, 8, invoice.ClientName, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 8, invoice.ClientAddress, "", 1, "R", false, 0, "")
	pdf.Ln(5)
	pdf.SetFillColor(15, 79, 79)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(font, "B", 10)
	for _, h := range []string{"الإجمالي", "سعر العبوة", "الكمية", "الكود", "المنتج"} {
		pdf.CellFormat(38, 9, h, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetTextColor(30, 50, 60)
	pdf.SetFont(font, "", 10)
	for _, item := range invoice.Items {
		pdf.CellFormat(38, 9, moneyPDF(item.TotalMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, moneyPDF(item.UnitPriceMinor), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, fmt.Sprint(item.Quantity), "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, item.Code, "1", 0, "C", false, 0, "")
		pdf.CellFormat(38, 9, item.Title, "1", 1, "R", false, 0, "")
	}
	pdf.Ln(6)
	pdf.SetFont(font, "B", 13)
	pdf.CellFormat(0, 9, fmt.Sprintf("الإجمالي: %s    المدفوع: %s    المتبقي: %s", moneyPDF(invoice.TotalMinor), moneyPDF(invoice.PaidMinor), moneyPDF(invoice.RemainingMinor)), "", 1, "R", false, 0, "")
	if invoice.PaymentStatus != "" {
		pdf.SetFont(font, "", 11)
		pdf.CellFormat(0, 8, "حالة الدفع: "+invoice.PaymentStatus, "", 1, "R", false, 0, "")
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
	c.JSON(201, gin.H{"invoice_id": id, "url": url, "key": key})
}

func moneyPDF(minor int64) string { return fmt.Sprintf("EGP %d.%02d", minor/100, minor%100) }
