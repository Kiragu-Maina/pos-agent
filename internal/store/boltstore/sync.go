package boltstore

import (
	"encoding/json"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

var (
	bSyncState    = []byte("sync_state")      // link state + cursor + settings-dirty marker
	bPendingProd  = []byte("pending_prods")   // product ids awaiting push
	bPendingSale  = []byte("pending_sales")   // sale ids awaiting push
	bPendingStock = []byte("pending_stockev") // stock-event ids awaiting push
)

// syncBuckets are created alongside the primary buckets in init.
var syncBuckets = [][]byte{bSyncState, bPendingProd, bPendingSale, bPendingStock}

const (
	kLinked      = "linked"
	kBaseURL     = "base_url"
	kShopID      = "shop_id"
	kEmail       = "email"
	kToken       = "token"
	kCursor      = "cursor"
	kLastSync    = "last_sync"
	kLastError   = "last_error"
	kSettingsDue = "settings_updated_at" // set when settings change, cleared on push
)

// markProductPending records a product id for the next push. Called inside the
// caller's write transaction so the mark commits atomically with the change.
func markProductPending(tx *bolt.Tx, id string) error {
	return tx.Bucket(bPendingProd).Put([]byte(id), nil)
}

func markSalePending(tx *bolt.Tx, id string) error {
	return tx.Bucket(bPendingSale).Put([]byte(id), nil)
}

func markStockEventPending(tx *bolt.Tx, id string) error {
	return tx.Bucket(bPendingStock).Put([]byte(id), nil)
}

// markSettingsPending stamps the settings change time so a later push carries a
// last-write-wins clock for the shop settings.
func (s *Store) markSettingsPending(tx *bolt.Tx, at time.Time) error {
	return tx.Bucket(bSyncState).Put([]byte(kSettingsDue), []byte(at.UTC().Format(time.RFC3339Nano)))
}

// --- Sync state ---

// SyncState returns the device's link to a cloud shop (empty when unlinked).
func (s *Store) SyncState() (store.SyncState, error) {
	var st store.SyncState
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSyncState)
		st.Linked = string(b.Get([]byte(kLinked))) == "yes"
		st.BaseURL = string(b.Get([]byte(kBaseURL)))
		st.ShopID = string(b.Get([]byte(kShopID)))
		st.Email = string(b.Get([]byte(kEmail)))
		st.Token = string(b.Get([]byte(kToken)))
		st.Cursor, _ = strconv.ParseInt(string(b.Get([]byte(kCursor))), 10, 64)
		if v := b.Get([]byte(kLastSync)); len(v) > 0 {
			st.LastSync, _ = time.Parse(time.RFC3339Nano, string(v))
		}
		st.LastError = string(b.Get([]byte(kLastError)))
		return nil
	})
	return st, err
}

// Link records a successful cloud login and resets the cursor for a full first
// sync. It does not clear pending items, so everything the device created while
// standalone is uploaded on the first push.
func (s *Store) Link(baseURL, shopID, email, token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSyncState)
		put := func(k, v string) error { return b.Put([]byte(k), []byte(v)) }
		if err := put(kLinked, "yes"); err != nil {
			return err
		}
		if err := put(kBaseURL, baseURL); err != nil {
			return err
		}
		if err := put(kShopID, shopID); err != nil {
			return err
		}
		if err := put(kEmail, email); err != nil {
			return err
		}
		if err := put(kToken, token); err != nil {
			return err
		}
		return b.Put([]byte(kLastError), nil)
	})
}

// Unlink forgets the cloud link. Pending items are left intact so re-linking the
// same shop resumes cleanly.
func (s *Store) Unlink() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSyncState)
		for _, k := range []string{kLinked, kBaseURL, kShopID, kEmail, kToken, kCursor, kLastError} {
			if err := b.Put([]byte(k), nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetToken refreshes the stored session token (after a silent re-login).
func (s *Store) SetToken(token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bSyncState).Put([]byte(kToken), []byte(token))
	})
}

