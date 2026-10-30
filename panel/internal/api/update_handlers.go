package api

import (
	"net/http"

	"kgpanel/panel/internal/update"
)

// handleUpdateCheck reports current vs latest from the release feed and
// whether this panel should self-update. GET /api/update/check (admin).
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	st, _ := update.Latest(s.updateURL)
	writeJSON(w, http.StatusOK, st)
}

// handleUpdate triggers the self-update: download, verify, swap, restart.
// POST /api/update (admin only). On success the process exits and relaunches.
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
	st, err := update.Apply(s.updateURL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Apply may have called os.Exit already; if we're here, nothing to do.
	writeJSON(w, http.StatusOK, st)
}
