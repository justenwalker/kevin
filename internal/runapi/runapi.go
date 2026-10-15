// Package runapi is the contract between a running kevin and the clients
// that act on it: the console's JSON endpoints, the MCP server and the
// command line.
package runapi

import (
	"time"

	"github.com/justenwalker/kevin/internal/session"
)

// StepSummary is one step's status.
type StepSummary struct {
	Name       string   `json:"name"               jsonschema:"the step's name, from kevin.cue"`
	Label      string   `json:"label,omitempty"    jsonschema:"the step's display label, if it has one different from name"`
	State      string   `json:"state"              jsonschema:"one of: pending, running, ready, failed, skipped, removing, removed"`
	Message    string   `json:"message,omitempty"  jsonschema:"the state's detail text, e.g. an error message when state is failed"`
	Kind       string   `json:"kind,omitempty"     jsonschema:"the step type's kind: resource, action, or probe"`
	Provider   string   `json:"provider,omitempty" jsonschema:"the plugin that backs this step, e.g. builtin"`
	Needs      []string `json:"needs,omitempty"    jsonschema:"names of the steps this one depends on"`
	Idempotent bool     `json:"idempotent"         jsonschema:"whether rerun_step's cascade may re-run this step as a side effect of rerunning something it depends on"`
}

// StepSummaryOf summarizes one step of a session.
func StepSummaryOf(s session.Step) StepSummary {
	return StepSummary{
		Name:       s.Name,
		Label:      s.Label,
		State:      string(s.State),
		Message:    s.Message,
		Kind:       s.Kind,
		Provider:   s.Provider,
		Needs:      s.Needs,
		Idempotent: s.Idempotent,
	}
}

// Status is the running environment's current step states.
type Status struct {
	Project   string        `json:"project"`
	Network   string        `json:"network"`
	ProxyAddr string        `json:"proxy_addr,omitempty"`
	Steps     []StepSummary `json:"steps"`
}

// RerunRequest names the step to run again.
type RerunRequest struct {
	// Step is the step's name, from kevin.cue.
	Step string `json:"step"`

	// Cascade also reruns the step's dependents: one that already completed
	// only if its step type is idempotent, one that never completed always.
	Cascade bool `json:"cascade,omitempty"`
}

// DeniedRequest is one proxy request the environment blocked.
type DeniedRequest struct {
	Time   time.Time `json:"time"   jsonschema:"when the proxy blocked the request"`
	Method string    `json:"method" jsonschema:"the request's HTTP method"`
	Host   string    `json:"host"   jsonschema:"the host the request targeted"`
	Path   string    `json:"path"   jsonschema:"the request's path"`
}

// RerunResult is what a rerun that ran left behind.
type RerunResult struct {
	// Step is the step that was targeted.
	Step string `json:"step"`

	// Failed is true when the targeted step ended failed.
	Failed bool `json:"failed"`

	// Changed holds the targeted step and every other step whose state or
	// message differs from before the rerun.
	Changed []StepSummary `json:"changed,omitempty"`

	// Denied holds the proxy requests blocked while the rerun was in flight,
	// oldest first.
	Denied []DeniedRequest `json:"denied,omitempty"`
}
