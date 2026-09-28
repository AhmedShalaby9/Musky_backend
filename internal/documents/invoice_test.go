package documents

import (
	"os"
	"strings"
	"testing"
)

func sampleInvoiceData() InvoiceData {
	return InvoiceData{
		TenantName: "شركه الطيب",
		TraderName: "وليد مجدي",
		Contacts: []ContactLine{
			{Title: "موبايل / واتساب", Value: "01126387853"},
			{Title: "Instapay", Value: "01000000000"},
		},
		DocumentTypeLabel: DocumentTypeLabel("sale"),
		DocumentTypeShort: DocumentTypeShort("sale"),
		Number:            FormatInvoiceNumber(ptrInt64(671)),
		IssueDate:         FormatDate("2026-09-19"),
		StatusLabel:       InvoiceStatusLabel("posted"),
		ClientName:        "احمد ابو عمر",
		ClientAddress:     "حاره اليهود - بجوار سنتر حمزه",
		Notes:             "توصيل خلال يومين",
		Items: []InvoiceItemLine{
			{Title: "مدفع", Code: "236", UnitsPerPackage: "30", PackageCount: "1", UnitPrice: FormatMoney(145000), Total: FormatMoney(145000)},
			{Title: "مدفع", Code: "235", UnitsPerPackage: "40", PackageCount: "1", UnitPrice: FormatMoney(155000), Total: FormatMoney(155000)},
		},
		Total:         FormatMoney(300000),
		Paid:          FormatMoney(0),
		Remaining:     FormatMoney(300000),
		PaymentStatus: PaymentStatusLabel("unpaid"),
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestFormatMoney(t *testing.T) {
	cases := map[int64]string{
		300000:   "3,000.00",
		4657700:  "46,577.00",
		150:      "1.50",
		5:        "0.05",
		-300000:  "-3,000.00",
		1000000:  "10,000.00",
		10000000: "100,000.00",
	}
	for minor, want := range cases {
		if got := FormatMoney(minor); got != want {
			t.Errorf("FormatMoney(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestFormatInvoiceNumber(t *testing.T) {
	if got := FormatInvoiceNumber(nil); got != "مسودة" {
		t.Errorf("draft number = %q, want مسودة", got)
	}
	if got := FormatInvoiceNumber(ptrInt64(10)); got != "000010" {
		t.Errorf("FormatInvoiceNumber(10) = %q, want 000010", got)
	}
}

func TestFormatDate(t *testing.T) {
	if got := FormatDate("2026-09-19"); got != "19/09/2026" {
		t.Errorf("FormatDate = %q, want 19/09/2026", got)
	}
	if got := FormatDate("not-a-date"); got != "not-a-date" {
		t.Errorf("FormatDate should pass through unrecognized input, got %q", got)
	}
}

func TestLogoDataURI(t *testing.T) {
	if got := LogoDataURI(nil, "png"); got != "" {
		t.Errorf("no logo bytes must render as no logo, got %q", got)
	}
	if got := LogoDataURI([]byte{1, 2, 3}, "unknown"); got != "" {
		t.Errorf("unrecognized extension must render as no logo, got %q", got)
	}
	got := LogoDataURI([]byte{1, 2, 3}, "png")
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("LogoDataURI(png) = %q, want an image/png data URI", got)
	}
	if got := LogoDataURI([]byte{1, 2, 3}, "jpg"); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Errorf("jpg extension must map to image/jpeg, got %q", got)
	}
}

func TestRenderInvoiceHTMLContainsExpectedContent(t *testing.T) {
	html, err := RenderInvoiceHTML(sampleInvoiceData())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(html), "<!doctype html>") {
		t.Fatal("output must be a full HTML document")
	}
	for _, want := range []string{
		`dir="rtl"`,
		"وليد مجدي - شركه الطيب",
		"موبايل / واتساب",
		"01126387853",
		"Instapay",
		"فاتورة بيع رقم:",
		"000671",
		"19/09/2026",
		"احمد ابو عمر",
		"حاره اليهود - بجوار سنتر حمزه",
		"توصيل خلال يومين",
		"مدفع",
		"236",
		"1,450.00",
		"3,000.00",
		"غير مدفوع",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
	if path := os.Getenv("MUSKY_INVOICE_HTML_SAMPLE"); path != "" {
		if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderInvoiceHTMLLogoDataURISurvivesEscaping(t *testing.T) {
	data := sampleInvoiceData()
	data.LogoDataURI = "data:image/png;base64,AAAA"
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "ZgotmplZ") {
		t.Fatal("logo data URI must not be rejected by html/template's URL sanitizer")
	}
	if !strings.Contains(html, `src="data:image/png;base64,AAAA"`) {
		t.Fatal("expected the logo data URI to appear verbatim in the img src")
	}
}

func TestRenderInvoiceHTMLEscapesUserContent(t *testing.T) {
	data := sampleInvoiceData()
	data.ClientName = `<script>alert(1)</script> & "quoted"`
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("client name must be HTML-escaped, not injected raw")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("expected the client name to appear HTML-escaped")
	}
}

func TestRenderInvoiceHTMLWithoutOptionalFields(t *testing.T) {
	data := InvoiceData{
		DocumentTypeLabel: DocumentTypeLabel("sale"),
		DocumentTypeShort: DocumentTypeShort("sale"),
		Number:            FormatInvoiceNumber(nil),
		IssueDate:         FormatDate(""),
		StatusLabel:       InvoiceStatusLabel("draft"),
		ClientName:        "عميل بدون بيانات إضافية",
		Total:             FormatMoney(0),
	}
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "class=\"logo\"") {
		t.Fatal("no logo means no <img class=\"logo\"> element")
	}
	if strings.Contains(html, "العنوان:") || strings.Contains(html, "ملاحظات:") {
		t.Fatal("address/notes row must be omitted when both are empty")
	}
	if strings.Contains(html, "المدفوع:") {
		t.Fatal("paid/remaining block must be omitted when PaymentStatus is empty")
	}
	if !strings.Contains(html, "مسودة") {
		t.Fatal("expected the draft number label")
	}
}

func TestRenderInvoiceHTMLManyItemsAndLongNames(t *testing.T) {
	data := sampleInvoiceData()
	longName := strings.Repeat("منتج بوصف طويل جداً يختبر التفاف النص ", 6)
	data.Items = nil
	for i := 0; i < 40; i++ {
		data.Items = append(data.Items, InvoiceItemLine{
			Title:           longName,
			Code:            "WM-0001",
			UnitsPerPackage: "12",
			PackageCount:    "3",
			UnitPrice:       FormatMoney(12345),
			Total:           FormatMoney(37035),
		})
	}
	html, err := RenderInvoiceHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(html, "WM-0001") != 40 {
		t.Fatalf("expected 40 item rows, found %d", strings.Count(html, "WM-0001"))
	}
}
