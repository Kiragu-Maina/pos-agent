package boltstore

import (
	"testing"
	"time"

	"pos-system/internal/store"
)

// idsOf collects the ids of a sales slice for order-sensitive assertions.
func idsOf(sales []store.Sale) []string {
	out := make([]string, len(sales))
	for i, s := range sales {
		out[i] = s.ID
	}
	return out
}

// containsID reports whether any sale in the slice has the given id.
func containsID(sales []store.Sale, id string) bool {
	for _, s := range sales {
		if s.ID == id {
			return true
		}
	}
	return false
}

// --- SearchSales ---

func TestSearchByReferenceUsesIndex(t *testing.T) {
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
	other := mk("DIFFERENT")

	got, err := s.SearchSales(store.SearchCriteria{Reference: "QGH7ABC"})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SearchSales(ref) len = %d, want 2", len(got))
	}
	// Newest first via the reference index ordering.
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("order = %v, want newest-first [%s,%s]", idsOf(got), second.ID, first.ID)
	}
	// The unrelated reference is excluded: index path returns only the exact ref.
	if containsID(got, other.ID) {
		t.Errorf("SearchSales(ref) leaked an unrelated reference")
	}

	// Partial/prefix reference must not match (exact index).
	if r, _ := s.SearchSales(store.SearchCriteria{Reference: "QGH7"}); len(r) != 0 {
		t.Errorf("SearchSales(partial ref) len = %d, want 0", len(r))
	}
}

func TestSearchByItemNameSubstringCaseInsensitive(t *testing.T) {
	s, _ := newStore(t)
	bread := addProduct(t, s, store.ProductDraft{Name: "White Bread", PriceCents: 100})
	milk := addProduct(t, s, store.ProductDraft{Name: "Fresh Milk", PriceCents: 200})

	breadSale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: bread.ID, Qty: 1}}, PaidCents: 100})
	if err != nil {
		t.Fatalf("CreateSale bread: %v", err)
	}
	milkSale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: milk.ID, Qty: 1}}, PaidCents: 200})
	if err != nil {
		t.Fatalf("CreateSale milk: %v", err)
	}
	// A mixed sale matches either term.
	mixed, err := s.CreateSale(store.SaleInput{
		Lines:     []store.SaleLine{{ProductID: bread.ID, Qty: 1}, {ProductID: milk.ID, Qty: 1}},
		PaidCents: 300,
	})
	if err != nil {
		t.Fatalf("CreateSale mixed: %v", err)
	}

	// Case-insensitive substring on any line item.
	got, err := s.SearchSales(store.SearchCriteria{ItemName: "bread"})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 2 || !containsID(got, breadSale.ID) || !containsID(got, mixed.ID) {
		t.Errorf("ItemName=bread matched %v, want breadSale+mixed", idsOf(got))
	}
	if containsID(got, milkSale.ID) {
		t.Errorf("ItemName=bread leaked milk-only sale")
	}

	// Uppercase query still matches.
	if r, _ := s.SearchSales(store.SearchCriteria{ItemName: "MILK"}); len(r) != 2 {
		t.Errorf("ItemName=MILK len = %d, want 2 (case-insensitive)", len(r))
	}
}

func TestSearchByPaymentMethod(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	cash, _ := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100, PaymentMethod: store.PaymentCash})
	mpesa, _ := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100, PaymentMethod: store.PaymentMpesa, Reference: "X1"})

	got, err := s.SearchSales(store.SearchCriteria{PaymentMethod: store.PaymentMpesa})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 1 || got[0].ID != mpesa.ID {
		t.Errorf("PaymentMethod=mpesa matched %v, want [%s]", idsOf(got), mpesa.ID)
	}
	if containsID(got, cash.ID) {
		t.Errorf("mpesa filter leaked a cash sale")
	}
}

