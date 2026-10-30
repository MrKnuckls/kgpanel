// Package backups creates and manages server backups as tar.gz archives.
// Backups live under <dataDir>/servers/<serverID>/backups/ and are created
// from the server's files/ directory (which is also where they're stored, so
// a backup never archives other backups).
package backups

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Info struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

func dir(dataDir, serverID string) (string, error) {
	d := filepath.Join(dataDir, "servers", serverID, "backups")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

func filesDir(dataDir, serverID string) string {
	return filepath.Join(dataDir, "servers", serverID, "files")
}

func List(dataDir, serverID string) ([]Info, error) {
	d, err := dir(dataDir, serverID)
	if err != nil {
		return nil, err
	}
	infos, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(infos))
	for _, in := range infos {
		if in.IsDir() || !strings.HasSuffix(in.Name(), ".tar.gz") {
			continue
		}
		fi, err := in.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{Name: in.Name(), Size: fi.Size(), Created: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func safeName(d, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return "", errors.New("invalid backup name")
	}
	p := filepath.Join(d, name)
	if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(d)+string(os.PathSeparator)) {
		return "", errors.New("invalid backup name")
	}
	return p, nil
}

// Create archives the server's files into <name>.tar.gz (gzip, deterministic
// mtimes). Returns the created info.
func Create(dataDir, serverID, name string) (Info, error) {
	d, err := dir(dataDir, serverID)
	if err != nil {
		return Info{}, err
	}
	if name == "" {
		name = "backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		name += ".tar.gz"
	}
	dst, err := safeName(d, name)
	if err != nil {
		return Info{}, err
	}
	src := filesDir(dataDir, serverID)
	out, err := os.Create(dst)
	if err != nil {
		return Info{}, err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, "..") {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(fi, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = fi.ModTime()
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !fi.IsDir() {
			f, ferr := os.Open(p)
			if ferr != nil {
				return ferr
			}
			_, cerr := io.Copy(tw, f)
			f.Close()
			return cerr
		}
		return nil
	})
	if err != nil {
		tw.Close()
		gz.Close()
		out.Close()
		os.Remove(dst)
		return Info{}, err
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		out.Close()
		os.Remove(dst)
		return Info{}, err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(dst)
		return Info{}, err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return Info{}, err
	}
	fi, _ := os.Stat(dst)
	return Info{Name: filepath.Base(dst), Size: fi.Size(), Created: fi.ModTime()}, nil
}

// Open returns a readable handle for a backup (for download). Also returns size.
func Open(dataDir, serverID, name string) (*os.File, int64, error) {
	d, err := dir(dataDir, serverID)
	if err != nil {
		return nil, 0, err
	}
	p, err := safeName(d, name)
	if err != nil {
		return nil, 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, 0, err
	}
	if fi.IsDir() {
		return nil, 0, errors.New("not a backup")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func Delete(dataDir, serverID, name string) error {
	d, err := dir(dataDir, serverID)
	if err != nil {
		return err
	}
	p, err := safeName(d, name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Restore wipes the current files/ and extracts the backup into it.
func Restore(dataDir, serverID, name string) error {
	d, err := dir(dataDir, serverID)
	if err != nil {
		return err
	}
	p, err := safeName(d, name)
	if err != nil {
		return err
	}
	dst := filesDir(dataDir, serverID)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// strip leading slashes/dot segments; never extract outside dst
		name := filepath.FromSlash(hdr.Name)
		name = strings.TrimPrefix(name, string(os.PathSeparator))
		if strings.HasPrefix(name, "..") || strings.Contains(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("backup contains unsafe path %q", hdr.Name)
		}
		target := filepath.Join(dst, name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) && target != filepath.Clean(dst) {
			return fmt.Errorf("backup path escapes root: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			w.Close()
		}
	}
	return nil
}
