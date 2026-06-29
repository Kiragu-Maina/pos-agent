package boltstore

import (
	"path/filepath"
	"testing"

	"pos-system/internal/store"
)

// openTaxStore opens a fresh store and turns on exclusive 16% VAT.
func openTaxStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "tax.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetSetting(store.KeyTaxRateBps, "1600"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	if err := s.SetSetting(store.KeyTaxMode, store.TaxModeExclusive); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	return s
}

func TestCreateSaleAppliesExclusiveTax(t *testing.T) {
	s := openTaxStore(t)
	p, err := s.AddProduct(store.ProductDraft{Name: "Soap", PriceCents: 10000, Taxable: true})
	if err != nil {
		t.Fatalf("add product: %v", err)
	}

	sale, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 1}},
		PaidCents: 11600,
	})
	if err != nil {
		t.Fatalf("create sale: %v", err)
	}
	if sale.SubtotalCents != 10000 || sale.TaxCents != 1600 || sale.TotalCents != 11600 {
		t.Fatalf("got sub=%d tax=%d total=%d, want 10000/1600/11600",
			sale.SubtotalCents, sale.TaxCents, sale.TotalCents)
	}
	if sale.ChangeCents != 0 {
		t.Fatalf("change: got %d, want 0", sale.ChangeCents)
	}
	// The rate and mode are snapshotted so a reprint stays faithful.
	if sale.TaxRateBps != 1600 || sale.TaxMode != store.TaxModeExclusive {
		t.Fatalf("snapshot: got rate=%d mode=%q, want 1600/exclusive", sale.TaxRateBps, sale.TaxMode)
	}
	if sale.Items[0].TaxCents != 1600 {
		t.Fatalf("line tax: got %d, want 1600", sale.Items[0].TaxCents)
	}
}

func TestCreateSaleRejectsPayingOnlyTheSubtotal(t *testing.T) {
	s := openTaxStore(t)
	p, err := s.AddProduct(store.ProductDraft{Name: "Soap", PriceCents: 10000, Taxable: true})
	if err != nil {
		t.Fatalf("add product: %v", err)
	}
	// Paying the pre-tax subtotal must be rejected: the total is 11600 with VAT.
	if _, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 1}},
		PaidCents: 10000,
	}); err == nil {
		t.Fatal("expected payment-too-low error when paying only the subtotal")
	}
}

func TestCreateSaleExemptProductIsUntaxed(t *testing.T) {
	s := openTaxStore(t)
	p, err := s.AddProduct(store.ProductDraft{Name: "Maize flour", PriceCents: 10000, Taxable: false})
	if err != nil {
		t.Fatalf("add product: %v", err)
	}
	sale, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: p.ID, Qty: 1}},
		PaidCents: 10000,
	})
	if err != nil {
		t.Fatalf("create sale: %v", err)
	}
	if sale.TaxCents != 0 || sale.TotalCents != 10000 {
		t.Fatalf("exempt: got tax=%d total=%d, want 0/10000", sale.TaxCents, sale.TotalCents)
	}
}
