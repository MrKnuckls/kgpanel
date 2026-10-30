package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kgpanel/panel/internal/auth"
	"kgpanel/panel/internal/store"
	"kgpanel/panel/internal/update"
	"kgpanel/panel/internal/wingsclient"
)

type Server struct {
	st        *store.Store
	secret    string
	wings     *wingsclient.Client
	updateURL string
}

func New(st *store.Store, secret string) *Server {
	return &Server{st: st, secret: secret, wings: wingsclient.New(), updateURL: update.DefaultFeed}
}

// SetUpdateURL overrides the release feed (env KGPANEL_UPDATE_URL).
func (s *Server) SetUpdateURL(u string) {
	if u != "" {
		s.updateURL = u
	}
}

// userFromRequest resolves the authenticated user id (set by middleware) or "".
// We store it in a context value only in the authenticated branch; here we read
// it via the middleware-set header to keep things dependency-free.
func userFromRequest(r *http.Request) string {
	return r.Header.Get("X-KG-User")
}

// permRank maps a permission label to a comparable rank.
func permRank(p string) int {
	switch p {
	case "admin":
		return 3
	case "control":
		return 2
	case "view":
		return 1
	}
	return 0
}

// actorPerm returns the caller's permission on a server: panel users (subjects
// without the "sub:" prefix) are implicitly admin; subusers get their stored
// permission for that server, or "" if they're not on it.
func (s *Server) actorPerm(r *http.Request, serverID string) string {
	uid := userFromRequest(r)
	if !strings.HasPrefix(uid, "sub:") {
		return "admin"
	}
	return s.st.SubUserPerm(serverID, strings.TrimPrefix(uid, "sub:"))
}

// requirePerm checks the caller has at least min permission on the server in
// the URL path; on failure it writes 403 and returns false.
func (s *Server) requirePerm(w http.ResponseWriter, r *http.Request, min string) bool {
	serverID := r.PathValue("id")
	got := s.actorPerm(r, serverID)
	if permRank(got) < permRank(min) {
		writeErr(w, http.StatusForbidden, "insufficient permission")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// --- setup (one-time admin bootstrap) ---

// handleSetupStatus reports whether first-run setup is needed.
// GET /api/setup/status -> {"setup_required": bool}
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"setup_required": s.st.UserCount() == 0,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.st.UserCount() > 0 {
		writeErr(w, http.StatusConflict, "setup already completed")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Username == "" || len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "username required, password >= 8 chars")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash failed")
		return
	}
	u := &store.User{Username: in.Username, PasswordHash: hash, Admin: true, CreatedAt: time.Now()}
	if err := s.st.CreateUser(u); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	s.st.Audit(u.Username, "setup", "panel", "bootstrap admin created")
	tok, _ := auth.Sign(s.secret, u.ID)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok, "username": u.Username})
}

// --- login ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	u := s.st.GetUserByUsername(in.Username)
	// constant-ish fallback: always do one hash compare even if user missing
	var fallbackHash, _ = auth.HashPassword("nope")
	if u == nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		_ = auth.CheckPassword(fallbackHash, in.Password)
		if u == nil {
			// a subuser logs in with their own username+password; their token
			// subject is sub:<username> so permission checks resolve per-server.
			if _, su, ok := s.st.FindSubUser(in.Username); ok && auth.CheckPassword(su.PasswordHash, in.Password) {
				s.st.Audit(su.Username, "subuser_login", "", "web login")
				tok, _ := auth.Sign(s.secret, "sub:"+su.Username)
				writeJSON(w, http.StatusOK, map[string]string{"token": tok, "username": su.Username, "subuser": "true"})
				return
			}
		}
		s.st.Audit(in.Username, "login_failed", "panel", "bad credentials")
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.st.Audit(u.Username, "login", "panel", "web login")
	tok, _ := auth.Sign(s.secret, u.ID)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok, "username": u.Username})
}

// --- me ---

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	uid := userFromRequest(r)
	if strings.HasPrefix(uid, "sub:") {
		uname := strings.TrimPrefix(uid, "sub:")
		// subusers may be on several servers; list them so the UI can show
		// only what they can reach.
		type view struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Permission string `json:"permission"`
		}
		var servers []view
		for _, sv := range s.st.ListServers() {
			if p := s.st.SubUserPerm(sv.ID, uname); p != "" {
				servers = append(servers, view{ID: sv.ID, Name: sv.Name, Permission: p})
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id": uid, "username": uname, "admin": false, "subuser": true, "servers": servers,
		})
		return
	}
	u := s.st.GetUser(uid)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "no such user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": u.ID, "username": u.Username, "admin": u.Admin, "created_at": u.CreatedAt,
	})
}
