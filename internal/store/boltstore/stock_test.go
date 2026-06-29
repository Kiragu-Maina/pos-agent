package boltstore

import (
	"testing"

	"pos-system/internal/store"
)

// stockOf reads a product's current derived on-hand via the catalogue.
func stockOf(t *testing.T, s *Store, id string) int {
	t.Helper()
	prods, err := s.Products()
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	for _, p := range prods {
		if p.ID == id {
			return p.Stock
		}
	}
	t.Fatalf("product %s not found", id)
	return 0
}

func TestStockDerivedFromEvents(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Soda", PriceCents: 8000, TrackStock: true, Stock: 10})

	// Initial event sets on-hand to 10.
	if got := stockOf(t, s, p.ID); got != 10 {
		t.Fatalf("after add, stock = %d, want 10", got)
	}

	// A sale derives the sold quantity; no counter is written.
	if _, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 3}}, PaidCents: 24000}); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if got := stockOf(t, s, p.ID); got != 7 {
		t.Fatalf("after selling 3, stock = %d, want 7", got)
	}

	// A relative restock adds on top, and returns the new on-hand.
	got, err := s.Restock(p.ID, 5)
	if err != nil {
		t.Fatalf("restock: %v", err)
	}
	if got.Stock != 12 {
		t.Errorf("restock returned stock %d, want 12", got.Stock)
	}
	if got := stockOf(t, s, p.ID); got != 12 {
		t.Errorf("after restock, stock = %d, want 12", got)
	}
}

func TestStockEditSetsAbsoluteViaAdjustment(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Rice", PriceCents: 18000, TrackStock: true, Stock: 8})
	if _, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 3}}, PaidCents: 54000}); err != nil {
		t.Fatalf("sale: %v", err)
	}
	// On-hand is 5; editing the count to 20 records a +15 correction.
	updated, err := s.UpdateProduct(p.ID, store.ProductDraft{Name: "Rice", PriceCents: 18000, Taxable: false, TrackStock: true, Stock: 20})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Stock != 20 {
		t.Errorf("update returned stock %d, want 20", updated.Stock)
	}
	if got := stockOf(t, s, p.ID); got != 20 {
		t.Errorf("after setting to 20, stock = %d, want 20", got)
	}

	// Editing only the name leaves on-hand untouched (no spurious adjustment).
	if _, err := s.UpdateProduct(p.ID, store.ProductDraft{Name: "Rice 1kg", PriceCents: 18000, Taxable: false, TrackStock: true, Stock: 20}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := stockOf(t, s, p.ID); got != 20 {
		t.Errorf("after rename, stock = %d, want 20 unchanged", got)
	}
}

func TestStockUntrackedStaysZero(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "Loose", PriceCents: 5000, TrackStock: false})
	if _, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 9}}, PaidCents: 45000}); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if got := stockOf(t, s, p.ID); got != 0 {
		t.Errorf("untracked stock = %d, want 0", got)
	}
}
