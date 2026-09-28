package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/gin-gonic/gin"

	"musky/backend/internal/documents"
	"musky/backend/internal/model"
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
	renderer, err := a.documentRenderer()
	if err != nil {
		fail(c, 503, "PDF generation is unavailable: "+err.Error())
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
	var contacts []documents.ContactLine
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
			}
		}
	}

	html, err := documents.RenderInvoiceHTML(invoiceDocumentData(invoice, tenantRow.Name, creator.Name, contacts, logoData, logoExt))
	if err != nil {
		fail(c, 500, "could not build invoice document")
		return
	}
	footer := documents.DocumentTypeLabel(invoice.DocumentType) + " " + documents.FormatInvoiceNumber(invoice.Number)
	out, err := renderer.RenderPDF(c.Request.Context(), html, documents.PrintOptions{FooterLabel: footer})
	if err != nil {
		fail(c, 502, "could not generate invoice PDF")
		return
	}
	key := fmt.Sprintf("tenants/%d/invoices/%d/invoice.pdf", tenantID(c), id)
	if err = a.files.Put(c.Request.Context(), key, "application/pdf", int64(len(out)), bytes.NewReader(out)); err != nil {
		fail(c, 502, "file storage upload failed")
		return
	}
	url := a.files.URL(key)
	// Finish the metadata transaction even if the download request is cancelled.
	metadataCtx, cancelMetadata := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelMetadata()
	err = a.orm.WithContext(metadataCtx).Transaction(func(tx *gorm.DB) error {
		file := fileRecord{TenantID: tenantID(c), UploadedByUserID: actor(c).ID, ObjectKey: key, OriginalName: fmt.Sprintf("invoice-%d.pdf", id), ContentType: "application/pdf", SizeBytes: int64(len(out)), PublicURL: url}
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

// invoiceDocumentData maps Musky's domain model onto the documents
// package's rendering DTO. Kept separate from the HTTP handler so the
// mapping itself has no gin/DB dependency and is easy to unit test.
func invoiceDocumentData(invoice model.Invoice, tenantName, creatorName string, contacts []documents.ContactLine, logoData []byte, logoExt string) documents.InvoiceData {
	items := make([]documents.InvoiceItemLine, len(invoice.Items))
	for i, item := range invoice.Items {
		items[i] = documents.InvoiceItemLine{
			Title:           item.Title,
			Code:            item.Code,
			UnitsPerPackage: documents.FormatQuantity(item.UnitsPerPackage),
			PackageCount:    documents.FormatQuantity(item.PackageCount),
			UnitPrice:       documents.FormatMoney(item.UnitPriceMinor),
			Total:           documents.FormatMoney(item.TotalMinor),
		}
	}
	return documents.InvoiceData{
		TenantName:  tenantName,
		LogoDataURI: documents.LogoDataURI(logoData, logoExt),
		TraderName:  strings.TrimSpace(creatorName),
		Contacts:    contacts,

		DocumentTypeLabel: documents.DocumentTypeLabel(invoice.DocumentType),
		DocumentTypeShort: documents.DocumentTypeShort(invoice.DocumentType),
		Number:            documents.FormatInvoiceNumber(invoice.Number),

		IssueDate:     documents.FormatDate(invoice.IssueDate),
		StatusLabel:   documents.InvoiceStatusLabel(invoice.Status),
		ClientName:    invoice.ClientName,
		ClientAddress: invoice.ClientAddress,
		Notes:         invoice.Notes,

		Items: items,

		Total:         documents.FormatMoney(invoice.TotalMinor),
		Paid:          documents.FormatMoney(invoice.PaidMinor),
		Remaining:     documents.FormatMoney(invoice.RemainingMinor),
		PaymentStatus: documents.PaymentStatusLabel(invoice.PaymentStatus),
	}
}

// moneyPDF renders EGP minor units as a plain decimal string (no thousands
// separator). It predates the documents package and is kept here because
// invoice_edit.go still uses it for an error message unrelated to PDF
// rendering; documents.FormatMoney is the version used in documents.
func moneyPDF(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }
