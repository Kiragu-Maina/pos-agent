// Package store is the offline data layer's contract. It defines the domain
// types and the Local interface that every local store implementation honours,
// keeping the web and (later) sync layers independent of the storage engine.
//
// The shipping implementation is boltstore (pure-Go bbolt, Windows 7 safe). A
// SQLite implementation can slot in behind a build tag later without touching
// callers, because they depend on this interface, not the engine.
//
// Records carry sync metadata from the start so the cloud sync engine (a later
// phase) can reconcile them: IDs are client-generatable UUIDs, mutable rows
// carry UpdatedAt and a DeletedAt tombstone, and sales snapshot tax and payment
// so a reprint stays faithful. The shape mirrors the cloud model deliberately;
// the two never share a module (the cloud pulls in pgx and a newer toolchain),
// so the sync engine maps between them at the boundary.
package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned when a record does not exist. Handlers map it to a
// 404 so a missing product or sale reads the same to the client.
var ErrNotFound = errors.New("that item could not be found")

// ErrBarcodeTaken is returned when a product barcode is already in use, so a
// scan always maps to exactly one product.
var ErrBarcodeTaken = errors.New("an item with this barcode already exists")

// Tax modes. A shop is "none" until it opts in; inclusive prices already contain
// the tax, exclusive prices add it on top. The rate is stored in basis points.
const (
	TaxModeNone      = "none"
	TaxModeInclusive = "inclusive"
	TaxModeExclusive = "exclusive"
)

// Setting keys and their defaults.
const (
	KeyShopName     = "shop_name"
	KeyPaperWidth   = "paper_width" // millimetres, "58" or "80"
	KeyPrinterAddr  = "printer_addr"
	KeyScanner      = "barcode_scanner" // "on" or "off", opt-in
	KeyReceiptTheme = "receipt_theme"   // "classic", "minimal", or "bold"
	KeyHeaderLine   = "receipt_header"  // optional line under the shop name
	KeyFooter       = "receipt_footer"  // ending message, may be multi-line
	KeyHasLogo      = "has_logo"        // "yes" when a logo is stored
	KeyTaxRateBps   = "tax_rate_bps"    // basis points, "1600" = 16.00%
	KeyTaxMode      = "tax_mode"        // "none", "inclusive", or "exclusive"
)

// Payment methods recorded on a sale. Reference holds the M-Pesa code or number
// when paid by M-Pesa, and is searchable.
const (
	PaymentCash  = "cash"
	PaymentMpesa = "mpesa"
)

// Defaults returns the settings a fresh shop starts with, merged under by any
// values the owner has saved.
func Defaults() map[string]string {
	return map[string]string{
		KeyShopName:     "My Shop",
		KeyPaperWidth:   "80",
		KeyPrinterAddr:  "",
		KeyScanner:      "off",
		KeyReceiptTheme: "classic",
		KeyHeaderLine:   "",
		KeyFooter:       "Asante sana\nThank you",
		KeyHasLogo:      "no",
		KeyTaxRateBps:   "0",
		KeyTaxMode:      "none",
	}
}

