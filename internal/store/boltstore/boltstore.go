// Package boltstore is the shipping local store: a pure-Go, single-file, ACID
// bbolt database. No cgo, tiny footprint, conservative about the Go version,
// which keeps the Windows 7 build trivial. Sales never depend on a network
// connection.
//
// It implements store.Local. Alongside the primary buckets it maintains two
// exact-match secondary indexes so the common lookups stay O(1) and feed the
// later search and sync work: barcode -> product id, and a reference index over
// sales keyed by "<reference>\x00<sale id>" for prefix scans by M-Pesa code.
package boltstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

var (
	bProducts   = []byte("products")
	bSales      = []byte("sales")
	bSettings   = []byte("settings")
	bBlobs      = []byte("blobs")       // binary data kept out of the settings JSON
	bBarcodeIdx = []byte("barcode_idx") // barcode -> product id (active products)
	bRefIdx     = []byte("ref_idx")     // "<reference>\x00<sale id>" -> "" (prefix scan)
)

const logoKey = "logo"

// sep separates a reference from a sale id in the reference index key. A NUL
// byte never appears in an M-Pesa code, so the prefix scan is unambiguous.
const sep = 0x00

// Store is the bbolt-backed implementation of store.Local. now is injectable so
// the day-boundary analytics and UpdatedAt stamps are deterministic in tests.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// compile-time assertion that Store satisfies the interface callers depend on.
var _ store.Local = (*Store)(nil)

// Open creates or opens the database at path and ensures the buckets exist.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// WithClock overrides the time source (tests). It returns the store for chaining.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

// Close releases the database file.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{bProducts, bSales, bSettings, bBlobs, bBarcodeIdx, bRefIdx, bAudit, bStockEvents}
		buckets = append(buckets, syncBuckets...)
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- Products ---

