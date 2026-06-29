// Package web serves the local POS user interface and the small JSON API the
// browser uses to drive the agent. The UI is embedded into the binary so a
// deployed machine has nothing loose to lose.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pos-system/internal/printer"
	"pos-system/internal/scan"
	"pos-system/internal/store"
)

//go:embed ui
var uiFiles embed.FS

// Server wires the embedded UI and the JSON API onto one HTTP mux.
type Server struct {
	mux      *http.ServeMux
	db       store.Local
	sync     Syncer
	cloudURL string
}

// New builds the server and its routes around the given store. It accepts the
// store.Local interface so the storage engine (bbolt today, SQLite later) is
// interchangeable. sync may be nil, in which case the agent runs purely local
// and the sync endpoints report that cloud sync is unavailable. cloudURL is the
// default cloud address offered in the link form, empty when none is configured.
func New(db store.Local, sync Syncer, cloudURL string) (*Server, error) {
	uiFS, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}

	s := &Server{mux: http.NewServeMux(), db: db, sync: sync, cloudURL: cloudURL}
	s.mux.Handle("/", http.FileServer(http.FS(uiFS)))

	// Capabilities: the same UI runs here (local) and on the hosted cloud. The
	// local agent has the printer, scanner, device sync, search, restock, and
	// exports, so every feature is on.
	s.mux.HandleFunc("/api/config", s.handleConfig)

	// Printer setup.
	s.mux.HandleFunc("/api/scan", s.handleScan)
	s.mux.HandleFunc("/api/test-print", s.handleTestPrint)
	s.mux.HandleFunc("/api/receipt/preview", s.handleReceiptPreview)
	s.mux.HandleFunc("/api/receipt/test", s.handleReceiptTest)
	s.mux.HandleFunc("/api/logo", s.handleLogo)
	s.mux.HandleFunc("/api/logo/delete", s.handleLogoDelete)

	// Catalogue, settings, and selling.
	s.mux.HandleFunc("/api/products", s.handleProducts)
	s.mux.HandleFunc("/api/products/update", s.handleProductUpdate)
	s.mux.HandleFunc("/api/products/delete", s.handleProductDelete)
	s.mux.HandleFunc("/api/products/restock", s.handleProductRestock)
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/sales", s.handleSales)
	s.mux.HandleFunc("/api/sales/today", s.handleSalesToday)
	s.mux.HandleFunc("/api/sales/search", s.handleSalesSearch)
	s.mux.HandleFunc("/api/sales/export", s.handleSalesExport)
	s.mux.HandleFunc("/api/audit/export", s.handleAuditExport)

	// Cloud sync (optional).
	s.mux.HandleFunc("/api/sync/status", s.handleSyncStatus)
	s.mux.HandleFunc("/api/sync/link", s.handleSyncLink)
	s.mux.HandleFunc("/api/sync/now", s.handleSyncNow)
	s.mux.HandleFunc("/api/sync/unlink", s.handleSyncUnlink)
	s.mux.HandleFunc("/api/analytics", s.handleAnalytics)
	s.mux.HandleFunc("/api/reprint", s.handleReprint)
	return s, nil
}

// Handler exposes the mux for http.Server.
func (s *Server) Handler() http.Handler { return s.mux }

// --- Printer setup ---

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	devices, err := scan.Scan(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not search the network.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) handleTestPrint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var body struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Addr == "" {
		writeError(w, http.StatusBadRequest, "No printer was selected.")
		return
	}
	if err := printer.TestPrint(body.Addr); err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach the printer. Please check that it is on.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// receiptOverrides lets a preview show unsaved edits. A nil pointer means "use
// the saved value".
type receiptOverrides struct {
	theme  string
	header *string
	footer *string
}

// maxLogoBytes caps an uploaded logo so the database stays small.
const maxLogoBytes = 2 << 20 // 2 MB

func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, ok, err := s.db.Logo()
		if err != nil || !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	case http.MethodPost:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogoBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "That image is too big. Please use one under 2 MB.")
			return
		}
		if !printer.ValidImage(data) {
			writeError(w, http.StatusBadRequest, "That file is not a picture we can use. Please upload a PNG or JPG.")
			return
		}
		if err := s.db.SetLogo(data); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not save the logo.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}