// SetCursor records the highest server sequence pulled.
func (s *Store) SetCursor(cursor int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bSyncState).Put([]byte(kCursor), []byte(strconv.FormatInt(cursor, 10)))
	})
}

// SetSyncResult records the outcome of a sync attempt for display.
func (s *Store) SetSyncResult(at time.Time, errMsg string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSyncState)
		if err := b.Put([]byte(kLastSync), []byte(at.UTC().Format(time.RFC3339Nano))); err != nil {
			return err
		}
		return b.Put([]byte(kLastError), []byte(errMsg))
	})
}

// --- Outbox ---

// PendingProducts returns the products awaiting push (current value by id).
func (s *Store) PendingProducts() ([]store.Product, error) {
	out := []store.Product{}
	err := s.db.View(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bProducts)
		return tx.Bucket(bPendingProd).ForEach(func(id, _ []byte) error {
			raw := pb.Get(id)
			if raw == nil {
				return nil // product gone; the pending mark is cleared after push
			}
			var p store.Product
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			out = append(out, p)
			return nil
		})
	})
	return out, err
}

// PendingSales returns the sales awaiting push, with their items.
func (s *Store) PendingSales() ([]store.Sale, error) {
	out := []store.Sale{}
	err := s.db.View(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bSales)
		return tx.Bucket(bPendingSale).ForEach(func(id, _ []byte) error {
			raw := sb.Get(id)
			if raw == nil {
				return nil
			}
			var sale store.Sale
			if err := json.Unmarshal(raw, &sale); err != nil {
				return err
			}
			out = append(out, sale)
			return nil
		})
	})
	return out, err
}

// PendingStockEvents returns the stock events awaiting push.
func (s *Store) PendingStockEvents() ([]store.StockEvent, error) {
	out := []store.StockEvent{}
	err := s.db.View(func(tx *bolt.Tx) error {
		eb := tx.Bucket(bStockEvents)
		return tx.Bucket(bPendingStock).ForEach(func(id, _ []byte) error {
			raw := eb.Get(id)
			if raw == nil {
				return nil
			}
			var ev store.StockEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				return err
			}
			out = append(out, ev)
			return nil
		})
	})
	return out, err
}

// PendingSettings returns the shop settings to push and the change time, or
// ok=false when settings are not dirty. The per-device printer address and the
// logo flag are excluded; they never sync.
func (s *Store) PendingSettings() (map[string]string, time.Time, bool, error) {
	var (
		at time.Time
		ok bool
		m  map[string]string
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bSyncState).Get([]byte(kSettingsDue))
		if len(v) == 0 {
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, string(v))
		if err != nil {
			return nil
		}
		at, ok = t, true
		all := store.Defaults()
		_ = tx.Bucket(bSettings).ForEach(func(k, val []byte) error {
			all[string(k)] = string(val)
			return nil
		})
		m = map[string]string{}
		for _, k := range syncSettingKeys {
			m[k] = all[k]
		}
		return nil
	})
	return m, at, ok, err
}

// ClearPushed clears the outbox marks for the ids that pushed successfully and,
// when the settings were pushed at settingsAt, clears the settings-dirty marker
// only if it has not advanced since.
func (s *Store) ClearPushed(productIDs, saleIDs, stockEventIDs []string, settingsAt time.Time, settingsPushed bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pp := tx.Bucket(bPendingProd)
		for _, id := range productIDs {
			if err := pp.Delete([]byte(id)); err != nil {
				return err
			}
		}
		ps := tx.Bucket(bPendingSale)
		for _, id := range saleIDs {
			if err := ps.Delete([]byte(id)); err != nil {
				return err
			}
		}
		pe := tx.Bucket(bPendingStock)
		for _, id := range stockEventIDs {
			if err := pe.Delete([]byte(id)); err != nil {
				return err
			}
		}
		if settingsPushed {
			b := tx.Bucket(bSyncState)
			cur := b.Get([]byte(kSettingsDue))
			if t, err := time.Parse(time.RFC3339Nano, string(cur)); err == nil && !t.After(settingsAt) {
				return b.Put([]byte(kSettingsDue), nil)
			}
		}
		return nil
	})
}

