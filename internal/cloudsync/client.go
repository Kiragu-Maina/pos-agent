// Package cloudsync is the local agent's client for the multi-tenant cloud: it
// links a device to a shop (login), then pushes the device's new sales and
// changed products and pulls everyone else's, reconciling into the local store.
// It is pure Go (net/http) so it stays Windows 7 friendly.
//
// The cloud is the optional sync hub, never a dependency of selling: every method
// here can fail (offline, server down, session expired) and the local agent keeps
// working. The engine records the outcome and tries again later.
package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pos-system/internal/store"
)

const sessionCookie = "pos_session"

// ErrUnauthorized means the cloud rejected the session (expired or revoked). The
// caller should prompt the user to sign in again; the device cannot silently
// re-authenticate without the password.
var ErrUnauthorized = errors.New("cloud session is no longer valid")

// PushPayload is what the device sends up. The JSON shapes of store.Product and
// store.Sale match the cloud's wire exactly, so they cross without translation.
type PushPayload struct {
	Products          []store.Product    `json:"products"`
	Sales             []store.Sale       `json:"sales"`
	StockEvents       []store.StockEvent `json:"stockEvents"`
	Settings          map[string]string  `json:"settings,omitempty"`
	SettingsUpdatedAt *time.Time         `json:"settingsUpdatedAt,omitempty"`
}

// PushResult is the server's report plus the cursor to advance to.
type PushResult struct {
	Cursor          int64 `json:"cursor"`
	ProductsApplied int   `json:"productsApplied"`
	ProductsSkipped int   `json:"productsSkipped"`
	SalesApplied    int   `json:"salesApplied"`
	SettingsApplied bool  `json:"settingsApplied"`
}

// PullResult is everything that changed past the cursor the device sent.
type PullResult struct {
	Cursor            int64              `json:"cursor"`
	Products          []store.Product    `json:"products"`
	Sales             []store.Sale       `json:"sales"`
	StockEvents       []store.StockEvent `json:"stockEvents"`
	Settings          map[string]string  `json:"settings"`
	SettingsUpdatedAt *time.Time         `json:"settingsUpdatedAt"`
}

// LinkInfo is the result of a successful login.
type LinkInfo struct {
	ShopID string
	Email  string
	Token  string // the session cookie value, stored for later sync calls
}

// HTTPClient talks to the cloud over HTTP.
type HTTPClient struct {
	http *http.Client
}

// NewClient builds a client with a sane request timeout.
func NewClient() *HTTPClient {
	return &HTTPClient{http: &http.Client{Timeout: 20 * time.Second}}
}

// Login authenticates against the cloud and returns the shop id and the session
// token captured from the Set-Cookie response.
func (c *HTTPClient) Login(baseURL, email, password string) (LinkInfo, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequest(http.MethodPost, joinURL(baseURL, "/api/auth/login"), bytes.NewReader(body))
	if err != nil {
		return LinkInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return LinkInfo{}, fmt.Errorf("cannot reach the cloud: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return LinkInfo{}, errors.New("that email or password was not recognised")
	}
	if resp.StatusCode != http.StatusOK {
		return LinkInfo{}, serverError(resp)
	}
	var out struct {
		ShopID string `json:"shopId"`
		Email  string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return LinkInfo{}, err
	}
	token := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			token = ck.Value
		}
	}
	if token == "" {
		return LinkInfo{}, errors.New("the cloud did not return a session")
	}
	return LinkInfo{ShopID: out.ShopID, Email: out.Email, Token: token}, nil
}

// Pull fetches everything changed past since.
func (c *HTTPClient) Pull(baseURL, token string, since int64) (PullResult, error) {
	var out PullResult
	err := c.post(baseURL, "/api/sync/pull", token, map[string]int64{"since": since}, &out)
	return out, err
}

// Push uploads the device's batch and returns the new cursor.
func (c *HTTPClient) Push(baseURL, token string, payload PushPayload) (PushResult, error) {
	var out PushResult
	err := c.post(baseURL, "/api/sync/push", token, payload, &out)
	return out, err
}

// post sends an authenticated JSON request and decodes the JSON response.
func (c *HTTPClient) post(baseURL, path, token string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, joinURL(baseURL, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the cloud: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return serverError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// serverError turns a non-200 response into an error carrying the server's
// message when it sent one.
func serverError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(data, &body)
	if body.Error != "" {
		return errors.New(body.Error)
	}
	return fmt.Errorf("cloud returned %d", resp.StatusCode)
}
