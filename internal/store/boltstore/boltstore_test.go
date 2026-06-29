package boltstore

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

// Documentation-side compile-time check that the concrete bbolt store satisfies
// the interface every caller depends on.
var _ store.Local = (*Store)(nil)

// rawProduct reads a product row directly from the bucket, including inactive
// (tombstoned) rows that Products() filters out, so tombstone fields can be
// asserted. White-box access keeps this test honest about the on-disk shape.
func rawProduct(t *testing.T, s *Store, id string) store.Product {
	t.Helper()
	var p store.Product
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bProducts).Get([]byte(id))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &p)
	})
	if err != nil {
		t.Fatalf("rawProduct: %v", err)
	}
	if !found {
		t.Fatalf("rawProduct: id %q not present", id)
	}
	return p
}

// clock is a controllable time source for deterministic tests. Tests call
// clock.set to pin "now"; the store reads it through clock.now.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) set(t time.Time)     { c.t = t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

// baseTime is a fixed, non-midnight instant used as the default clock so that
// CreateSale/UpdateProduct timestamps are deterministic.
var baseTime = time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC)

// newStore opens a fresh bbolt database in a temp dir wired to a controllable
// clock, registers cleanup, and returns both.
func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{t: baseTime}
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.WithClock(c.now)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, c
}

// addProduct is a small helper that adds a product and fails on error.
func addProduct(t *testing.T, s *Store, d store.ProductDraft) store.Product {
	t.Helper()
	p, err := s.AddProduct(d)
	if err != nil {
		t.Fatalf("AddProduct(%q): %v", d.Name, err)
	}
	return p
}

func mustUUID(t *testing.T, id string) {
	t.Helper()
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("id %q is not a valid UUID: %v", id, err)
	}
}

// --- 1. UUID IDs ---

func TestProductAndSaleIDsAreDistinctUUIDs(t *testing.T) {
	s, _ := newStore(t)

	p1 := addProduct(t, s, store.ProductDraft{Name: "A", PriceCents: 100})
	p2 := addProduct(t, s, store.ProductDraft{Name: "B", PriceCents: 200})
	mustUUID(t, p1.ID)
	mustUUID(t, p2.ID)
	if p1.ID == p2.ID {
		t.Errorf("two products share an id: %q", p1.ID)
	}

	sale1, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p1.ID, Qty: 1}}, PaidCents: 100})
	if err != nil {
		t.Fatalf("CreateSale 1: %v", err)
	}
	sale2, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p2.ID, Qty: 1}}, PaidCents: 200})
	if err != nil {
		t.Fatalf("CreateSale 2: %v", err)
	}
	mustUUID(t, sale1.ID)
	mustUUID(t, sale2.ID)
	if sale1.ID == sale2.ID {
		t.Errorf("two sales share an id: %q", sale1.ID)
	}
}

// --- 2. Products listing: active only, sorted by name ---

