package store

// ComputeTax applies a shop's tax settings to a sale's snapshot lines. It sets
// each line's TaxCents in place and returns the sale-level subtotal, tax, and
// total. The invariant holds: tax == sum of every line's TaxCents.
//
// rateBps is the rate in basis points (1600 = 16.00%). Only lines with
// Taxable true are taxed. All maths is integer cents, rounded half up per line
// so the printed per-line figures sum exactly to the sale tax (no rounding
// drift between the receipt body and its total).
//
// Modes:
//   - none (or rateBps <= 0): no tax. subtotal == total == gross, tax == 0.
//   - exclusive: tax is added on top. subtotal == gross, total == gross + tax.
//   - inclusive: the price already contains the tax. subtotal == total == gross
//     and tax is the embedded portion, reported for the receipt but not added.
func ComputeTax(rateBps int, mode string, items []SaleItem) (subtotal, tax, total int64) {
	for i := range items {
		gross := items[i].PriceCents * int64(items[i].Qty)
		subtotal += gross

		if rateBps <= 0 || mode == TaxModeNone || mode == "" || !items[i].Taxable {
			items[i].TaxCents = 0
			continue
		}
		switch mode {
		case TaxModeExclusive:
			items[i].TaxCents = roundHalfUp(gross*int64(rateBps), 10000)
		case TaxModeInclusive:
			div := int64(10000 + rateBps)
			items[i].TaxCents = roundHalfUp(gross*int64(rateBps), div)
		default:
			items[i].TaxCents = 0
		}
		tax += items[i].TaxCents
	}

	if mode == TaxModeExclusive && rateBps > 0 {
		total = subtotal + tax
	} else {
		// none and inclusive both leave the gross total unchanged; for inclusive
		// the tax is informational and already inside the price.
		total = subtotal
	}
	return subtotal, tax, total
}

// roundHalfUp divides n by d, rounding a half up to the next integer. Both are
// non-negative here (money), so the simple (n + d/2) / d is exact.
func roundHalfUp(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	return (n + d/2) / d
}