func TestSearchByDateRange(t *testing.T) {
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

	start := baseTime
	atStart := mkAt(start)               // exactly at Start -> included (inclusive)
	middle := mkAt(start.Add(time.Hour)) // inside
	atEnd := mkAt(start.Add(2 * time.Hour))
	before := mkAt(start.Add(-time.Hour))

	// [start, start+2h): includes atStart and middle, excludes atEnd (exclusive)
	// and before.
	got, err := s.SearchSales(store.SearchCriteria{Start: start, End: start.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 2 || !containsID(got, atStart.ID) || !containsID(got, middle.ID) {
		t.Errorf("date range matched %v, want atStart+middle", idsOf(got))
	}
	if containsID(got, atEnd.ID) {
		t.Errorf("End is exclusive but matched the sale exactly at End")
	}
	if containsID(got, before.ID) {
		t.Errorf("matched a sale before Start")
	}

	// Start only (open-ended upper bound) keeps everything from Start on.
	if r, _ := s.SearchSales(store.SearchCriteria{Start: start}); len(r) != 3 {
		t.Errorf("Start-only len = %d, want 3", len(r))
	}
	// End only keeps everything strictly before End.
	if r, _ := s.SearchSales(store.SearchCriteria{End: start}); len(r) != 1 || r[0].ID != before.ID {
		t.Errorf("End-only matched %v, want [%s]", idsOf(r), before.ID)
	}
}

func TestSearchByAmountRange(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	mkQty := func(qty int) store.Sale {
		sale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: qty}}, PaidCents: int64(qty) * 100})
		if err != nil {
			t.Fatalf("CreateSale: %v", err)
		}
		return sale
	}

	s1 := mkQty(1) // 100
	s2 := mkQty(2) // 200
	s3 := mkQty(3) // 300

	// Inclusive bounds: [200, 300] keeps s2 and s3.
	got, err := s.SearchSales(store.SearchCriteria{MinCents: 200, MaxCents: 300})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 2 || !containsID(got, s2.ID) || !containsID(got, s3.ID) {
		t.Errorf("amount range matched %v, want s2+s3", idsOf(got))
	}
	if containsID(got, s1.ID) {
		t.Errorf("amount range leaked the below-min sale")
	}

	// Min only.
	if r, _ := s.SearchSales(store.SearchCriteria{MinCents: 300}); len(r) != 1 || r[0].ID != s3.ID {
		t.Errorf("MinCents=300 matched %v, want [%s]", idsOf(r), s3.ID)
	}
	// Max only.
	if r, _ := s.SearchSales(store.SearchCriteria{MaxCents: 100}); len(r) != 1 || r[0].ID != s1.ID {
		t.Errorf("MaxCents=100 matched %v, want [%s]", idsOf(r), s1.ID)
	}
}

func TestSearchCombinedCriteriaAND(t *testing.T) {
	s, c := newStore(t)
	bread := addProduct(t, s, store.ProductDraft{Name: "Bread", PriceCents: 100})
	milk := addProduct(t, s, store.ProductDraft{Name: "Milk", PriceCents: 100})

	c.set(baseTime)
	// Target: bread, mpesa, in window, amount in range.
	target, err := s.CreateSale(store.SaleInput{
		Lines:         []store.SaleLine{{ProductID: bread.ID, Qty: 3}}, // 300
		PaidCents:     300,
		PaymentMethod: store.PaymentMpesa,
		Reference:     "REF1",
	})
	if err != nil {
		t.Fatalf("CreateSale target: %v", err)
	}
	// Decoys, each failing exactly one criterion.
	c.add(time.Minute)
	_, _ = s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: milk.ID, Qty: 3}}, PaidCents: 300, PaymentMethod: store.PaymentMpesa})  // wrong item
	_, _ = s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: bread.ID, Qty: 3}}, PaidCents: 300, PaymentMethod: store.PaymentCash})  // wrong method
	_, _ = s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: bread.ID, Qty: 1}}, PaidCents: 100, PaymentMethod: store.PaymentMpesa}) // below min

	got, err := s.SearchSales(store.SearchCriteria{
		ItemName:      "bread",
		PaymentMethod: store.PaymentMpesa,
		Start:         baseTime,
		End:           baseTime.Add(time.Hour),
		MinCents:      200,
		MaxCents:      400,
	})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Errorf("combined AND matched %v, want only [%s]", idsOf(got), target.ID)
	}
}

