package api

import (
	"net/http"
	"strconv"
	"strings"

	"kgpanel/panel/internal/store"
)

// validateCron does a light sanity check on a 5-field cron expression.
func validateCron(c string) bool {
	fields := strings.Fields(c)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		if f == "" || f == "*" {
			continue
		}
		// allow lists "1,15", ranges "0-30", steps "*/5", and single ints
		for _, part := range strings.Split(f, ",") {
			part = strings.TrimPrefix(part, "*")
			part = strings.TrimPrefix(part, "/")
			if i := strings.Index(part, "-"); i >= 0 {
				part = part[strings.Index(part, "-")+1:]
			}
			if parts := strings.SplitN(part, "/", 2); len(parts) > 1 {
				part = parts[1]
			}
			if _, err := strconv.Atoi(strings.TrimSpace(part)); err != nil && part != "" {
				return false
			}
		}
	}
	return true
}

func (s *Server) handleSchedulesList(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	if s.st.GetServer(serverID) == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"schedules": s.st.ListSchedules(serverID)})
}

func (s *Server) handleSchedulesCreate(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	if s.st.GetServer(serverID) == nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	var in struct {
		Name   string `json:"name"`
		Cron   string `json:"cron"`
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Name == "" || in.Cron == "" {
		writeErr(w, http.StatusBadRequest, "name and cron required")
		return
	}
	if !validateCron(in.Cron) {
		writeErr(w, http.StatusBadRequest, "cron must be 5 fields (min hour dom month dow)")
		return
	}
	switch in.Action {
	case "start", "stop", "restart":
	default:
		in.Action = "restart"
	}
	sc := &store.Schedule{ServerID: serverID, Name: in.Name, Cron: in.Cron, Action: in.Action}
	if err := s.st.CreateSchedule(sc); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "schedule_add", in.Name, in.Cron)
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleSchedulesDelete(w http.ResponseWriter, r *http.Request) {
	schedID := r.PathValue("sid")
	sc := s.st.GetSchedule(schedID)
	if sc == nil {
		writeErr(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err := s.st.DeleteSchedule(schedID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.Audit(userFromRequest(r), "schedule_del", sc.Name, sc.Cron)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
