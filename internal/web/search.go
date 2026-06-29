package web

import (
	"encoding/json"
	"net/http"
	"time"

	"pos-system/internal/store"
)

// searchRequest is the JSON body POST /api/sales/search accepts. Every field is
// optional and combines with AND. start and end accept either an RFC3339
// timestamp or a plain "2006-01-02" date; a plain start date means start-of-day
// and a plain end date covers the whole day (the exclusive End bound is pushed to
// the next midnight). Unparseable optional fields are ignored rather than failing
// the request.
type searchRequest struct {
	Reference     string `json:"reference"`
	ItemName      string `json:"itemName"`
	PaymentMethod string `json:"paymentMethod"`
	Start         string `json:"start"`
	End           string `json:"end"`
	MinCents      int64  `json:"minCents"`
	MaxCents      int64  `json:"maxCents"`
	Limit         int    `json:"limit"`
}

// handleSalesSearch serves POST /api/sales/search: it parses the criteria, runs
// the store search, and returns the matching sales newest first.
func (s *Server) handleSalesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}

	var body searchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read the search.")
		return
	}

	c := store.SearchCriteria{
		Reference:     body.Reference,
		ItemName:      body.ItemName,
		PaymentMethod: body.PaymentMethod,
		MinCents:      body.MinCents,
		MaxCents:      body.MaxCents,
		Limit:         body.Limit,
	}
	// Parse dates leniently: ignore anything we cannot read rather than erroring
	// the whole request. A plain end date covers its whole day (exclusive bound
	// at the next midnight).
	if t, ok := parseSearchTime(body.Start, false); ok {
		c.Start = t
	}
	if t, ok := parseSearchTime(body.End, true); ok {
		c.End = t
	}

	sales, err := s.db.SearchSales(c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not run the search.")
		return
	}

	resp := map[string]any{"sales": sales}
	// Surface truncation so the client can hint there may be more matches.
	limit := c.Limit
	if limit <= 0 {
		limit = defaultSearchCap
	}
	if len(sales) == limit {
		resp["truncated"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// defaultSearchCap mirrors the store's default cap so the handler can report
// truncation when no explicit limit was given.
const defaultSearchCap = 500

// parseSearchTime reads either an RFC3339 timestamp or a plain "2006-01-02"
// date. For a plain date, a start bound is start-of-day and an end bound is the
// following midnight so the whole day is included under the exclusive End. An
// empty or unparseable value returns ok=false.
func parseSearchTime(v string, endOfDay bool) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if d, err := time.Parse("2006-01-02", v); err == nil {
		if endOfDay {
			return d.AddDate(0, 0, 1), true
		}
		return d, true
	}
	return time.Time{}, false
}
