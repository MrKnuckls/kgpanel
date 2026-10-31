// Package files serves server file operations. Files live under
// <dataDir>/servers/<serverID>/files/. Every path is resolved and checked to
// stay inside the server root (path traversal protection).
package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Root returns the safe root directory for a server, creating it if needed.
func Root(dataDir, serverID string) (string, error) {
	root := filepath.Join(dataDir, "servers", serverID, "files")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

// Wipe removes everything under the server's files root (reinstall path).
// The root itself stays; only contents go. ServerID comes from the panel's
// URL path, and root is always <dataDir>/servers/<id>/files, so this can
// never touch anything outside a server dir.
func Wipe(dataDir, serverID string) error {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return err
	}
	des, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, de := range des {
		if err := os.RemoveAll(filepath.Join(root, de.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Resolve validates server-relative rel against the server's files root and
// returns the absolute path. Used by the SFTP server for the same
// escape-proofing the HTTP file manager relies on.
func Resolve(dataDir, serverID, rel string) (string, error) {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return "", err
	}
	return safePath(root, rel)
}

// safePath resolves rel (server-relative, e.g. "/worlds/x.dat") inside root
// and rejects anything that escapes. Empty/"/" => root itself.
func safePath(root, rel string) (string, error) {
	if rel == "" {
		rel = "/"
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("path escapes server root")
	}
	return abs, nil
}

func List(dataDir, serverID, rel string) ([]Entry, string, error) {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return nil, "", err
	}
	p, err := safePath(root, rel)
	if err != nil {
		return nil, "", err
	}
	infos, err := os.ReadDir(p)
	if err != nil {
		return nil, "", err
	}
	sort.Slice(infos, func(i, j int) bool {
		di, dj := infos[i].IsDir(), infos[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(infos[i].Name()) < strings.ToLower(infos[j].Name())
	})
	entries := make([]Entry, 0, len(infos))
	for _, in := range infos {
		fi, err := in.Info()
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:     in.Name(),
			Dir:      in.IsDir(),
			Size:     fi.Size(),
			Modified: fi.ModTime(),
		})
	}
	disp := rel
	if disp == "" {
		disp = "/"
	}
	return entries, disp, nil
}

// Read returns a handle to a file inside the server root (nil error means ok).
func Read(dataDir, serverID, rel string) (io.ReadSeekCloser, int64, error) {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return nil, 0, err
	}
	p, err := safePath(root, rel)
	if err != nil {
		return nil, 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, 0, err
	}
	if fi.IsDir() {
		return nil, 0, errors.New("is a directory")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func Write(dataDir, serverID, rel string, r io.Reader) error {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return err
	}
	p, err := safePath(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".kgtmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p) // atomic replace
}

func Mkdir(dataDir, serverID, rel string) error {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return err
	}
	p, err := safePath(root, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

func Delete(dataDir, serverID, rel string) error {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return err
	}
	p, err := safePath(root, rel)
	if err != nil {
		return err
	}
	if p == root {
		return errors.New("cannot delete server root")
	}
	return os.RemoveAll(p)
}

// Walk calls fn for every regular file under root (used by backups).
func Walk(dataDir, serverID string, fn func(path string, fi fs.FileInfo) error) error {
	root, err := Root(dataDir, serverID)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return fn(p, fi)
	})
}
