package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"kgpanel/panel/internal/store"
	"kgpanel/panel/internal/wingsclient"
)

// handleNodes lists nodes. The wings token is masked on the wire.
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes := s.st.ListNodes()
	for _, n := range nodes {
		if n.Token != "" {
			n.Token = "••••••••"
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"nodes": nodes})
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var in store.Node
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Name == "" || in.URL == "" || in.Token == "" {
		writeErr(w, http.StatusBadRequest, "name, url and token required")
		return
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		writeErr(w, http.StatusBadRequest, "url must start with http(s)://")
		return
	}
	if err := s.st.CreateNode(&in); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	s.st.Audit(userFromRequest(r), "node_create", in.Name, in.URL)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": in.ID})
}

// handleEggs lists, handleCreateEgg adds an egg template.
func (s *Server) handleEggs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"eggs": s.st.ListEggs()})
}

func (s *Server) handleCreateEgg(w http.ResponseWriter, r *http.Request) {
	var in store.Egg
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Name == "" || in.Image == "" || in.Startup == "" {
		writeErr(w, http.StatusBadRequest, "name, image and startup required")
		return
	}
	if err := s.st.CreateEgg(&in); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	s.st.Audit(userFromRequest(r), "egg_create", in.Name, "")
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": in.ID})
}

// handleServers lists servers; join node/egg names for the UI.
func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	servers := s.st.ListServers()
	type row struct {
		*store.Server
		NodeName string `json:"node_name"`
		NodeURL  string `json:"node_url"`
		EggName  string `json:"egg_name"`
	}
	rows := make([]row, 0, len(servers))
	for _, sv := range servers {
		rw := row{Server: sv}
		if n := s.st.GetNode(sv.NodeID); n != nil {
			rw.NodeName = n.Name
			rw.NodeURL = n.URL
		}
		if e := s.st.GetEgg(sv.EggID); e != nil {
			rw.EggName = e.Name
		}
		rows = append(rows, rw)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"servers": rows})
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var in store.Server
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Name == "" || in.NodeID == "" || in.EggID == "" {
		writeErr(w, http.StatusBadRequest, "name, node and egg required")
		return
	}
	if in.MemoryMB <= 0 {
		writeErr(w, http.StatusBadRequest, "memory_mb required")
		return
	}
	in.OwnerID = userFromRequest(r)
	if err := s.st.CreateServer(&in); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	// push the container spec to wings so it can create the container on start
	s.pushServerConfig(&in, false)
	s.st.Audit(in.OwnerID, "server_create", in.Name, "")
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": in.ID})
}

// pushServerConfig resolves the server's egg + node and pushes the container
// spec to wings. Failures are logged, not fatal — a wings that's briefly down
// must not block server creation (the spec is re-pushed on start as a fallback).
// recreate forces wings to rebuild the container (for port/limit/image changes).
func (s *Server) pushServerConfig(sv *store.Server, recreate bool) {
	egg := s.st.GetEgg(sv.EggID)
	if egg == nil {
		return
	}
	n := s.st.GetNode(sv.NodeID)
	if n == nil {
		return
	}
	sp := wingsclient.ServerSpec{
		Image:      egg.Image,
		Startup:    egg.Startup,
		StopCmd:    egg.StopCommand,
		Env:        sv.Env,
		Ports:      sv.Ports,
		MemoryMB:   sv.MemoryMB,
		CPUPercent: sv.CPUPercent,
		Recreate:   recreate,
	}
	if err := s.wings.ServerConfig(n, sv.ID, sp); err != nil {
		log.Printf("pushServerConfig %s: %v", sv.Name, err)
	}
}

// handleServerSettings updates a server's own settings (memory, cpu, ports, env)
// and pushes the new spec to wings with recreate so the running container picks
// up port/limit changes.
func (s *Server) handleServerSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		MemoryMB   int               `json:"memory_mb"`
		CPUPercent int               `json:"cpu_percent"`
		Ports      []string          `json:"ports"`
		Env        map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.st.UpdateServer(id, func(sv *store.Server) {
		if in.MemoryMB > 0 {
			sv.MemoryMB = in.MemoryMB
		}
		if in.CPUPercent >= 0 {
			sv.CPUPercent = in.CPUPercent
		}
		if in.Ports != nil {
			sv.Ports = in.Ports
		}
		if in.Env != nil {
			sv.Env = in.Env
		}
	}); err != nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	sv := s.st.GetServer(id)
	s.pushServerConfig(sv, true)
	s.st.Audit(userFromRequest(r), "server_settings", sv.Name, "memory/cpu/ports/env updated")
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleUpdateEgg edits an existing egg template (image, startup, ports, vars).
func (s *Server) handleUpdateEgg(w http.ResponseWriter, r *http.Request) {
	var in store.Egg
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	id := in.ID
	if id == "" || in.Name == "" || in.Image == "" || in.Startup == "" {
		writeErr(w, http.StatusBadRequest, "id, name, image and startup required")
		return
	}
	if err := s.st.UpdateEgg(id, func(e *store.Egg) {
		e.Name = in.Name
		e.Description = in.Description
		e.Image = in.Image
		e.Startup = in.Startup
		e.StopCommand = in.StopCommand
		e.Variables = in.Variables
		e.Ports = in.Ports
	}); err != nil {
		writeErr(w, http.StatusNotFound, "egg not found")
		return
	}
	// push the updated spec to every server that uses this egg so the change
	// propagates (recreate=false — port changes to an egg should apply on next
	// start; for immediate effect the user edits server settings).
	for _, sv := range s.st.ListServers() {
		if sv.EggID == id {
			s.pushServerConfig(sv, false)
		}
	}
	s.st.Audit(userFromRequest(r), "egg_update", in.Name, "")
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
