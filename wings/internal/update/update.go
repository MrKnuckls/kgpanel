// Package update implements wings self-update. Same design as the panel's
// updater: check the release feed, download the platform asset, verify sha256
// against checksums.txt, swap the binary and restart. Asset names use the
// wings- prefix (wings-linux-amd64, wings-windows-amd64.exe, ...).
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"kgpanel/wings/internal/version"
)

const DefaultFeed = "https://api.github.com/repos/MrKnuckls/kgpanel/releases/latest"

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Status struct {
	Component    string `json:"component"`
	Current      string `json:"current"`
	Latest       string `json:"latest"`
	UpdateNeeded bool   `json:"update_needed"`
	Asset        string `json:"asset,omitempty"`
	Message      string `json:"message,omitempty"`
}

func assetName() string {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	return "wings-" + runtime.GOOS + "-" + runtime.GOARCH + exe
}

// Latest fetches the newest release and reports whether this wings is behind.
func Latest(feed string) (Status, error) {
	st := Status{Component: "wings", Current: version.Version}
	if feed == "" {
		feed = DefaultFeed
	}
	req, err := http.NewRequest(http.MethodGet, feed, nil)
	if err != nil {
		return st, fmt.Errorf("bad feed url: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kgpanel-wings-updater/"+version.Version)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		st.Message = "feed unreachable: " + err.Error()
		return st, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Message = fmt.Sprintf("feed http %d", resp.StatusCode)
		return st, nil
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		st.Message = "feed parse: " + err.Error()
		return st, nil
	}
	st.Latest = strings.TrimPrefix(rel.TagName, "v")
	if st.Latest == "" {
		st.Message = "feed has no tag_name"
		return st, nil
	}
	if a := findAsset(rel); a != "" {
		st.Asset = a
	}
	if compareVersions(st.Latest, strings.TrimPrefix(version.Version, "v")) > 0 {
		st.UpdateNeeded = true
	}
	return st, nil
}

func findAsset(rel Release) string {
	want := assetName()
	for _, a := range rel.Assets {
		if a.Name == want || a.Name == want+".tar.gz" || a.Name == want+".zip" {
			return a.Name
		}
	}
	return ""
}

func compareVersions(a, b string) int {
	ap := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' })
	bp := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' })
	for i := 0; i < len(ap) && i < len(bp); i++ {
		var ai, bi int
		fmt.Sscanf(ap[i], "%d", &ai)
		fmt.Sscanf(bp[i], "%d", &bi)
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return len(ap) - len(bp)
}

// Apply downloads, verifies and swaps the binary, then restarts (os.Exit).
func Apply(feed string) (Status, error) {
	if feed == "" {
		feed = DefaultFeed
	}
	st, err := Latest(feed)
	if err != nil {
		return st, err
	}
	if !st.UpdateNeeded {
		st.Message = "already up to date (" + st.Current + ")"
		return st, nil
	}
	rel, err := fetchRelease(feed)
	if err != nil {
		return st, err
	}
	asset := findAsset(*rel)
	if asset == "" {
		return st, fmt.Errorf("no asset for %s-%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	tmp, err := os.CreateTemp("", "wings-update-*"+extFor(asset))
	if err != nil {
		return st, err
	}
	defer os.Remove(tmp.Name())
	if err := downloadTo(assetURLOf(rel, asset), tmp); err != nil {
		return st, err
	}
	tmp.Close()
	sum, err := expectedChecksum(checksumsURLOf(rel), asset)
	if err != nil {
		return st, fmt.Errorf("checksums: %w", err)
	}
	if sum != "" {
		got, err := fileSHA256(tmp.Name())
		if err != nil {
			return st, err
		}
		if !strings.EqualFold(got, sum) {
			return st, fmt.Errorf("checksum mismatch: got %s want %s", got, sum)
		}
	}
	if err := swapAndRestart(tmp.Name()); err != nil {
		return st, err
	}
	return st, nil
}

func fetchRelease(feed string) (*Release, error) {
	req, _ := http.NewRequest(http.MethodGet, feed, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kgpanel-wings-updater")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func assetURLOf(rel *Release, name string) string {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func checksumsURLOf(rel *Release) string {
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.URL, "/checksums.txt") {
			return a.URL
		}
	}
	for _, a := range rel.Assets {
		if i := strings.LastIndex(a.URL, "/"); i > 0 {
			return a.URL[:i+1] + "checksums.txt"
		}
	}
	return ""
}

func downloadTo(url string, f *os.File) error {
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: http %d", url, resp.StatusCode)
	}
	_, err = io.Copy(f, resp.Body)
	return err
}

func expectedChecksum(url, asset string) (string, error) {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil // no checksums file — skip
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == asset {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("asset %s not in checksums.txt", asset)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extFor(name string) string {
	if strings.HasSuffix(name, ".exe") {
		return ".exe"
	}
	return ".bin"
}

func swapAndRestart(newBin string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self = filepath.Clean(self)
	tmp := self + ".new"
	if err := copyFile(newBin, tmp); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmp, 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, self); err != nil {
			return err
		}
	} else {
		if err := os.Rename(self, self+".old"); err != nil {
			return err
		}
		if err := os.Rename(tmp, self); err != nil {
			os.Rename(self+".old", self) // roll back
			return err
		}
	}
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
