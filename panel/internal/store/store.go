package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Types shared across the store. JSON-file backed for v1 (swap for SQL later
// behind this same package boundary).

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Admin        bool      `json:"admin"`
	CreatedAt    time.Time `json:"created_at"`
}

type Node struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"` // wings base url, e.g. http://10.15.34.36:8081
	Token  string `json:"token"`
	Online bool   `json:"online"`
}

type Egg struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	Startup     string   `json:"startup"`      // e.g. "java -Xmx{{MEM}}M -jar {{JAR}}"
	StopCommand string   `json:"stop_command"` // sent to console to stop cleanly
	Variables   []string `json:"variables"`    // var names in {{...}} form
	Ports       []string `json:"ports"`        // e.g. "25565/tcp"
	// Network joins game containers to a shared docker network (DB games use
	// the node's kggames-net to reach MariaDB at host "db"). Empty = bridge.
	Network string `json:"network,omitempty"`
	// DB requests a per-server database on the node's shared MariaDB; wings
	// provisions DB+user and injects KG_DB_* env vars into the container.
	DB bool `json:"db,omitempty"`
}

type SubUser struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Permission   string    `json:"permission"` // view | control | admin
	PasswordHash string    `json:"password_hash,omitempty"` // bcrypt; never re-serialized to the UI
	CreatedAt    time.Time `json:"created_at"`
}

type Server struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	OwnerID      string            `json:"owner_id"`
	NodeID       string            `json:"node_id"`
	EggID        string            `json:"egg_id"`
	MemoryMB     int               `json:"memory_mb"`
	CPUPercent   int               `json:"cpu_percent"`
	Ports        []string          `json:"ports"`
	Env          map[string]string `json:"env"`
	SubUsers     []SubUser         `json:"sub_users,omitempty"`
	SFTPPassword string            `json:"sftp_password,omitempty"`
	ContainerID  string            `json:"container_id,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type Schedule struct {
	ID        string    `json:"id"`
	ServerID  string    `json:"server_id"`
	Name      string    `json:"name"`
	Cron      string    `json:"cron"` // 5-field: min hour dom month dow
	Action    string    `json:"action"` // start | stop | restart
	Enabled   bool       `json:"enabled"`
	LastRun   *time.Time `json:"last_run"`
	LastError string     `json:"last_error"`
	CreatedAt time.Time  `json:"created_at"`
}

type AuditEntry struct {
	Time    time.Time `json:"time"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Detail  string    `json:"detail"`
}

// Store is the whole persistence layer. Files: users.json, nodes.json, eggs.json,
// servers.json, schedules.json, audit.json — rewritten atomically (tmp+rename)
// on each mutation.
type Store struct {
	mu        sync.Mutex
	dir       string
	users     map[string]*User
	nodes     map[string]*Node
	eggs      map[string]*Egg
	servers   map[string]*Server
	schedules map[string]*Schedule
	audit     []AuditEntry
	settings  Settings
}

// Settings are panel-wide preferences persisted to settings.json.
type Settings struct {
	AutoUpdate bool `json:"auto_update"` // check the feed on a timer and apply new releases automatically
}

// GetSettings returns a copy of the persisted panel settings.
func (s *Store) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// SetAutoUpdate persists the auto-update preference.
func (s *Store) SetAutoUpdate(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.AutoUpdate = on
	return s.save("settings.json", s.settings)
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:       dir,
		users:     map[string]*User{},
		nodes:     map[string]*Node{},
		eggs:      map[string]*Egg{},
		servers:   map[string]*Server{},
		schedules: map[string]*Schedule{},
		audit:     []AuditEntry{},
	}
	if err := s.load("users.json", &s.users); err != nil {
		return nil, err
	}
	if err := s.load("nodes.json", &s.nodes); err != nil {
		return nil, err
	}
	if err := s.load("eggs.json", &s.eggs); err != nil {
		return nil, err
	}
	if err := s.load("servers.json", &s.servers); err != nil {
		return nil, err
	}
	if err := s.load("schedules.json", &s.schedules); err != nil {
		return nil, err
	}
	if err := s.load("audit.json", &s.audit); err != nil {
		return nil, err
	}
	if err := s.load("settings.json", &s.settings); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load(name string, dst interface{}) error {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first boot
		}
		return err
	}
	return json.Unmarshal(b, dst)
}