func TestProductsActiveOnlySortedByName(t *testing.T) {
	s, _ := newStore(t)

	addProduct(t, s, store.ProductDraft{Name: "Cherry", PriceCents: 1})
	addProduct(t, s, store.ProductDraft{Name: "Apple", PriceCents: 1})
	banana := addProduct(t, s, store.ProductDraft{Name: "Banana", PriceCents: 1})
	deleted := addProduct(t, s, store.ProductDraft{Name: "Zebra", PriceCents: 1})

	if err := s.DeleteProduct(deleted.ID); err != nil {
		t.Fatalf("DeleteProduct: %v", err)
	}

	got, err := s.Products()
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	want := []string{"Apple", "Banana", "Cherry"}
	if len(got) != len(want) {
		t.Fatalf("Products len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("Products[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
	_ = banana
}

// --- 3. Barcode uniqueness via index ---

func TestBarcodeUniqueness(t *testing.T) {
	s, _ := newStore(t)

	addProduct(t, s, store.ProductDraft{Name: "First", PriceCents: 1, Barcode: "111"})

	if _, err := s.AddProduct(store.ProductDraft{Name: "Dup", PriceCents: 1, Barcode: "111"}); err != store.ErrBarcodeTaken {
		t.Errorf("AddProduct duplicate barcode err = %v, want ErrBarcodeTaken", err)
	}

	// Empty barcode never conflicts, even repeatedly.
	addProduct(t, s, store.ProductDraft{Name: "NoCode1", PriceCents: 1, Barcode: ""})
	addProduct(t, s, store.ProductDraft{Name: "NoCode2", PriceCents: 1, Barcode: ""})
}

func TestUpdateProductBarcodeReassignment(t *testing.T) {
	s, _ := newStore(t)

	a := addProduct(t, s, store.ProductDraft{Name: "A", PriceCents: 1, Barcode: "AAA"})
	b := addProduct(t, s, store.ProductDraft{Name: "B", PriceCents: 1, Barcode: "BBB"})

	// Updating a product to keep its own barcode succeeds.
	if _, err := s.UpdateProduct(a.ID, store.ProductDraft{Name: "A2", PriceCents: 2, Barcode: "AAA"}); err != nil {
		t.Fatalf("UpdateProduct keep own barcode: %v", err)
	}

	// Updating a to a barcode held by another active product fails.
	if _, err := s.UpdateProduct(a.ID, store.ProductDraft{Name: "A2", PriceCents: 2, Barcode: "BBB"}); err != store.ErrBarcodeTaken {
		t.Errorf("UpdateProduct to other's barcode err = %v, want ErrBarcodeTaken", err)
	}

	// Change a's barcode to a fresh code; "AAA" frees up.
	if _, err := s.UpdateProduct(a.ID, store.ProductDraft{Name: "A2", PriceCents: 2, Barcode: "CCC"}); err != nil {
		t.Fatalf("UpdateProduct change barcode: %v", err)
	}
	// New code resolves to a.
	if got, ok, _ := s.ProductByBarcode("CCC"); !ok || got.ID != a.ID {
		t.Errorf("ProductByBarcode(CCC) = (%v,%v), want a", got.ID, ok)
	}
	// Old code "AAA" is free and can be reused by another product.
	reuse := addProduct(t, s, store.ProductDraft{Name: "Reuse", PriceCents: 1, Barcode: "AAA"})
	if got, ok, _ := s.ProductByBarcode("AAA"); !ok || got.ID != reuse.ID {
		t.Errorf("ProductByBarcode(AAA) after reuse = (%v,%v), want reuse", got.ID, ok)
	}
	_ = b
}

// --- 4. ProductByBarcode ---

func TestProductByBarcode(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Item", PriceCents: 1, Barcode: "XYZ"})

	got, ok, err := s.ProductByBarcode("XYZ")
	if err != nil {
		t.Fatalf("ProductByBarcode: %v", err)
	}
	if !ok || got.ID != p.ID {
		t.Errorf("ProductByBarcode(XYZ) = (%v,%v), want active product", got.ID, ok)
	}

	if _, ok, _ := s.ProductByBarcode("nope"); ok {
		t.Errorf("ProductByBarcode(unknown) ok = true, want false")
	}

	if err := s.DeleteProduct(p.ID); err != nil {
		t.Fatalf("DeleteProduct: %v", err)
	}
	if _, ok, _ := s.ProductByBarcode("XYZ"); ok {
		t.Errorf("ProductByBarcode after delete ok = true, want false")
	}
}

// --- 5. Tombstone on delete ---

func TestDeleteProductTombstone(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Gone", PriceCents: 1, Barcode: "B1"})
	createTime := p.UpdatedAt

	c.add(time.Hour)
	if err := s.DeleteProduct(p.ID); err != nil {
		t.Fatalf("DeleteProduct: %v", err)
	}

	// Tombstone row: Active=false, DeletedAt!=nil, UpdatedAt advanced.
	row := rawProduct(t, s, p.ID)
	if row.Active {
		t.Errorf("tombstone Active = true, want false")
	}
	if row.DeletedAt == nil {
		t.Errorf("tombstone DeletedAt = nil, want set")
	}
	if !row.UpdatedAt.After(createTime) {
		t.Errorf("tombstone UpdatedAt = %v, want after create %v", row.UpdatedAt, createTime)
	}

	// No longer listed by Products().
	got, err := s.Products()
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	for _, x := range got {
		if x.ID == p.ID {
			t.Errorf("deleted product still listed")
		}
	}
	// Barcode freed from the index.
	if _, ok, _ := s.ProductByBarcode("B1"); ok {
		t.Errorf("barcode still indexed after delete")
	}

	// Deleting a missing id returns ErrNotFound.
	if err := s.DeleteProduct(uuid.NewString()); err != store.ErrNotFound {
		t.Errorf("DeleteProduct(missing) err = %v, want ErrNotFound", err)
	}
}

// --- 6. UpdatedAt advances on update ---

func TestUpdateProductAdvancesUpdatedAt(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "X", PriceCents: 1})
	created := p.UpdatedAt

	c.add(90 * time.Minute)
	updated, err := s.UpdateProduct(p.ID, store.ProductDraft{Name: "X2", PriceCents: 2})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	if !updated.UpdatedAt.After(created) {
		t.Errorf("UpdatedAt = %v, want after create %v", updated.UpdatedAt, created)
	}
	if !updated.UpdatedAt.Equal(c.now()) {
		t.Errorf("UpdatedAt = %v, want clock now %v", updated.UpdatedAt, c.now())
	}
}

// --- 7. CreateSale: snapshot, totals, change, payment, taxable, errors ---

func TestCreateSaleSnapshotAndTotals(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Widget", PriceCents: 500, Taxable: true})

	sale, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 3}},
		PaidCents: 2000,
	})
	if err != nil {
		t.Fatalf("CreateSale: %v", err)
	}
	if len(sale.Items) != 1 {
		t.Fatalf("Items len = %d, want 1", len(sale.Items))
	}
	it := sale.Items[0]
	if it.Name != "Widget" || it.PriceCents != 500 || it.Qty != 3 {
		t.Errorf("SaleItem snapshot = %+v, want Widget/500/3", it)
	}
	if !it.Taxable {
		t.Errorf("SaleItem.Taxable = false, want true (mirrors product)")
	}
	if sale.SubtotalCents != 1500 || sale.TotalCents != 1500 {
		t.Errorf("Subtotal/Total = %d/%d, want 1500/1500", sale.SubtotalCents, sale.TotalCents)
	}
	if sale.TaxCents != 0 {
		t.Errorf("TaxCents = %d, want 0", sale.TaxCents)
	}
	if sale.ChangeCents != 500 {
		t.Errorf("ChangeCents = %d, want 500", sale.ChangeCents)
	}
	if sale.PaymentMethod != store.PaymentCash {
		t.Errorf("PaymentMethod = %q, want default cash", sale.PaymentMethod)
	}

	// Changing the product price afterward must not change the past sale line.
	if _, err := s.UpdateProduct(p.ID, store.ProductDraft{Name: "Widget", PriceCents: 999, Taxable: true}); err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	again, ok, err := s.SaleByID(sale.ID)
	if err != nil || !ok {
		t.Fatalf("SaleByID = (%v,%v)", ok, err)
	}
	if again.Items[0].PriceCents != 500 {
		t.Errorf("past sale price changed to %d, want frozen at 500", again.Items[0].PriceCents)
	}
}

