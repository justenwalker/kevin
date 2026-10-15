package console

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/justenwalker/kevin/internal/run/api"
	"github.com/justenwalker/kevin/internal/run/session"
)

// status writes the running environment's current step states as JSON.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot()
	steps := make([]api.StepSummary, len(v.Steps))
	for i, st := range v.Steps {
		steps[i] = api.StepSummaryOf(st)
	}
	writeJSON(w, r, api.Status{Project: s.project, Network: s.network, ProxyAddr: v.ProxyAddr, Steps: steps})
}

// apiRerun runs a step again and writes what the rerun changed as JSON. A
// step that ran and failed is a normal result with Failed set. A step that
// did not start is an error: 404 for a name the environment does not
// declare, 409 for a step that is already running.
func (s *Server) apiRerun(w http.ResponseWriter, r *http.Request) {
	if s.rerunFn == nil {
		http.Error(w, "rerun is not available", http.StatusServiceUnavailable)
		return
	}

	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		http.Error(w, "the request must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req api.RerunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.rerunFn(r.Context(), req)
	switch {
	case errors.Is(err, session.ErrStepBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, session.ErrUnknownStep):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		writeJSON(w, r, result)
	}
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Ctx(r.Context()).Debug("write json response", "error", err)
	}
}