// Products returns active products sorted by name, each with its on-hand stock
// derived from its stock events minus what its sales sold.
func (s *Store) Products() ([]store.Product, error) {
	out := []store.Product{}
	err := s.db.View(func(tx *bolt.Tx) error {
		delta, sold, err := stockTotals(tx)
		if err != nil {
			return err
		}
		return tx.Bucket(bProducts).ForEach(func(_, v []byte) error {
			var p store.Product
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			if p.Active {
				p.Stock = derivedStock(p, delta, sold)
				out = append(out, p)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// derivedStock returns a product's on-hand from the precomputed totals: events
// minus sold for tracked items, and zero for untracked ones (their count is not
// shown).
func derivedStock(p store.Product, delta, sold map[string]int) int {
	if !p.TrackStock {
		return 0
	}
	return max0(delta[p.ID] - sold[p.ID])
}

// AddProduct stores a new product and returns it with its assigned UUID. When a
// barcode is given it must not already be in use, so a scan maps to exactly one
// product.
func (s *Store) AddProduct(d store.ProductDraft) (store.Product, error) {
	return s.addProduct(d, true)
}

// addProduct stores a new product, marking it for the next sync push unless it
// is a seed item. The default starter catalogue is never pushed, so linking a
// device never uploads example items.
func (s *Store) addProduct(d store.ProductDraft, pending bool) (store.Product, error) {
	now := s.now()
	p := store.Product{
		ID:         uuid.NewString(),
		Name:       d.Name,
		PriceCents: d.PriceCents,
		Barcode:    d.Barcode,
		TrackStock: d.TrackStock,
		Stock:      d.Stock,
		Taxable:    d.Taxable,
		Active:     true,
		UpdatedAt:  now,
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		idx := tx.Bucket(bBarcodeIdx)
		if d.Barcode != "" && idx.Get([]byte(d.Barcode)) != nil {
			return store.ErrBarcodeTaken
		}
		if err := putJSON(tx.Bucket(bProducts), []byte(p.ID), p); err != nil {
			return err
		}
		if d.Barcode != "" {
			if err := idx.Put([]byte(d.Barcode), []byte(p.ID)); err != nil {
				return err
			}
		}
		if pending {
			// Seed items pass pending=false and are not audited; only real,
			// owner-added items become activity-log lines.
			if err := appendAudit(tx, now, "Item added", p.Name+", "+auditPrice(p.PriceCents)); err != nil {
				return err
			}
			// On-hand is derived from stock events, so a tracked item starts with
			// an initial event rather than a stored counter.
			if d.TrackStock && d.Stock != 0 {
				ev := store.StockEvent{
					ID:        uuid.NewString(),
					ProductID: p.ID,
					Delta:     d.Stock,
					Reason:    store.StockInitial,
					CreatedAt: now,
				}
				if err := appendStockEvent(tx, ev); err != nil {
					return err
				}
			}
			return markProductPending(tx, p.ID)
		}
		return nil
	})
	return p, err
}

// UpdateProduct edits an existing product in place and returns the new value,
// keeping the barcode index in step with any barcode change.
func (s *Store) UpdateProduct(id string, d store.ProductDraft) (store.Product, error) {
	var p store.Product
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bProducts)
		raw := pb.Get([]byte(id))
		if raw == nil {
			return store.ErrNotFound
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		// Capture the prior fields so the activity log can show a price change, the
		// classic thing an audit needs to answer, and the prior on-hand so an edit
		// to the stock count becomes a single correcting event.
		oldName, oldPrice, oldBarcode, oldTaxable := p.Name, p.PriceCents, p.Barcode, p.Taxable
		beforeOnHand, err := onHandOf(tx, id)
		if err != nil {
			return err
		}
		idx := tx.Bucket(bBarcodeIdx)
		if d.Barcode != "" {
			if cur := idx.Get([]byte(d.Barcode)); cur != nil && string(cur) != id {
				return store.ErrBarcodeTaken
			}
		}
		// Re-point the index if the barcode changed.
		if p.Barcode != d.Barcode && p.Barcode != "" {
			if err := idx.Delete([]byte(p.Barcode)); err != nil {
				return err
			}
		}
		p.Name = d.Name
		p.PriceCents = d.PriceCents
		p.Barcode = d.Barcode
		p.TrackStock = d.TrackStock
		p.Stock = d.Stock
		p.Taxable = d.Taxable
		p.UpdatedAt = s.now()
		if err := putJSON(pb, []byte(id), p); err != nil {
			return err
		}
		if d.Barcode != "" {
			if err := idx.Put([]byte(d.Barcode), []byte(id)); err != nil {
				return err
			}
		}
		// Log a product edit only when something the owner sees actually changed,
		// so editing the stock count alone does not also write a no-op "Item
		// updated" line; the stock change is logged on its own below.
		if oldName != d.Name || oldPrice != d.PriceCents || oldBarcode != d.Barcode || oldTaxable != d.Taxable {
			detail := d.Name
			if oldPrice != d.PriceCents {
				detail = d.Name + ", price " + auditPrice(oldPrice) + " to " + auditPrice(d.PriceCents)
			} else if oldName != d.Name {
				detail = oldName + " renamed to " + d.Name
			}
			if err := appendAudit(tx, p.UpdatedAt, "Item updated", detail); err != nil {
				return err
			}
		}
		if err := markProductPending(tx, id); err != nil {
			return err
		}

		// Reconcile on-hand to the entered count by appending one correcting event
		// for the difference. Editing the absolute count is a manual correction;
		// the merge-safe relative path for routine restocking is Restock.
		if d.TrackStock {
			if delta := d.Stock - beforeOnHand; delta != 0 {
				ev := store.StockEvent{
					ID:        uuid.NewString(),
					ProductID: id,
					Delta:     delta,
					Reason:    store.StockAdjustment,
					CreatedAt: p.UpdatedAt,
				}
				if err := appendStockEvent(tx, ev); err != nil {
					return err
				}
				if err := appendAudit(tx, p.UpdatedAt, "Stock changed", d.Name+", set to "+strconv.Itoa(d.Stock)); err != nil {
					return err
				}
			}
			p.Stock = d.Stock
		} else {
			p.Stock = 0
		}
		return nil
	})
	return p, err
}

// DeleteProduct soft deletes a product: it stays as a tombstone (Active false,
// DeletedAt set) so the deletion can sync and past sales keep their snapshot. Its
// barcode is freed from the index so it can be reused immediately.
func (s *Store) DeleteProduct(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bProducts)
		raw := pb.Get([]byte(id))
		if raw == nil {
			return store.ErrNotFound
		}
		var p store.Product
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.Barcode != "" {
			if err := tx.Bucket(bBarcodeIdx).Delete([]byte(p.Barcode)); err != nil {
				return err
			}
		}
		now := s.now()
		p.Active = false
		p.DeletedAt = &now
		p.UpdatedAt = now
		if err := putJSON(pb, []byte(id), p); err != nil {
			return err
		}
		if err := appendAudit(tx, now, "Item removed", p.Name); err != nil {
			return err
		}
		return markProductPending(tx, id)
	})
}