func TestCreateSaleRecordsPaymentAndReference(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	sale, err := s.CreateSale(store.SaleInput{
		Lines:         []store.SaleLine{{ProductID: p.ID, Qty: 1}},
		PaidCents:     100,
		PaymentMethod: store.PaymentMpesa,
		Reference:     "QGH7XYZ",
	})
	if err != nil {
		t.Fatalf("CreateSale: %v", err)
	}
	if sale.PaymentMethod != store.PaymentMpesa {
		t.Errorf("PaymentMethod = %q, want mpesa", sale.PaymentMethod)
	}
	if sale.Reference != "QGH7XYZ" {
		t.Errorf("Reference = %q, want QGH7XYZ", sale.Reference)
	}
	if sale.TaxMode != store.TaxModeNone {
		t.Errorf("TaxMode = %q, want none", sale.TaxMode)
	}
}

func TestCreateSaleErrors(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	tests := []struct {
		name string
		in   store.SaleInput
	}{
		{"empty lines", store.SaleInput{Lines: nil, PaidCents: 100}},
		{"qty zero", store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 0}}, PaidCents: 100}},
		{"qty negative", store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: -2}}, PaidCents: 100}},
		{"unknown product", store.SaleInput{Lines: []store.SaleLine{{ProductID: uuid.NewString(), Qty: 1}}, PaidCents: 100}},
		{"paid less than total", store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 50}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateSale(tc.in); err == nil {
				t.Errorf("CreateSale(%s) err = nil, want error", tc.name)
			}
		})
	}
}

