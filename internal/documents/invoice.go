// Package documents renders Musky's business documents (currently the
// sales/purchase invoice) from Go data to PDF, via server-side HTML/CSS
// printed through headless Chromium (see renderer.go). It has no database
// or HTTP dependency: callers gather the data first and pass in a plain
// DTO, which keeps rendering easy to unit test and reusable if more
// document types are added later.
//
// Arabic text is written straight into the HTML templates and left for
// Chromium to shape and bidi-order — unlike the older gofpdf-based
// renderer (internal/server/client_statement_pdf.go, unaffected by this
// package), nothing here manually reshapes Arabic glyphs or reverses cell
// order for RTL: CSS Grid and <table> already lay out right-to-left
// correctly under dir="rtl".
package documents

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"sync"
)

//go:embed fonts/Cairo.ttf
var cairoFont []byte

// cairoFontDataURI embeds the font directly in the generated HTML so
// rendering needs no filesystem or network access beyond the Chromium
// process itself — required for a Contabo deployment with no outbound
// internet access and no globally installed fonts.
var cairoFontDataURI = "data:font/truetype;base64," + base64.StdEncoding.EncodeToString(cairoFont)

//go:embed templates/invoice/invoice.html
var invoiceTemplateSource string

//go:embed templates/invoice/invoice.css
var invoiceCSSSource string

var invoiceCSS = template.CSS(strings.Replace(invoiceCSSSource, "__FONT_DATA_URI__", cairoFontDataURI, 1))

var (
	invoiceTemplateOnce sync.Once
	invoiceTemplate     *template.Template
	invoiceTemplateErr  error
)

func loadInvoiceTemplate() (*template.Template, error) {
	invoiceTemplateOnce.Do(func() {
		invoiceTemplate, invoiceTemplateErr = template.New("invoice").Parse(invoiceTemplateSource)
	})
	return invoiceTemplate, invoiceTemplateErr
}

// ContactLine is one of a trader's printable contact rows (title/value,
// e.g. "Instapay" / "0102223232"), already filtered by the caller to the
// ones a super admin approved for invoices.
type ContactLine struct {
	Title string
	Value string
}

// InvoiceItemLine is one pre-formatted row of the invoice's item table.
type InvoiceItemLine struct {
	Title           string
	Code            string
	UnitsPerPackage string
	PackageCount    string
	UnitPrice       string
	Total           string
}

// InvoiceData is everything the invoice template needs, already formatted
// for display (see the Format* helpers below). Handlers gather this from
// Musky's domain models; the renderer itself never touches the database.
type InvoiceData struct {
	TenantName  string // business name; "" renders no tenant heading
	LogoDataURI string // "" when the tenant has no logo
	TraderName  string // the user who created the invoice
	Contacts    []ContactLine

	DocumentTypeLabel string // "فاتورة بيع" / "فاتورة شراء"
	DocumentTypeShort string // "بيع" / "شراء"
	Number            string // zero-padded posted number, or "مسودة"

	IssueDate     string // dd/mm/yyyy
	StatusLabel   string // مسودة / مرحّلة / باطلة / ملغاة
	ClientName    string
	ClientAddress string
	Notes         string

	Items []InvoiceItemLine

	Total         string
	Paid          string
	Remaining     string
	PaymentStatus string // "" hides the paid/remaining/status block entirely
}

