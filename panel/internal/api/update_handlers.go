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

// handleUpdate triggers the panel update through wings when a node is
// registered (docker install: wings rebuilds the image and recreates the
// container), or falls back to the bare-metal binary swap otherwise.
// POST /api/update (admin only). The response is sent FIRST (the panel
// container is replaced, which would otherwise kill the connection mid-write).
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

	// docker install: delegate to wings (rebuild + recreate the panel
	// container). Primary path when a node is registered.
	if nodes := s.st.ListNodes(); len(nodes) > 0 {
		s.st.Audit(u.Username, "update_trigger", "panel", "wings:"+nodes[0].Name)
		var tag string
		if st, err := update.Latest(s.updateURL); err == nil {
			tag = st.Latest
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"applying": true, "component": "kgpanel", "mode": "wings-docker", "tag": tag,
		})
		go func() {
			time.Sleep(300 * time.Millisecond) // let the response reach the client
			_ = s.wings.PanelUpdate(nodes[0], tag)
		}()
		return
	}

	// bare-metal: swap the binary in place.
	s.st.Audit(u.Username, "update_trigger", "panel", s.updateURL)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applying": true, "component": "kgpanel", "mode": "binary",
	})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the response reach the client
		update.Apply(s.updateURL)
		// Apply calls os.Exit on success; reaching here means no update needed.
	}()
}

// handleUpdateStatus reports whether a wings-driven panel update is running
// and the outcome of the last one. GET /api/update/status (admin).
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	nodes := s.st.ListNodes()
	if len(nodes) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"mode": "binary", "running": false, "last_ok": false,
		})
		return
	}
	running, ok, tag, errMsg, err := s.wings.PanelUpdateStatus(nodes[0])
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"mode": "wings-docker", "running": false, "last_ok": false, "last_error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode": "wings-docker", "running": running, "last_ok": ok,
		"last_tag": tag, "last_error": errMsg,
	})
}

// handleRollback recreates the panel container from the <image>-previous
// snapshot wings made before the last update. POST /api/rollback (admin).
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
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
	nodes := s.st.ListNodes()
	if len(nodes) == 0 {
		writeErr(w, http.StatusBadRequest, "no node registered — nothing to roll back")
		return
	}
	s.st.Audit(u.Username, "rollback", "panel", nodes[0].Name)
	// respond first; wings swaps the container (the panel itself goes down).
	writeJSON(w, http.StatusOK, map[string]interface{}{"applying": true, "mode": "wings-docker"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = s.wings.PanelRollback(nodes[0])
	}()
}
