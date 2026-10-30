package api

import (
	"log"
	"time"

	"kgpanel/panel/internal/cron"
)

// StartScheduler launches the cron loop that fires scheduled power actions.
// It ticks every 30s and runs each matching schedule once. Tolerates store or
// wings errors (logs + records on the schedule, keeps going).
func (s *Server) StartScheduler() {
	go func() {
		// avoid firing a schedule immediately on boot for the current minute
		lastMinute := time.Now().Minute()
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for range tick.C {
			now := time.Now()
			if now.Minute() == lastMinute {
				continue
			}
			lastMinute = now.Minute()
			s.runDue(now)
		}
	}()
}

// runDue fires every schedule whose expression matches now.
func (s *Server) runDue(now time.Time) {
	for _, sc := range s.st.AllSchedules() {
		if !sc.Enabled {
			continue
		}
		expr, err := cron.Parse(sc.Cron)
		if err != nil {
			continue // invalid expr — ignore (validated at creation)
		}
		if !expr.Match(now) {
			continue
		}
		// skip if it already ran this minute (last run within the tick window)
		if sc.LastRun != nil && now.Sub(*sc.LastRun) < time.Minute {
			continue
		}
		sv := s.st.GetServer(sc.ServerID)
		if sv == nil {
			s.st.MarkScheduleRun(sc.ID, errNotFound("server"))
			continue
		}
		n := s.st.GetNode(sv.NodeID)
		if n == nil {
			s.st.MarkScheduleRun(sc.ID, errNotFound("node"))
			continue
		}
		if err := s.wings.Power(n, sv.ID, sc.Action); err != nil {
			s.st.MarkScheduleRun(sc.ID, err)
			s.st.Audit("scheduler", "schedule_"+sc.Action, sc.Name, "failed: "+err.Error())
			log.Printf("schedule %s (%s) failed: %v", sc.Name, sc.Action, err)
			continue
		}
		s.st.MarkScheduleRun(sc.ID, nil)
		s.st.Audit("scheduler", "schedule_"+sc.Action, sc.Name, sv.Name)
		log.Printf("schedule %s fired %s on %s", sc.Name, sc.Action, sv.Name)
	}
}

type notFoundErr string

func (e notFoundErr) Error() string { return string(e) }
func errNotFound(what string) error { return notFoundErr(what + " not found") }