// --- 8. Tracked stock ---

func TestTrackedStockDecrementsAndFloors(t *testing.T) {
	s, _ := newStore(t)
	tracked := addProduct(t, s, store.ProductDraft{Name: "Tracked", PriceCents: 100, TrackStock: true, Stock: 5})
	loose := addProduct(t, s, store.ProductDraft{Name: "Loose", PriceCents: 100, TrackStock: false, Stock: 0})

	if _, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: tracked.ID, Qty: 2}, {ProductID: loose.ID, Qty: 99}},
		PaidCents: 101 * 100,
	}); err != nil {
		t.Fatalf("CreateSale: %v", err)
	}

	prods, _ := s.Products()
	byID := map[string]store.Product{}
	for _, p := range prods {
		byID[p.ID] = p
	}
	if got := byID[tracked.ID].Stock; got != 3 {
		t.Errorf("tracked stock = %d, want 3", got)
	}
	if got := byID[loose.ID].Stock; got != 0 {
		t.Errorf("loose stock = %d, want 0 (untracked, unchanged)", got)
	}

	// Oversell the tracked product: stock floors at zero, never negative.
	if _, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: tracked.ID, Qty: 100}},
		PaidCents: 100 * 100,
	}); err != nil {
		t.Fatalf("CreateSale oversell: %v", err)
	}
	prods, _ = s.Products()
	for _, p := range prods {
		if p.ID == tracked.ID && p.Stock != 0 {
			t.Errorf("oversold tracked stock = %d, want floored at 0", p.Stock)
		}
	}
}

// --- 9. Reference index / SalesByReference ---

func TestSalesByReference(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	mk := func(ref string) store.Sale {
		c.add(time.Minute)
		sale, err := s.CreateSale(store.SaleInput{
			Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 1}},
			PaidCents: 100,
			Reference: ref,
		})
		if err != nil {
			t.Fatalf("CreateSale(ref=%q): %v", ref, err)
		}
		return sale
	}

	first := mk("QGH7ABC")
	second := mk("QGH7ABC") // same ref, later
	mk("DIFFERENT")
	mk("") // empty ref, not indexed

	got, err := s.SalesByReference("QGH7ABC")
	if err != nil {
		t.Fatalf("SalesByReference: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SalesByReference len = %d, want 2", len(got))
	}
	// Newest first.
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("SalesByReference order = [%s,%s], want newest-first [%s,%s]", got[0].ID, got[1].ID, second.ID, first.ID)
	}

	// Exact match only: a partial/prefix does not match.
	if r, _ := s.SalesByReference("QGH7"); len(r) != 0 {
		t.Errorf("SalesByReference(partial) len = %d, want 0", len(r))
	}
	if r, _ := s.SalesByReference("QGH7ABCD"); len(r) != 0 {
		t.Errorf("SalesByReference(superstring) len = %d, want 0", len(r))
	}
	// Empty ref returns empty.
	if r, _ := s.SalesByReference(""); len(r) != 0 {
		t.Errorf("SalesByReference(empty) len = %d, want 0", len(r))
	}
	// Unknown ref returns empty.
	if r, _ := s.SalesByReference("NOPE"); len(r) != 0 {
		t.Errorf("SalesByReference(unknown) len = %d, want 0", len(r))
	}
}