// ProductByBarcode resolves a scanned barcode to its active product via the
// exact-match index.
func (s *Store) ProductByBarcode(code string) (store.Product, bool, error) {
	var p store.Product
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(bBarcodeIdx).Get([]byte(code))
		if id == nil {
			return nil
		}
		raw := tx.Bucket(bProducts).Get(id)
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		found = p.Active
		if found && p.TrackStock {
			oh, err := onHandOf(tx, p.ID)
			if err != nil {
				return err
			}
			p.Stock = max0(oh)
		}
		return nil
	})
	if !found {
		return store.Product{}, false, err
	}
	return p, true, err
}

// --- Sales ---

// CreateSale validates the requested lines against current products, computes
// the total and change, persists the sale, and returns it. Prices and names are
// snapshotted from the products at this moment. Tax is not applied yet (a later
// phase); the tax fields are written as zero so reprints and sync are ready for
// it. The reference, when present, is added to the exact-match index.
func (s *Store) CreateSale(in store.SaleInput) (store.Sale, error) {
	var sale store.Sale
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bProducts)
		lines := make([]store.SaleItem, 0, len(in.Lines))
		for _, it := range in.Lines {
			if it.Qty <= 0 {
				return fmt.Errorf("invalid quantity")
			}
			raw := pb.Get([]byte(it.ProductID))
			if raw == nil {
				return fmt.Errorf("unknown product %q", it.ProductID)
			}
			var p store.Product
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			lines = append(lines, store.SaleItem{
				ProductID:  p.ID,
				Name:       p.Name,
				PriceCents: p.PriceCents,
				Qty:        it.Qty,
				Taxable:    p.Taxable,
			})
			// Stock is not decremented here: on-hand is derived from the sales
			// themselves (this sale included) minus the product's stock events, so
			// a sale never writes a counter that another till could clobber.
		}
		if len(lines) == 0 {
			return fmt.Errorf("empty sale")
		}
		// Apply the shop's tax settings as they stand now, and snapshot them onto
		// the sale so a later reprint is faithful even if the rate changes.
		rateBps, mode := taxFromBucket(tx)
		subtotal, tax, total := store.ComputeTax(rateBps, mode, lines)
		if in.PaidCents < total {
			return fmt.Errorf("payment is less than the total")
		}

		method := in.PaymentMethod
		if method == "" {
			method = store.PaymentCash
		}
		sale = store.Sale{
			ID:            uuid.NewString(),
			CreatedAt:     s.now(),
			Items:         lines,
			SubtotalCents: subtotal,
			TaxCents:      tax,
			TotalCents:    total,
			PaidCents:     in.PaidCents,
			ChangeCents:   in.PaidCents - total,
			PaymentMethod: method,
			Reference:     in.Reference,
			TaxRateBps:    rateBps,
			TaxMode:       mode,
		}
		if err := putJSON(tx.Bucket(bSales), []byte(sale.ID), sale); err != nil {
			return err
		}
		if sale.Reference != "" {
			if err := tx.Bucket(bRefIdx).Put(refKey(sale.Reference, sale.ID), nil); err != nil {
				return err
			}
		}
		return markSalePending(tx, sale.ID)
	})
	return sale, err
}

