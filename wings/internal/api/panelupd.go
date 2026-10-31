// Panel update / rollback driven from the panel UI. The wings node does this
// because it (not the panel container) has the docker socket + CLI, and its
// KGWINGS_HOST_DATA translation lets the daemon resolve build contexts and
// mounts. Flow for an update:
//
//	1. snapshot the running panel image as <image>-previous (rollback target)
//	2. download the release SOURCE tarball from the update feed's repo
//	3. extract and `docker build` the panel image (same tag as current)
//	4. recreate the panel container from the fresh image with identical
//	   env/mounts/ports/restart policy (the container restart IS the refresh)
//
// Rollback: recreate the panel container from <image>-previous.
package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"kgpanel/wings/internal/docker"
	"kgpanel/wings/internal/update"
)

const (
	panelContainer   = "kgpanel"          // container name wings recreates on update
	updateWorkRel    = "panel-update-src" // where release source is extracted (under dataDir)
	buildTimeout     = 15 * time.Minute
	repoPattern      = `api\.github\.com/repos/([^/]+)/([^/]+)/`
)

// updateState tracks the (single, at most one at a time) panel update run so
// the panel can poll status instead of holding an HTTP request open for the
// whole docker build.
var updateState = struct {
	sync.Mutex
	Running  bool   `json:"running"`
	LastOK   bool   `json:"last_ok"`
	LastErr  string `json:"last_error,omitempty"`
	LastTag  string `json:"last_tag,omitempty"`
}{}

// hostDiskPath maps a path under the container-visible dataDir to the path the
// docker daemon sees on the host (KGWINGS_HOST_DATA). Same translation the
// server-file mounts use.
func hostDiskPath(dataDir, hostData, p string) string {
	if hostData == "" || hostData == dataDir {
		return p
	}
	return filepath.Join(hostData, strings.TrimPrefix(filepath.Clean(p), filepath.Clean(dataDir)))
}

// repoFromFeed extracts owner/repo from the release feed URL.
func repoFromFeed(feed string) (string, string, error) {
	if feed == "" {
		feed = update.DefaultFeed
	}
	m := regexp.MustCompile(repoPattern).FindStringSubmatch(feed)
	if m == nil {
		return "", "", fmt.Errorf("cannot derive repo from feed %q", feed)
	}
	return m[1], m[2], nil
}

// sourceTarballURL builds the GitHub source archive URL for a tag.
func sourceTarballURL(feed, tag string) (string, error) {
	owner, repo, err := repoFromFeed(feed)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://github.com/%s/%s/archive/refs/tags/%s.tar.gz", owner, repo, tag), nil
}