func (s *Server) handleLogoDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	if err := s.db.DeleteLogo(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not remove the logo.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sampleReceipt builds a representative sale for previewing or test printing.
// Any override that is set replaces the saved value, so the preview can reflect
// edits before they are saved.
func (s *Server) sampleReceipt(ov receiptOverrides) printer.ReceiptData {
	settings, _ := s.db.Settings()
	width, _ := strconv.Atoi(settings[store.KeyPaperWidth])
	theme := ov.theme
	if theme == "" {
		theme = settings[store.KeyReceiptTheme]
	}
	header := settings[store.KeyHeaderLine]
	if ov.header != nil {
		header = *ov.header
	}
	footer := settings[store.KeyFooter]
	if ov.footer != nil {
		footer = *ov.footer
	}
	logo, _, _ := s.db.Logo()

	// Reflect the shop's current tax settings on the sample so the Setup preview
	// shows the tax lines a real receipt would. Reuse ComputeTax so the preview
	// can never drift from what selling actually prints. With tax off this is a
	// no-op (tax 0, total unchanged).
	rateBps, _ := strconv.Atoi(settings[store.KeyTaxRateBps])
	mode := settings[store.KeyTaxMode]
	sample := []store.SaleItem{
		{Name: "Bread", Qty: 2, PriceCents: 6500, Taxable: true},
		{Name: "Cooking Oil 1L", Qty: 1, PriceCents: 35000, Taxable: true},
		{Name: "Sugar 1kg", Qty: 1, PriceCents: 20000, Taxable: true},
	}
	subtotal, tax, total := store.ComputeTax(rateBps, mode, sample)
	const samplePaid = 100000

	items := make([]printer.ReceiptItem, 0, len(sample))
	for _, it := range sample {
		items = append(items, printer.ReceiptItem{Name: it.Name, Qty: it.Qty, PriceCents: it.PriceCents})
	}
	return printer.ReceiptData{
		ShopName:      settings[store.KeyShopName],
		HeaderLine:    header,
		Footer:        footer,
		Logo:          logo,
		When:          time.Now(),
		WidthMM:       width,
		Theme:         theme,
		Items:         items,
		SubtotalCents: subtotal,
		TaxCents:      tax,
		TaxRateBps:    rateBps,
		TaxMode:       mode,
		TotalCents:    total,
		PaidCents:     samplePaid,
		ChangeCents:   samplePaid - total,
	}
}

func (s *Server) handleReceiptPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ov := receiptOverrides{theme: q.Get("theme")}
	if q.Has("header") {
		v := q.Get("header")
		ov.header = &v
	}
	if q.Has("footer") {
		v := q.Get("footer")
		ov.footer = &v
	}
	text := printer.PreviewText(s.sampleReceipt(ov))
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

func (s *Server) handleReceiptTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	settings, _ := s.db.Settings()
	addr := settings[store.KeyPrinterAddr]
	if addr == "" {
		writeError(w, http.StatusBadRequest, "No printer is set up yet.")
		return
	}
	var body struct {
		Theme string `json:"theme"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := printer.PrintReceipt(addr, s.sampleReceipt(receiptOverrides{theme: body.Theme})); err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach the printer. Please check that it is on.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Catalogue ---

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		products, err := s.db.Products()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load your items.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"products": products})
	case http.MethodPost:
		draft, ok := decodeDraft(w, r)
		if !ok {
			return
		}
		p, err := s.db.AddProduct(draft)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Could not save the item. "+capitalize(err.Error()))
			return
		}
		s.nudgeSync()
		writeJSON(w, http.StatusOK, map[string]any{"product": p})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}

func (s *Server) handleProductUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var id struct {
		ID string `json:"id"`
	}
	// Decode once into a combined view so we get the id and the draft fields.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read the item.")
		return
	}
	_ = json.Unmarshal(raw["id"], &id.ID)
	if id.ID == "" {
		writeError(w, http.StatusBadRequest, "No item was selected.")
		return
	}
	draft, ok := draftFromFields(w, raw)
	if !ok {
		return
	}
	p, err := s.db.UpdateProduct(id.ID, draft)
	if err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return
	}
	s.nudgeSync()
	writeJSON(w, http.StatusOK, map[string]any{"product": p})
}

func (s *Server) handleProductDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "No item was selected.")
		return
	}
	if err := s.db.DeleteProduct(body.ID); err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return
	}
	s.nudgeSync()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProductRestock adds (or, with a negative quantity, removes) on-hand stock
// by appending a stock event. Relative restocks are merge-safe across devices, so
// this is the right path for routine restocking rather than re-typing a total.
func (s *Server) handleProductRestock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var body struct {
		ID  string `json:"id"`
		Qty int    `json:"qty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "No item was selected.")
		return
	}
	if body.Qty == 0 {
		writeError(w, http.StatusBadRequest, "Enter how many to add.")
		return
	}
	p, err := s.db.Restock(body.ID, body.Qty)
	if err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return
	}
	s.nudgeSync()
	writeJSON(w, http.StatusOK, map[string]any{"product": p})
}

