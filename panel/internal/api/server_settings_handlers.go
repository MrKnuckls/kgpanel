package api

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// --- SFTP details ---

// handleServerSFTP returns the connection details for the server's SFTP
// access (wings' built-in SSH/SFTP server). The password is generated on
// first fetch for pre-feature servers and pushed to wings with the spec.
// GET /api/servers/{id}/sftp (control+)
func (s *Server) handleServerSFTP(w http.ResponseWriter, r *http.Request) {
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
	pw, err := s.st.EnsureSFTPPassword(serverID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// make sure wings knows the password (spec may predate it)
	s.pushServerConfig(sv, false)

	host := nodeHost(n.URL)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"host":     host,
		"port":     "2022",
		"username": serverID,
		"password": pw,
		"path":     "/",
	})
}

// --- change server details ---

// handleServerDetails updates the display name / description (panel-only —
// the container name is the server id, so no wings push is needed).
// POST /api/servers/{id}/details (admin)
func (s *Server) handleServerDetails(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" && in.Description == "" {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if err := s.st.UpdateServerDetails(serverID, in.Name, in.Description); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "server_details", serverID, in.Name+" | "+in.Description)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- reinstall / repair ---

// handleServerReinstall asks wings to destroy + rebuild the server container.
// wipe_files=true clears the files dir (reinstall); false keeps files and only
// rebuilds the container (repair — install re-validates on next start).
// POST /api/servers/{id}/reinstall {wipe_files} (control+)
func (s *Server) handleServerReinstall(w http.ResponseWriter, r *http.Request) {
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
	var in struct {
		WipeFiles bool `json:"wipe_files"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	// re-push the spec first so wings always rebuilds from the latest egg
	// (image/startup may have changed in the panel since the last push)
	s.pushServerConfig(sv, false)
	if err := s.wings.Reinstall(n, serverID, in.WipeFiles); err != nil {
		log.Printf("reinstall %s: %v", sv.Name, err)
		msg := "wings unreachable: " + err.Error()
		if strings.Contains(err.Error(), "401") {
			msg = "wings rejected the token (restart kgpanel to reload node tokens)"
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	s.st.Audit(userFromRequest(r), map[bool]string{true: "server_reinstall", false: "server_repair"}[in.WipeFiles], serverID, sv.Name)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": "true", "wipe_files": in.WipeFiles})
}

// handleServerDelete permanently destroys a server: wings removes the
// container + files + database, then the panel forgets the record.
// DELETE /api/servers/{id} (admin+)
func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := s.wings.Destroy(n, serverID); err != nil {
		log.Printf("destroy %s: %v", sv.Name, err)
		msg := "wings unreachable: " + err.Error()
		if strings.Contains(err.Error(), "401") {
			msg = "wings rejected the token (restart kgpanel to reload node tokens)"
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	if err := s.st.DeleteServer(serverID); err != nil {
		writeErr(w, http.StatusInternalServerError, "store: "+err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "server_delete", serverID, sv.Name)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": "true"})
}

// nodeHost extracts the hostname/IP from a node URL (http://10.0.0.1:8080).
func nodeHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}
