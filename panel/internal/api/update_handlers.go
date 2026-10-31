package api

import (
	"encoding/json"
	"net/http"
	"time"

	"kgpanel/panel/internal/store"
	"kgpanel/panel/internal/update"
)

// handleUpdateCheck reports current vs latest from the release feed and
// whether this panel should self-update. GET /api/update/check (admin).
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	st, _ := update.Latest(s.updateURL)
	st.AutoUpdate = s.st.GetSettings().AutoUpdate
	writeJSON(w, http.StatusOK, st)
}

// handleUpdateAuto reads or sets the auto-update preference.
// GET  /api/update/auto -> {"auto_update":bool}
// POST /api/update/auto {"auto_update":true|false} (admin only)
func (s *Server) handleUpdateAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]interface{}{"auto_update": s.st.GetSettings().AutoUpdate})
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST or GET only")
		return
	}
	uid := userFromRequest(r)
	u := s.st.GetUser(uid)
	if u == nil || !u.Admin {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	var req struct{ AutoUpdate bool `json:"auto_update"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	if err := s.st.SetAutoUpdate(req.AutoUpdate); err != nil {
		writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	s.st.Audit(u.Username, "auto_update", "panel", map[bool]string{true: "on", false: "off"}[req.AutoUpdate])
	writeJSON(w, http.StatusOK, map[string]interface{}{"auto_update": req.AutoUpdate})
}

// startAutoUpdater checks the release feed on a timer and applies new
// releases when auto-update is enabled. It shares the same trigger path as
// the Apply button, so on docker installs wings rebuilds and recreates the
// panel container; on bare metal the binary is swapped in place.
func (s *Server) StartAutoUpdater() {
	s.startAutoUpdater()
}

func (s *Server) startAutoUpdater() {
	go func() {
		tick := time.NewTicker(30 * time.Minute)
		defer tick.Stop()
		s.checkAutoUpdate() // once shortly after boot
		for range tick.C {
			s.checkAutoUpdate()
		}
	}()
}

func (s *Server) checkAutoUpdate() {
	if !s.st.GetSettings().AutoUpdate {
		return
	}
	st, err := update.Latest(s.updateURL)
	if err != nil || !st.UpdateNeeded {
		return
	}
	if nodes := s.st.ListNodes(); len(nodes) > 0 {
		s.triggerUpdate(st.Latest, nodes[0], "auto-update")
		return
	}
	s.triggerBareMetalUpdate("auto-update")
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
		var tag string
		if st, err := update.Latest(s.updateURL); err == nil {
			tag = st.Latest
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"applying": true, "component": "kgpanel", "mode": "wings-docker", "tag": tag,
		})
		go func() {
			time.Sleep(300 * time.Millisecond) // let the response reach the client
			s.triggerUpdate(tag, nodes[0], u.Username)
		}()
		return
	}

	// bare-metal: swap the binary in place.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applying": true, "component": "kgpanel", "mode": "binary",
	})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the response reach the client
		s.triggerBareMetalUpdate(u.Username)
	}()
}

// triggerUpdate delegates a panel update to wings (rebuild + recreate the
// panel container from the release source). Shared by the Apply button and
// the auto-update loop.
func (s *Server) triggerUpdate(tag string, node *store.Node, actor string) {
	s.st.Audit(actor, "update_trigger", "panel", "wings:"+node.Name)
	_ = s.wings.PanelUpdate(node, tag)
}

// triggerBareMetalUpdate swaps the panel binary in place (non-docker installs).
func (s *Server) triggerBareMetalUpdate(actor string) {
	s.st.Audit(actor, "update_trigger", "panel", s.updateURL)
	update.Apply(s.updateURL) // Apply calls os.Exit on success
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
