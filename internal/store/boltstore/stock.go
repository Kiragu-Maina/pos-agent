package boltstore

import (
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

// bStockEvents is the append-only log of on-hand changes (initial counts,
// restocks, corrections). A product's on-hand is derived from these plus what its
// sales sold, never stored as a counter, so two offline tills reconcile by adding
// their events up instead of overwriting a shared number.
var bStockEvents = []byte("stock_events")

// appendStockEvent stores one stock event inside the caller's transaction and
// marks it for the next sync push, the same append-only path sales take.
func appendStockEvent(tx *bolt.Tx, ev store.StockEvent) error {
	if err := putJSON(tx.Bucket(bStockEvents), []byte(ev.ID), ev); err != nil {
		return err
	}
	return markStockEventPending(tx, ev.ID)
}

// stockTotals returns, for every product, the sum of its stock-event deltas and
// the quantity its sales sold. On-hand is delta minus sold. It is the one-pass
// form used when reading the whole catalogue.
func stockTotals(tx *bolt.Tx) (delta, sold map[string]int, err error) {
	delta = map[string]int{}
	sold = map[string]int{}
	err = tx.Bucket(bStockEvents).ForEach(func(_, v []byte) error {
		var ev store.StockEvent
		if err := json.Unmarshal(v, &ev); err != nil {
			return err
		}
		delta[ev.ProductID] += ev.Delta
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	err = tx.Bucket(bSales).ForEach(func(_, v []byte) error {
		var sale store.Sale
		if err := json.Unmarshal(v, &sale); err != nil {
			return err
		}
		for _, it := range sale.Items {
			sold[it.ProductID] += it.Qty
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return delta, sold, nil
}

// max0 floors a value at zero, for displaying on-hand: the true signed sum is
// kept internally (so corrections and restocks stay merge-safe), but a shop never
// shows negative stock.
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// onHandOf computes a single product's true (signed) on-hand within a
// transaction. It is not floored: callers that display it floor with max0, while
// stock-correction maths needs the real value so an oversold item corrects right.
func onHandOf(tx *bolt.Tx, productID string) (int, error) {
	total := 0
	err := tx.Bucket(bStockEvents).ForEach(func(_, v []byte) error {
		var ev store.StockEvent
		if err := json.Unmarshal(v, &ev); err != nil {
			return err
		}
		if ev.ProductID == productID {
			total += ev.Delta
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	err = tx.Bucket(bSales).ForEach(func(_, v []byte) error {
		var sale store.Sale
		if err := json.Unmarshal(v, &sale); err != nil {
			return err
		}
		for _, it := range sale.Items {
			if it.ProductID == productID {
				total -= it.Qty
			}
		}
		return nil
	})
	return total, err
}

// Restock appends a stock event changing a product's on-hand by qty (negative to
// remove) and returns the product with its new derived on-hand.
func (s *Store) Restock(productID string, qty int) (store.Product, error) {
	var p store.Product
	if qty == 0 {
		// Nothing to record; still return the current product so the UI refreshes.
		err := s.db.View(func(tx *bolt.Tx) error {
			raw := tx.Bucket(bProducts).Get([]byte(productID))
			if raw == nil {
				return store.ErrNotFound
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			oh, err := onHandOf(tx, productID)
			if err != nil {
				return err
			}
			p.Stock = max0(oh)
			return nil
		})
		return p, err
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bProducts).Get([]byte(productID))
		if raw == nil {
			return store.ErrNotFound
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		now := s.now()
		ev := store.StockEvent{
			ID:        uuid.NewString(),
			ProductID: productID,
			Delta:     qty,
			Reason:    store.StockRestock,
			CreatedAt: now,
		}
		if err := appendStockEvent(tx, ev); err != nil {
			return err
		}
		oh, err := onHandOf(tx, productID)
		if err != nil {
			return err
		}
		verb := "added " + strconv.Itoa(qty)
		if qty < 0 {
			verb = "removed " + strconv.Itoa(-qty)
		}
		if err := appendAudit(tx, now, "Stock changed", p.Name+", "+verb+" (now "+strconv.Itoa(max0(oh))+")"); err != nil {
			return err
		}
		p.Stock = max0(oh)
		return nil
	})
	return p, err
}
