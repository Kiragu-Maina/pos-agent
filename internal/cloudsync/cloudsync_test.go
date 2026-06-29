package cloudsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pos-system/internal/store"
)

// --- engine, against fakes ---

type fakeStore struct {
	state       store.SyncState
	prods       []store.Product
	sales       []store.Sale
	stockEvents []store.StockEvent
	settings    map[string]string
	settingsAt  time.Time
	dirty       bool
	clearedP    []string
	clearedS    []string
	clearedE    []string
	applied     bool
	appliedP    []store.Product
	appliedS    []store.Sale
	appliedE    []store.StockEvent
	cursor      int64
	lastErr     string
	resultCalls int
}

func (f *fakeStore) SyncState() (store.SyncState, error)             { return f.state, nil }
func (f *fakeStore) SetCursor(c int64) error                         { f.cursor = c; f.state.Cursor = c; return nil }
func (f *fakeStore) PendingProducts() ([]store.Product, error)       { return f.prods, nil }
func (f *fakeStore) PendingSales() ([]store.Sale, error)             { return f.sales, nil }
func (f *fakeStore) PendingStockEvents() ([]store.StockEvent, error) { return f.stockEvents, nil }
func (f *fakeStore) PendingSettings() (map[string]string, time.Time, bool, error) {
	return f.settings, f.settingsAt, f.dirty, nil
}
func (f *fakeStore) ClearPushed(p, s, e []string, at time.Time, pushed bool) error {
	f.clearedP, f.clearedS, f.clearedE = p, s, e
	return nil
}
func (f *fakeStore) ApplyPulled(p []store.Product, s []store.Sale, e []store.StockEvent, m map[string]string, at *time.Time) error {
	f.applied, f.appliedP, f.appliedS, f.appliedE = true, p, s, e
	return nil
}
func (f *fakeStore) SetSyncResult(at time.Time, msg string) error {
	f.lastErr = msg
	f.resultCalls++
	return nil
}

type fakeClient struct {
	pushed   PushPayload
	pushRes  PushResult
	pullRes  PullResult
	pushErr  error
	pullErr  error
	sinceGot int64
}

func (c *fakeClient) Push(_, _ string, p PushPayload) (PushResult, error) {
	c.pushed = p
	return c.pushRes, c.pushErr
}
func (c *fakeClient) Pull(_, _ string, since int64) (PullResult, error) {
	c.sinceGot = since
	return c.pullRes, c.pullErr
}

func TestSyncOncePushesThenPulls(t *testing.T) {
	fs := &fakeStore{
		state: store.SyncState{Linked: true, BaseURL: "http://x", Token: "t", Cursor: 5},
		prods: []store.Product{{ID: "p1", Name: "Soap", UpdatedAt: time.Now()}},
		sales: []store.Sale{{ID: "s1", TotalCents: 100}},
	}
	fc := &fakeClient{
		pushRes: PushResult{Cursor: 9, SalesApplied: 1},
		pullRes: PullResult{Cursor: 12, Products: []store.Product{{ID: "p2", Name: "Milk"}}},
	}
	eng := NewEngine(fs, fc)

	if err := eng.SyncOnce(); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// Pushed the outbox.
	if len(fc.pushed.Products) != 1 || len(fc.pushed.Sales) != 1 {
		t.Fatalf("expected outbox pushed, got %+v", fc.pushed)
	}
	// Cleared the marks it pushed.
	if len(fs.clearedP) != 1 || fs.clearedP[0] != "p1" || len(fs.clearedS) != 1 || fs.clearedS[0] != "s1" {
		t.Fatalf("expected cleared marks, got %v %v", fs.clearedP, fs.clearedS)
	}
	// Pulled from this device's existing cursor (5), not the server's post-push
	// high-water mark: a push must not advance the pull cursor, or another
	// device's rows at a lower sequence would be skipped. Ended at 12.
	if fc.sinceGot != 5 {
		t.Fatalf("pull should start from the device cursor 5, got %d", fc.sinceGot)
	}
	if !fs.applied || len(fs.appliedP) != 1 || fs.appliedP[0].Name != "Milk" {
		t.Fatalf("expected pulled changes applied, got %+v", fs.appliedP)
	}
	if fs.cursor != 12 {
		t.Fatalf("expected final cursor 12, got %d", fs.cursor)
	}
	if fs.lastErr != "" {
		t.Fatalf("expected success, got error %q", fs.lastErr)
	}
}

func TestSyncOnceSkipsWhenUnlinked(t *testing.T) {
	fs := &fakeStore{state: store.SyncState{Linked: false}}
	fc := &fakeClient{}
	if err := NewEngine(fs, fc).SyncOnce(); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if fs.applied || fs.resultCalls != 0 {
		t.Fatal("an unlinked device must not sync or record a result")
	}
}

func TestSyncOnceRecordsPushError(t *testing.T) {
	fs := &fakeStore{state: store.SyncState{Linked: true}, sales: []store.Sale{{ID: "s1"}}}
	fc := &fakeClient{pushErr: ErrUnauthorized}
	err := NewEngine(fs, fc).SyncOnce()
	if err == nil {
		t.Fatal("expected the push error to surface")
	}
	if fs.lastErr == "" {
		t.Fatal("expected the failure recorded for the UI")
	}
	if fs.applied {
		t.Fatal("must not pull after a failed push")
	}
}

// --- HTTP client, against a fake cloud ---

func TestClientLoginPullPush(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "session-xyz"})
			writeJSON(w, map[string]string{"shopId": "shop-7", "email": "a@b.co"})
		case "/api/sync/pull":
			if c, err := r.Cookie(sessionCookie); err != nil || c.Value != "session-xyz" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, PullResult{Cursor: 3, Products: []store.Product{{ID: "p1", Name: "Sukari"}}})
		case "/api/sync/push":
			writeJSON(w, PushResult{Cursor: 4, SalesApplied: 1})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient()
	info, err := c.Login(srv.URL, "a@b.co", "pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if info.ShopID != "shop-7" || info.Token != "session-xyz" {
		t.Fatalf("login info wrong: %+v", info)
	}

	pull, err := c.Pull(srv.URL, info.Token, 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pull.Cursor != 3 || len(pull.Products) != 1 || pull.Products[0].Name != "Sukari" {
		t.Fatalf("pull result wrong: %+v", pull)
	}

	push, err := c.Push(srv.URL, info.Token, PushPayload{Sales: []store.Sale{{ID: "s1"}}})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if push.Cursor != 4 || push.SalesApplied != 1 {
		t.Fatalf("push result wrong: %+v", push)
	}
}

func TestClientUnauthorizedMapsToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := NewClient().Pull(srv.URL, "stale", 0)
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
