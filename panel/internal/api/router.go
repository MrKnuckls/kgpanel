package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	nhooyr "golang.org/x/net/websocket"

	"kgpanel/panel/internal/auth"
	"kgpanel/panel/internal/web"
	"kgpanel/panel/internal/wingsclient"
)

func decodeJSON(r *http.Request, dst interface{}) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// Handler builds the full mux: API under /api, embedded UI under /, with
// auth middleware on everything under /api except setup + login.
func (s *Server) Handler(ui http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("/api/login", s.handleLogin)

	// protected API
	mux.Handle("/api/me", s.auth(http.HandlerFunc(s.handleMe)))
	mux.Handle("/api/nodes", s.auth(http.HandlerFunc(s.handleNodes)))
	mux.Handle("/api/nodes/create", s.auth(http.HandlerFunc(s.handleCreateNode)))
	mux.Handle("/api/eggs", s.auth(http.HandlerFunc(s.handleEggs)))
	mux.Handle("/api/eggs/create", s.auth(http.HandlerFunc(s.handleCreateEgg)))
	mux.Handle("/api/eggs/update", s.auth(http.HandlerFunc(s.handleUpdateEgg)))
	mux.Handle("/api/servers", s.auth(http.HandlerFunc(s.handleServers)))
	mux.Handle("/api/servers/create", s.auth(http.HandlerFunc(s.handleCreateServer)))
	mux.Handle("/api/servers/{id}/power", s.auth(s.perm("control", http.HandlerFunc(s.handlePower))))
	mux.Handle("/api/servers/{id}/settings", s.auth(s.perm("admin", http.HandlerFunc(s.handleServerSettings))))
	mux.Handle("/api/servers/{id}/status", s.auth(s.perm("view", http.HandlerFunc(s.handleStatus))))
	mux.Handle("/api/servers/{id}/metrics", s.auth(s.perm("view", http.HandlerFunc(s.handleMetrics))))
	mux.Handle("/api/servers/{id}/files", s.auth(s.perm("view", http.HandlerFunc(s.handleFilesList))))
	mux.Handle("/api/servers/{id}/files/upload", s.auth(s.perm("control", http.HandlerFunc(s.handleFilesUpload))))
	mux.Handle("/api/servers/{id}/files/mkdir", s.auth(s.perm("control", http.HandlerFunc(s.handleFilesMkdir))))
	mux.Handle("/api/servers/{id}/files/delete", s.auth(s.perm("control", http.HandlerFunc(s.handleFilesDelete))))
	mux.Handle("/api/servers/{id}/files/download", s.auth(s.perm("view", http.HandlerFunc(s.handleFilesDownload))))
	mux.Handle("/api/servers/{id}/backups", s.auth(s.perm("view", http.HandlerFunc(s.handleBackupsList))))
	mux.Handle("/api/servers/{id}/backups/create", s.auth(s.perm("control", http.HandlerFunc(s.handleBackupsCreate))))
	mux.Handle("/api/servers/{id}/backups/delete", s.auth(s.perm("control", http.HandlerFunc(s.handleBackupsDelete))))
	mux.Handle("/api/servers/{id}/backups/restore", s.auth(s.perm("control", http.HandlerFunc(s.handleBackupsRestore))))
	mux.Handle("/api/servers/{id}/backups/download", s.auth(s.perm("view", http.HandlerFunc(s.handleBackupsDownload))))
	mux.Handle("/api/servers/{id}/schedules", s.auth(s.perm("view", http.HandlerFunc(s.handleSchedulesList))))
	mux.Handle("/api/servers/{id}/schedules/create", s.auth(s.perm("control", http.HandlerFunc(s.handleSchedulesCreate))))
	mux.Handle("/api/servers/{id}/schedules/{sid}/delete", s.auth(s.perm("control", http.HandlerFunc(s.handleSchedulesDelete))))
	mux.Handle("/api/servers/{id}/activity", s.auth(s.perm("view", http.HandlerFunc(s.handleServerActivity))))
	mux.Handle("/api/servers/{id}/users", s.auth(s.perm("admin", http.HandlerFunc(s.handleSubUsers))))
	mux.Handle("/api/servers/{id}/users/create", s.auth(s.perm("admin", http.HandlerFunc(s.handleSubUserCreate))))
	mux.Handle("/api/servers/{id}/users/{sid}/delete", s.auth(s.perm("admin", http.HandlerFunc(s.handleSubUserDelete))))
	mux.Handle("/api/servers/{id}/sftp", s.auth(s.perm("control", http.HandlerFunc(s.handleServerSFTP))))
	mux.Handle("/api/servers/{id}/details", s.auth(s.perm("admin", http.HandlerFunc(s.handleServerDetails))))
	mux.Handle("/api/servers/{id}/reinstall", s.auth(s.perm("control", http.HandlerFunc(s.handleServerReinstall))))
	mux.Handle("/api/audit", s.auth(http.HandlerFunc(s.handleAudit)))
	mux.Handle("/api/update/check", s.auth(http.HandlerFunc(s.handleUpdateCheck)))
	mux.Handle("/api/update/status", s.auth(http.HandlerFunc(s.handleUpdateStatus)))
	mux.Handle("/api/update", s.auth(http.HandlerFunc(s.handleUpdate)))
	mux.Handle("/api/rollback", s.auth(http.HandlerFunc(s.handleRollback)))

	// websocket console proxy: browser -> panel -> wings
	mux.Handle("/api/console/{id}", s.auth(nhooyr.Handler(s.handleConsoleProxy)))

	// UI: gate the root — show the login/setup page until authenticated, then
	// the dashboard. The embedded FileServer (ui) serves everything else.
	mux.Handle("/", s.gateUI(ui))
	return logRequests(mux)
}

