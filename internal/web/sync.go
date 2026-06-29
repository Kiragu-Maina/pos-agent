package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"pos-system/internal/cloudsync"
)

// Syncer is the cloud-sync controller the agent drives from Setup. cloudsync.
// Controller satisfies it. It is optional: when nil, the sync endpoints report
// that sync is unavailable and the agent stays purely local.
type Syncer interface {
	Link(baseURL, email, password string) error
	Unlink() error
	Sync() error
	Nudge()
	Status() (cloudsync.Status, error)
}

// nudgeSync asks the controller to sync soon after a local change. It is safe to
// call when sync is unavailable.
func (s *Server) nudgeSync() {
	if s.sync != nil {
		s.sync.Nudge()
	}
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	s.writeSyncStatus(w)
}

func (s *Server) handleSyncLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	if s.sync == nil {
		writeError(w, http.StatusBadRequest, "Cloud sync is not available on this device.")
		return
	}
	var body struct {
		BaseURL  string `json:"baseUrl"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read your details.")
		return
	}
	base := strings.TrimSpace(body.BaseURL)
	if base == "" {
		base = s.cloudURL
	}
	if strings.TrimSpace(body.Email) == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "Please enter your email and password.")
		return
	}
	if base == "" {
		writeError(w, http.StatusInternalServerError, "Cloud sync is not set up on this device.")
		return
	}
	if err := s.sync.Link(base, strings.TrimSpace(body.Email), body.Password); err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return
	}
	s.writeSyncStatus(w)
}

func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	if s.sync == nil {
		writeError(w, http.StatusBadRequest, "Cloud sync is not available on this device.")
		return
	}
	if err := s.sync.Sync(); err != nil {
		if cloudsync.IsOffline(err) {
			writeError(w, http.StatusBadGateway, "You appear to be offline. Your changes are saved on this device and will sync when you reconnect.")
			return
		}
		writeError(w, http.StatusBadGateway, capitalize(err.Error())+".")
		return
	}
	s.writeSyncStatus(w)
}

func (s *Server) handleSyncUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	if s.sync != nil {
		if err := s.sync.Unlink(); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not unlink.")
			return
		}
	}
	s.writeSyncStatus(w)
}

// writeSyncStatus returns the current sync status, or an unavailable marker when
// the agent is running without a cloud controller.
func (s *Server) writeSyncStatus(w http.ResponseWriter) {
	if s.sync == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	st, err := s.sync.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the sync status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":       true,
		"defaultBaseUrl":  s.cloudURL,
		"linked":          st.Linked,
		"email":           st.Email,
		"baseUrl":         st.BaseURL,
		"lastSync":        st.LastSync,
		"lastError":       st.LastError,
		"pendingProducts": st.PendingProducts,
		"pendingSales":    st.PendingSales,
		"pendingSettings": st.PendingSettings,
		"syncing":         st.Syncing,
		"online":          st.Online,
	})
}
