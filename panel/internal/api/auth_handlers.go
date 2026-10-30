package api

import (
	"encoding/json"
	"net/http"
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

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// --- setup (one-time admin bootstrap) ---

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
	u := s.st.GetUser(uid)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "no such user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": u.ID, "username": u.Username, "admin": u.Admin, "created_at": u.CreatedAt,
	})
}
