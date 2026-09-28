# Musky PDF Rendering Migration --- Agent Handoff

## Implementation status (2026-09-28)

The plan below was implemented **for the invoice only**, on explicit
instruction — the client statement (`client_statement_pdf.go`) was left
untouched on its existing `gofpdf` renderer and is out of scope here. Any
"Client Statement" section below is the original plan, not something that
was built.

**What exists now:**

- `internal/documents/renderer.go` — `Renderer`, a long-lived headless
  Chromium instance (`chromedp`), started once and reused; `RenderPDF`
  prints arbitrary HTML to an A4 PDF in its own isolated tab per call, with
  an optional running footer (`PrintOptions.FooterLabel`) showing on every
  page via Chrome's native header/footer templates — this is what keeps a
  multi-page invoice identifiable on page 2+ without page 1.
- `internal/documents/invoice.go` — `InvoiceData`/`InvoiceItemLine`/
  `ContactLine` DTOs, `RenderInvoiceHTML` (Go `html/template`, so untrusted
  invoice/client text is auto-escaped), and the `Format*`/`*Label` helpers
  (money with thousands separators, dates, invoice numbers, status labels).
  Pure functions only — no DB, no HTTP, no Chromium.
- `internal/documents/templates/invoice/{invoice.html,invoice.css}` — the
  actual design. RTL/Arabic layout is handled by the browser natively:
  items are written in plain reading order in the HTML and a `<table>`
  under `dir="rtl"` lays its columns out right-to-left on its own; the info
  grid uses CSS Grid, which does the same for `direction: rtl`. No manual
  cell-order reversal, no `rtl.Shape()`, anywhere in this package.
- `internal/documents/fonts/Cairo.ttf` (+ `Cairo-OFL.txt`) — Google's Cairo
  variable font (OFL-licensed, Arabic + Latin + digits in one file),
  embedded via `//go:embed` and inlined into the page as a base64
  `@font-face` data URI, so rendering needs no installed fonts and no
  network access.
