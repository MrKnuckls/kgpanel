package api

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"

	"kgpanel/wings/internal/backups"
	"kgpanel/wings/internal/files"
)

func serverID(r *http.Request) string { return r.PathValue("id") }

// --- files ---

func handleFilesList(w http.ResponseWriter, r *http.Request, dataDir string) {
	path := r.URL.Query().Get("path")
	entries, disp, err := files.List(dataDir, serverID(r), path)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": disp, "entries": entries})
}

func handleFilesUpload(w http.ResponseWriter, r *http.Request, dataDir string) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	dir := r.URL.Query().Get("path")
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpError(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpError(w, http.StatusBadRequest, "no file field")
		return
	}
	defer file.Close()
	// ignore dir traversal in the upload name: only the base name is used
	rel := filepath.Join(dir, filepath.Base(hdr.Filename))
	if err := files.Write(dataDir, serverID(r), rel, file); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func handleFilesMkdir(w http.ResponseWriter, r *http.Request, dataDir string) {
	var in struct{ Path string `json:"path"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := files.Mkdir(dataDir, serverID(r), in.Path); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func handleFilesDelete(w http.ResponseWriter, r *http.Request, dataDir string) {
	var in struct{ Path string `json:"path"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := files.Delete(dataDir, serverID(r), in.Path); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func handleFilesDownload(w http.ResponseWriter, r *http.Request, dataDir string) {
	path := r.URL.Query().Get("path")
	f, size, err := files.Read(dataDir, serverID(r), path)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()
	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}

// --- backups ---

func handleBackupsList(w http.ResponseWriter, r *http.Request, dataDir string) {
	list, err := backups.List(dataDir, serverID(r))
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"backups": list})
}

func handleBackupsCreate(w http.ResponseWriter, r *http.Request, dataDir string) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	info, err := backups.Create(dataDir, serverID(r), "")
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func handleBackupsDownload(w http.ResponseWriter, r *http.Request, dataDir string) {
	name := r.URL.Query().Get("name")
	f, size, err := backups.Open(dataDir, serverID(r), name)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}

func handleBackupsDelete(w http.ResponseWriter, r *http.Request, dataDir string) {
	var in struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := backups.Delete(dataDir, serverID(r), in.Name); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func handleBackupsRestore(w http.ResponseWriter, r *http.Request, dataDir string) {
	var in struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := backups.Restore(dataDir, serverID(r), in.Name); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
