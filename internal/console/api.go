package console

import (
	"encoding/json"
	"net/http"

	"github.com/justenwalker/kevin/internal/mcpserver"
)

// StatusResponse is what GET /api/status returns - the same step data the
// page and the MCP server's list_steps already read from the store, for a
// caller with no browser (kevin status).
type StatusResponse struct {
	Project   string                  `json:"project"`
	Network   string                  `json:"network"`
	ProxyAddr string                  `json:"proxy_addr,omitempty"`
	Steps     []mcpserver.StepSummary `json:"steps"`
}

// status writes the running environment's current step states as JSON.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot()
	steps := make([]mcpserver.StepSummary, len(v.Steps))
	for i, st := range v.Steps {
		steps[i] = mcpserver.StepSummaryOf(st)
	}
	resp := StatusResponse{Project: s.project, Network: s.network, ProxyAddr: v.ProxyAddr, Steps: steps}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Ctx(r.Context()).Debug("write status response", "error", err)
	}
}