// downloadToFile streams a URL to a local file.
func downloadToFile(url, path string) error {
	resp, err := (&http.Client{Timeout: 8 * time.Minute}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: http %d", url, resp.StatusCode)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// extractTarball strips the single top-level directory GitHub puts in source
// archives (kgpanel-<tag>/) and writes contents into dest, so dest is the repo
// root (contains deploy/Dockerfile.panel and panel/).
func extractTarball(tarball, dest string) error {
	f, err := os.Open(tarball)
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
		if !strings.Contains(hdr.Name, "/") {
			continue // top-level dir entry (kgpanel-<tag>/ itself)
		}
		rel := strings.SplitN(hdr.Name, "/", 2)[1] // strip kgpanel-<tag>/
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if hdr.Typeflag == tar.TypeSymlink {
				if err := os.Symlink(hdr.Linkname, target); err != nil {
					return err
				}
				continue
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

// panelUpdateBody is the panel's request to run an update.
type panelUpdateBody struct {
	Tag string `json:"tag"` // empty => latest from feed
}

// runPanelUpdate performs the full update (called in its own goroutine).
func runPanelUpdate(dataDir, hostData, feed, tag string) {
	updateState.Lock()
	updateState.Running = true
	updateState.LastErr = ""
	updateState.LastOK = false
	updateState.LastTag = tag
	updateState.Unlock()

	finish := func(ok bool, err error) {
		updateState.Lock()
		updateState.Running = false
		updateState.LastOK = ok
		if err != nil {
			updateState.LastErr = err.Error()
		}
		updateState.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	_, _, _, _, _, _, err := docker.PanelInspect(ctx, panelContainer)
	if err != nil {
		finish(false, fmt.Errorf("inspect panel container: %w", err))
		return
	}
	curImg, err := docker.PanelImage(ctx, panelContainer)
	if err != nil {
		finish(false, fmt.Errorf("read panel image ref: %w", err))
		return
	}
	prevImg := curImg + "-previous"

	// 1. snapshot rollback target BEFORE touching anything
	if docker.ImageExists(ctx, curImg) {
		if err := docker.TagImage(ctx, curImg, prevImg); err != nil {
			finish(false, fmt.Errorf("snapshot previous image: %w", err))
			return
		}
		log.Printf("panel update: snapshotted %s -> %s", curImg, prevImg)
	}

	// 2+3. download release source and build the panel image
	workRel := filepath.Join(updateWorkRel, strings.ReplaceAll(tag, "/", "_"))
	tarball := filepath.Join(dataDir, workRel+".tar.gz")
	extractDir := filepath.Join(dataDir, workRel)
	if err := os.MkdirAll(filepath.Dir(tarball), 0o755); err != nil {
		finish(false, fmt.Errorf("create update workdir: %w", err))
		return
	}
	_ = os.RemoveAll(extractDir)
	_ = os.Remove(tarball)
	defer os.Remove(tarball)

	url, err := sourceTarballURL(feed, tag)
	if err != nil {
		finish(false, err)
		return
	}
	log.Printf("panel update: downloading %s", url)
	if err := downloadToFile(url, tarball); err != nil {
		finish(false, fmt.Errorf("download source: %w", err))
		return
	}
	if err := extractTarball(tarball, extractDir); err != nil {
		finish(false, fmt.Errorf("extract source: %w", err))
		return
	}

	hostCtx := hostDiskPath(dataDir, hostData, extractDir)
	log.Printf("panel update: building %s (VERSION=%s) from %s", curImg, tag, hostCtx)
	if err := docker.BuildImage(ctx, hostCtx, "deploy/Dockerfile.panel", curImg, tag); err != nil {
		finish(false, fmt.Errorf("docker build: %w", err))
		return
	}

	// 4. recreate the container so the fresh image actually runs
	log.Printf("panel update: recreating %s from %s", panelContainer, curImg)
	if err := docker.RecreatePanelContainer(ctx, panelContainer, curImg); err != nil {
		finish(false, fmt.Errorf("recreate panel container: %w", err))
		return
	}
	log.Printf("panel update: %s updated to %s", panelContainer, tag)
	finish(true, nil)
}

// handlePanelUpdate: POST /v1/panel/update {tag}. Responds immediately; the
// build+swap happens in a background goroutine (a hosted build would take
// minutes and the panel's HTTP client would time out / cancel it).
func handlePanelUpdate(dataDir, hostData, feed string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var in panelUpdateBody
		_ = json.NewDecoder(r.Body).Decode(&in)
		tag := strings.TrimSpace(in.Tag)
		if tag == "" {
			st, err := update.Latest(feed)
			if err != nil {
				httpError(w, http.StatusBadGateway, "cannot determine latest version: "+err.Error())
				return
			}
			tag = "v" + st.Latest
		}
		updateState.Lock()
		if updateState.Running {
			updateState.Unlock()
			httpError(w, http.StatusConflict, "an update is already running")
			return
		}
		updateState.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"started": "true", "tag": tag})
		go func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("panel update: PANIC %v", p)
					updateState.Lock()
					updateState.Running = false
					updateState.LastOK = false
					updateState.LastErr = fmt.Sprintf("internal error: %v", p)
					updateState.Unlock()
				}
			}()
			runPanelUpdate(dataDir, hostData, feed, tag)
		}()
	}
}

// handlePanelRollback: POST /v1/panel/rollback — recreates the panel container
// from <image>-previous (synchronous; docker operations only, no build).
func handlePanelRollback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		curImg, err := docker.PanelImage(ctx, panelContainer)
		if err != nil {
			httpError(w, http.StatusBadGateway, "cannot read panel image: "+err.Error())
			return
		}
		prevImg := curImg + "-previous"
		if !docker.ImageExists(ctx, prevImg) {
			httpError(w, http.StatusNotFound, "no previous image ("+prevImg+") — nothing to roll back to")
			return
		}
		if err := docker.RecreatePanelContainer(ctx, panelContainer, prevImg); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		updateState.Lock()
		updateState.LastOK = true
		updateState.LastErr = ""
		updateState.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "image": prevImg})
	}
}

// handlePanelUpdateStatus: GET /v1/panel/update/status {running, last_ok,
// last_error, last_tag}.
func handlePanelUpdateStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateState.Lock()
		st := struct {
			Running bool   `json:"running"`
			LastOK  bool   `json:"last_ok"`
			LastErr string `json:"last_error,omitempty"`
			LastTag string `json:"last_tag,omitempty"`
		}{updateState.Running, updateState.LastOK, updateState.LastErr, updateState.LastTag}
		updateState.Unlock()
		writeJSON(w, http.StatusOK, st)
	}
}
