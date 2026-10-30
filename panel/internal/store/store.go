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
}

type Server struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerID     string    `json:"owner_id"`
	NodeID      string    `json:"node_id"`
	EggID       string    `json:"egg_id"`
	MemoryMB    int       `json:"memory_mb"`
	CPUPercent  int       `json:"cpu_percent"`
	Ports       []string  `json:"ports"`
	Env         map[string]string `json:"env"`
	ContainerID string    `json:"container_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type AuditEntry struct {
	Time    time.Time `json:"time"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Detail  string    `json:"detail"`
}

// Store is the whole persistence layer. Files: users.json, nodes.json, eggs.json,
// servers.json, audit.json — rewritten atomically (tmp+rename) on each mutation.
type Store struct {
	mu      sync.Mutex
	dir     string
	users   map[string]*User
	nodes   map[string]*Node
	eggs    map[string]*Egg
	servers map[string]*Server
	audit   []AuditEntry
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:     dir,
		users:   map[string]*User{},
		nodes:   map[string]*Node{},
		eggs:    map[string]*Egg{},
		servers: map[string]*Server{},
		audit:   []AuditEntry{},
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
	if err := s.load("audit.json", &s.audit); err != nil {
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
