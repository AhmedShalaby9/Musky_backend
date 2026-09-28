package documents

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// tinyPlaceholderPNG is a 60x30 solid-color PNG, embedded here (rather
// than read from a file) so the logo-rendering test is self-contained.
const tinyPlaceholderPNG = "iVBORw0KGgoAAAANSUhEUgAAADwAAAAeCAIAAAD/+uoYAAAAOklEQVR4nO3OAQkAIBAAMZMY0YhmM4b3MFiArX3uOOv7QDpMWlo6QFpaOkBaWjpAWlo6QFpaOmBk+gFSlRDYo7vgKQAAAABJRU5ErkJggg=="

// newTestRenderer starts a real headless Chromium for the integration
// tests below. It skips the test (not fails it) when Chromium can't be
// started, so the suite stays green on a machine/CI image without
// Chrome/Chromium installed; set MUSKY_CHROME_PATH to point at one to
// exercise these tests. This mirrors how the MySQL integration tests in
// internal/server skip without MYSQL_TEST_DSN.
func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer(context.Background())
	if err != nil {
		t.Skipf("headless Chromium not available (set MUSKY_CHROME_PATH to test PDF rendering): %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

func TestRenderPDFProducesAValidPDF(t *testing.T) {
	r := newTestRenderer(t)
	html, err := RenderInvoiceHTML(sampleInvoiceData())
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := r.RenderPDF(context.Background(), html, PrintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatal("output does not start with a PDF signature")
	}
	if len(pdf) < 1000 {
		t.Fatalf("output looks too small to be a real PDF (%d bytes)", len(pdf))
	}
	if path := os.Getenv("MUSKY_INVOICE_PDF_SAMPLE"); path != "" {
		if err := os.WriteFile(path, pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderPDFHandlesArabicAndManyPages(t *testing.T) {
	r := newTestRenderer(t)
	data := sampleInvoiceData()
	data.ClientName = "عميل باسم طويل جداً يحتوي على أرقام ١٢٣ و English mixed 456"
	data.Items = nil
	for i := 0; i < 80; i++ {
		data.Items = append(data.Items, InvoiceItemLine{
			Title:           "صنف رقم " + FormatQuantity(int64(i)),
			Code:            "WM-000",
			UnitsPerPackage: "12",
			PackageCount:    "1",
			UnitPrice:       FormatMoney(10000),
			Total:           FormatMoney(120000),
		})
	}
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := r.RenderPDF(context.Background(), html, PrintOptions{FooterLabel: DocumentTypeLabel("sale") + " " + data.Number})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatal("output does not start with a PDF signature")
	}
	if path := os.Getenv("MUSKY_INVOICE_MULTIPAGE_SAMPLE"); path != "" {
		if err := os.WriteFile(path, pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderPDFWithLogo(t *testing.T) {
	r := newTestRenderer(t)
	logoBytes, err := base64.StdEncoding.DecodeString(tinyPlaceholderPNG)
	if err != nil {
		t.Fatal(err)
	}
	data := sampleInvoiceData()
	data.LogoDataURI = LogoDataURI(logoBytes, "png")
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	// Regression guard for a real bug hit during development: Go's
	// html/template auto-escaper rejects a data: URI in a src="" attribute
	// unless it's explicitly typed as a trusted URL, silently replacing it
	// with "#ZgotmplZ" instead of erroring — which would render the page
	// with no visible failure but a missing logo.
	if strings.Contains(html, "ZgotmplZ") {
		t.Fatal("logo src was rejected by html/template's URL sanitizer (see RenderInvoiceHTML's template.URL cast)")
	}
	if !strings.Contains(html, "data:image/png;base64,") {
		t.Fatal("expected the logo's data URI to appear verbatim in the img src")
	}
	pdf, err := r.RenderPDF(context.Background(), html, PrintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatal("output does not start with a PDF signature")
	}
	if path := os.Getenv("MUSKY_INVOICE_LOGO_PDF_SAMPLE"); path != "" {
		if err := os.WriteFile(path, pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderPDFHandlesEmptyInvoice(t *testing.T) {
	r := newTestRenderer(t)
	html, err := RenderInvoiceHTML(InvoiceData{
		DocumentTypeLabel: DocumentTypeLabel("sale"),
		DocumentTypeShort: DocumentTypeShort("sale"),
		Number:            FormatInvoiceNumber(nil),
		StatusLabel:       InvoiceStatusLabel("draft"),
		ClientName:        "عميل تجريبي",
		Total:             FormatMoney(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := r.RenderPDF(context.Background(), html, PrintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatal("output does not start with a PDF signature")
	}
}
