package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"kgpanel/panel/internal/auth"
	"kgpanel/panel/internal/store"
)

// handleServerActivity returns audit entries touching this server (newest first).
func (s *Server) handleServerActivity(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	if s.st.GetServer(serverID) == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	n := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 500 {
			n = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"activity": s.st.AuditForServer(serverID, n)})
}

// handleSubUsers lists the server's subusers (never the password hashes).
func (s *Server) handleSubUsers(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	sv := s.st.GetServer(serverID)
	if sv == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	clean := make([]map[string]interface{}, 0, len(sv.SubUsers))
	for _, u := range sv.SubUsers {
		clean = append(clean, map[string]interface{}{
			"id":         u.ID,
			"username":   u.Username,
			"permission": u.Permission,
			"created_at": u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": clean})
}

// handleSubUserCreate adds a subuser to a server.
func (s *Server) handleSubUserCreate(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	var in struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		Permission string `json:"permission"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, http.StatusBadRequest, "username required")
		return
	}
	if len(in.Password) < 4 {
		writeErr(w, http.StatusBadRequest, "password must be at least 4 characters")
		return
	}
	switch in.Permission {
	case "view", "control", "admin":
	default:
		writeErr(w, http.StatusBadRequest, "permission must be view|control|admin")
		return
	}
	hash, herr := auth.HashPassword(in.Password)
	if herr != nil {
		writeErr(w, http.StatusInternalServerError, "password hashing failed")
		return
	}
	su := &store.SubUser{Username: in.Username, Permission: in.Permission, PasswordHash: hash}
	if err := s.st.AddSubUser(serverID, su); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "subuser_add", serverID, in.Username)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": su.ID})
}

// handleSubUserDelete removes a subuser from a server.
func (s *Server) handleSubUserDelete(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	subID := r.PathValue("sid")
	sv := s.st.GetServer(serverID)
	if sv == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	for _, x := range sv.SubUsers {
		if x.ID == subID {
			_ = s.st.RemoveSubUser(serverID, subID)
			s.st.Audit(userFromRequest(r), "subuser_del", serverID, x.Username)
			writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "no such user")
}
