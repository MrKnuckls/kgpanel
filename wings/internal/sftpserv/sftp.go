// Package sftpserv runs wings' built-in SSH/SFTP server. Clients log in with
// the server ID as username and the per-server SFTP password from the server's
// config.json (panel sets it at server create). Each session is chrooted to
// the server's files directory via the same rooted-path guard the file
// manager uses — SFTP can never escape the server root.
package sftpserv

import (
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"kgpanel/wings/internal/files"
	"kgpanel/wings/internal/srvconfig"
)

// passwordLookup returns the stored SFTP password for a serverID, or an error
// if the server has no spec / no password set.
type passwordLookup func(serverID string) (string, error)

func storedPassword(dataDir, serverID string) (string, error) {
	sp, err := srvconfig.Load(dataDir, serverID)
	if err != nil {
		return "", err
	}
	if sp.SFTPPassword == "" {
		return "", errors.New("sftp password not set")
	}
	return sp.SFTPPassword, nil
}

// Server is an SSH/SFTP listener. It serves concurrently on one port.
type Server struct {
	addr    string
	dataDir string
	cfg     ssh.ServerConfig
	ln      net.Listener
	wg      sync.WaitGroup
	closed  chan struct{}
}

// New creates the SSH server (not yet listening). hostKeyFile is a PEM private
// key (RSA/ECDSA/Ed25519) the server uses for the host key.
func New(addr, dataDir, hostKeyFile string) (*Server, error) {
	keyData, err := os.ReadFile(hostKeyFile)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, err
	}
	s := &Server{
		addr:    addr,
		dataDir: dataDir,
		closed:  make(chan struct{}),
	}
	s.cfg = ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			user := c.User()
			// username is the server UUID
			if strings.TrimSpace(user) == "" || strings.ContainsAny(user, "/\\.: ") {
				return nil, errors.New("bad username")
			}
			want, err := storedPassword(s.dataDir, user)
			if err != nil || !secureEqual(want, string(pass)) {
				return nil, errors.New("invalid credentials")
			}
			return nil, nil
		},
	}
	s.cfg.AddHostKey(signer)
	return s, nil
}

// Listen binds the port and starts serving. Returns immediately; errors after
// bind (per-connection) are logged, not fatal.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln
	log.Printf("SFTP listening on %s (data: %s)", s.addr, s.dataDir)
	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) handleConn(conn net.Conn) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, &s.cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	serverID := sconn.User()
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range chReqs {
				// sftp subsystem request is handled below; others (shell, exec,
				// pty) are rejected so this port is SFTP-only.
				ok := false
				if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
					ok = true
				}
				_ = req.Reply(ok, nil)
			}
		}()
		root, err := files.Root(s.dataDir, serverID)
		if err != nil {
			_ = ch.Close()
			continue
		}
		h := &rootFS{root: root, dataDir: s.dataDir, serverID: serverID}
		server := sftp.NewRequestServer(ch, sftp.Handlers{
			FileGet:  h,
			FilePut:  h,
			FileCmd:  h,
			FileList: h,
		}, sftp.WithStartDirectory("/"))
		if err := server.Serve(); err != nil && err != io.EOF {
			log.Printf("sftp %s: %v", serverID, err)
		}
		_ = ch.Close()
	}
}

// Close stops the listener and waits for active sessions to finish.
func (s *Server) Close() {
	close(s.closed)
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.wg.Wait()
}

// secureEqual is a constant-time string compare (avoids timing oracle on the
// password check; overkill here but cheap).
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// --- chrooted filesystem backed by the panel's own files package ---

// rootFS implements sftp.Handlers (FileGet/FilePut/FileCmd) rooted at the
// server's files dir. All paths are validated by files.safePath — traversal
// attempts return errors instead of escaping.
type rootFS struct {
	root     string
	dataDir  string
	serverID string
}

func (f *rootFS) resolve(rel string) (string, error) {
	abs, err := files.Resolve(f.dataDir, f.serverID, rel)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func (f *rootFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	abs, err := f.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

func (f *rootFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	abs, err := f.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	// always truncate: uploads are offset-based WriteAt calls, and O_APPEND
	// conflicts with WriteAt semantics
	return os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}

func (f *rootFS) Filecmd(r *sftp.Request) error {
	abs, err := f.resolve(r.Filepath)
	if err != nil {
		return err
	}
	switch r.Method {
	case "Setstat":
		if r.Attributes().Size != 0 {
			return os.Truncate(abs, int64(r.Attributes().Size))
		}
		return nil
	case "Rename":
		to, err := f.resolve(r.Target)
		if err != nil {
			return err
		}
		return os.Rename(abs, to)
	case "Rmdir":
		return os.RemoveAll(abs)
	case "Remove":
		return os.Remove(abs)
	case "Mkdir":
		return os.MkdirAll(abs, 0o755)
	}
	return errors.New("unsupported filecmd " + r.Method)
}

func (f *rootFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	abs, err := f.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	switch r.Method {
	case "Stat", "Lstat":
		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	case "List": // directory listing (opendir)
		des, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(des))
		for _, de := range des {
			fi, err := de.Info()
			if err != nil {
				continue
			}
			infos = append(infos, fi)
		}
		return listerAt(infos), nil
	}
	return nil, errors.New("unsupported filelist " + r.Method)
}

// listerAt is an in-memory slice of os.FileInfo satisfying sftp's ListAt
// contract (offset-based; io.EOF past the end).
type listerAt []os.FileInfo

func (l listerAt) ListAt(ls []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[off:])
	return n, nil
}