// --- Applying pulled changes ---

// ApplyPulled merges changes pulled from the cloud into the local store. It
// never marks the merged rows pending, so applying a pull does not create an
// echo push. Products are last-write-wins on UpdatedAt; sales are inserted if
// absent (append-only); settings apply when the pulled change is newer.
func (s *Store) ApplyPulled(products []store.Product, sales []store.Sale, stockEvents []store.StockEvent, settings map[string]string, settingsAt *time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, p := range products {
			if err := applyPulledProduct(tx, p); err != nil {
				return err
			}
		}
		for _, sale := range sales {
			if err := applyPulledSale(tx, sale); err != nil {
				return err
			}
		}
		for _, ev := range stockEvents {
			if err := applyPulledStockEvent(tx, ev); err != nil {
				return err
			}
		}
		if settings != nil && settingsAt != nil {
			if err := applyPulledSettings(tx, settings, *settingsAt); err != nil {
				return err
			}
		}
		return nil
	})
}

func applyPulledProduct(tx *bolt.Tx, p store.Product) error {
	pb := tx.Bucket(bProducts)
	idx := tx.Bucket(bBarcodeIdx)
	var existing store.Product
	have := false
	if raw := pb.Get([]byte(p.ID)); raw != nil {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return err
		}
		have = true
		if !p.UpdatedAt.After(existing.UpdatedAt) {
			return nil // local is newer or equal: keep it
		}
	}
	// Re-point the barcode index off the old value.
	if have && existing.Barcode != "" {
		if err := idx.Delete([]byte(existing.Barcode)); err != nil {
			return err
		}
	}
	if err := putJSON(pb, []byte(p.ID), p); err != nil {
		return err
	}
	if p.Active && p.Barcode != "" {
		return idx.Put([]byte(p.Barcode), []byte(p.ID))
	}
	return nil
}

func applyPulledSale(tx *bolt.Tx, sale store.Sale) error {
	sb := tx.Bucket(bSales)
	if sb.Get([]byte(sale.ID)) != nil {
		return nil // already have it (append-only)
	}
	if err := putJSON(sb, []byte(sale.ID), sale); err != nil {
		return err
	}
	if sale.Reference != "" {
		return tx.Bucket(bRefIdx).Put(refKey(sale.Reference, sale.ID), nil)
	}
	return nil
}

// applyPulledStockEvent inserts a stock event pulled from another device if we do
// not already have it (append-only). It is never marked pending, so applying a
// pull does not echo back, and on-hand is recomputed from the merged events.
func applyPulledStockEvent(tx *bolt.Tx, ev store.StockEvent) error {
	eb := tx.Bucket(bStockEvents)
	if eb.Get([]byte(ev.ID)) != nil {
		return nil // already have it
	}
	return putJSON(eb, []byte(ev.ID), ev)
}

func applyPulledSettings(tx *bolt.Tx, settings map[string]string, at time.Time) error {
	b := tx.Bucket(bSyncState)
	// Only apply when newer than our local settings change time.
	if cur := b.Get([]byte(kSettingsDue)); len(cur) > 0 {
		if local, err := time.Parse(time.RFC3339Nano, string(cur)); err == nil && local.After(at) {
			return nil
		}
	}
	sb := tx.Bucket(bSettings)
	for _, k := range syncSettingKeys {
		if v, ok := settings[k]; ok {
			if err := sb.Put([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncSettingKeys are the shop-level settings that sync. printer_addr is
// per-device and has_logo depends on the (unsynced) logo blob, so both are
// excluded.
var syncSettingKeys = []string{
	store.KeyShopName, store.KeyPaperWidth, store.KeyScanner, store.KeyReceiptTheme,
	store.KeyHeaderLine, store.KeyFooter, store.KeyTaxRateBps, store.KeyTaxMode,
}