// gateUI wraps the static file server so / and /index.html only render the
// dashboard for authenticated users; everyone else gets login.html (which is
// also the first-run setup wizard). Other static assets (/css, /js, /img,
// /login.html) pass straight through so the login page can load.
func (s *Server) gateUI(ui http.Handler) http.Handler {
	index := web.Page("index.html")
	login := web.Page("login.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index", "/index.html":
			page := login
			if s.authenticated(r) {
				page = index
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		default:
			ui.ServeHTTP(w, r)
		}
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	tok := auth.Bearer(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" {
		if c, err := r.Cookie("kg_token"); err == nil {
			tok = c.Value
		}
	}
	_, err := auth.Verify(s.secret, tok)
	return err == nil
}

// auth middleware verifies the bearer token and stamps the user id header
// that handlers read via userFromRequest.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := auth.Bearer(r)
		if tok == "" && r.Method == http.MethodGet {
			// allow ?token= for websocket handshakes that cannot set headers
			tok = r.URL.Query().Get("token")
		}
		uid, err := auth.Verify(s.secret, tok)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Header.Set("X-KG-User", uid)
		next.ServeHTTP(w, r)
	})
}

// perm wraps a handler with a minimum permission check on the server in the
// URL path ({id}). Panel owners pass as admin; subusers need their stored perm.
func (s *Server) perm(min string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requirePerm(w, r, min) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

// handleConsoleProxy: browser opens a ws to /api/console/<id>?token=..., we
// open the matching wings console ws and bridge frames both ways.
func (s *Server) handleConsoleProxy(ws *nhooyr.Conn) {
	uid := ws.Request().Header.Get("X-KG-User")
	if uid == "" {
		_ = ws.Close()
		return
	}
	serverID := ws.Request().PathValue("id")
	sv := s.st.GetServer(serverID)
	if sv == nil {
		_ = ws.Close()
		return
	}
	if permRank(s.actorPerm(ws.Request(), serverID)) < permRank("view") {
		_ = ws.Close()
		return
	}
	n := s.st.GetNode(sv.NodeID)
	if n == nil {
		_ = ws.Close()
		return
	}
	// Connect to wings with the node token via query param (wings reads header
	// or query; our proxy passes the node token, never the user token).
	client := wingsclient.New()
	up, err := nhooyr.Dial(client.ConsoleURL(n, serverID)+"?token="+n.Token, "", client.ConsoleURL(n, serverID))
	if err != nil {
		_ = ws.Close()
		return
	}
	defer up.Close()
	go func() {
		// wings -> browser
		buf := make([]byte, 8192)
		for {
			m, err := up.Read(buf)
			if err != nil {
				_ = ws.Close()
				return
			}
			if _, err := ws.Write(buf[:m]); err != nil {
				return
			}
		}
	}()
	// browser -> wings
	buf := make([]byte, 8192)
	for {
		m, err := ws.Read(buf)
		if err != nil {
			return
		}
		if _, err := up.Write(buf[:m]); err != nil {
			return
		}
	}
}