func (s *Store) save(name string, src interface{}) error {
	b, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, name))
}

// --- users ---

func (s *Store) CreateUser(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u.ID = newID()
	s.users[u.ID] = u
	return s.save("users.json", s.users)
}

func (s *Store) GetUserByUsername(name string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Username == name {
			return u
		}
	}
	return nil
}

func (s *Store) GetUser(id string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users[id]
}

func (s *Store) UserCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

// --- nodes ---

func (s *Store) CreateNode(n *Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n.ID = newID()
	s.nodes[n.ID] = n
	return s.save("nodes.json", s.nodes)
}

func (s *Store) ListNodes() []*Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	return out
}

func (s *Store) GetNode(id string) *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodes[id]
}

// --- eggs ---

func (s *Store) CreateEgg(e *Egg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = newID()
	s.eggs[e.ID] = e
	return s.save("eggs.json", s.eggs)
}

func (s *Store) ListEggs() []*Egg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Egg, 0, len(s.eggs))
	for _, e := range s.eggs {
		out = append(out, e)
	}
	return out
}

func (s *Store) GetEgg(id string) *Egg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eggs[id]
}

// UpdateEgg applies fn to the egg with the given id and persists it.
func (s *Store) UpdateEgg(id string, fn func(*Egg)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.eggs[id]
	if !ok {
		return fmt.Errorf("egg %s not found", id)
	}
	fn(e)
	return s.save("eggs.json", s.eggs)
}

// --- servers ---

func (s *Store) CreateServer(sv *Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv.ID = newID()
	sv.CreatedAt = time.Now()
	s.servers[sv.ID] = sv
	return s.save("servers.json", s.servers)
}

func (s *Store) ListServers() []*Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Server, 0, len(s.servers))
	for _, sv := range s.servers {
		out = append(out, sv)
	}
	return out
}

func (s *Store) GetServer(id string) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.servers[id]
}

// UpdateServer applies fn to the server with the given id and persists it.
func (s *Store) UpdateServer(id string, fn func(*Server)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[id]
	if !ok {
		return fmt.Errorf("server %s not found", id)
	}
	fn(sv)
	return s.save("servers.json", s.servers)
}

// AddSubUser appends a server-scoped subuser (panel accounts residing on the
// server record; permission gates what they may do in the UI).
func (s *Store) AddSubUser(serverID string, su *SubUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return fmt.Errorf("server %s not found", serverID)
	}
	for _, x := range sv.SubUsers {
		if x.Username == su.Username {
			return fmt.Errorf("user %s already exists", su.Username)
		}
	}
	su.ID = newID()
	su.CreatedAt = time.Now()
	sv.SubUsers = append(sv.SubUsers, *su)
	return s.save("servers.json", s.servers)
}

// RemoveSubUser deletes a subuser by id from the server record.
func (s *Store) RemoveSubUser(serverID, subID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return fmt.Errorf("server %s not found", serverID)
	}
	for i, x := range sv.SubUsers {
		if x.ID == subID {
			sv.SubUsers = append(sv.SubUsers[:i], sv.SubUsers[i+1:]...)
			return s.save("servers.json", s.servers)
		}
	}
	return fmt.Errorf("no such user")
}

// SubUserPerm returns the subuser's permission on a server ("" if none).
// Panel owners always have "admin" implicitly — callers check that first.
func (s *Store) SubUserPerm(serverID, username string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return ""
	}
	for _, x := range sv.SubUsers {
		if x.Username == username {
			return x.Permission
		}
	}
	return ""
}