// Product is something the shop sells. Prices are integer cents to avoid any
// floating point rounding on money. Barcode is optional and only used when the
// shop has opted into a barcode scanner.
//
// TrackStock is opt-in per product. When false the item sells freely and shows
// no stock badge. When true, Stock is shown, counts down as the item sells, and
// drives the low and out of stock warnings. Selling is never hard blocked; a
// busy counter stays forgiving and Stock simply floors at zero.
//
// Taxable marks whether tax applies to the item once a shop turns tax on
// (per-item exempt). ID is a UUID string minted on the device. UpdatedAt and
// DeletedAt drive last-write-wins sync; a deleted product keeps its row as a
// tombstone so the deletion propagates and past sales stay faithful.
type Product struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	PriceCents int64      `json:"priceCents"`
	Barcode    string     `json:"barcode"`
	TrackStock bool       `json:"trackStock"`
	Stock      int        `json:"stock"`
	Taxable    bool       `json:"taxable"`
	Active     bool       `json:"active"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	DeletedAt  *time.Time `json:"deletedAt,omitempty"`
}

// ProductDraft carries the editable fields of a product for create and update.
type ProductDraft struct {
	Name       string
	PriceCents int64
	Barcode    string
	TrackStock bool
	Stock      int
	Taxable    bool
}

// Stock-event reasons. A product's on-hand is derived from these append-only
// events (plus what its sales sold), never from a stored counter, so two offline
// tills cannot clobber each other's stock when they reconcile.
const (
	StockInitial    = "initial"    // the count set when tracking is turned on
	StockRestock    = "restock"    // more stock came in (relative, merge-safe)
	StockAdjustment = "adjustment" // a manual correction to an absolute count
)

// StockEvent is one append-only change to a tracked product's on-hand quantity.
// Sales are deliberately not stock events: the quantity sold is derived from the
// sales themselves, so on-hand = sum of a product's stock events minus its sold
// quantity. Events carry a UUID and CreatedAt for the same append-only sync as
// sales (inserted if absent, never updated), which is what lets restocks from
// two devices add up instead of overwriting one another.
type StockEvent struct {
	ID        string    `json:"id"`
	ProductID string    `json:"productId"`
	Delta     int       `json:"delta"` // signed: positive adds stock, negative removes
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

// SaleItem is one line on a sale. It captures the name and price at the time of
// sale so a reprint is always faithful even if the product later changes.
// Taxable and TaxCents are snapshotted too; both are zero until tax is enabled.
type SaleItem struct {
	ProductID  string `json:"productId"`
	Name       string `json:"name"`
	PriceCents int64  `json:"priceCents"`
	Qty        int    `json:"qty"`
	Taxable    bool   `json:"taxable"`
	TaxCents   int64  `json:"taxCents"`
}

// SaleLine is one requested line in a CreateSale call: which product, how many.
// Names and prices are looked up and snapshotted by the store.
type SaleLine struct {
	ProductID string
	Qty       int
}

// SaleInput is a request to complete a sale. PaymentMethod and Reference record
// how it was paid; Reference holds the M-Pesa code or number when relevant and
// is indexed for search.
type SaleInput struct {
	Lines         []SaleLine
	PaidCents     int64
	PaymentMethod string
	Reference     string
}

// SearchCriteria filters a sales search. Every field is optional and combines
// with AND; a zero value means "no bound on this dimension". Reference is an
// exact match (it uses the reference index); ItemName is a case-insensitive
// substring match against any line item; PaymentMethod is exact. Start is an
// inclusive lower bound and End an exclusive upper bound on CreatedAt. MinCents
// and MaxCents bound TotalCents inclusively. Limit caps the result count.
type SearchCriteria struct {
	Reference     string
	ItemName      string
	PaymentMethod string
	Start         time.Time
	End           time.Time
	MinCents      int64
	MaxCents      int64
	Limit         int
}

// Sale is a completed transaction. Tax rate and mode are snapshotted so a later
// reprint shows the figures as they were even if the shop changes its rate.
// Sales are append-only: they never update or delete, so they need no tombstone,
// but they carry CreatedAt for the sync cursor.
type Sale struct {
	ID            string     `json:"id"`
	CreatedAt     time.Time  `json:"createdAt"`
	Items         []SaleItem `json:"items"`
	SubtotalCents int64      `json:"subtotalCents"`
	TaxCents      int64      `json:"taxCents"`
	TotalCents    int64      `json:"totalCents"`
	PaidCents     int64      `json:"paidCents"`
	ChangeCents   int64      `json:"changeCents"`
	PaymentMethod string     `json:"paymentMethod"`
	Reference     string     `json:"reference"`
	TaxRateBps    int        `json:"taxRateBps"`
	TaxMode       string     `json:"taxMode"`
}

// AuditEntry is one line in the device's activity log: a change made on this
// device, kept so the owner can export an accountable history of what happened
// (an item added or edited, a price or setting changed). Entries are local to the
// device that made the change and are not synced. Sales are not audited here:
// they are the transactions export, which already covers every synced sale.
type AuditEntry struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Action string    `json:"action"` // short label, for example "Item added"
	Detail string    `json:"detail"` // the specifics, for example "Bread, KSh 65.00"
}

// --- Analytics ---

// DaySummary totals one day of trading.
type DaySummary struct {
	TotalCents int64 `json:"totalCents"`
	SaleCount  int   `json:"saleCount"`
	ItemCount  int   `json:"itemCount"`
}

// DayBucket is one bar in the seven day trend.
type DayBucket struct {
	Date       string `json:"date"`  // 2006-01-02
	Label      string `json:"label"` // short weekday, for example "Mon"
	TotalCents int64  `json:"totalCents"`
}

// ProductStat ranks one product over the analytics window.
type ProductStat struct {
	Name         string `json:"name"`
	Qty          int    `json:"qty"`
	RevenueCents int64  `json:"revenueCents"`
}

// HourBucket totals one hour of the day across the window.
type HourBucket struct {
	Hour       int   `json:"hour"`
	TotalCents int64 `json:"totalCents"`
}

// Analytics is a light summary for the dashboard. Everything is computed from a
// single pass over the last seven days of sales, so it stays cheap even on a
// modest machine.
type Analytics struct {
	Today       DaySummary    `json:"today"`
	Yesterday   DaySummary    `json:"yesterday"`
	Days        []DayBucket   `json:"days"`        // seven buckets, oldest first
	TopProducts []ProductStat `json:"topProducts"` // best sellers, highest first
	Hours       []HourBucket  `json:"hours"`       // 24 buckets, midnight first
}

// SyncState is the local agent's link to a cloud shop. It is empty until the
// device is linked. Token is the cloud session cookie value; Cursor is the
// highest server sequence this device has pulled. LastError holds the most
// recent sync failure for display, empty when the last sync succeeded.
type SyncState struct {
	Linked    bool      `json:"linked"`
	BaseURL   string    `json:"baseUrl"`
	ShopID    string    `json:"shopId"`
	Email     string    `json:"email"`
	Token     string    `json:"-"` // secret: never serialised to the UI
	Cursor    int64     `json:"-"`
	LastSync  time.Time `json:"lastSync"`
	LastError string    `json:"lastError"`
}

// Local is the contract a local store implementation must satisfy. The web
// layer and the sync engine depend on this interface, never on a concrete
// engine, so bbolt today and SQLite later are interchangeable.
//
// It is single-tenant: one device serves one shop, so methods take no shop id
// (the shop identity lives in settings and is attached at the sync boundary).
type Local interface {
	// Products.
	Products() ([]Product, error)
	AddProduct(d ProductDraft) (Product, error)
	UpdateProduct(id string, d ProductDraft) (Product, error)
	DeleteProduct(id string) error
	// ProductByBarcode resolves a scanned barcode to its active product via the
	// exact-match index. ok is false when no active product uses the code.
	ProductByBarcode(code string) (Product, bool, error)
	// Restock adds qty (may be negative to remove) to a tracked product's on-hand
	// by appending a stock event, and returns the product with its new derived
	// on-hand. Relative restocks are merge-safe across devices.
	Restock(productID string, qty int) (Product, error)

	// Sales.
	CreateSale(in SaleInput) (Sale, error)
	SalesSince(t time.Time) ([]Sale, error)
	SaleByID(id string) (Sale, bool, error)
	// SalesByReference returns every sale whose Reference exactly matches ref
	// (an M-Pesa code or number), newest first, via the exact-match index.
	SalesByReference(ref string) ([]Sale, error)
	// SearchSales returns sales matching every set field of c, newest first.
	SearchSales(c SearchCriteria) ([]Sale, error)

	// Analytics.
	Analytics() (Analytics, error)

	// Audit returns activity-log entries with At in [from, to), oldest first, for
	// export. A zero from or to is unbounded on that end.
	Audit(from, to time.Time) ([]AuditEntry, error)

	// Settings.
	Settings() (map[string]string, error)
	SetSetting(key, value string) error

	// Logo (binary, kept out of the settings map).
	Logo() ([]byte, bool, error)
	SetLogo(data []byte) error
	DeleteLogo() error

	// Lifecycle and seeding.
	SeedIfEmpty() error
	Close() error
}
