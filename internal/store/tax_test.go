package store

import "testing"

// sumLineTax is the invariant the receipt depends on: the sale tax equals the
// sum of the per-line tax snapshots.
func sumLineTax(items []SaleItem) int64 {
	var t int64
	for _, it := range items {
		t += it.TaxCents
	}
	return t
}

func TestComputeTaxNone(t *testing.T) {
	items := []SaleItem{
		{PriceCents: 6500, Qty: 2, Taxable: true},
		{PriceCents: 35000, Qty: 1, Taxable: true},
	}
	sub, tax, total := ComputeTax(0, TaxModeNone, items)
	if sub != 48000 || tax != 0 || total != 48000 {
		t.Fatalf("none: got sub=%d tax=%d total=%d, want 48000/0/48000", sub, tax, total)
	}
	if sumLineTax(items) != 0 {
		t.Fatalf("none: per-line tax should be zero, got %d", sumLineTax(items))
	}
}

func TestComputeTaxZeroRateIsUntaxed(t *testing.T) {
	items := []SaleItem{{PriceCents: 10000, Qty: 1, Taxable: true}}
	// A zero rate, even in exclusive mode, must leave the sale untaxed and the
	// total unchanged so a shop that has not set a rate is never charged tax.
	sub, tax, total := ComputeTax(0, TaxModeExclusive, items)
	if sub != 10000 || tax != 0 || total != 10000 {
		t.Fatalf("zero rate: got sub=%d tax=%d total=%d, want 10000/0/10000", sub, tax, total)
	}
}

func TestComputeTaxExclusive(t *testing.T) {
	items := []SaleItem{
		{PriceCents: 10000, Qty: 1, Taxable: true},
		{PriceCents: 5000, Qty: 2, Taxable: true},
	}
	// 16% exclusive on 20000 = 3200 added on top.
	sub, tax, total := ComputeTax(1600, TaxModeExclusive, items)
	if sub != 20000 || tax != 3200 || total != 23200 {
		t.Fatalf("exclusive: got sub=%d tax=%d total=%d, want 20000/3200/23200", sub, tax, total)
	}
	if sumLineTax(items) != tax {
		t.Fatalf("exclusive: line tax sum %d != sale tax %d", sumLineTax(items), tax)
	}
}

func TestComputeTaxInclusive(t *testing.T) {
	items := []SaleItem{{PriceCents: 11600, Qty: 1, Taxable: true}}
	// 16% inclusive: the 11600 price already contains the tax. Embedded tax is
	// 11600 * 1600 / 11600 = 1600; the total is unchanged.
	sub, tax, total := ComputeTax(1600, TaxModeInclusive, items)
	if sub != 11600 || tax != 1600 || total != 11600 {
		t.Fatalf("inclusive: got sub=%d tax=%d total=%d, want 11600/1600/11600", sub, tax, total)
	}
	if sumLineTax(items) != tax {
		t.Fatalf("inclusive: line tax sum %d != sale tax %d", sumLineTax(items), tax)
	}
}

func TestComputeTaxMixedTaxableAndExempt(t *testing.T) {
	items := []SaleItem{
		{PriceCents: 10000, Qty: 1, Taxable: true},  // taxed
		{PriceCents: 10000, Qty: 1, Taxable: false}, // exempt (e.g. unprocessed food)
	}
	sub, tax, total := ComputeTax(1600, TaxModeExclusive, items)
	if sub != 20000 || tax != 1600 || total != 21600 {
		t.Fatalf("mixed: got sub=%d tax=%d total=%d, want 20000/1600/21600", sub, tax, total)
	}
	if items[0].TaxCents != 1600 || items[1].TaxCents != 0 {
		t.Fatalf("mixed: per-line tax got %d/%d, want 1600/0", items[0].TaxCents, items[1].TaxCents)
	}
}

func TestComputeTaxRoundsHalfUp(t *testing.T) {
	// 16% of 9999 = 1599.84, which must round to 1600, not truncate to 1599.
	items := []SaleItem{{PriceCents: 9999, Qty: 1, Taxable: true}}
	_, tax, total := ComputeTax(1600, TaxModeExclusive, items)
	if tax != 1600 {
		t.Fatalf("rounding: got tax=%d, want 1600 (half rounds up)", tax)
	}
	if total != 11599 {
		t.Fatalf("rounding: got total=%d, want 11599", total)
	}
}

func TestComputeTaxFractionalRate(t *testing.T) {
	// 16.5% exclusive on 10000 = 1650.
	items := []SaleItem{{PriceCents: 10000, Qty: 1, Taxable: true}}
	_, tax, _ := ComputeTax(1650, TaxModeExclusive, items)
	if tax != 1650 {
		t.Fatalf("fractional rate: got tax=%d, want 1650", tax)
	}
}

func TestComputeTaxPerLineSumsToTotalUnderRounding(t *testing.T) {
	// Several odd lines: the per-line rounded tax must still sum exactly to the
	// returned sale tax (the receipt body and its total can never disagree).
	items := []SaleItem{
		{PriceCents: 333, Qty: 1, Taxable: true},
		{PriceCents: 777, Qty: 3, Taxable: true},
		{PriceCents: 101, Qty: 7, Taxable: true},
	}
	_, tax, _ := ComputeTax(1600, TaxModeExclusive, items)
	if got := sumLineTax(items); got != tax {
		t.Fatalf("per-line sum %d != sale tax %d", got, tax)
	}
}