func TestSearchCombinedWithReferenceIndexPath(t *testing.T) {
	s, _ := newStore(t)
	bread := addProduct(t, s, store.ProductDraft{Name: "Bread", PriceCents: 100})
	milk := addProduct(t, s, store.ProductDraft{Name: "Milk", PriceCents: 100})

	// Two sales share a reference but differ by item; the AND of reference and
	// item name must narrow the index candidates.
	breadRef, _ := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: bread.ID, Qty: 1}}, PaidCents: 100, Reference: "SAME"})
	_, _ = s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: milk.ID, Qty: 1}}, PaidCents: 100, Reference: "SAME"})

	got, err := s.SearchSales(store.SearchCriteria{Reference: "SAME", ItemName: "bread"})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 1 || got[0].ID != breadRef.ID {
		t.Errorf("reference+item matched %v, want [%s]", idsOf(got), breadRef.ID)
	}
}

func TestSearchEmptyCriteriaReturnsAllNewestFirst(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	var sales []store.Sale
	for i := 0; i < 3; i++ {
		c.add(time.Minute)
		sale, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100})
		if err != nil {
			t.Fatalf("CreateSale: %v", err)
		}
		sales = append(sales, sale)
	}

	got, err := s.SearchSales(store.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("empty criteria len = %d, want 3", len(got))
	}
	// Newest first: reverse of creation order.
	if got[0].ID != sales[2].ID || got[1].ID != sales[1].ID || got[2].ID != sales[0].ID {
		t.Errorf("order = %v, want newest-first", idsOf(got))
	}
}

func TestSearchAlwaysNonNil(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.SearchSales(store.SearchCriteria{ItemName: "nothing matches"})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if got == nil {
		t.Errorf("SearchSales returned a nil slice, want non-nil empty")
	}
	if len(got) != 0 {
		t.Errorf("no-match search len = %d, want 0", len(got))
	}
}

func TestSearchLimitCaps(t *testing.T) {
	s, c := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	var sales []store.Sale
	for i := 0; i < 5; i++ {
		c.add(time.Minute)
		sale, _ := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100})
		sales = append(sales, sale)
	}

	got, err := s.SearchSales(store.SearchCriteria{Limit: 2})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Limit=2 len = %d, want 2", len(got))
	}
	// The cap keeps the newest ones.
	if got[0].ID != sales[4].ID || got[1].ID != sales[3].ID {
		t.Errorf("Limit kept %v, want the two newest", idsOf(got))
	}
}

func TestSearchDefaultCap(t *testing.T) {
	s, _ := newStore(t)
	p := addProduct(t, s, store.ProductDraft{Name: "P", PriceCents: 100})

	// Create more than the default cap so an unbounded query is capped.
	for i := 0; i < defaultSearchCap+10; i++ {
		if _, err := s.CreateSale(store.SaleInput{Lines: []store.SaleLine{{ProductID: p.ID, Qty: 1}}, PaidCents: 100}); err != nil {
			t.Fatalf("CreateSale %d: %v", i, err)
		}
	}

	got, err := s.SearchSales(store.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(got) != defaultSearchCap {
		t.Errorf("unbounded search len = %d, want default cap %d", len(got), defaultSearchCap)
	}

	// An explicit limit above the default is honoured beyond the default cap.
	all, err := s.SearchSales(store.SearchCriteria{Limit: defaultSearchCap + 100})
	if err != nil {
		t.Fatalf("SearchSales: %v", err)
	}
	if len(all) != defaultSearchCap+10 {
		t.Errorf("explicit high limit len = %d, want %d", len(all), defaultSearchCap+10)
	}
}