// --- 10. SaleByID ---

func TestSaleByID(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})
	sale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100})
	if err != nil {
		t.Fatalf("CreateSale: %v", err)
	}

	got, ok, err := s.SaleByID(sale.ID)
	if err != nil {
		t.Fatalf("SaleByID: %v", err)
	}
	if !ok || got.ID != sale.ID {
		t.Errorf("SaleByID = (%v,%v), want found", got.ID, ok)
	}

	missing, ok, err := s.SaleByID(uuid.NewString())
	if err != nil {
		t.Errorf("SaleByID(unknown) err = %v, want nil", err)
	}
	if ok {
		t.Errorf("SaleByID(unknown) ok = true, want false (got %+v)", missing)
	}
}

// --- 11. SalesSince ---

func TestSalesSince(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	mkAt := func(at time.Time) store.Sale {
		c.set(at)
		sale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100})
		if err != nil {
			t.Fatalf("CreateSale: %v", err)
		}
		return sale
	}

	old := mkAt(baseTime.Add(-48 * time.Hour))
	mid := mkAt(baseTime.Add(-1 * time.Hour))
	recent := mkAt(baseTime.Add(1 * time.Hour))

	cutoff := baseTime.Add(-2 * time.Hour)
	got, err := s.SalesSince(cutoff)
	if err != nil {
		t.Fatalf("SalesSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SalesSince len = %d, want 2 (excludes the 48h-old one)", len(got))
	}
	// Newest first.
	if got[0].ID != recent.ID || got[1].ID != mid.ID {
		t.Errorf("SalesSince order = [%s,%s], want [%s,%s]", got[0].ID, got[1].ID, recent.ID, mid.ID)
	}
	for _, sale := range got {
		if sale.ID == old.ID {
			t.Errorf("SalesSince included the old sale")
		}
	}
}

// --- 12. Analytics ---

