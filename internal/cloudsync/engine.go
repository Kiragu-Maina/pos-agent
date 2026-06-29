package cloudsync

import (
	"time"

	"pos-system/internal/store"
)

// SyncStore is the slice of the local store the engine needs. boltstore.Store
// satisfies it.
type SyncStore interface {
	SyncState() (store.SyncState, error)
	SetCursor(cursor int64) error
	SetSyncResult(at time.Time, errMsg string) error
	PendingProducts() ([]store.Product, error)
	PendingSales() ([]store.Sale, error)
	PendingStockEvents() ([]store.StockEvent, error)
	PendingSettings() (map[string]string, time.Time, bool, error)
	ClearPushed(productIDs, saleIDs, stockEventIDs []string, settingsAt time.Time, settingsPushed bool) error
	ApplyPulled(products []store.Product, sales []store.Sale, stockEvents []store.StockEvent, settings map[string]string, settingsAt *time.Time) error
}

// SyncClient is the cloud transport the engine drives. *HTTPClient satisfies it.
type SyncClient interface {
	Push(baseURL, token string, payload PushPayload) (PushResult, error)
	Pull(baseURL, token string, since int64) (PullResult, error)
}

// Engine runs one sync cycle: push the outbox, then pull and apply remote
// changes. It is safe to call repeatedly; each cycle is push-then-pull and
// records its outcome on the store for the UI.
type Engine struct {
	store  SyncStore
	client SyncClient
	now    func() time.Time
}

// NewEngine builds an engine over the store and client.
func NewEngine(s SyncStore, c SyncClient) *Engine {
	return &Engine{store: s, client: c, now: time.Now}
}

// SyncOnce performs one push-then-pull cycle. It is a no-op (nil) when the device
// is not linked. Any failure is recorded on the store and returned; selling is
// never affected because the caller runs this in the background.
func (e *Engine) SyncOnce() error {
	st, err := e.store.SyncState()
	if err != nil {
		return err
	}
	if !st.Linked {
		return nil
	}

	if err := e.push(st); err != nil {
		e.record(err)
		return err
	}
	// Re-read the link state in case it changed during the push; the pull then
	// runs from this device's existing cursor (push does not advance it).
	if st, err = e.store.SyncState(); err != nil {
		return err
	}
	if err := e.pull(st); err != nil {
		e.record(err)
		return err
	}
	e.record(nil)
	return nil
}

// push uploads the outbox and clears the marks the server accepted.
func (e *Engine) push(st store.SyncState) error {
	products, err := e.store.PendingProducts()
	if err != nil {
		return err
	}
	sales, err := e.store.PendingSales()
	if err != nil {
		return err
	}
	stockEvents, err := e.store.PendingStockEvents()
	if err != nil {
		return err
	}
	settings, settingsAt, settingsDirty, err := e.store.PendingSettings()
	if err != nil {
		return err
	}
	if len(products) == 0 && len(sales) == 0 && len(stockEvents) == 0 && !settingsDirty {
		return nil // nothing to push
	}

	payload := PushPayload{Products: products, Sales: sales, StockEvents: stockEvents}
	if settingsDirty {
		payload.Settings = settings
		at := settingsAt
		payload.SettingsUpdatedAt = &at
	}
	if _, err := e.client.Push(st.BaseURL, st.Token, payload); err != nil {
		return err
	}

	productIDs := make([]string, len(products))
	for i, p := range products {
		productIDs[i] = p.ID
	}
	saleIDs := make([]string, len(sales))
	for i, s := range sales {
		saleIDs[i] = s.ID
	}
	stockEventIDs := make([]string, len(stockEvents))
	for i, ev := range stockEvents {
		stockEventIDs[i] = ev.ID
	}
	if err := e.store.ClearPushed(productIDs, saleIDs, stockEventIDs, settingsAt, settingsDirty); err != nil {
		return err
	}
	// Do not advance the pull cursor from the push result here. The server's
	// cursor is the shop's highest sequence, which includes other devices' rows
	// this device has not pulled yet; jumping to it would skip them. The pull
	// that immediately follows advances the cursor from where this device
	// actually left off, and re-pulling our own just-pushed rows is a harmless
	// idempotent no-op (last-write-wins keeps them, append-only ignores them).
	return nil
}

// pull fetches changes past the cursor, applies them, and advances the cursor.
func (e *Engine) pull(st store.SyncState) error {
	res, err := e.client.Pull(st.BaseURL, st.Token, st.Cursor)
	if err != nil {
		return err
	}
	if err := e.store.ApplyPulled(res.Products, res.Sales, res.StockEvents, res.Settings, res.SettingsUpdatedAt); err != nil {
		return err
	}
	return e.store.SetCursor(res.Cursor)
}

// record stamps the sync outcome for the UI.
func (e *Engine) record(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = e.store.SetSyncResult(e.now(), msg)
}