- `internal/server/invoice_pdf.go` — the `POST /invoices/:id/pdf` handler
  now gathers data exactly as before (invoice, items, tenant, logo, the
  creating trader's name, their approved `user_contacts`) and calls into
  `documents` instead of drawing with `gofpdf`. External behavior is
  unchanged: same route, same upload-to-R2/S3 + `pdf_url` update flow, same
  JSON response shape. The logo is fetched once in the handler and passed
  to the renderer as a data URI, per the "avoid an authenticated URL
  dependency" requirement below.
- `internal/server/router.go` — `API` gained a `documentRenderer()` method:
  Chromium is started eagerly in `New()` (logged, not fatal, if it fails —
  see "Startup vs. lazy" below) and lazily retried exactly once on first
  use otherwise, so at most one Chromium process ever runs per server
  process.
- Tests: `internal/documents/invoice_test.go` (template-level, no
  Chromium — content assertions, HTML-escaping, missing-logo/contacts,
  long names, 40 items) and `internal/documents/renderer_test.go`
  (real-Chromium integration tests: valid PDF signature, Arabic + 80 items
  across pages, a real embedded logo, an empty draft). The renderer tests
  **skip** (not fail) when Chromium isn't available, mirroring how this
  repo's `MYSQL_TEST_DSN`-gated tests already skip without a database; set
  `MUSKY_CHROME_PATH` to run them for real. Both suites support dumping a
  sample via env var, same convention as the existing
  `MUSKY_STATEMENT_SAMPLE`: `MUSKY_INVOICE_HTML_SAMPLE`,
  `MUSKY_INVOICE_PDF_SAMPLE`, `MUSKY_INVOICE_MULTIPAGE_SAMPLE`.
- `internal/server/invoice_pdf_test.go` (the old `buildInvoicePDF` gofpdf
  tests) was deleted — its subject no longer exists.

**Two real bugs found and fixed during implementation** (not just design
choices — verified by actually rendering PDFs with a downloaded Chrome for
Testing binary and reading them back):
1. Cancelling a `context.WithTimeout` wrapper *derived from* the shared
   chromedp browser context (rather than a context chromedp itself
   created) corrupts that browser context for every tab opened afterward,
   even though the cancelled wrapper was never reused. `NewRenderer`'s
   startup check now runs directly on the browser context, with a
   goroutine+`select` for the "don't hang forever" timeout instead of a
   cancelled wrapper.
2. Go's `html/template` auto-escaper only trusts a handful of URL schemes
   for a plain string in a `src=""` attribute — `data:` is not one of
   them, and a rejected value is silently replaced with `#ZgotmplZ`
   instead of erroring, so the logo would render with no visible failure
   at all. Fixed by casting the (self-generated, trusted) logo data URI to
   `template.URL` before executing the template. A regression test for
   this exists at both the template level and the real-Chromium level.

**Deviations from the plan below, with reasons:**

- **Chromium lifecycle is "eager, non-fatal, lazy-retry-once,"
  not "must fail at startup."** The plan says the application must fail
  clearly if Chromium is unavailable. Taken literally as a *process*-level
  startup failure, that would take down the entire API (unrelated
  endpoints included) whenever Chromium is missing or broken, and would
  break `go test` for anyone without Chrome installed, since
  `internal/server`'s test suite constructs `API` via `New(db)` directly
  (`commerce_test.go`, `integration_test.go`). Instead, a missing Chromium
  is logged at startup and every `/invoices/:id/pdf` request returns a
  clear `503` with the underlying error until it's fixed — "fail clearly"
  is honored at the request level rather than the process level.
- **`server.New`'s signature is unchanged**, so there's no clean hook for
  `cmd/server/main.go` to call `Renderer.Close()` on graceful shutdown.
  `Close()` exists and is nil-safe; wiring it in is a small follow-up
  (would need `New` to expose the `*API` or a shutdown func) not done here
  to avoid an unrelated signature change across its three call sites.
- **No "previous balance" line.** The reference invoice you can see in
  chat history showed a running client balance; Musky doesn't compute
  "balance as of this specific invoice" anywhere today (only the client's
  *current* total balance), and reconstructing that historically is real
  scope beyond a rendering migration. Not rendered, per "render only
  information Musky currently owns."
- **No "amount in Arabic words."** Derivable in principle, but an
  accurate Arabic number-to-words converter (grammar/gender agreement) is
  its own correctness-sensitive feature, not a rendering concern. Left out
  as a follow-up rather than rendering something un-audited.
- **Invoice number padding kept at 6 digits** (`%06d`, matching the old
  `gofpdf` renderer and the live reference invoice's own "000671"), not
  the 7 digits shown in this doc's own RTL example below — that example
  reads as illustrative, and changing display padding isn't a business
  rule worth guessing at.

**Contabo / deployment:**

- Install a Chromium or Google Chrome package (e.g. `apt install
  chromium`) and set `MUSKY_CHROME_PATH` to its executable (see
  `.env.example`); the server logs and keeps running without it, but every
  invoice PDF request will 503 until it's set and the process is
  restarted (or the lazy retry succeeds, which it won't on its own if the
  binary is simply absent).
- No outbound network access is required for rendering: the font and
  logo are both inlined as data URIs before Chromium ever sees the HTML.
- Chromium needs to actually launch headless in whatever container/VM
  runs the server; on a minimal Debian/Ubuntu box this usually means a
  small set of shared-library packages alongside the browser package
  itself (Chromium's own dependency list, not something this migration
  adds) — verify with `MUSKY_CHROME_PATH=/usr/bin/chromium go run
  ./cmd/server` (or the renderer tests) on the actual target image before
  relying on it in production.
- If the musky-backend process runs as root (a root systemd unit, say),
  Chrome will refuse to launch at all ("Running as root without
  --no-sandbox is not supported") — see `MUSKY_CHROME_NO_SANDBOX` in
  `.env.example`. Prefer running the service as a dedicated non-root user
  instead of setting it; that keeps Chrome's sandbox intact.
- On Ubuntu, prefer Google's own `google-chrome-stable` .deb over the
  `chromium`/`chromium-browser` apt package where that package is a Snap
  wrapper (common on Ubuntu 20.04+) — Snap-confined Chromium is unreliable
  to launch from a systemd service. See the README's "Invoice PDF
  rendering" section for install steps.

**Not done (explicitly out of scope this round):** client statement
migration (Phase 4 below), and Phase 6 cleanup (`gofpdf`/`nautilus/pdf/rtl`
stay as dependencies — `client_statement_pdf.go` still uses both).

---

## Objective

Replace Musky's current low-level `gofpdf` PDF generation with a
professional server-side HTML/CSS → Headless Chromium → PDF rendering
system.

The Flutter desktop application must continue to only trigger PDF
generation and display/download the resulting PDF. PDF creation remains
entirely in the Go/Gin backend.

## Current System/Users/ahmedgamal/Desktop/MUSKY_PDF_RENDERING_AGENT_HANDOFF.md

Repository: `musky-backend`

Current PDF files include: - `internal/server/invoice_pdf.go` -
`internal/server/client_statement_pdf.go` - `invoice_pdf_test.go` -
`client_statement_pdf_test.go`

Current stack: - `github.com/phpdave11/gofpdf` -
`github.com/gvanbeck/nautilus/pdf/rtl` - embedded
`internal/server/fonts/DejaVuSansCondensed.ttf` - optional
`MUSKY_PDF_FONT_PATH`

Current flow:

1.  Flutter calls `POST /invoices/:id/pdf`.
2.  Backend loads invoice, items, tenant data, logo, trader/contact
    information.
3.  `buildInvoicePDF(...)` manually draws the document using `gofpdf`.
4.  PDF bytes are uploaded using the existing R2/S3-compatible storage
    layer.
5.  Invoice `pdf_url` is updated.
6.  Flutter receives the URL and displays it using `pdfx`.

Client statements follow the same general model using
`buildClientStatementPDF(...)`.

Do NOT change the Flutter PDF viewer flow unless required for
compatibility.

## Problems With Current Implementation

The existing renderer is too low-level and produces poor visual results.

Problems include: - manual X/Y positioning - manual borders and cells -
manual RTL workarounds - explicit Arabic shaping through `rtl.Shape()` -
difficult mixed Arabic + Latin/number rendering - difficult pagination -
difficult repeated table headers - difficult maintenance - invoice and
statement layouts duplicate rendering concepts - visual changes require
editing imperative Go drawing code

The new renderer should make document design primarily an HTML/CSS
concern.

------------------------------------------------------------------------

# Target Architecture

Use:

`Go domain data → html/template → HTML/CSS → Headless Chromium → PDF []byte → existing R2/S3 storage`

Preferred Go integration: `chromedp` controlling an installed
Chromium/Google Chrome binary and Chrome DevTools `Page.printToPDF`.

Do not introduce a Node.js PDF service unless there is a strong
technical reason discovered during implementation. Musky should remain a
Go backend.

The production server is a Contabo VPS.

------------------------------------------------------------------------

# Required Documents

The new system must support:

1.  Sales invoice
2.  Client statement

Build the rendering infrastructure so additional document types can be
added later without duplicating the renderer.

------------------------------------------------------------------------

# Proposed Package Structure

Adapt this to the existing repository conventions rather than forcing it
literally:

``` text
internal/
  documents/
    renderer.go
    chromium.go

    templates/
      shared/
        base.css
        header.html
        footer.html

      invoice/
        invoice.html
        invoice.css

      client_statement/
        statement.html
        statement.css

    invoice.go
    client_statement.go

  documents/fonts/
    <chosen Arabic/Latin font files>
```

Templates, CSS and required fonts/assets should preferably be embedded
in the Go binary using `//go:embed`, so production rendering does not
depend on the process working directory.

Do not depend on fonts installed globally on the Contabo VPS.

------------------------------------------------------------------------

# Separation of Responsibilities

Keep document data preparation separate from HTML generation and
Chromium rendering.

Conceptually:

``` go
RenderInvoiceHTML(data InvoiceDocumentData) (string, error)

RenderClientStatementHTML(data ClientStatementDocumentData) (string, error)

RenderPDF(ctx context.Context, html string, options PDFOptions) ([]byte, error)
```

Exact APIs may differ based on the repository.

The renderer must NOT query the database.

Handlers/services should collect the required data first and pass a
document DTO to the renderer.

Preserve the useful property of the current builders: rendering should
be testable without requiring DB or object storage.

------------------------------------------------------------------------

# Arabic / RTL Requirements

Arabic support is critical.

Example that MUST render correctly:

``` text
فاتورة بيع رقم: 0000010
```

Requirements: - Arabic letters must join correctly. - RTL ordering must
be correct. - English/Western numbers must remain LTR and readable
inside Arabic sentences. - Product codes, phone numbers, dates, monetary
numbers and invoice numbers must not become visually reversed. - Do not
use `rtl.Shape()` in the new renderer. - Let Chromium perform Arabic
shaping and bidi layout. - Use semantic HTML direction where
appropriate.

Examples:

``` html
<html lang="ar" dir="rtl">
```

and for isolated numeric/code values:

``` html
<span dir="ltr">0000010</span>
```

CSS can use `direction`, `unicode-bidi`, logical properties, etc. where
needed.

Test mixed Arabic/Latin content explicitly.

------------------------------------------------------------------------

# Fonts

Replace the visual dependence on `DejaVuSansCondensed.ttf` for the new
templates.

Use a professional font with strong Arabic and Latin/numeric coverage.
Noto Sans Arabic or another appropriate freely distributable font is
acceptable.

Requirements: - font files should be local/embedded - PDF generation
must not require Google Fonts/network access - output must be
deterministic on development and Contabo - ensure the font licensing
permits bundling in the application

If separate Arabic and Latin fonts are used, make sure numbers/codes
visually match the document.

Do not delete the existing font/current renderer until migration is
complete.

------------------------------------------------------------------------

# Invoice Design

Do NOT reproduce the current Musky PDF layout.

Create a clean professional invoice with: - tenant logo -
tenant/business name - configured visible phone/contact numbers -
invoice/document title - invoice number - client name - date/time where
available - sale/payment type/status where available -
salesperson/trader where available - address/notes where available -
item table - totals/payment summary - relevant balance information if
provided by the existing domain model

Tenant customization currently needs to support primarily: - logo -
name - numbers/contact information

Do not build a full template editor/theme system.

The uploaded customer/reference invoice was supplied as a
business-content reference, NOT as a visual design that must be copied
exactly.

Its useful information hierarchy includes: - company/trader identity -
phone / WhatsApp / landline - sales invoice number - date - sale type -
time - client - salesperson - notes - item code - item description -
cartons count - package - pieces - quantity - price - total - total
cartons/pieces/quantity - invoice total - remaining amount - amount in
Arabic words - previous balance - invoice value - paid amount -
resulting/current balance - signature - address

IMPORTANT: Do not invent database fields merely because they appear in
the reference invoice. Inspect Musky's actual models/data first. Render
only information Musky currently owns or can correctly derive from
existing data. If important reference fields do not exist, document that
finding instead of silently changing accounting logic.

------------------------------------------------------------------------

# Items Table

The item table is one of the most important parts of the document.

It must: - use RTL-aware column ordering appropriate for Arabic - align
text and numbers cleanly - make product descriptions easy to scan - use
stable column widths where useful - handle long product names - handle
large monetary values - handle codes as LTR - avoid clipping - avoid
overlapping text - avoid splitting an individual item row across pages
where possible

Do not shrink the entire document to unreadable font sizes just to fit
wide tables.

------------------------------------------------------------------------

# Multi-Page Invoices

Invoices may contain many items and can exceed one page.

The implementation MUST handle this professionally.

Requirements: - automatic page breaks - table header repeats on
subsequent pages - item rows should not split across pages where
avoidable - totals block should remain together where possible - no
content may overlap footer/page boundaries - page 2+ should still be
understandable without page 1 - avoid large accidental blank areas
caused by incorrect page-break rules

Useful print CSS concepts include:

``` css
thead {
    display: table-header-group;
}

tr {
    break-inside: avoid;
}

.totals {
    break-inside: avoid;
}
```

Test with at least: - 1 item - \~10 items - enough items for 2 pages -
enough items for 3+ pages - unusually long Arabic product descriptions

------------------------------------------------------------------------

# Page Size

There is currently no physical printing requirement.

Still generate a standard, predictable document size suitable for PDF
viewing and future printing. A4 portrait is a sensible default for
invoice and statement unless existing business requirements indicate
otherwise.

Do not optimize for thermal printers.

Use reasonable margins and make good use of page width.

------------------------------------------------------------------------

# Client Statement

Migrate `client_statement_pdf.go` to the same HTML/Chromium rendering
infrastructure.

The statement should have its own template because it is a denser
transaction-history document, but it should reuse: - typography - tenant
header/branding - shared formatting helpers - page setup - RTL rules -
Chromium renderer - shared CSS primitives where appropriate

Requirements: - professional transaction table - date range clearly
displayed - client identity clearly displayed - opening/previous balance
where the existing system provides it -
debit/credit/payment/invoice/balance information according to the
CURRENT Musky accounting model - correct Arabic/number direction -
automatic pagination - repeated table headers - page numbers if
practical

Do NOT alter financial calculations as part of the visual migration.

------------------------------------------------------------------------

# Number Formatting

Create reusable formatting helpers instead of formatting values ad hoc
inside templates.

Consider helpers for: - money - quantities - dates - times -
invoice/document numbers - optional values

Examples should visually render as expected:

``` text
3,000.00
46,577.00
0000010
19/09/2026
```

Use the project's existing monetary precision/business rules. Do not
change rounding/accounting behavior during this migration.

------------------------------------------------------------------------

# Logo / Image Handling

Tenant logos may come from existing uploaded storage.

The PDF renderer must render them reliably.

Avoid making Chromium depend on an authenticated external URL if it can
be avoided.

Preferred approaches: - fetch the logo in backend code and convert it to
a data URI, or - otherwise provide Chromium a reliable local/embedded
representation.

Handle: - no logo - wide logo - tall logo - transparent PNG - JPEG

Constrain dimensions without stretching/distorting the image.

------------------------------------------------------------------------

# Security

Treat template data as untrusted application data.

Use Go `html/template`, not unsafe string concatenation.

Do not allow invoice/client/product text to inject arbitrary HTML or
scripts.

Avoid loading arbitrary remote resources from document data.

Templates should not require JavaScript unless genuinely necessary.

Chromium should be configured appropriately for a backend service. Do
not blindly copy insecure flags such as `--no-sandbox`; if the
deployment environment requires a flag, document why and minimize
privileges.

------------------------------------------------------------------------

# Chromium Lifecycle / Performance

Do NOT start a brand-new Chromium process for every single invoice if
that creates unnecessary production overhead.

Design a reusable renderer/lifecycle appropriate for the Gin server: -
initialize/manage Chromium at application/service startup where
practical - create isolated page/tab contexts for render jobs - use
render timeouts - cancel contexts correctly - close resources - make
concurrent requests safe - prevent one failed render from permanently
breaking the renderer

Inspect expected Musky traffic and choose a simple robust implementation
rather than premature complexity.

Log useful errors without logging sensitive document contents.

------------------------------------------------------------------------

# Contabo Deployment

Add/document everything required on the Contabo server.

At minimum document: - Chromium/Chrome package requirement - how the
executable path is discovered/configured - any required environment
variable - required Linux packages if applicable - service
restart/deployment implications

Prefer an environment variable such as:

``` text
MUSKY_CHROME_PATH=/usr/bin/chromium
```

but first inspect existing configuration conventions and follow them.

The application must fail with a clear error if Chromium is unavailable
rather than returning a corrupt/empty PDF.

Do not make PDF generation dependent on outbound internet access.

------------------------------------------------------------------------

# Existing Storage/API Behavior

Preserve existing external behavior unless there is a compelling reason
to change it.

Invoice flow should remain approximately:

``` text
POST /invoices/:id/pdf
        ↓
load invoice data
        ↓
render HTML
        ↓
Chromium PrintToPDF
        ↓
PDF []byte
        ↓
existing R2/S3-compatible storage
        ↓
update pdf_url
        ↓
return URL
        ↓
Flutter opens with pdfx
```

Do not replace R2/S3 storage.

Do not move PDF generation to Flutter.

Do not require Flutter to render HTML.

------------------------------------------------------------------------

# Migration Strategy

Do this incrementally.

## Phase 1 --- Inspect

Before coding: 1. Inspect current invoice PDF handler and builder. 2.
Inspect statement builder. 3. Inspect invoice, line-item, tenant,
trader/client and payment/balance models. 4. Inspect storage upload
logic. 5. Inspect current tests. 6. Identify exactly which
reference-invoice fields Musky currently supports. 7. Identify all code
paths that call the current PDF builders.

Do not guess field semantics.

## Phase 2 --- Introduce New Renderer

Add the reusable HTML/template + Chromium rendering infrastructure
without deleting the existing `gofpdf` implementation.

## Phase 3 --- Invoice

Implement professional invoice HTML/CSS and connect it to the existing
endpoint.

Generate sample outputs and verify Arabic/number rendering and
pagination.

## Phase 4 --- Client Statement

Migrate the client statement onto the same renderer.

## Phase 5 --- Tests

Add/update tests.

## Phase 6 --- Cleanup

Only after both document types work and tests pass: - remove unused
`gofpdf` rendering code - remove `nautilus/pdf/rtl` if nothing else uses
it - remove obsolete font handling if nothing else uses it - run
`go mod tidy`

Do not remove dependencies until repository-wide usage has been checked.

------------------------------------------------------------------------

# Testing Requirements

Keep testing at multiple levels.

## Template Tests

Render fabricated data to HTML and assert important content exists.

Examples: - Arabic client name - invoice number - tenant name - product
rows - totals - optional fields

## Renderer Integration Tests

Generate actual PDF bytes through Chromium.

At minimum verify: - no error - output is non-empty - output begins with
a valid PDF signature - realistic Arabic data renders without renderer
errors

## Visual Samples

Keep a developer-only mechanism similar to the current sample env
behavior.

It should be easy to generate: - invoice sample HTML - invoice sample
PDF - statement sample HTML - statement sample PDF

Do not commit generated customer documents containing real
personal/business data.

Use fabricated fixtures.

## Edge Cases

Test: - missing logo - missing optional contact fields - Arabic +
English numbers in same line - long client/company name - long product
name - many items - zero paid - partial payment - large totals -
statement with many transactions - values containing HTML-special
characters

------------------------------------------------------------------------

# Visual Quality Bar

The generated PDFs should look like professionally designed business
documents, not HTML screenshots and not manually drawn debug tables.

Prioritize: - clear visual hierarchy - consistent spacing - readable
Arabic typography - restrained borders - clear numeric alignment -
consistent padding - strong table readability - professional totals
section - balanced use of whitespace - clean tenant branding

Avoid: - excessive dark borders around every field - tiny text - cramped
cells - inconsistent font weights - arbitrary colors - huge unused
header areas - RTL/LTR collisions - text touching borders - raw
database-looking presentation

Use print-specific CSS.

------------------------------------------------------------------------

# Important Non-Goals

Do NOT: - rewrite accounting calculations - redesign database schema
solely for the sample invoice - move PDF generation into Flutter -
replace R2/S3 storage - build a tenant drag-and-drop invoice designer -
build thermal printing - introduce Node just for PDF generation - copy
the reference invoice pixel-for-pixel - keep manually shaping Arabic in
the new renderer

------------------------------------------------------------------------

# Acceptance Criteria

The task is complete when:

1.  Invoice PDFs are generated using HTML/CSS + Chromium, not `gofpdf`.
2.  Client statements use the same rendering engine.
3.  Arabic text renders correctly.
4.  Western numbers/codes remain correctly ordered inside RTL content.
5.  Tenant logo/name/contact numbers render correctly.
6.  Multi-page invoices paginate correctly.
7.  Table headers repeat across pages.
8.  Long rows/content do not overlap or clip.
9.  Existing financial values/calculations remain unchanged.
10. Existing R2/S3 upload and `pdf_url` flow remains functional.
11. Flutter can continue opening the returned PDF with `pdfx`.
12. Renderer/template tests exist.
13. Real PDF integration tests exist where practical.
14. Contabo Chromium installation/configuration is documented.
15. Old PDF dependencies are removed only after both migrations are
    confirmed.
16. `go test ./...` passes.

------------------------------------------------------------------------

# Agent Working Instructions

Start by reading the repository. Do not immediately implement from
assumptions.

Before making significant changes, report: - current PDF call flow -
relevant models/fields - fields available for the invoice design -
fields from the supplied reference that Musky does NOT have - current
accounting calculations that must remain untouched - proposed files to
add/change - any deployment concern discovered

Then implement the migration.

When a business/accounting field is ambiguous, preserve existing
behavior instead of inventing a new interpretation.

Focus this task on PDF rendering quality and maintainability.