func TestAnalytics(t *testing.T) {
	s, c := newStore(t)
	// Fix "now" to a known noon so day boundaries are stable.
	now := time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC)

	cheap := addProduct(t, s, store.ProductDraft{Name: "Cheap", PriceCents: 100})
	pricey := addProduct(t, s, store.ProductDraft{Name: "Pricey", PriceCents: 1000})

	mkAt := func(at time.Time, lines ...store.SaleLine) {
		c.set(at)
		var paid int64 = 1_000_000
		if _, err := s.CreateSale(store.SaleInput{Lines: lines, PaidCents: paid}); err != nil {
			t.Fatalf("CreateSale at %v: %v", at, err)
		}
	}

	// Today: two sales.
	mkAt(now.Add(-1*time.Hour), store.SaleLine{ProductID: pricey.ID, Qty: 2}) // 2000, hour 11
	mkAt(now, store.SaleLine{ProductID: cheap.ID, Qty: 3})                    // 300, hour 12
	// Yesterday: one sale.
	mkAt(now.AddDate(0, 0, -1), store.SaleLine{ProductID: cheap.ID, Qty: 5}) // 500
	// Three days ago: one sale (in 7-day window, not today/yesterday).
	mkAt(now.AddDate(0, 0, -3), store.SaleLine{ProductID: pricey.ID, Qty: 1}) // 1000
	// Ten days ago: outside the window entirely.
	mkAt(now.AddDate(0, 0, -10), store.SaleLine{ProductID: pricey.ID, Qty: 9})

	// Restore now for the Analytics computation.
	c.set(now)
	a, err := s.Analytics()
	if err != nil {
		t.Fatalf("Analytics: %v", err)
	}

	// Today: 2 sales, total 2000+300=2300, items 2+3=5.
	if a.Today.SaleCount != 2 || a.Today.TotalCents != 2300 || a.Today.ItemCount != 5 {
		t.Errorf("Today = %+v, want {2300 2 5}", a.Today)
	}
	// Yesterday: 1 sale, 500, 5 items.
	if a.Yesterday.SaleCount != 1 || a.Yesterday.TotalCents != 500 || a.Yesterday.ItemCount != 5 {
		t.Errorf("Yesterday = %+v, want {500 1 5}", a.Yesterday)
	}

	// Days: 7 buckets oldest-first; index 6 = today, 5 = yesterday, 3 = -3 days.
	if len(a.Days) != 7 {
		t.Fatalf("Days len = %d, want 7", len(a.Days))
	}
	if a.Days[6].Date != now.Format("2006-01-02") {
		t.Errorf("Days[6].Date = %q, want today %q", a.Days[6].Date, now.Format("2006-01-02"))
	}
	if a.Days[6].Label != now.Format("Mon") {
		t.Errorf("Days[6].Label = %q, want %q", a.Days[6].Label, now.Format("Mon"))
	}
	if a.Days[6].TotalCents != 2300 {
		t.Errorf("Days[6].TotalCents = %d, want 2300", a.Days[6].TotalCents)
	}
	if a.Days[5].TotalCents != 500 {
		t.Errorf("Days[5].TotalCents = %d, want 500 (yesterday)", a.Days[5].TotalCents)
	}
	if a.Days[3].TotalCents != 1000 {
		t.Errorf("Days[3].TotalCents = %d, want 1000 (-3 days)", a.Days[3].TotalCents)
	}
	// The 10-days-ago sale is outside the window: it should not appear in any bucket.
	var dayTotal int64
	for _, d := range a.Days {
		dayTotal += d.TotalCents
	}
	if dayTotal != 2300+500+1000 {
		t.Errorf("sum of Days = %d, want 3800 (excludes out-of-window sale)", dayTotal)
	}

	// Hours: 24 buckets midnight-first.
	if len(a.Hours) != 24 {
		t.Fatalf("Hours len = %d, want 24", len(a.Hours))
	}
	for h, hb := range a.Hours {
		if hb.Hour != h {
			t.Errorf("Hours[%d].Hour = %d, want %d", h, hb.Hour, h)
		}
	}
	// Within window, hour 11 saw 2000 (today) and hour 12 saw 300+500=800.
	if a.Hours[11].TotalCents != 2000 {
		t.Errorf("Hours[11] = %d, want 2000", a.Hours[11].TotalCents)
	}
	if a.Hours[12].TotalCents != 300+500+1000 {
		t.Errorf("Hours[12] = %d, want 1800", a.Hours[12].TotalCents)
	}

	// TopProducts ranked by revenue desc, within window only.
	// Pricey: today 2000 + (-3d) 1000 = 3000. Cheap: 300 + 500 = 800.
	if len(a.TopProducts) != 2 {
		t.Fatalf("TopProducts len = %d, want 2", len(a.TopProducts))
	}
	if a.TopProducts[0].Name != "Pricey" || a.TopProducts[0].RevenueCents != 3000 {
		t.Errorf("TopProducts[0] = %+v, want Pricey/3000", a.TopProducts[0])
	}
	if a.TopProducts[1].Name != "Cheap" || a.TopProducts[1].RevenueCents != 800 {
		t.Errorf("TopProducts[1] = %+v, want Cheap/800", a.TopProducts[1])
	}
}

func TestAnalyticsTopProductsCapAtFive(t *testing.T) {
	s, c := newStore(t)
	now := baseTime
	c.set(now)

	for i := 0; i < 7; i++ {
		// Each product priced distinctly so revenue ordering is unambiguous.
		p := addProduct(t, s, store.ProductDraft{Name: string(rune('A' + i)), PriceCents: int64((i + 1) * 100)})
		if _, err := s.CreateSale(store.SaleInput{
			Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 1}},
			PaidCents: 1_000_000,
		}); err != nil {
			t.Fatalf("CreateSale: %v", err)
		}
	}
	a, err := s.Analytics()
	if err != nil {
		t.Fatalf("Analytics: %v", err)
	}
	if len(a.TopProducts) != 5 {
		t.Fatalf("TopProducts len = %d, want capped at 5", len(a.TopProducts))
	}
	// Highest priced (G=700) should rank first.
	if a.TopProducts[0].Name != "G" {
		t.Errorf("TopProducts[0] = %q, want G (highest revenue)", a.TopProducts[0].Name)
	}
	// Revenue must be non-increasing.
	for i := 1; i < len(a.TopProducts); i++ {
		if a.TopProducts[i].RevenueCents > a.TopProducts[i-1].RevenueCents {
			t.Errorf("TopProducts not sorted desc at %d: %+v", i, a.TopProducts)
		}
	}
}

