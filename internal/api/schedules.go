package api

import (
	"context"
	"errors"
	"net/http"

	"ply/internal/schedule"
)

// Scheduler is the background tasks' schedules as the settings page shows and changes them
// (implemented by *schedule.Scheduler). Each entry is a task the queue already has, with when it
// runs and why it cannot run now; nothing here queues work a person did not ask for.
type Scheduler interface {
	List(ctx context.Context) ([]schedule.Entry, error)
	Set(ctx context.Context, task string, edit schedule.Edit) (schedule.Entry, error)
	RunNow(ctx context.Context, task string) error
}

//	GET  /api/schedules                each task's schedule, its next and last run, and what it lacks
//	PUT  /api/schedules/{task}         {enabled, at ("HH:MM"), days ([1..7], empty every day)}
//	POST /api/schedules/{task}/run     queue the task now, as a person's request
//
// They are private (see private), like the queue's own endpoints.

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if s.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, "schedules are not available")
		return
	}
	list, err := s.Schedules.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePutSchedule(w http.ResponseWriter, r *http.Request) {
	if s.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, "schedules are not available")
		return
	}
	var req schedule.Edit
	if !decodeBody(w, r, &req) {
		return
	}
	entry, err := s.Schedules.Set(r.Context(), r.PathValue("task"), req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, entry)
	case errors.Is(err, schedule.ErrUnknownTask):
		writeError(w, http.StatusNotFound, "no such scheduled task")
	case errors.Is(err, schedule.ErrInvalid):
		// The reason names the value, never a key.
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	if s.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, "schedules are not available")
		return
	}
	err := s.Schedules.RunNow(r.Context(), r.PathValue("task"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, schedule.ErrUnknownTask):
		writeError(w, http.StatusNotFound, "no such scheduled task")
	case errors.Is(err, schedule.ErrBusy):
		writeError(w, http.StatusConflict, "that task is already queued or running")
	default:
		if refused, ok := errors.AsType[*schedule.Refused](err); ok {
			// The key it needs is missing, or online lookups are off: the page says so, and its
			// "Run now" button is shown disabled with this reason.
			writeError(w, http.StatusConflict, refused.Reason)
			return
		}
		s.internalError(w, r, err)
	}
}
