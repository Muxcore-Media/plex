package internal

import (
	"net/http"
)

func (m *Module) handleSyncListsHTTP(w http.ResponseWriter, r *http.Request) {
	if !m.checkHTTPSecret(r.Header.Get(headerPlexBridgeSecret)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !m.configured() {
		http.Error(w, "plex not configured", http.StatusServiceUnavailable)
		return
	}
	userID := r.URL.Query().Get("user_id")
	clientID := r.URL.Query().Get("client_id")
	if refresh := r.URL.Query().Get("refresh"); refresh == "1" || refresh == "true" {
		if err := m.refreshSyncLists(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	snap := m.cachedSyncLists(userID, clientID)
	if snap.UpdatedAt == "" {
		if err := m.refreshSyncLists(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		snap = m.cachedSyncLists(userID, clientID)
	}
	writeJSON(w, http.StatusOK, snap)
}