// --- 13. Settings ---

func TestSettingsDefaultsAndOverrides(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	// Tax defaults present.
	if got[store.KeyTaxRateBps] != "0" {
		t.Errorf("KeyTaxRateBps = %q, want 0", got[store.KeyTaxRateBps])
	}
	if got[store.KeyTaxMode] != store.TaxModeNone {
		t.Errorf("KeyTaxMode = %q, want none", got[store.KeyTaxMode])
	}
	if got[store.KeyShopName] != "My Shop" {
		t.Errorf("KeyShopName = %q, want default My Shop", got[store.KeyShopName])
	}

	if err := s.SetSetting(store.KeyShopName, "Duka Bora"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got2, _ := s.Settings()
	if got2[store.KeyShopName] != "Duka Bora" {
		t.Errorf("after SetSetting KeyShopName = %q, want Duka Bora", got2[store.KeyShopName])
	}
	// Untouched defaults still merge through.
	if got2[store.KeyPaperWidth] != "80" {
		t.Errorf("KeyPaperWidth = %q, want default 80", got2[store.KeyPaperWidth])
	}
}

// --- 14. Logo ---

func TestLogoLifecycle(t *testing.T) {
	s, _ := newStore(t)

	if _, ok, err := s.Logo(); err != nil || ok {
		t.Errorf("Logo() initial = (ok=%v, err=%v), want (false,nil)", ok, err)
	}
	if got, _ := s.Settings(); got[store.KeyHasLogo] != "no" {
		t.Errorf("KeyHasLogo initial = %q, want no", got[store.KeyHasLogo])
	}

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02}
	if err := s.SetLogo(data); err != nil {
		t.Fatalf("SetLogo: %v", err)
	}
	got, ok, err := s.Logo()
	if err != nil || !ok {
		t.Fatalf("Logo() = (ok=%v, err=%v), want found", ok, err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Logo bytes = %v, want %v", got, data)
	}
	if set, _ := s.Settings(); set[store.KeyHasLogo] != "yes" {
		t.Errorf("KeyHasLogo after SetLogo = %q, want yes", set[store.KeyHasLogo])
	}

	if err := s.DeleteLogo(); err != nil {
		t.Fatalf("DeleteLogo: %v", err)
	}
	if _, ok, _ := s.Logo(); ok {
		t.Errorf("Logo() after delete ok = true, want false")
	}
	if set, _ := s.Settings(); set[store.KeyHasLogo] != "no" {
		t.Errorf("KeyHasLogo after DeleteLogo = %q, want no", set[store.KeyHasLogo])
	}
}

// --- 15. SeedIfEmpty ---

func TestSeedIfEmpty(t *testing.T) {
	s, _ := newStore(t)

	if err := s.SeedIfEmpty(); err != nil {
		t.Fatalf("SeedIfEmpty: %v", err)
	}
	got, err := s.Products()
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("seeded products = %d, want 8", len(got))
	}
	for _, p := range got {
		if !p.Taxable {
			t.Errorf("seeded %q Taxable = false, want true", p.Name)
		}
		if !p.Active {
			t.Errorf("seeded %q Active = false, want true", p.Name)
		}
	}

	// Idempotent: a second call is a no-op when products already exist.
	if err := s.SeedIfEmpty(); err != nil {
		t.Fatalf("SeedIfEmpty second call: %v", err)
	}
	got2, _ := s.Products()
	if len(got2) != 8 {
		t.Errorf("after second SeedIfEmpty products = %d, want still 8", len(got2))
	}
}