// FindSubUser returns the subuser record for a username on any server: for login
// (a subuser logs in with their own username) as (serverID, user, found).
func (s *Store) FindSubUser(username string) (string, SubUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sv := range s.servers {
		for _, x := range sv.SubUsers {
			if x.Username == username {
				return sv.ID, x, true
			}
		}
	}
	return "", SubUser{}, false
}

// ListSubUsersFor returns durable copies of a server's subusers.
func (s *Store) ListSubUsersFor(serverID string) []SubUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return nil
	}
	out := make([]SubUser, len(sv.SubUsers))
	copy(out, sv.SubUsers)
	return out
}

// UpdateServerDetails updates the display name and/or description. Empty
// fields are left unchanged (callers send only what they edit).
func (s *Store) UpdateServerDetails(serverID, name, desc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return fmt.Errorf("server %s not found", serverID)
	}
	if name != "" {
		sv.Name = name
	}
	if desc != "" {
		sv.Description = desc
	}
	return s.save("servers.json", s.servers)
}

// EnsureSFTPPassword returns the server's SFTP password, generating + saving
// one on first call (existing servers created before the feature get one
// lazily). Never serialized to the browser except via the /sftp endpoint.
func (s *Store) EnsureSFTPPassword(serverID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[serverID]
	if !ok {
		return "", fmt.Errorf("server %s not found", serverID)
	}
	if sv.SFTPPassword == "" {
		sv.SFTPPassword = randomPassword(16)
		if err := s.save("servers.json", s.servers); err != nil {
			return "", err
		}
	}
	return sv.SFTPPassword, nil
}

// randomPassword returns a URL-safe random string of n bytes (base64ish).
func randomPassword(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// AuditForServer returns the most recent audit entries targeting this server.
func (s *Store) AuditForServer(serverID string, n int) []AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditEntry, 0, n)
	for i := len(s.audit) - 1; i >= 0 && len(out) < n; i-- {
		// server actions record the id in Target (backups) or Detail (power) —
		// match either so Activity is reliable regardless of convention.
		if s.audit[i].Target == serverID || s.audit[i].Detail == serverID {
			out = append(out, s.audit[i])
		}
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Store) SetServerContainer(id, containerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.servers[id]
	if !ok {
		return fmt.Errorf("server %s not found", id)
	}
	sv.ContainerID = containerID
	return s.save("servers.json", s.servers)
}

// --- schedules ---

func (s *Store) CreateSchedule(sc *Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc.ID = newID()
	sc.CreatedAt = time.Now()
	sc.Enabled = true
	s.schedules[sc.ID] = sc
	return s.save("schedules.json", s.schedules)
}

func (s *Store) ListSchedules(serverID string) []*Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Schedule, 0, len(s.schedules))
	for _, sc := range s.schedules {
		if sc.ServerID == serverID {
			out = append(out, sc)
		}
	}
	return out
}

func (s *Store) GetSchedule(id string) *Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules[id]
}

func (s *Store) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.schedules, id)
	return s.save("schedules.json", s.schedules)
}

// MarkScheduleRun records the outcome of a schedule execution.
func (s *Store) MarkScheduleRun(id string, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.schedules[id]
	if !ok {
		return
	}
	now := time.Now()
	sc.LastRun = &now
	if runErr != nil {
		sc.LastError = runErr.Error()
	} else {
		sc.LastError = ""
	}
	_ = s.save("schedules.json", s.schedules)
}

// AllSchedules returns every schedule in the store (cron engine).
func (s *Store) AllSchedules() []*Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Schedule, 0, len(s.schedules))
	for _, sc := range s.schedules {
		out = append(out, sc)
	}
	return out
}

// --- audit ---

func (s *Store) Audit(actor, action, target, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, AuditEntry{Time: time.Now(), Actor: actor, Action: action, Target: target, Detail: detail})
	_ = s.save("audit.json", s.audit)
}

func (s *Store) RecentAudit(n int) []AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.audit) {
		n = len(s.audit)
	}
	return s.audit[len(s.audit)-n:]
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
