package cloudsync

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"pos-system/internal/store"
)

// LinkStore is the local store as the controller needs it: the engine's slice
// plus link management. boltstore.Store satisfies it.
type LinkStore interface {
	SyncStore
	Link(baseURL, shopID, email, token string) error
	Unlink() error
}

// Controller owns the device's link to the cloud and serialises sync. The web
// layer drives it (link, sync now, unlink, status); a background loop runs it on
// a timer. Only one sync runs at a time, so a manual sync and the timer never
// collide.
type Controller struct {
	store   LinkStore
	client  *HTTPClient
	engine  *Engine
	mu      sync.Mutex
	trigger chan struct{} // a write asks for a near-term sync
	syncing atomic.Bool   // a sync is running right now
	offline atomic.Bool   // the last attempt could not reach the cloud
}

// NewController builds a controller over the local store.
func NewController(s LinkStore) *Controller {
	c := NewClient()
	return &Controller{store: s, client: c, engine: NewEngine(s, c), trigger: make(chan struct{}, 1)}
}

// Status is the link state plus how much is waiting to upload, whether a sync is
// in flight, and whether the cloud is currently reachable, all for the UI.
type Status struct {
	store.SyncState
	PendingProducts int  `json:"pendingProducts"`
	PendingSales    int  `json:"pendingSales"`
	PendingSettings bool `json:"pendingSettings"`
	Syncing         bool `json:"syncing"`
	Online          bool `json:"online"`
}

// Link logs into the cloud, records the link, and runs a first sync so the
// device is immediately up to date.
func (c *Controller) Link(baseURL, email, password string) error {
	info, err := c.client.Login(baseURL, email, password)
	if err != nil {
		return err
	}
	if err := c.store.Link(baseURL, info.ShopID, info.Email, info.Token); err != nil {
		return err
	}
	return c.Sync()
}

// Unlink forgets the cloud link.
func (c *Controller) Unlink() error { return c.store.Unlink() }

// Sync runs one push-then-pull cycle, serialised against any other sync. It
// records whether the cloud was reachable so the UI can tell "offline" apart
// from a real error.
func (c *Controller) Sync() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncing.Store(true)
	err := c.engine.SyncOnce()
	c.syncing.Store(false)
	c.offline.Store(IsOffline(err))
	return err
}

// Nudge asks for a sync soon. The web layer calls it after a sale or a catalogue
// or settings change, so edits reach the cloud within seconds instead of waiting
// for the next timer tick. It never blocks: if a sync is already queued, the
// nudge is dropped because that pending sync will pick the change up anyway.
func (c *Controller) Nudge() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

// Status reports the link state and the outbox depth.
func (c *Controller) Status() (Status, error) {
	st, err := c.store.SyncState()
	if err != nil {
		return Status{}, err
	}
	out := Status{SyncState: st}
	if prods, err := c.store.PendingProducts(); err == nil {
		out.PendingProducts = len(prods)
	}
	if sales, err := c.store.PendingSales(); err == nil {
		out.PendingSales = len(sales)
	}
	if _, _, dirty, err := c.store.PendingSettings(); err == nil {
		out.PendingSettings = dirty
	}
	out.Syncing = c.syncing.Load()
	// Online only means something once linked; an unlinked device is neither.
	out.Online = st.Linked && !c.offline.Load()
	return out, nil
}

// Run reconciles with the cloud until the context is cancelled. It syncs once at
// startup (so a device that comes back online catches up immediately), then on
// every timer tick and whenever a write nudges it. Each cycle is a no-op when the
// device is unlinked, so it is cheap to leave running, and a failure is recorded
// for the UI and retried on the next tick.
func (c *Controller) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	_ = c.Sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.Sync()
		case <-c.trigger:
			c.coalesce(ctx)
			_ = c.Sync()
		}
	}
}

// coalesce waits a short, quiet window after a nudge so a burst of writes (a
// quick run of sales, or saving several settings) becomes a single sync rather
// than one per change.
func (c *Controller) coalesce(ctx context.Context) {
	timer := time.NewTimer(1500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.trigger:
			// Another write arrived; keep absorbing until things go quiet.
		case <-timer.C:
			return
		}
	}
}

// IsOffline reports whether err is a network-reachability failure, meaning the
// device is offline or the cloud is unreachable, as opposed to an auth or server
// error that did get a reply. The transport wraps such failures with %w, so the
// underlying *url.Error / net.Error is recoverable here.
func IsOffline(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
