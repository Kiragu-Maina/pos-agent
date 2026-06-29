package boltstore

import (
	"encoding/json"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

// defaultSearchCap bounds an unbounded query so a caller that sets no Limit can
// never pull the whole table into memory. An explicit c.Limit overrides it.
const defaultSearchCap = 500

// SearchSales returns the sales matching every set field of c, newest first.
//
// The candidate set is index-backed when a reference is given: it starts from
// SalesByReference (an exact-match index scan) and filters that small set down.
// Otherwise it scans the sales bucket once, unmarshalling and testing each row.
// All set criteria combine with AND. The result is sorted newest first and
// capped to c.Limit, or to defaultSearchCap when no limit is given. The slice is
// always non-nil.
func (s *Store) SearchSales(c store.SearchCriteria) ([]store.Sale, error) {
	out := []store.Sale{}

	if c.Reference != "" {
		// Index-backed path: SalesByReference already returns newest-first via
		// the exact-match reference index. Apply the remaining filters to it.
		candidates, err := s.SalesByReference(c.Reference)
		if err != nil {
			return out, err
		}
		for _, sale := range candidates {
			if matchesCriteria(sale, c) {
				out = append(out, sale)
			}
		}
	} else {
		// Scan path: a single pass over the sales bucket.
		err := s.db.View(func(tx *bolt.Tx) error {
			return tx.Bucket(bSales).ForEach(func(_, v []byte) error {
				var sale store.Sale
				if err := json.Unmarshal(v, &sale); err != nil {
					return err
				}
				if matchesCriteria(sale, c) {
					out = append(out, sale)
				}
				return nil
			})
		})
		if err != nil {
			return []store.Sale{}, err
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	limit := c.Limit
	if limit <= 0 {
		limit = defaultSearchCap
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// matchesCriteria reports whether a sale satisfies every set field of c. A zero
// value on a field means "no bound on this dimension". Reference is not tested
// here: when set, the caller already narrowed candidates to the exact-match
// index, so the dimension is satisfied by construction.
func matchesCriteria(sale store.Sale, c store.SearchCriteria) bool {
	if c.ItemName != "" {
		q := strings.ToLower(c.ItemName)
		found := false
		for _, item := range sale.Items {
			if strings.Contains(strings.ToLower(item.Name), q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if c.PaymentMethod != "" && sale.PaymentMethod != c.PaymentMethod {
		return false
	}
	if !c.Start.IsZero() && sale.CreatedAt.Before(c.Start) {
		return false
	}
	if !c.End.IsZero() && !sale.CreatedAt.Before(c.End) {
		return false
	}
	if c.MinCents > 0 && sale.TotalCents < c.MinCents {
		return false
	}
	if c.MaxCents > 0 && sale.TotalCents > c.MaxCents {
		return false
	}
	return true
}
