package documents

import (
	"context"
	"fmt"
	"html"
	"os"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// renderTimeout bounds a single PDF render. Musky's documents are at most
// a few pages of plain HTML with an embedded font/logo, so this is
// generous headroom rather than a tuned limit.
const renderTimeout = 20 * time.Second

// A4 in inches, matching Chrome DevTools Protocol's PrintToPDF units.
const (
	paperWidthInches  = 8.27
	paperHeightInches = 11.69
	marginInches      = 0.35
)

// Renderer owns one long-lived headless Chromium instance and prints HTML
// documents to PDF through it. Create a single Renderer (via NewRenderer)
// and reuse it for the process lifetime — spawning a fresh Chromium
// process per PDF would be unnecessary production overhead. Each RenderPDF
// call runs in its own isolated tab, so concurrent requests don't
// interfere with each other, and a failed render in one tab can't take
// down the shared browser.
type Renderer struct {
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

// NewRenderer launches headless Chromium once and keeps it running.
//
// The executable path comes from the MUSKY_CHROME_PATH environment
// variable when set; otherwise chromedp searches common platform install
// locations (this is normally enough on a developer's machine, but
// Contabo/production should set MUSKY_CHROME_PATH explicitly — see
// docs/pdf-rendering-migration.md). NewRenderer starts Chromium
// immediately and returns an error if it can't, so a missing or broken
// Chromium is caught here rather than surfacing later as a corrupt or
// empty PDF on a real request.
func NewRenderer(ctx context.Context) (*Renderer, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if path := os.Getenv("MUSKY_CHROME_PATH"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("MUSKY_CHROME_PATH %q is not usable: %w", path, err)
		}
		opts = append(opts, chromedp.ExecPath(path))
	}
	// Chrome refuses to launch as root without --no-sandbox (its sandbox
	// needs unprivileged-user namespace support it won't set up for root).
	// Off by default — only set MUSKY_CHROME_NO_SANDBOX=1 if the server
	// actually runs the process as root and a dedicated non-root systemd
	// user isn't an option; prefer the latter, since this flag removes a
	// real security boundary.
	if os.Getenv("MUSKY_CHROME_NO_SANDBOX") == "1" {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Run the startup check directly on browserCtx, not on a
	// context.WithTimeout wrapper: cancelling a plain derived context of a
	// chromedp browser context (as that wrapper's defer would) corrupts it
	// for every tab opened afterward, even though the cancelled context
	// itself was never reused. A goroutine+select gives the same "don't
	// hang forever" protection without ever cancelling browserCtx on the
	// success path.
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(browserCtx, chromedp.Navigate("about:blank")) }()
	select {
	case err := <-done:
		if err != nil {
			browserCancel()
			allocCancel()
			return nil, fmt.Errorf("could not start headless Chromium (set MUSKY_CHROME_PATH to a Chrome/Chromium binary): %w", err)
		}
	case <-time.After(renderTimeout):
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("timed out starting headless Chromium (set MUSKY_CHROME_PATH to a Chrome/Chromium binary)")
	}
	return &Renderer{allocCancel: allocCancel, browserCtx: browserCtx, browserCancel: browserCancel}, nil
}

// Close shuts down the shared Chromium instance. Call it once, when the
// server is shutting down. It is safe to call on a nil *Renderer.
func (r *Renderer) Close() {
	if r == nil {
		return
	}
	r.browserCancel()
	r.allocCancel()
}

// PrintOptions are the per-render knobs on top of RenderPDF's fixed A4
// layout.
type PrintOptions struct {
	// FooterLabel, when set, appears on the left of a slim running footer
	// on every page, with "page X / Y" on the right — so a document that
	// spans multiple pages stays identifiable on page 2+ without page 1
	// (e.g. an invoice number). Left empty, no footer is drawn at all.
	FooterLabel string
}

// RenderPDF prints html to an A4 PDF in its own browser tab, bounded by
// renderTimeout.
func (r *Renderer) RenderPDF(ctx context.Context, html string, opts PrintOptions) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("PDF renderer is not available")
	}
	tabCtx, cancelTab := chromedp.NewContext(r.browserCtx)
	defer cancelTab()
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, renderTimeout)
	defer cancelTimeout()
	_ = ctx // reserved for a future caller-supplied deadline; renderTimeout governs for now.

	var pdf []byte
	err := chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			frameTree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return fmt.Errorf("reading frame tree: %w", err)
			}
			if err := page.SetDocumentContent(frameTree.Frame.ID, html).Do(ctx); err != nil {
				return fmt.Errorf("setting document content: %w", err)
			}
			return nil
		}),
		// Wait for the embedded @font-face to finish loading before
		// printing, so the first render of a process doesn't fall back to
		// a system font while Chromium is still parsing it.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Evaluate(`document.fonts.ready.then(() => true)`, nil,
				func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
			).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			print := page.PrintToPDF().
				WithPrintBackground(true).
				WithPaperWidth(paperWidthInches).
				WithPaperHeight(paperHeightInches).
				WithMarginTop(marginInches).
				WithMarginBottom(footerMarginInches(opts)).
				WithMarginLeft(marginInches).
				WithMarginRight(marginInches)
			if opts.FooterLabel != "" {
				print = print.
					WithDisplayHeaderFooter(true).
					WithHeaderTemplate(`<span></span>`). // suppress Chromium's default header
					WithFooterTemplate(footerTemplate(opts.FooterLabel))
			}
			data, _, err := print.Do(ctx)
			if err != nil {
				return fmt.Errorf("printing to PDF: %w", err)
			}
			pdf = data
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}
	if len(pdf) == 0 {
		return nil, fmt.Errorf("renderer produced an empty PDF")
	}
	return pdf, nil
}

// footerMarginInches reserves extra bottom margin for the footer text when
// one is requested, so it has room to sit below the document content
// instead of overlapping it.
func footerMarginInches(opts PrintOptions) float64 {
	if opts.FooterLabel == "" {
		return marginInches
	}
	return marginInches + 0.2
}

// footerTemplate builds Chrome's print footer: label is escaped by hand
// (this HTML is handed straight to the DevTools Protocol, not through
// html/template) since a caller could in principle build it from
// data-derived text.
func footerTemplate(label string) string {
	return `<div style="width:100%;font-size:8px;padding:0 12mm;` +
		`display:flex;justify-content:space-between;direction:rtl;` +
		`font-family:sans-serif;color:#667671;">` +
		`<span>` + html.EscapeString(label) + `</span>` +
		`<span dir="ltr"><span class="pageNumber"></span> / <span class="totalPages"></span></span>` +
		`</div>`
}
