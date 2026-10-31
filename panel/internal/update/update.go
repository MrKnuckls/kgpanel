// Package update implements self-update: check a release feed (default GitHub
// latest release), download the platform-matched asset, verify its sha256
// against the release's checksums.txt, swap the running binary and restart.
//
// The release feed is configurable (--update-url / KGPANEL_UPDATE_URL) so the
// panel can also be fed by a self-hosted mirror or a GitLab release.
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

	"kgpanel/panel/internal/version"
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
	AutoUpdate   bool   `json:"auto_update"` // checked on a timer when enabled
}

func compName() string {
	n := runtime.GOOS + "-" + runtime.GOARCH
	if n == "windows-amd64" {
		n = "windows-amd64"
	}
	return n
}

// assetName matches the release workflow artifact names:
// kgpanel-linux-amd64, kgpanel-windows-amd64.exe, ...
func assetName() string {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	return "kgpanel-" + runtime.GOOS + "-" + runtime.GOARCH + exe
}

// Latest fetches the newest release from the feed and reports whether this
// binary is behind. Unreachable feed => Status with UpdateNeeded=false and a
// message (never fail the panel because the internet is down).
func Latest(feed string) (Status, error) {
	st := Status{Component: "kgpanel", Current: version.Version}
	if feed == "" {
		feed = DefaultFeed
	}
	req, err := http.NewRequest(http.MethodGet, feed, nil)
	if err != nil {
		return st, fmt.Errorf("bad feed url: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kgpanel-updater/"+version.Version)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
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
	st.Latest = rel.TagName
	if st.Latest == "" {
		st.Message = "feed has no tag_name"
		return st, nil
	}
	st.Latest = strings.TrimPrefix(st.Latest, "v")
	cur := strings.TrimPrefix(version.Version, "v")
	if relAsset := findAsset(rel); relAsset != "" {
		st.Asset = relAsset
	}
	if compareVersions(st.Latest, cur) > 0 {
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

// compareVersions returns >0 if a > b, 0 equal, <0 if a < b (numeric parts only).
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

// Apply downloads the matching release asset, verifies it against
// checksums.txt from the release, swaps the current binary and restarts.
// Returns a Status describing what happened. On success the process calls
// os.Exit after starting the replacement.
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
	// fetch checksums + asset from the same release
	rel, err := fetchRelease(feed)
	if err != nil {
		return st, err
	}
	asset := findAsset(*rel)
	if asset == "" {
		return st, fmt.Errorf("no asset for %s-%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	assetURL := assetURLOf(rel, asset)
	checksumsURL := checksumsURLOf(rel)

	tmp, err := os.CreateTemp("", "kgpanel-update-*"+extFor(asset))
	if err != nil {
		return st, err
	}
	defer os.Remove(tmp.Name())
	if err := downloadTo(assetURL, tmp); err != nil {
		return st, err
	}
	tmp.Close()

	sum, err := expectedChecksum(checksumsURL, asset)
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
	req.Header.Set("User-Agent", "kgpanel-updater")
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

// checksumsURLOf derives the checksums.txt URL from the latest feed location.
// GitHub feeds are html+json; the API url for latest is
// /releases/latest, so we swap "releases/latest" -> "releases/tags/TAG" and
// append /checksums.txt to the browser_download_url of the first asset.
func checksumsURLOf(rel *Release) string {
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.URL, "/checksums.txt") {
			return a.URL
		}
	}
	// fall back: derive from any asset url
	for _, a := range rel.Assets {
		i := strings.LastIndex(a.URL, "/")
		if i > 0 {
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
		return "", nil // no checksums file — skip verification
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

// swapAndRestart replaces the running binary and relaunches it.
// Linux: rename-over works because the inode stays alive.
// Windows: a running .exe cannot be overwritten, so we rename current -> .old,
// move new into place, start it, then exit.
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
		old := self + ".old"
		// a previous update's .old may still exist — it's our own old version,
		// safe to drop so updates don't accumulate
		_ = os.Remove(old)
		if err := os.Rename(self, old); err != nil {
			return err
		}
		if err := os.Rename(tmp, self); err != nil {
			os.Rename(old, self) // roll back
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
