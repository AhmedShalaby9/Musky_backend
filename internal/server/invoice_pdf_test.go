package server

import (
	"bytes"
	"os"
	"testing"

	"musky/backend/internal/model"
)

func sampleInvoiceForPDF() model.Invoice {
	number := int64(671)
	return model.Invoice{
		Number:         &number,
		Status:         "posted",
		DocumentType:   "sale",
		IssueDate:      "2026-09-19",
		ClientName:     "احمد ابو عمر",
		ClientAddress:  "حاره اليهود - بجوار سنتر حمزه",
		Notes:          "توصيل خلال يومين",
		TotalMinor:     300000,
		PaidMinor:      0,
		RemainingMinor: 300000,
		PaymentStatus:  "unpaid",
		Items: []model.InvoiceItem{
			{Title: "مدفع", Code: "236", UnitsPerPackage: 30, PackageCount: 1, UnitPriceMinor: 145000, TotalMinor: 145000},
			{Title: "مدفع", Code: "235", UnitsPerPackage: 40, PackageCount: 1, UnitPriceMinor: 155000, TotalMinor: 155000},
		},
	}
}

func TestBuildInvoicePDF(t *testing.T) {
	invoice := sampleInvoiceForPDF()
	contacts := []invoiceContactLine{
		{Title: "موبايل / واتساب", Value: "01126387853"},
		{Title: "Instapay", Value: "01000000000"},
	}
	pdf := buildInvoicePDF(invoice, "شركه الطيب", "وليد مجدي", contacts, nil, "")
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil || !bytes.HasPrefix(out.Bytes(), []byte("%PDF")) {
		t.Fatal("invoice pdf failed", err)
	}
	if path := os.Getenv("MUSKY_INVOICE_SAMPLE"); path != "" {
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildInvoicePDFWithoutContactsOrLogo(t *testing.T) {
	invoice := sampleInvoiceForPDF()
	pdf := buildInvoicePDF(invoice, "", "", nil, nil, "")
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil || !bytes.HasPrefix(out.Bytes(), []byte("%PDF")) {
		t.Fatal("invoice pdf without contacts/tenant/logo failed", err)
	}
}
