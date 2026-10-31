package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"kgpanel/wings/internal/db"
	"kgpanel/wings/internal/docker"
	"kgpanel/wings/internal/files"
	"kgpanel/wings/internal/metrics"
	"kgpanel/wings/internal/srvconfig"
	"kgpanel/wings/internal/update"
	"kgpanel/wings/internal/version"
)

// Handler wires the wings HTTP API. Auth: X-KG-Token header or ?token= query
// must equal the configured shared secret (subtle.ConstantTimeCompare-style).
func Handler(token, dataDir, hostDataDir, updateURL string, reg *metrics.Registry) http.Handler {
	mux := http.NewServeMux()

	// config: POST /v1/servers/{id}/config {image,startup,stop_command,env,ports,memory_mb,cpu_percent,recreate}
	// stores the container spec wings needs to (re)create the container.
	// When recreate is true, wings also rebuilds the existing container so
	// port/limit/image/startup changes take effect immediately.
	mux.HandleFunc("/v1/servers/{id}/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if !checkAuth(w, r, token) {
			return
		}
		var in struct {
			srvconfig.Spec
			Recreate bool `json:"recreate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpError(w, http.StatusBadRequest, "bad json")
			return
		}
		if err := srvconfig.Save(dataDir, r.PathValue("id"), in.Spec); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		if in.Recreate {
			ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
			defer cancel()
			env := in.Spec.Env
			if in.Spec.DB {
				var err error
				env, err = provisionDB(ctx, dataDir, r.PathValue("id"), env)
				if err != nil {
					httpError(w, http.StatusBadGateway, err.Error())
					return
				}
			}
			if err := docker.Recreate(ctx, hostDataDir, r.PathValue("id"), in.Spec.Image, env, in.Spec.Ports, in.Spec.MemoryMB, in.Spec.CPUPercent, in.Spec.Startup, in.Spec.Network); err != nil {
				httpError(w, http.StatusBadGateway, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})

	// power: POST /v1/servers/{id}/power {signal}
	mux.HandleFunc("/v1/servers/{id}/power", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		id := r.PathValue("id")
		var in struct {
			Signal string `json:"signal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpError(w, http.StatusBadRequest, "bad json")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
		defer cancel()
		if in.Signal == "start" {
			// ensure the container exists first (create on first boot / after reboot)
			sp, err := srvconfig.Load(dataDir, id)
			if err != nil {
				httpError(w, http.StatusBadRequest, "server config missing: "+err.Error())
				return
			}
			env := sp.Env
			if sp.DB {
				var derr error
				env, derr = provisionDB(ctx, dataDir, id, env)
				if derr != nil {
					httpError(w, http.StatusBadGateway, derr.Error())
					return
				}
			}
			if _, err := docker.EnsureExists(ctx, hostDataDir, id, sp.Image, env, sp.Ports, sp.MemoryMB, sp.CPUPercent, sp.Startup, sp.Network); err != nil {
				httpError(w, http.StatusBadGateway, err.Error())
				return
			}
		}
		if err := docker.Power(ctx, id, in.Signal, 15); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})

	// status: GET /v1/servers/{id}/status
	mux.HandleFunc("/v1/servers/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		state, err := docker.Status(ctx, id)
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": state})
	})

	// metrics: GET /v1/servers/{id}/metrics (rolling history from the sampler)
	mux.HandleFunc("/v1/servers/{id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		id := r.PathValue("id")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":      id,
			"samples": reg.History(id),
		})
	})

	// console: WS /v1/servers/{id}/console (streams container log lines)
	mux.Handle("/v1/servers/{id}/console", websocket.Handler(func(ws *websocket.Conn) {
		if !authorized(ws.Request(), token) {
			_ = ws.Close()
			return
		}
		id := ws.Request().PathValue("id")
		ctx, cancel := context.WithCancel(ws.Request().Context())
		defer cancel()
		// stream logs out; also accept stdin lines and exec them per-line
		go func() {
			defer cancel()
			buf := make([]byte, 4096)
			for {
				n, err := ws.Read(buf)
				if err != nil {
					return
				}
				line := strings.TrimSpace(string(buf[:n]))
				if line == "" {
					continue
				}
				// send input into the container's stdin via sh
				_, _ = docker.ExecCommand(ctx, id, *bytes.NewBufferString(line+"\n"))
			}
		}()
		err := docker.ConsoleLines(ctx, id, func(b []byte) {
			if _, werr := ws.Write(b); werr != nil {
				cancel()
			}
		})
		if err != nil && err != context.Canceled {
			log.Printf("console %s: %v", id, err)
		}
		_ = ws.Close()
	}))

	// update: GET /v1/update/check, POST /v1/update (self-update)
	mux.HandleFunc("/v1/update/check", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		st, _ := update.Latest(updateURL)
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("/v1/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if !checkAuth(w, r, token) {
			return
		}
		st, err := update.Apply(updateURL)
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	})

	// files: list/read/upload/mkdir/delete under <dataDir>/servers/<id>/files
	mux.HandleFunc("/v1/servers/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleFilesList(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/files/upload", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleFilesUpload(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/files/mkdir", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleFilesMkdir(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/files/delete", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleFilesDelete(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/files/download", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleFilesDownload(w, r, dataDir)
	})

	// backups: list/create/download/restore/delete under <dataDir>/servers/<id>/backups
	mux.HandleFunc("/v1/servers/{id}/backups", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleBackupsList(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/backups/create", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleBackupsCreate(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/backups/download", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleBackupsDownload(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/backups/restore", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleBackupsRestore(w, r, dataDir)
	})
	mux.HandleFunc("/v1/servers/{id}/backups/delete", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r, token) {
			return
		}
		handleBackupsDelete(w, r, dataDir)
	})

	// reinstall: POST /v1/servers/{id}/reinstall {wipe_files:bool}
	// Destroys the container and (optionally) wipes the server's files, then
	// re-creates the container from the stored egg spec. Keep files = the
	// "repair" path (re-validates the install on next start without losing
	// configs/saves); wipe files = a true fresh reinstall.
	mux.HandleFunc("/v1/servers/{id}/reinstall", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if !checkAuth(w, r, token) {
			return
		}
		serverID := r.PathValue("id")
		var in struct {
			WipeFiles bool `json:"wipe_files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sp, err := srvconfig.Load(dataDir, serverID)
		if err != nil {
			httpError(w, http.StatusNotFound, "no config for server (create it from the panel first)")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
		defer cancel()
		// tear down the old container
		_ = docker.Remove(ctx, serverID)
		if in.WipeFiles {
			if err := files.Wipe(dataDir, serverID); err != nil {
				httpError(w, http.StatusBadGateway, err.Error())
				return
			}
		}
		// provision DB (idempotent) then rebuild the container from the spec
		env := sp.Env
		if sp.DB {
			if env, err = provisionDB(ctx, dataDir, serverID, env); err != nil {
				httpError(w, http.StatusBadGateway, err.Error())
				return
			}
		}
		if _, err := docker.EnsureExists(ctx, hostDataDir, serverID, sp.Image, env, sp.Ports, sp.MemoryMB, sp.CPUPercent, sp.Startup, sp.Network); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": "true", "wipe_files": in.WipeFiles,
		})
	})

	// health
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "data": dataDir, "version": version.Version})
	})

	return logWrap(mux)
}

// provisionDB ensures the node's shared MariaDB + a per-server database/user,
// then merges the KG_DB_* env vars into the container env. Called whenever a
// server with Spec.DB true is (re)created or started.
// dataDir is the wings-visible data dir (the mount inside the container); it is
// used for file I/O (db-root.txt). HostDataDir is NOT used here — that path only
// exists host-side for docker -v args, and this code runs inside the container.
func provisionDB(ctx context.Context, dataDir, serverID string, env map[string]string) (map[string]string, error) {
	m := db.NewManager(dataDir)
	d, err := m.Provision(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	for k, v := range m.DBEnv(d) {
		out[k] = v
	}
	return out, nil
}

func authorized(r *http.Request, token string) bool {
	given := r.Header.Get("X-KG-Token")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	return token != "" && given == token
}

func checkAuth(w http.ResponseWriter, r *http.Request, token string) bool {
	if !authorized(r, token) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func logWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
