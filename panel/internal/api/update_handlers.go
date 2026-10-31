package api

import (
	"net/http"
	"time"

	"kgpanel/panel/internal/update"
)

// handleUpdateCheck reports current vs latest from the release feed and
// whether this panel should self-update. GET /api/update/check (admin).
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	st, _ := update.Latest(s.updateURL)
	writeJSON(w, http.StatusOK, st)
}

// handleUpdate triggers the self-update: download, verify, swap, restart.
// POST /api/update (admin only). The response is sent FIRST (the swap below
// calls os.Exit, which would otherwise kill the connection mid-write and make
// every success look like a network error), then the update runs after a short
// grace so the HTTP response flushes; the listener rebinds on restart.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	uid := userFromRequest(r)
	u := s.st.GetUser(uid)
	if u == nil || !u.Admin {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	s.st.Audit(u.Username, "update_trigger", "panel", s.updateURL)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applying": true, "component": "kgpanel", "feed": s.updateURL,
	})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the response reach the client
		update.Apply(s.updateURL)
		// Apply calls os.Exit on success; reaching here means no update needed.
	}()
}
