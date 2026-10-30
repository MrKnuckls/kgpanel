package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"kgpanel/panel/internal/store"
)

// getServerNode resolves a server and its node, or writes an error.
func (s *Server) getServerNode(w http.ResponseWriter, r *http.Request) (*store.Server, *store.Node, bool) {
	sv := s.st.GetServer(r.PathValue("id"))
	if sv == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return nil, nil, false
	}
	n := s.st.GetNode(sv.NodeID)
	if n == nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return nil, nil, false
	}
	return sv, n, true
}

// --- files ---

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	entries, disp, err := s.wings.FilesList(n, r.PathValue("id"), path)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": disp, "entries": entries})
}

func (s *Server) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no file field")
		return
	}
	defer file.Close()
	// re-encode the multipart body so wings sees a fresh boundary + filename
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, werr := mw.CreateFormFile("file", hdr.Filename)
	if werr != nil {
		writeErr(w, http.StatusInternalServerError, werr.Error())
		return
	}
	if _, werr := io.Copy(fw, file); werr != nil {
		writeErr(w, http.StatusInternalServerError, werr.Error())
		return
	}
	mw.Close()
	if err := s.wings.FilesUpload(n, r.PathValue("id"), dir, &buf, mw.FormDataContentType()); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "file_upload", r.PathValue("id"), dir+"/"+hdr.Filename)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	var in struct{ Path string `json:"path"` }
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.wings.FilesAction(n, r.PathValue("id"), "mkdir", in.Path); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "file_mkdir", r.PathValue("id"), in.Path)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	var in struct{ Path string `json:"path"` }
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.wings.FilesAction(n, r.PathValue("id"), "delete", in.Path); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "file_delete", r.PathValue("id"), in.Path)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleFilesDownload(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	resp, err := s.wings.FilesDownload(n, r.PathValue("id"), path)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, resp.Body)
}

// --- backups ---

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	list, err := s.wings.BackupsList(n, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"backups": list})
}

func (s *Server) handleBackupsCreate(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	if err := s.wings.BackupCreate(n, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "backup_create", r.PathValue("id"), "")
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleBackupsDelete(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	var in struct{ Name string `json:"name"` }
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.wings.BackupAction(n, r.PathValue("id"), "delete", in.Name); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleBackupsRestore(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	var in struct{ Name string `json:"name"` }
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.wings.BackupAction(n, r.PathValue("id"), "restore", in.Name); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "backup_restore", r.PathValue("id"), in.Name)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleBackupsDownload(w http.ResponseWriter, r *http.Request) {
	_, n, ok := s.getServerNode(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	resp, err := s.wings.BackupDownload(n, r.PathValue("id"), name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = io.Copy(w, resp.Body)
}
