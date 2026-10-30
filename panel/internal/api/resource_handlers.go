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
		EggName  string `json:"egg_name"`
	}
	rows := make([]row, 0, len(servers))
	for _, sv := range servers {
		rw := row{Server: sv}
		if n := s.st.GetNode(sv.NodeID); n != nil {
			rw.NodeName = n.Name
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
	s.pushServerConfig(&in)
	s.st.Audit(in.OwnerID, "server_create", in.Name, "")
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": in.ID})
}

// pushServerConfig resolves the server's egg + node and pushes the container
// spec to wings. Failures are logged, not fatal — a wings that's briefly down
// must not block server creation (the spec is re-pushed on start as a fallback).
func (s *Server) pushServerConfig(sv *store.Server) {
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
	}
	if err := s.wings.ServerConfig(n, sv.ID, sp); err != nil {
		log.Printf("pushServerConfig %s: %v", sv.Name, err)
	}
}