// SalesSince returns sales created at or after t, newest first.
func (s *Store) SalesSince(t time.Time) ([]store.Sale, error) {
	out := []store.Sale{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSales).ForEach(func(_, v []byte) error {
			var sale store.Sale
			if err := json.Unmarshal(v, &sale); err != nil {
				return err
			}
			if !sale.CreatedAt.Before(t) {
				out = append(out, sale)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, err
}

// SaleByID looks up a single sale. The bool is false when no sale has that id.
func (s *Store) SaleByID(id string) (store.Sale, bool, error) {
	var sale store.Sale
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bSales).Get([]byte(id))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &sale)
	})
	if !found {
		return store.Sale{}, false, err
	}
	return sale, true, err
}

// SalesByReference returns every sale whose reference exactly matches ref,
// newest first, by scanning the "<reference>\x00" prefix in the index.
func (s *Store) SalesByReference(ref string) ([]store.Sale, error) {
	out := []store.Sale{}
	if ref == "" {
		return out, nil
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := append([]byte(ref), sep)
		sb := tx.Bucket(bSales)
		c := tx.Bucket(bRefIdx).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			id := k[len(prefix):]
			raw := sb.Get(id)
			if raw == nil {
				continue
			}
			var sale store.Sale
			if err := json.Unmarshal(raw, &sale); err != nil {
				return err
			}
			out = append(out, sale)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, err
}

// --- Analytics ---

// startOfDay returns midnight at the start of t, in t's own location.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Analytics aggregates the last seven days of sales for the dashboard.
func (s *Store) Analytics() (store.Analytics, error) {
	now := s.now()
	todayStart := startOfDay(now)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	windowStart := todayStart.AddDate(0, 0, -6) // seven days including today

	days := make([]store.DayBucket, 7)
	dayIndex := map[string]int{}
	for i := 0; i < 7; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		days[i] = store.DayBucket{Date: key, Label: d.Format("Mon")}
		dayIndex[key] = i
	}
	hours := make([]store.HourBucket, 24)
	for h := 0; h < 24; h++ {
		hours[h] = store.HourBucket{Hour: h}
	}

	a := store.Analytics{Days: days, Hours: hours}
	prodAgg := map[string]*store.ProductStat{}

	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSales).ForEach(func(_, v []byte) error {
			var sale store.Sale
			if err := json.Unmarshal(v, &sale); err != nil {
				return err
			}
			if sale.CreatedAt.Before(windowStart) {
				return nil
			}
			items := 0
			for _, it := range sale.Items {
				items += it.Qty
				ps := prodAgg[it.Name]
				if ps == nil {
					ps = &store.ProductStat{Name: it.Name}
					prodAgg[it.Name] = ps
				}
				ps.Qty += it.Qty
				ps.RevenueCents += it.PriceCents * int64(it.Qty)
			}

			if di, ok := dayIndex[sale.CreatedAt.Format("2006-01-02")]; ok {
				a.Days[di].TotalCents += sale.TotalCents
			}
			a.Hours[sale.CreatedAt.Hour()].TotalCents += sale.TotalCents

			switch {
			case !sale.CreatedAt.Before(todayStart):
				a.Today.TotalCents += sale.TotalCents
				a.Today.SaleCount++
				a.Today.ItemCount += items
			case !sale.CreatedAt.Before(yesterdayStart):
				a.Yesterday.TotalCents += sale.TotalCents
				a.Yesterday.SaleCount++
				a.Yesterday.ItemCount += items
			}
			return nil
		})
	})
	if err != nil {
		return store.Analytics{}, err
	}

	ranked := make([]store.ProductStat, 0, len(prodAgg))
	for _, ps := range prodAgg {
		ranked = append(ranked, *ps)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].RevenueCents != ranked[j].RevenueCents {
			return ranked[i].RevenueCents > ranked[j].RevenueCents
		}
		return ranked[i].Name < ranked[j].Name
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	a.TopProducts = ranked
	return a, nil
}

// --- Settings ---

// Settings returns all settings merged over sensible defaults.
func (s *Store) Settings() (map[string]string, error) {
	out := store.Defaults()
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSettings).ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	return out, err
}