// RenderInvoiceHTML renders one invoice to a self-contained HTML document
// (inline CSS, embedded font, and — if the caller supplied one — an
// embedded logo data URI). The result has no external references, so
// printing it doesn't depend on network access.
func RenderInvoiceHTML(data InvoiceData) (string, error) {
	tmpl, err := loadInvoiceTemplate()
	if err != nil {
		return "", fmt.Errorf("parsing invoice template: %w", err)
	}
	var buf bytes.Buffer
	view := struct {
		InvoiceData
		CSS template.CSS
		// LogoDataURI shadows InvoiceData's field of the same name (Go
		// promotes the shallower field). html/template's contextual
		// auto-escaper only trusts a handful of URL schemes for a plain
		// string in a src="" attribute — data: is not one of them, and a
		// disallowed scheme is silently replaced with "#ZgotmplZ" rather
		// than erroring, so the logo would just vanish. template.URL
		// marks it as a value we've deliberately built and trust.
		LogoDataURI template.URL
	}{InvoiceData: data, CSS: invoiceCSS, LogoDataURI: template.URL(data.LogoDataURI)}
	if err := tmpl.Execute(&buf, view); err != nil {
		return "", fmt.Errorf("rendering invoice template: %w", err)
	}
	return buf.String(), nil
}

// FormatMoney renders EGP minor units (piastres) as a decimal string with
// thousands separators, e.g. 300000 -> "3,000.00".
func FormatMoney(minor int64) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	whole := groupThousands(minor / 100)
	s := fmt.Sprintf("%s.%02d", whole, minor%100)
	if negative {
		s = "-" + s
	}
	return s
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var groups []string
	for len(s) > 3 {
		groups = append([]string{s[len(s)-3:]}, groups...)
		s = s[:len(s)-3]
	}
	groups = append([]string{s}, groups...)
	return strings.Join(groups, ",")
}

// FormatQuantity renders a plain whole-number quantity (package counts,
// pieces per package).
func FormatQuantity(n int64) string { return strconv.FormatInt(n, 10) }

// FormatInvoiceNumber zero-pads a posted invoice number, or returns the
// Arabic draft label when the invoice hasn't been posted yet.
func FormatInvoiceNumber(number *int64) string {
	if number == nil {
		return "مسودة"
	}
	return fmt.Sprintf("%06d", *number)
}

// FormatDate reformats Musky's stored YYYY-MM-DD issue date into
// dd/mm/yyyy for display. Unrecognized input is returned unchanged.
func FormatDate(value string) string {
	if len(value) == 10 && value[4] == '-' && value[7] == '-' {
		return value[8:10] + "/" + value[5:7] + "/" + value[:4]
	}
	return value
}

// DocumentTypeLabel is the full Arabic label for an invoice's document
// type, e.g. for the boxed title line ("فاتورة بيع رقم: ...").
func DocumentTypeLabel(documentType string) string {
	if documentType == "purchase" {
		return "فاتورة شراء"
	}
	return "فاتورة بيع"
}

// DocumentTypeShort is the short form used inside the info grid ("بيع" /
// "شراء"), since the full label already appears in the title bar.
func DocumentTypeShort(documentType string) string {
	if documentType == "purchase" {
		return "شراء"
	}
	return "بيع"
}

// InvoiceStatusLabel maps an invoice's stored status to its Arabic label.
func InvoiceStatusLabel(status string) string {
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

// PaymentStatusLabel maps an invoice's computed payment status to its
// Arabic label. An empty PaymentStatus input (e.g. for a draft, which has
// no payments yet) should be passed through as "" so the template hides
// the paid/remaining block rather than showing a misleading "unpaid" line.
func PaymentStatusLabel(status string) string {
	switch status {
	case "paid":
		return "مدفوع"
	case "partially_paid":
		return "مدفوع جزئياً"
	case "unpaid":
		return "غير مدفوع"
	default:
		return ""
	}
}

// LogoDataURI builds a data: URI for an uploaded tenant logo so Chromium
// never needs to fetch it over the network (which could require
// authentication, or simply be unavailable). ext is the file extension as
// stored (jpg/jpeg/png/webp); an unrecognized extension or empty data
// yields "", which the template renders as no logo.
func LogoDataURI(data []byte, ext string) string {
	if len(data) == 0 {
		return ""
	}
	var mime string
	switch strings.ToLower(ext) {
	case "png":
		mime = "image/png"
	case "jpg", "jpeg":
		mime = "image/jpeg"
	case "webp":
		mime = "image/webp"
	default:
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}