// decodeDraft reads a product draft from the request body and validates the
// minimum fields. It writes the error response itself and returns ok=false on
// failure.
func decodeDraft(w http.ResponseWriter, r *http.Request) (store.ProductDraft, bool) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read the item.")
		return store.ProductDraft{}, false
	}
	return draftFromFields(w, raw)
}

// draftFromFields builds and validates a ProductDraft from decoded JSON fields.
func draftFromFields(w http.ResponseWriter, raw map[string]json.RawMessage) (store.ProductDraft, bool) {
	var d store.ProductDraft
	d.Taxable = true // new items are taxable by default; the form can turn it off
	_ = json.Unmarshal(raw["name"], &d.Name)
	_ = json.Unmarshal(raw["priceCents"], &d.PriceCents)
	_ = json.Unmarshal(raw["barcode"], &d.Barcode)
	_ = json.Unmarshal(raw["trackStock"], &d.TrackStock)
	_ = json.Unmarshal(raw["stock"], &d.Stock)
	if v, ok := raw["taxable"]; ok {
		_ = json.Unmarshal(v, &d.Taxable)
	}

	if d.Name == "" || d.PriceCents < 0 {
		writeError(w, http.StatusBadRequest, "Please enter a name and a price.")
		return store.ProductDraft{}, false
	}
	if d.Stock < 0 {
		d.Stock = 0
	}
	if !d.TrackStock {
		d.Stock = 0
	}
	return d, true
}

// --- Settings ---

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.db.Settings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load your settings.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
	case http.MethodPost:
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "Could not read the settings.")
			return
		}
		allowed := map[string]bool{
			store.KeyShopName:     true,
			store.KeyPaperWidth:   true,
			store.KeyPrinterAddr:  true,
			store.KeyScanner:      true,
			store.KeyReceiptTheme: true,
			store.KeyHeaderLine:   true,
			store.KeyFooter:       true,
			store.KeyTaxRateBps:   true,
			store.KeyTaxMode:      true,
		}
		// Validate the tax fields before saving so a bad value can never make a
		// sale miscompute. The rate is non-negative basis points; the mode is one
		// of the known tax modes.
		if v, ok := body[store.KeyTaxRateBps]; ok {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "Tax rate must be a whole number of basis points, for example 1600 for 16%.")
				return
			}
		}
		if v, ok := body[store.KeyTaxMode]; ok {
			if v != store.TaxModeNone && v != store.TaxModeInclusive && v != store.TaxModeExclusive {
				writeError(w, http.StatusBadRequest, "Tax mode must be none, inclusive, or exclusive.")
				return
			}
		}
		for k, v := range body {
			if !allowed[k] {
				continue
			}
			if err := s.db.SetSetting(k, v); err != nil {
				writeError(w, http.StatusInternalServerError, "Could not save your settings.")
				return
			}
		}
		s.nudgeSync()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}

// --- Selling ---

