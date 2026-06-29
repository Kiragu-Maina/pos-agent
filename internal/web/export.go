package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"pos-system/internal/store"
)

// exportTable is a renderer-agnostic view of an export: a heading, column
// headers, and rows of already-formatted strings. numeric marks the columns that
// hold money so each renderer can treat them correctly (right-aligned in the PDF,
// real numbers in the spreadsheet, formula-guarded only in CSV). Building the data
// once and rendering it three ways keeps the formats in lockstep.
type exportTable struct {
	title   string   // heading shown in the PDF and used as the sheet name
	base    string   // download file base name, e.g. "transactions"
	headers []string // column headings
	numeric []bool   // per-column: true for money columns
	rows    [][]string
}

// handleSalesExport streams completed sales in a date range, for the shop's own
// books, as CSV, Excel (xlsx), or PDF (?format=csv|xlsx|pdf, default csv). from/to
// accept a plain "2006-01-02" date or an RFC3339 timestamp; a plain to date covers
// its whole day. With both blank, every sale is exported. Because sales sync in
// from other devices, this is the full shop ledger, not just sales rung on this
// till.
func (s *Server) handleSalesExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	from, to := exportRange(r)
	sales, err := s.db.SalesSince(from) // a zero from means all sales
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not gather your sales.")
		return
	}
	// Keep only those before the upper bound, then order oldest first so the file
	// reads as a ledger.
	rows := sales[:0]
	for _, sale := range sales {
		if to.IsZero() || sale.CreatedAt.Before(to) {
			rows = append(rows, sale)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })

	t := exportTable{
		title:   "Transactions",
		base:    "transactions",
		headers: []string{"Date", "Time", "Items", "Subtotal", "Tax", "Total", "Payment", "Reference"},
		numeric: []bool{false, false, false, true, true, true, false, false},
	}
	for _, sale := range rows {
		st := sale.CreatedAt.Local()
		t.rows = append(t.rows, []string{
			st.Format("2006-01-02"),
			st.Format("15:04"),
			itemsSummary(sale.Items),
			decimalCents(sale.SubtotalCents),
			decimalCents(sale.TaxCents),
			decimalCents(sale.TotalCents),
			paymentLabel(sale.PaymentMethod),
			sale.Reference,
		})
	}
	writeExport(w, r, t)
}

// handleAuditExport streams the device's activity log in a date range: the items
// added, edited, and removed and the settings changed on this device. Same
// ?format=csv|xlsx|pdf choice as the transactions export.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	from, to := exportRange(r)
	entries, err := s.db.Audit(from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not gather your activity log.")
		return
	}
	t := exportTable{
		title:   "Activity log",
		base:    "activity-log",
		headers: []string{"Date", "Time", "Action", "Detail"},
		numeric: []bool{false, false, false, false},
	}
	for _, e := range entries {
		et := e.At.Local()
		t.rows = append(t.rows, []string{
			et.Format("2006-01-02"),
			et.Format("15:04"),
			e.Action,
			e.Detail,
		})
	}
	writeExport(w, r, t)
}

// writeExport renders the table in the format named by ?format= (csv by default)
// and sets the headers that make a browser download it as a dated file.
func writeExport(w http.ResponseWriter, r *http.Request, t exportTable) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	stamp := time.Now().Format("2006-01-02")
	switch format {
	case "xlsx":
		attach(w, t.base+"-"+stamp+".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		_ = writeXLSX(w, t)
	case "pdf":
		attach(w, t.base+"-"+stamp+".pdf", "application/pdf")
		_ = writePDF(w, t)
	default: // "csv" or anything unrecognised
		attach(w, t.base+"-"+stamp+".csv", "text/csv; charset=utf-8")
		writeCSV(w, t)
	}
}

// attach sets the download headers for a named file of the given content type.
func attach(w http.ResponseWriter, filename, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
}

// writeCSV renders the table as CSV, guarding free-text (non-numeric) cells
// against spreadsheet formula injection.
func writeCSV(w http.ResponseWriter, t exportTable) {
	cw := csv.NewWriter(w)
	_ = cw.Write(t.headers)
	for _, row := range t.rows {
		out := make([]string, len(row))
		for i, cell := range row {
			if i < len(t.numeric) && t.numeric[i] {
				out[i] = cell
			} else {
				out[i] = csvSafe(cell)
			}
		}
		_ = cw.Write(out)
	}
	cw.Flush()
}

// exportRange reads the from/to query parameters with the same lenient parsing as
// search: a plain date or an RFC3339 timestamp, with a plain to date covering its
// whole day. An unset or unreadable bound is left zero (unbounded).
func exportRange(r *http.Request) (from, to time.Time) {
	q := r.URL.Query()
	if t, ok := parseSearchTime(q.Get("from"), false); ok {
		from = t
	}
	if t, ok := parseSearchTime(q.Get("to"), true); ok {
		to = t
	}
	return from, to
}

// itemsSummary renders a sale's lines as "Bread x2; Milk x1" for one cell.
func itemsSummary(items []store.SaleItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s x%d", it.Name, it.Qty))
	}
	return strings.Join(parts, "; ")
}

// paymentLabel renders a payment method for a person reading the export.
func paymentLabel(method string) string {
	switch method {
	case store.PaymentMpesa:
		return "M-Pesa"
	case store.PaymentCash, "":
		return "Cash"
	default:
		return method
	}
}

// decimalCents renders integer cents as a fixed two-decimal string (6500 ->
// "65.00") so spreadsheets read the column as money without a currency prefix.
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

// csvSafe defuses spreadsheet formula injection: a cell that a spreadsheet would
// treat as a formula (it starts with =, +, -, or @) is prefixed with an
// apostrophe so it is shown as plain text instead of being executed.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@':
		return "'" + v
	}
	return v
}
