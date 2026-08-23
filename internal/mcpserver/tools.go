package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/justenwalker/kevin/internal/session"
)

// StepSummary is one step's status, as list_steps and get_step report it.
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

func stepSummary(s session.Step) StepSummary {
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

// ListStepsOutput is the result of list_steps.
type ListStepsOutput struct {
	Steps []StepSummary `json:"steps" jsonschema:"every step in the environment, in the order kevin.cue's DAG brought them up"`
}

func (s *Server) listSteps(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListStepsOutput, error) {
	steps := s.view.Snapshot().Steps
	out := make([]StepSummary, len(steps))
	for i, st := range steps {
		out[i] = stepSummary(st)
	}
	return nil, ListStepsOutput{Steps: out}, nil
}

// GetStepInput names the step get_step reports on.
type GetStepInput struct {
	Name string `json:"name" jsonschema:"the step's name, from kevin.cue - see list_steps"`
}

// sensitiveMask replaces a Sensitive Detail's real value, matching
// DetailRow.Value's documented contract.
const sensitiveMask = "********"

// DetailRow is one row of a step's card, as get_step reports it.
type DetailRow struct {
	Label     string `json:"label"     jsonschema:"the row's caption, e.g. tcp 80 or KUBECONFIG"`
	Value     string `json:"value"     jsonschema:"the row's value, e.g. an address, a hostname, or a path - masked as ******** when sensitive is true"`
	Sensitive bool   `json:"sensitive" jsonschema:"whether value is masked and must not be treated as the real secret"`
}

// LogLine is one buffered line of a step's output.
type LogLine struct {
	Stream string `json:"stream" jsonschema:"the output stream this line came from, e.g. stdout or stderr"`
	Text   string `json:"text"   jsonschema:"the line's content"`
}

// GetStepOutput is the result of get_step.
type GetStepOutput struct {
	StepSummary

	Details []DetailRow `json:"details,omitempty" jsonschema:"the step's card rows - an exposed address, a routed hostname, a generated credential path, etc."`
	Logs    []LogLine   `json:"logs,omitempty"    jsonschema:"the step's buffered output, oldest first"`
}

func (s *Server) getStep(_ context.Context, _ *mcp.CallToolRequest, in GetStepInput) (*mcp.CallToolResult, GetStepOutput, error) {
	v := s.view.Snapshot()
	for _, st := range v.Steps {
		if st.Name != in.Name {
			continue
		}
		details := make([]DetailRow, len(st.Details))
		for i, d := range st.Details {
			value := d.Value
			if d.Sensitive {
				value = sensitiveMask
			}
			details[i] = DetailRow{Label: d.Label, Value: value, Sensitive: d.Sensitive}
		}
		lines := v.StepLogs[st.Name]
		logs := make([]LogLine, len(lines))
		for i, l := range lines {
			logs[i] = LogLine{Stream: l.Stream, Text: l.Text}
		}
		return nil, GetStepOutput{StepSummary: stepSummary(st), Details: details, Logs: logs}, nil
	}
	return nil, GetStepOutput{}, fmt.Errorf("mcpserver: no step named %q", in.Name)
}

// RerunStepInput names the step to re-run and whether to cascade to its
// dependents.
type RerunStepInput struct {
	Name    string `json:"name"              jsonschema:"the step's name, from kevin.cue - see list_steps"`
	Cascade bool   `json:"cascade,omitempty" jsonschema:"also re-run this step's dependents: an already-completed dependent only if its step type is idempotent, a never-completed one always"`
}

// RerunStepOutput is the result of rerun_step.
type RerunStepOutput struct {
	Name string   `json:"name"          jsonschema:"the step that was targeted"`
	Ran  []string `json:"ran,omitempty" jsonschema:"names of every step whose status actually changed - name itself, plus any dependents cascade brought along"`
}

func (s *Server) rerunStep(ctx context.Context, _ *mcp.CallToolRequest, in RerunStepInput) (*mcp.CallToolResult, RerunStepOutput, error) {
	before := stepStates(s.view.Snapshot().Steps)
	if err := s.rerun(ctx, in.Name, in.Cascade); err != nil {
		return nil, RerunStepOutput{}, fmt.Errorf("mcpserver: rerun %s: %w", in.Name, err)
	}

	var ran []string
	for _, st := range s.view.Snapshot().Steps {
		if before[st.Name] != st.State {
			ran = append(ran, st.Name)
		}
	}
	return nil, RerunStepOutput{Name: in.Name, Ran: ran}, nil
}

func stepStates(steps []session.Step) map[string]session.State {
	states := make(map[string]session.State, len(steps))
	for _, st := range steps {
		states[st.Name] = st.State
	}
	return states
}

// ExportStepInput names the step to export.
type ExportStepInput struct {
	Name string `json:"name" jsonschema:"the step's name, from kevin.cue - see list_steps; must be a step type that supports export, e.g. builtin:kind"`
}

// ExportStepOutput is the result of export_step.
type ExportStepOutput struct {
	Name string            `json:"name" jsonschema:"the step that was exported"`
	Env  map[string]string `json:"env"  jsonschema:"environment variables to set so an external command (kubectl, psql, ...) reaches what this step created, e.g. KUBECONFIG"`
}

func (s *Server) exportStep(ctx context.Context, _ *mcp.CallToolRequest, in ExportStepInput) (*mcp.CallToolResult, ExportStepOutput, error) {
	vars, err := s.export(ctx, in.Name)
	if err != nil {
		return nil, ExportStepOutput{}, fmt.Errorf("mcpserver: export %s: %w", in.Name, err)
	}
	return nil, ExportStepOutput{Name: in.Name, Env: vars}, nil
}

// RouteInfo is one entry of the proxy's routing table.
type RouteInfo struct {
	Host     string `json:"host"     jsonschema:"the hostname a client asks for, e.g. web.kevin.home"`
	Upstream string `json:"upstream" jsonschema:"the address the proxy forwards to, e.g. a container's published loopback port"`
	TLS      bool   `json:"tls"      jsonschema:"whether the upstream itself speaks TLS"`
}

// EgressInfo is the proxy's egress allow list.
type EgressInfo struct {
	Deny      bool     `json:"deny"                jsonschema:"whether the proxy blocks a host that no route and no allow entry covers - true unless the environment disabled egress control"`
	Allow     []string `json:"allow,omitempty"     jsonschema:"exact external hostnames every step may reach"`
	Wildcards []string `json:"wildcards,omitempty" jsonschema:"leading-dot subdomain wildcards every step may reach, e.g. .github.com matches api.github.com but not github.com itself"`
}

// GetProxyInfoOutput is the result of get_proxy_info.
type GetProxyInfoOutput struct {
	Addr   string      `json:"addr"             jsonschema:"host:port where the proxy listens"`
	Domain string      `json:"domain"           jsonschema:"the environment's base domain, e.g. kevin.home - a route's host is always under it"`
	Routes []RouteInfo `json:"routes,omitempty" jsonschema:"every hostname currently routed to a workload"`
	Egress EgressInfo  `json:"egress"           jsonschema:"which external hosts a step is permitted to reach"`
}

func (s *Server) getProxyInfo(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, GetProxyInfoOutput, error) {
	routes := s.proxy.Routes()
	out := make([]RouteInfo, len(routes))
	for i, r := range routes {
		out[i] = RouteInfo{Host: r.Host, Upstream: r.Upstream, TLS: r.TLS}
	}
	allow, wildcards, deny := s.proxy.EgressAllowList()

	return nil, GetProxyInfoOutput{
		Addr:   s.view.Snapshot().ProxyAddr,
		Domain: s.domain,
		Routes: out,
		Egress: EgressInfo{Deny: deny, Allow: allow, Wildcards: wildcards},
	}, nil
}