func (s *Server) handleSales(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var body struct {
		Items []struct {
			ProductID string `json:"productId"`
			Qty       int    `json:"qty"`
		} `json:"items"`
		PaidCents int64 `json:"paidCents"`
		// PaymentMethod and Reference record how the sale was paid. Reference
		// carries the M-Pesa code or number and is searchable. Both are optional
		// and default to a plain cash sale.
		PaymentMethod string `json:"paymentMethod"`
		Reference     string `json:"reference"`
		// Print defaults to true. The web client sets it false so it can save
		// the sale first and then drive printing as a separate, visible step
		// that the cashier can watch and retry.
		Print *bool `json:"print"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Items) == 0 {
		writeError(w, http.StatusBadRequest, "Your cart is empty.")
		return
	}

	lines := make([]store.SaleLine, 0, len(body.Items))
	for _, it := range body.Items {
		lines = append(lines, store.SaleLine{ProductID: it.ProductID, Qty: it.Qty})
	}

	sale, err := s.db.CreateSale(store.SaleInput{
		Lines:         lines,
		PaidCents:     body.PaidCents,
		PaymentMethod: body.PaymentMethod,
		Reference:     body.Reference,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "Could not complete the sale. Please check the amount paid.")
		return
	}
	s.nudgeSync()

	// The sale is saved no matter what. Printing is best effort so a printer
	// problem never loses a recorded sale.
	printed := false
	printMsg := ""
	if body.Print == nil || *body.Print {
		printed, printMsg = s.printSale(sale)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sale":       sale,
		"printed":    printed,
		"printError": printMsg,
	})
}

func (s *Server) handleSalesToday(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sales, err := s.db.SalesSince(start)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load today's sales.")
		return
	}
	var total int64
	for _, sale := range sales {
		total += sale.TotalCents
	}
	writeJSON(w, http.StatusOK, map[string]any{"sales": sales, "totalCents": total})
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	a, err := s.db.Analytics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load your sales summary.")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleReprint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "No sale was selected.")
		return
	}
	sale, found, err := s.db.SaleByID(body.ID)
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "That sale could not be found.")
		return
	}
	printed, printMsg := s.printSale(sale)
	if !printed {
		writeError(w, http.StatusBadGateway, printMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// printSale renders and prints a sale using the saved printer settings. It
// returns whether printing succeeded and a plain message when it did not.
func (s *Server) printSale(sale store.Sale) (bool, string) {
	settings, err := s.db.Settings()
	if err != nil {
		return false, "Could not load your printer settings."
	}
	addr := settings[store.KeyPrinterAddr]
	if addr == "" {
		return false, "No printer is set up yet."
	}
	width, _ := strconv.Atoi(settings[store.KeyPaperWidth])
	logo, _, _ := s.db.Logo()

	items := make([]printer.ReceiptItem, 0, len(sale.Items))
	for _, it := range sale.Items {
		items = append(items, printer.ReceiptItem{Name: it.Name, Qty: it.Qty, PriceCents: it.PriceCents})
	}
	data := printer.ReceiptData{
		ShopName:      settings[store.KeyShopName],
		HeaderLine:    settings[store.KeyHeaderLine],
		Footer:        settings[store.KeyFooter],
		Logo:          logo,
		When:          sale.CreatedAt,
		WidthMM:       width,
		Theme:         settings[store.KeyReceiptTheme],
		Items:         items,
		SubtotalCents: sale.SubtotalCents,
		TaxCents:      sale.TaxCents,
		TaxRateBps:    sale.TaxRateBps,
		TaxMode:       sale.TaxMode,
		TotalCents:    sale.TotalCents,
		PaidCents:     sale.PaidCents,
		ChangeCents:   sale.ChangeCents,
	}
	if err := printer.PrintReceipt(addr, data); err != nil {
		return false, "The sale was saved, but the receipt could not print. Please check the printer."
	}
	return true, ""
}

// handleConfig reports that the UI is running on the local agent, where every
// feature is available. The hosted cloud serves the same UI but reports "hosted"
// with the local-only features off, so one UI build serves both.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": "local",
		"features": map[string]bool{
			"printing":    true,
			"scanner":     true,
			"deviceSync":  true,
			"salesSearch": true,
			"restock":     true,
			"exports":     true,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// capitalize upper-cases the first letter so a lower-cased error reads as a
// proper sentence when shown to the user.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
