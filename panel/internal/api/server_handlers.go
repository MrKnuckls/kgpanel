package api

import (
	"net/http"
	"strconv"
)

// handleAudit returns the most recent audit entries (default 50).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	n := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 500 {
			n = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"audit": s.st.RecentAudit(n)})
}

// handlePower forwards a power signal to the server's node wings and audits it.
func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	sv := s.st.GetServer(serverID)
	if sv == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	var in struct {
		Signal string `json:"signal"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	switch in.Signal {
	case "start", "stop", "restart", "kill":
	default:
		writeErr(w, http.StatusBadRequest, "signal must be start|stop|restart|kill")
		return
	}
	n := s.st.GetNode(sv.NodeID)
	if n == nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	if err := s.wings.Power(n, serverID, in.Signal); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "power_"+in.Signal, sv.Name, serverID)
	writeJSON(w, http.StatusOK, map[string]string{"state": "sent"})
}

// handleStatus asks wings for the real container state (evidence-based).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	sv := s.st.GetServer(serverID)
	if sv == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	n := s.st.GetNode(sv.NodeID)
	if n == nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	state, err := s.wings.Status(n, serverID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": serverID, "state": state})
}
