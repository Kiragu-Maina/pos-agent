package boltstore

import (
	"path/filepath"
	"testing"
	"time"

	"pos-system/internal/store"
)

func syncStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOutboxMarksUserWritesNotSeed(t *testing.T) {
	s := syncStore(t)

	// Seed items must never be queued for push.
	if err := s.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pending, err := s.PendingProducts()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("seed items must not be pending, got %d", len(pending))
	}

	// A real add queues one product.
	p, err := s.AddProduct(store.ProductDraft{Name: "Airtime 50", PriceCents: 5000, Taxable: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	pending, _ = s.PendingProducts()
	if len(pending) != 1 || pending[0].ID != p.ID {
		t.Fatalf("expected the added product pending, got %v", pending)
	}

	// A sale queues one sale.
	if _, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 5000}); err != nil {
		t.Fatalf("sale: %v", err)
	}
	psales, _ := s.PendingSales()
	if len(psales) != 1 {
		t.Fatalf("expected 1 pending sale, got %d", len(psales))
	}
}

func TestClearPushedRemovesMarks(t *testing.T) {
	s := syncStore(t)
	p, _ := s.AddProduct(store.ProductDraft{Name: "Soap", PriceCents: 5000})
	sale, _ := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 5000})

	if err := s.ClearPushed([]string{p.ID}, []string{sale.ID}, nil, time.Time{}, false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	pp, _ := s.PendingProducts()
	ps, _ := s.PendingSales()
	if len(pp) != 0 || len(ps) != 0 {
		t.Fatalf("expected outbox empty, got %d products %d sales", len(pp), len(ps))
	}
}

func TestApplyPulledProductLWW(t *testing.T) {
	s := syncStore(t)
	id := "11111111-1111-1111-1111-111111111111"
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)

	// Insert via pull.
	must(t, s.ApplyPulled([]store.Product{{ID: id, Name: "Rice", PriceCents: 18000, Active: true, UpdatedAt: t2}}, nil, nil, nil, nil))
	if got := products(t, s); len(got) != 1 || got[0].Name != "Rice" {
		t.Fatalf("expected Rice, got %v", got)
	}
	// Applying a pulled change must not echo back into the outbox.
	if pp, _ := s.PendingProducts(); len(pp) != 0 {
		t.Fatalf("pulled product must not be pending, got %d", len(pp))
	}

	// Older pull loses.
	must(t, s.ApplyPulled([]store.Product{{ID: id, Name: "Stale", PriceCents: 1, Active: true, UpdatedAt: t1}}, nil, nil, nil, nil))
	if got := products(t, s); got[0].Name != "Rice" {
		t.Fatalf("older write must lose, got %q", got[0].Name)
	}

	// Newer pull wins.
	must(t, s.ApplyPulled([]store.Product{{ID: id, Name: "Rice 2kg", PriceCents: 36000, Active: true, UpdatedAt: t3}}, nil, nil, nil, nil))
	if got := products(t, s); got[0].Name != "Rice 2kg" || got[0].PriceCents != 36000 {
		t.Fatalf("newer write must win, got %v", got[0])
	}

	// A pulled tombstone removes it from the active list.
	del := t3.Add(time.Hour)
	must(t, s.ApplyPulled([]store.Product{{ID: id, Name: "Rice 2kg", Active: false, UpdatedAt: del, DeletedAt: &del}}, nil, nil, nil, nil))
	if got := products(t, s); len(got) != 0 {
		t.Fatalf("tombstone must hide the product, got %d", len(got))
	}
}

func TestApplyPulledSaleAppendOnly(t *testing.T) {
	s := syncStore(t)
	sale := store.Sale{ID: "22222222-2222-2222-2222-222222222222", CreatedAt: time.Now().UTC(),
		TotalCents: 9000, Reference: "ABC123", PaymentMethod: "mpesa"}

	must(t, s.ApplyPulled(nil, []store.Sale{sale}, nil, nil, nil))
	must(t, s.ApplyPulled(nil, []store.Sale{sale}, nil, nil, nil)) // idempotent

	got, found, err := s.SaleByID(sale.ID)
	if err != nil || !found {
		t.Fatalf("sale not found: %v found=%v", err, found)
	}
	if got.Reference != "ABC123" {
		t.Fatalf("reference: got %q", got.Reference)
	}
	byRef, _ := s.SalesByReference("ABC123")
	if len(byRef) != 1 {
		t.Fatalf("reference index: expected 1 sale, got %d", len(byRef))
	}
	if ps, _ := s.PendingSales(); len(ps) != 0 {
		t.Fatalf("pulled sale must not be pending, got %d", len(ps))
	}
}

func TestSettingsDirtyAndPulledLWW(t *testing.T) {
	s := syncStore(t)

	// The per-device printer address never marks settings dirty.
	must(t, s.SetSetting(store.KeyPrinterAddr, "10.0.0.5:9100"))
	if _, _, dirty, _ := s.PendingSettings(); dirty {
		t.Fatal("printer address must not make settings dirty")
	}

	// A shop-level setting does.
	must(t, s.SetSetting(store.KeyShopName, "Duka Bora"))
	m, _, dirty, err := s.PendingSettings()
	must(t, err)
	if !dirty || m[store.KeyShopName] != "Duka Bora" {
		t.Fatalf("expected dirty settings with the new name, got %v dirty=%v", m, dirty)
	}
	if _, ok := m[store.KeyPrinterAddr]; ok {
		t.Fatal("printer address must never be in the sync settings")
	}

	// A newer pulled settings change wins; an older one loses.
	future := time.Now().UTC().Add(24 * time.Hour)
	must(t, s.ApplyPulled(nil, nil, nil, map[string]string{store.KeyShopName: "Cloud Name"}, &future))
	if got, _ := s.Settings(); got[store.KeyShopName] != "Cloud Name" {
		t.Fatalf("newer pulled settings must win, got %q", got[store.KeyShopName])
	}
	past := time.Now().UTC().Add(-24 * time.Hour)
	must(t, s.ApplyPulled(nil, nil, nil, map[string]string{store.KeyShopName: "Old Name"}, &past))
	if got, _ := s.Settings(); got[store.KeyShopName] != "Cloud Name" {
		t.Fatalf("older pulled settings must lose, got %q", got[store.KeyShopName])
	}
}

func TestLinkStateRoundTrip(t *testing.T) {
	s := syncStore(t)
	if st, _ := s.SyncState(); st.Linked {
		t.Fatal("a fresh store must be unlinked")
	}
	must(t, s.Link("https://cloud.example", "shop-1", "mama@duka.co.ke", "tok"))
	must(t, s.SetCursor(42))
	st, _ := s.SyncState()
	if !st.Linked || st.ShopID != "shop-1" || st.Email != "mama@duka.co.ke" || st.Token != "tok" || st.Cursor != 42 {
		t.Fatalf("link state round trip failed: %+v", st)
	}
	must(t, s.Unlink())
	if st, _ := s.SyncState(); st.Linked {
		t.Fatal("unlink must clear the link")
	}
}

// helpers

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func products(t *testing.T, s *Store) []store.Product {
	t.Helper()
	got, err := s.Products()
	if err != nil {
		t.Fatalf("products: %v", err)
	}
	return got
}