// SetSetting writes one setting value. When the key is one that syncs, it stamps
// the settings change time so the next push carries a last-write-wins clock.
func (s *Store) SetSetting(key, value string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bSettings)
		// Only log a setting that actually changed, so re-saving a panel with the
		// same values does not fill the activity log with noise.
		changed := string(sb.Get([]byte(key))) != value
		if err := sb.Put([]byte(key), []byte(value)); err != nil {
			return err
		}
		if changed {
			action, detail := settingAudit(key, value)
			if err := appendAudit(tx, s.now(), action, detail); err != nil {
				return err
			}
		}
		if syncableSettingKey(key) {
			return s.markSettingsPending(tx, s.now())
		}
		return nil
	})
}

// syncableSettingKey reports whether a setting change should advance the sync
// clock. Per-device or logo-derived keys do not sync, so they do not.
func syncableSettingKey(key string) bool {
	for _, k := range syncSettingKeys {
		if k == key {
			return true
		}
	}
	return false
}

// --- Logo (binary) ---

// SetLogo stores the shop logo image bytes and flags its presence.
func (s *Store) SetLogo(data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bBlobs).Put([]byte(logoKey), data); err != nil {
			return err
		}
		return tx.Bucket(bSettings).Put([]byte(store.KeyHasLogo), []byte("yes"))
	})
}

// Logo returns the stored logo image bytes, or ok=false when none is set.
func (s *Store) Logo() ([]byte, bool, error) {
	var out []byte
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bBlobs).Get([]byte(logoKey))
		if raw == nil {
			return nil
		}
		found = true
		out = make([]byte, len(raw)) // copy: bbolt bytes are only valid in the txn
		copy(out, raw)
		return nil
	})
	return out, found, err
}

// DeleteLogo removes the stored logo.
func (s *Store) DeleteLogo() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bBlobs).Delete([]byte(logoKey)); err != nil {
			return err
		}
		return tx.Bucket(bSettings).Put([]byte(store.KeyHasLogo), []byte("no"))
	})
}

// --- Seeding ---

// SeedIfEmpty adds a small starter catalogue the first time the shop runs, so
// the sell screen is usable immediately. Owners edit or extend it later.
func (s *Store) SeedIfEmpty() error {
	products, err := s.Products()
	if err != nil {
		return err
	}
	if len(products) > 0 {
		return nil
	}
	starter := []struct {
		name  string
		cents int64
	}{
		{"Bread", 6500},
		{"Milk 500ml", 6000},
		{"Sugar 1kg", 20000},
		{"Soda 500ml", 8000},
		{"Cooking Oil 1L", 35000},
		{"Rice 1kg", 18000},
		{"Soap bar", 5000},
		{"Eggs (tray)", 38000},
	}
	for _, p := range starter {
		// Seed items are never pushed, so linking a device does not upload the
		// example catalogue to the shop.
		if _, err := s.addProduct(store.ProductDraft{Name: p.name, PriceCents: p.cents, Taxable: true}, false); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ---

// putJSON marshals v and writes it under key.
func putJSON(b *bolt.Bucket, key []byte, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put(key, buf)
}

// taxFromBucket reads the shop's tax rate (basis points) and mode straight from
// the settings bucket inside the caller's transaction. A missing or unparseable
// rate is zero and a missing mode is "none", so an unconfigured shop is untaxed.
func taxFromBucket(tx *bolt.Tx) (rateBps int, mode string) {
	sb := tx.Bucket(bSettings)
	if v := sb.Get([]byte(store.KeyTaxRateBps)); v != nil {
		rateBps, _ = strconv.Atoi(string(v))
	}
	mode = store.TaxModeNone
	if v := sb.Get([]byte(store.KeyTaxMode)); v != nil && len(v) > 0 {
		mode = string(v)
	}
	return rateBps, mode
}

// refKey builds the reference-index key "<reference>\x00<sale id>".
func refKey(ref, saleID string) []byte {
	k := make([]byte, 0, len(ref)+1+len(saleID))
	k = append(k, ref...)
	k = append(k, sep)
	k = append(k, saleID...)
	return k
}
