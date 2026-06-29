package boltstore

import (
	"path/filepath"
	"testing"
	"time"

	"pos-system/internal/store"
)

// openAuditStore opens a fresh store on a fixed clock so audit timestamps are
// deterministic.
func openAuditStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.WithClock(func() time.Time { return now })
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAuditRecordsChangesNotSeeds(t *testing.T) {
	at := time.Date(2026, time.June, 28, 9, 0, 0, 0, time.UTC)
	s := openAuditStore(t, at)

	// The seed catalogue must not appear in the activity log.
	if err := s.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if entries, _ := s.Audit(time.Time{}, time.Time{}); len(entries) != 0 {
		t.Fatalf("seeding wrote %d audit entries, want 0", len(entries))
	}

	// A real add, a price edit, a setting change, and a delete each log a line.
	p, err := s.AddProduct(store.ProductDraft{Name: "Bread", PriceCents: 6000, Taxable: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.UpdateProduct(p.ID, store.ProductDraft{Name: "Bread", PriceCents: 6500, Taxable: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.SetSetting(store.KeyTaxRateBps, "1600"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if err := s.DeleteProduct(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	entries, err := s.Audit(time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	want := []struct{ action, detail string }{
		{"Item added", "Bread, KSh 60.00"},
		{"Item updated", "Bread, price KSh 60.00 to KSh 65.00"},
		{"Setting changed", "Tax rate set to 16%"},
		{"Item removed", "Bread"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d audit entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].Action != w.action || entries[i].Detail != w.detail {
			t.Errorf("entry %d = {%q, %q}, want {%q, %q}", i, entries[i].Action, entries[i].Detail, w.action, w.detail)
		}
		if !entries[i].At.Equal(at) {
			t.Errorf("entry %d time = %v, want %v", i, entries[i].At, at)
		}
	}
}

func TestAuditUnchangedSettingNotLogged(t *testing.T) {
	at := time.Date(2026, time.June, 28, 9, 0, 0, 0, time.UTC)
	s := openAuditStore(t, at)

	if err := s.SetSetting(store.KeyShopName, "Duka"); err != nil {
		t.Fatalf("set 1: %v", err)
	}
	// Saving the same value again must not add a second line.
	if err := s.SetSetting(store.KeyShopName, "Duka"); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	entries, _ := s.Audit(time.Time{}, time.Time{})
	if len(entries) != 1 {
		t.Fatalf("re-saving an unchanged setting logged %d entries, want 1", len(entries))
	}
}

func TestAuditDateRangeFilter(t *testing.T) {
	day1 := time.Date(2026, time.June, 27, 10, 0, 0, 0, time.UTC)
	s := openAuditStore(t, day1)
	if _, err := s.AddProduct(store.ProductDraft{Name: "Old", PriceCents: 100}); err != nil {
		t.Fatalf("add day1: %v", err)
	}
	day2 := time.Date(2026, time.June, 28, 10, 0, 0, 0, time.UTC)
	s.WithClock(func() time.Time { return day2 })
	if _, err := s.AddProduct(store.ProductDraft{Name: "New", PriceCents: 200}); err != nil {
		t.Fatalf("add day2: %v", err)
	}

	// A window covering only day 2 returns just that entry.
	from := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)
	entries, err := s.Audit(from, to)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(entries) != 1 || entries[0].Detail != "New, KSh 2.00" {
		t.Fatalf("date-filtered audit = %+v, want only the day-2 entry", entries)
	}
}
