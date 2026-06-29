package boltstore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"pos-system/internal/store"
)

// bAudit is an append-only activity log of changes made on this device. Keys are
// a monotonic bucket sequence (big-endian) so entries iterate in the order they
// were recorded, independent of any clock; the timestamp used for date filtering
// lives in the stored value.
var bAudit = []byte("audit")

// appendAudit writes one activity-log entry inside the caller's transaction, so
// the entry commits atomically with the change it describes. It is best effort by
// design at the call sites: a change is never blocked by its own audit line.
func appendAudit(tx *bolt.Tx, at time.Time, action, detail string) error {
	b := tx.Bucket(bAudit)
	seq, err := b.NextSequence()
	if err != nil {
		return err
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, seq)
	return putJSON(b, key, store.AuditEntry{
		ID:     uuid.NewString(),
		At:     at,
		Action: action,
		Detail: detail,
	})
}

// Audit returns activity-log entries with At in [from, to), oldest first, which
// reads naturally as a ledger. A zero bound is unbounded on that end.
func (s *Store) Audit(from, to time.Time) ([]store.AuditEntry, error) {
	out := []store.AuditEntry{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bAudit).ForEach(func(_, v []byte) error {
			var e store.AuditEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			if !from.IsZero() && e.At.Before(from) {
				return nil
			}
			if !to.IsZero() && !e.At.Before(to) {
				return nil
			}
			out = append(out, e)
			return nil
		})
	})
	// The sequence key already yields insertion order; sort by time defensively so
	// the export is chronological even if the clock moved between entries.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, err
}

// auditPrice renders cents as a plain "KSh 65.00" for an audit line.
func auditPrice(cents int64) string {
	return "KSh " + decimalCents(cents)
}

// decimalCents renders integer cents as a fixed two-decimal string, for example
// 6500 -> "65.00". It avoids floating point so money never drifts.
func decimalCents(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	s := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	if neg {
		s = "-" + s
	}
	return s
}

// settingAudit builds a human activity line for a saved setting. Known keys get a
// friendly label and value (the tax rate as a percentage); unknown keys fall back
// to the raw key so nothing is silently dropped from the log.
func settingAudit(key, value string) (action, detail string) {
	label := settingLabels[key]
	if label == "" {
		label = key
	}
	switch {
	case key == store.KeyTaxRateBps:
		n, _ := strconv.Atoi(value)
		detail = label + " set to " + formatBps(n)
	case value == "":
		detail = label + " cleared"
	default:
		detail = label + " set to " + value
	}
	return "Setting changed", detail
}

// settingLabels gives saved settings a plain name for the activity log.
var settingLabels = map[string]string{
	store.KeyShopName:     "Shop name",
	store.KeyPaperWidth:   "Receipt paper size",
	store.KeyPrinterAddr:  "Printer address",
	store.KeyScanner:      "Barcode scanner",
	store.KeyReceiptTheme: "Receipt style",
	store.KeyHeaderLine:   "Receipt header line",
	store.KeyFooter:       "Receipt ending message",
	store.KeyTaxRateBps:   "Tax rate",
	store.KeyTaxMode:      "Tax mode",
}

// formatBps renders basis points as a trimmed percentage, for example 1600 ->
// "16%" and 1650 -> "16.5%".
func formatBps(bps int) string {
	whole := bps / 100
	frac := bps % 100
	switch {
	case frac == 0:
		return strconv.Itoa(whole) + "%"
	case frac%10 == 0:
		return fmt.Sprintf("%d.%d%%", whole, frac/10)
	default:
		return fmt.Sprintf("%d.%02d%%", whole, frac)
	}
}
