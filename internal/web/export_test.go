package web

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pos-system/internal/store"
)

// getCSV performs a GET and returns the parsed CSV rows plus the response.
func getCSV(t *testing.T, srv *Server, path string) ([][]string, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV from %s: %v", path, err)
	}
	return rows, rec
}

func TestSalesExportCSV(t *testing.T) {
	srv, db, _ := newTestServer(t)
	seedSale(t, db, "Bread", 6500, store.PaymentMpesa, "QGH7ABC")
	seedSale(t, db, "Milk", 6000, store.PaymentCash, "")

	rows, rec := getCSV(t, srv, "/api/sales/export")

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "transactions") {
		t.Errorf("Content-Disposition = %q, want a transactions attachment", cd)
	}
	if len(rows) != 3 { // header + two sales
		t.Fatalf("got %d CSV rows, want 3 (header + 2 sales)", len(rows))
	}
	header := strings.Join(rows[0], ",")
	if header != "Date,Time,Items,Subtotal,Tax,Total,Payment,Reference" {
		t.Errorf("header = %q", header)
	}
	// Both sales share the fixed test clock, so their relative order is not
	// meaningful; locate the bread row by its contents.
	var bread []string
	for _, row := range rows[1:] {
		if row[2] == "Bread x1" {
			bread = row
		}
	}
	if bread == nil {
		t.Fatalf("no Bread row in export: %v", rows)
	}
	if bread[5] != "65.00" || bread[6] != "M-Pesa" || bread[7] != "QGH7ABC" {
		t.Errorf("bread row = %v, want total 65.00 / M-Pesa / QGH7ABC", bread)
	}
}

func TestSalesExportDateRange(t *testing.T) {
	srv, db, _ := newTestServer(t)
	// The fixed test clock places both sales on 2026-06-28.
	seedSale(t, db, "A", 100, store.PaymentCash, "")
	seedSale(t, db, "B", 100, store.PaymentCash, "")

	// A window before the sales returns just the header.
	rows, _ := getCSV(t, srv, "/api/sales/export?from=2026-06-01&to=2026-06-02")
	if len(rows) != 1 {
		t.Fatalf("out-of-range export = %d rows, want 1 (header only)", len(rows))
	}
	// The sales' own day includes them (the plain to date covers the whole day).
	rows, _ = getCSV(t, srv, "/api/sales/export?from=2026-06-28&to=2026-06-28")
	if len(rows) != 3 {
		t.Fatalf("same-day export = %d rows, want 3 (header + 2)", len(rows))
	}
}

func TestAuditExportCSV(t *testing.T) {
	srv, db, _ := newTestServer(t)
	// AddProduct logs an "Item added" line; the seed catalogue does not.
	if _, err := db.AddProduct(store.ProductDraft{Name: "Bread", PriceCents: 6500}); err != nil {
		t.Fatalf("add product: %v", err)
	}

	rows, rec := getCSV(t, srv, "/api/audit/export")
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "activity-log") {
		t.Errorf("Content-Disposition = %q, want an activity-log attachment", cd)
	}
	if len(rows) != 2 { // header + one entry
		t.Fatalf("got %d CSV rows, want 2 (header + 1 entry)", len(rows))
	}
	if strings.Join(rows[0], ",") != "Date,Time,Action,Detail" {
		t.Errorf("header = %v", rows[0])
	}
	if rows[1][2] != "Item added" || rows[1][3] != "Bread, KSh 65.00" {
		t.Errorf("entry row = %v, want the Bread add", rows[1])
	}
}
