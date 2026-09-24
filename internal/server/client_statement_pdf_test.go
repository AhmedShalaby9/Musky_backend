package server

import (
	"bytes"
	"os"
	"testing"
	"time"

	"musky/backend/internal/model"
)

// Mirrors the reference report: four product lines on invoice 709, then a cash receipt.
func sampleStatement() ([]model.LedgerEntry, statementItems) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	entries := []model.LedgerEntry{
		{Kind: "invoice", RefID: ptr(uint64(1)), InvoiceNumber: ptr(int64(709)), DocumentType: "sale", DeltaMinor: 4341600, RunningBalance: 4341600, At: at},
		{Kind: "receipt", RefID: ptr(uint64(9)), DeltaMinor: -3000000, RunningBalance: 1341600, Method: ptr("cash"), Notes: "م 1536", At: at.Add(time.Hour)},
	}
	items := statementItems{invoices: map[uint64][]statementItem{1: {
		{Title: "طياراه سينسور", Code: "WM66-38", UnitsPerPackage: 120, PackageCount: 1, UnitPriceMinor: 7800, TotalMinor: 936000},
		{Title: "عنبه", Code: "WM81", UnitsPerPackage: 24, PackageCount: 5, UnitPriceMinor: 10300, TotalMinor: 1236000},
		{Title: "اسكوشي 2", Code: "WM66-53", UnitsPerPackage: 480, PackageCount: 2, UnitPriceMinor: 1550, TotalMinor: 1488000},
		{Title: "مسدس شخصيه 1", Code: "WM1-6", UnitsPerPackage: 96, PackageCount: 1, UnitPriceMinor: 7100, TotalMinor: 681600},
	}}, returns: map[uint64][]statementItem{}}
	return entries, items
}

func TestStatementLinesExpandItems(t *testing.T) {
	entries, items := sampleStatement()
	from := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	lines := statementLines(entries, items, 0, from)
	wantBalances := []int64{0, 936000, 2172000, 3660000, 4341600, 1341600}
	if len(lines) != len(wantBalances) {
		t.Fatalf("got %d lines, want %d", len(lines), len(wantBalances))
	}
	for i, want := range wantBalances {
		if lines[i].Balance != want {
			t.Fatalf("line %d balance = %d, want %d", i, lines[i].Balance, want)
		}
	}
	if !lines[0].Opening || lines[0].Description != "رصيد سابق" {
		t.Fatal("first line must be the previous balance", lines[0])
	}
	if lines[2].Description != "مبيعات صنف: عنبه" || lines[2].Code != "WM81" || lines[2].Packages != 5 || lines[2].Debit != 1236000 {
		t.Fatal("wrong item line", lines[2])
	}
	if lines[5].Credit != 3000000 || lines[5].Description != "متحصلات مباشرة: نقدي، ملحوظة: م 1536" {
		t.Fatal("wrong receipt line", lines[5])
	}
}

func TestStatementLinesFallBackWhenItemsDoNotMatch(t *testing.T) {
	entries, items := sampleStatement()
	items.invoices[1] = items.invoices[1][:1]
	lines := statementLines(entries, items, 0, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	if len(lines) != 3 || lines[1].Debit != 4341600 || lines[1].HasItem || lines[2].Balance != 1341600 {
		t.Fatal("mismatched items must collapse to one summary row", lines)
	}
}

func TestStatementMoney(t *testing.T) {
	for minor, want := range map[int64]string{936000: "9360", 1550: "15.5", 1525: "15.25", -3000000: "-30000", 5: "0.05"} {
		if got := statementMoney(minor); got != want {
			t.Fatalf("statementMoney(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestBuildClientStatementPDF(t *testing.T) {
	entries, items := sampleStatement()
	from, to := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	lines := statementLines(entries, items, 0, from)
	pdf := buildClientStatementPDF(model.Client{ID: 201, Name: "زياد احمد"}, lines, from, to)
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil || !bytes.HasPrefix(out.Bytes(), []byte("%PDF")) {
		t.Fatal("statement pdf failed", err)
	}
	if path := os.Getenv("MUSKY_STATEMENT_SAMPLE"); path != "" {
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFoldInvoiceAdjustmentsIntoPosting(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	// Scan order puts "adjust" before "invoice" at the same timestamp.
	entries := []model.LedgerEntry{
		{Kind: "opening", DeltaMinor: 1000, At: at.Add(-time.Hour)},
		{Kind: "adjust", RefID: ptr(uint64(1)), DeltaMinor: -681600, At: at},
		{Kind: "invoice", RefID: ptr(uint64(1)), DeltaMinor: 4341600, At: at},
		{Kind: "receipt", RefID: ptr(uint64(9)), DeltaMinor: -3000000, At: at.Add(time.Hour)},
	}
	got := foldInvoiceAdjustments(entries)
	if len(got) != 3 || got[1].Kind != "invoice" || got[1].DeltaMinor != 3660000 || got[1].RunningBalance != 3661000 || got[2].RunningBalance != 661000 {
		t.Fatal("adjustment must fold into its invoice", got)
	}

	// The statement then lists the edited invoice's current items.
	_, items := sampleStatement()
	items.invoices[1] = items.invoices[1][:3] // the 681600 line was removed by the edit
	lines := statementLines(got[1:], items, 1000, at.Add(-time.Hour))
	if len(lines) != 5 || !lines[3].HasItem || lines[3].Balance != 3661000 || lines[4].Balance != 661000 {
		t.Fatal("edited invoice must expand into its current items", lines)
	}
}

func TestFoldKeepsOrphanAdjustment(t *testing.T) {
	got := foldInvoiceAdjustments([]model.LedgerEntry{{Kind: "adjust", RefID: ptr(uint64(5)), DeltaMinor: 300}})
	if len(got) != 1 || got[0].RunningBalance != 300 {
		t.Fatal("an adjustment without a posting must stay visible", got)
	}
}
