package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pos-system/internal/store"
	"pos-system/internal/store/boltstore"
)

// newTestServer builds a Server backed by a fresh bbolt store on a temp dir,
// wired to a fixed clock so sale timestamps are deterministic.
func newTestServer(t *testing.T) (*Server, *boltstore.Store, func() time.Time) {
	t.Helper()
	now := time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	db, err := boltstore.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.WithClock(clock)
	t.Cleanup(func() { _ = db.Close() })
	srv, err := New(db, nil, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, db, clock
}

// postSearch sends a JSON body to the search handler and returns the response.
func postSearch(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sales/search", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeSearch reads the sales array and truncated flag from a response.
func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) ([]store.Sale, bool) {
	t.Helper()
	var resp struct {
		Sales     []store.Sale `json:"sales"`
		Truncated bool         `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp.Sales, resp.Truncated
}

func seedSale(t *testing.T, db *boltstore.Store, name string, cents int64, method, ref string) store.Sale {
	t.Helper()
	p, err := db.AddProduct(store.ProductDraft{Name: name, PriceCents: cents})
	if err != nil {
		t.Fatalf("AddProduct: %v", err)
	}
	sale, err := db.CreateSale(store.SaleInput{
		Lines:         []store.SaleLine{{ProductID: p.ID, Qty: 1}},
		PaidCents:     cents,
		PaymentMethod: method,
		Reference:     ref,
	})
	if err != nil {
		t.Fatalf("CreateSale: %v", err)
	}
	return sale
}

func TestHandleSearchMethodNotAllowed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sales/search", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

func TestHandleSearchBadBody(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := postSearch(t, srv, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", rec.Code)
	}
}

func TestHandleSearchByCriteria(t *testing.T) {
	srv, db, _ := newTestServer(t)
	mpesa := seedSale(t, db, "Bread", 300, store.PaymentMpesa, "QGH7ABC")
	seedSale(t, db, "Milk", 200, store.PaymentCash, "")

	// Item name substring (case-insensitive).
	rec := postSearch(t, srv, `{"itemName":"BREAD"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	sales, _ := decodeSearch(t, rec)
	if len(sales) != 1 || sales[0].ID != mpesa.ID {
		t.Errorf("itemName search = %d sales, want only the bread sale", len(sales))
	}

	// Payment method exact.
	rec = postSearch(t, srv, `{"paymentMethod":"mpesa"}`)
	sales, _ = decodeSearch(t, rec)
	if len(sales) != 1 || sales[0].PaymentMethod != store.PaymentMpesa {
		t.Errorf("paymentMethod search = %v, want one mpesa sale", sales)
	}

	// Reference exact.
	rec = postSearch(t, srv, `{"reference":"QGH7ABC"}`)
	sales, _ = decodeSearch(t, rec)
	if len(sales) != 1 || sales[0].Reference != "QGH7ABC" {
		t.Errorf("reference search = %v, want one matching sale", sales)
	}

	// Amount range.
	rec = postSearch(t, srv, `{"minCents":250}`)
	sales, _ = decodeSearch(t, rec)
	if len(sales) != 1 || sales[0].TotalCents != 300 {
		t.Errorf("minCents search = %v, want the 300-cent sale", sales)
	}
}

func TestHandleSearchPlainDateRange(t *testing.T) {
	srv, db, _ := newTestServer(t)
	// The fixed clock places both sales on 2026-06-28 at noon.
	seedSale(t, db, "A", 100, store.PaymentCash, "")
	seedSale(t, db, "B", 100, store.PaymentCash, "")

	// A plain end date for the same day must include that whole day (the
	// exclusive End is pushed to the next midnight).
	rec := postSearch(t, srv, `{"start":"2026-06-28","end":"2026-06-28"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	sales, _ := decodeSearch(t, rec)
	if len(sales) != 2 {
		t.Errorf("plain-date same-day range = %d sales, want 2", len(sales))
	}

	// A day before should match nothing.
	rec = postSearch(t, srv, `{"start":"2026-06-01","end":"2026-06-02"}`)
	sales, _ = decodeSearch(t, rec)
	if len(sales) != 0 {
		t.Errorf("out-of-range plain date = %d sales, want 0", len(sales))
	}
}

func TestHandleSearchRFC3339Date(t *testing.T) {
	srv, db, _ := newTestServer(t)
	seedSale(t, db, "A", 100, store.PaymentCash, "")

	// RFC3339 bounds straddling noon include the sale.
	rec := postSearch(t, srv, `{"start":"2026-06-28T00:00:00Z","end":"2026-06-29T00:00:00Z"}`)
	sales, _ := decodeSearch(t, rec)
	if len(sales) != 1 {
		t.Errorf("RFC3339 range = %d sales, want 1", len(sales))
	}
}

func TestHandleSearchTruncatedFlag(t *testing.T) {
	srv, db, _ := newTestServer(t)
	p, err := db.AddProduct(store.ProductDraft{Name: "P", PriceCents: 100})
	if err != nil {
		t.Fatalf("AddProduct: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100}); err != nil {
			t.Fatalf("CreateSale: %v", err)
		}
	}

	rec := postSearch(t, srv, `{"limit":2}`)
	sales, truncated := decodeSearch(t, rec)
	if len(sales) != 2 {
		t.Fatalf("limit=2 returned %d sales, want 2", len(sales))
	}
	if !truncated {
		t.Errorf("expected truncated=true when result count hits the limit")
	}

	// Below the limit, no truncation flag.
	rec = postSearch(t, srv, `{"limit":10}`)
	_, truncated = decodeSearch(t, rec)
	if truncated {
		t.Errorf("did not expect truncated when under the limit")
	}
}

func TestHandleSearchUnparseableDateIgnored(t *testing.T) {
	srv, db, _ := newTestServer(t)
	seedSale(t, db, "A", 100, store.PaymentCash, "")

	// A garbage date must be ignored rather than erroring the request, so all
	// sales come back.
	rec := postSearch(t, srv, `{"start":"not-a-date"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	sales, _ := decodeSearch(t, rec)
	if len(sales) != 1 {
		t.Errorf("unparseable date should be ignored, got %d sales", len(sales))
	}
}
