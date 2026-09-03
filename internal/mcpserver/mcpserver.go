// Package mcpserver exposes a running kevin environment to an MCP client
// over Streamable HTTP: list steps, inspect a step, rerun a step, read a
// step's exported environment, and read the proxy's routes and egress
// list.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/version"
)

// Path is where the console mounts the MCP server, alongside its own
// routes - the console and the MCP server share one listener rather than
// each binding their own.
const Path = "/_mcp"

// stepViewer is the read-only session state Server needs - satisfied by
// [*console.Server].
type stepViewer interface {
	Snapshot() session.View
}

// egressViewer is the proxy state Server needs - satisfied by
// [*proxy.Proxy].
type egressViewer interface {
	Routes() []proxy.Route
	EgressAllowList() ([]string, []string, bool)
}

// ToolDef describes one MCP tool a plugin contributes, already resolved
// against a running environment's steps.
type ToolDef struct {
	Name        string
	Description string

	// InputSchema is the tool's parameters, an "object" JSON Schema
	// document.
	InputSchema []byte
}

// Server holds the state needed to answer MCP tool calls about a running
// environment. The zero value is not usable. Call [New]. A Server is safe
// for concurrent use.
type Server struct {
	project  string
	domain   string
	view     stepViewer
	proxy    egressViewer
	rerun    func(ctx context.Context, step string, cascade bool) error
	export   func(ctx context.Context, step string) (map[string]string, error)
	tools    []ToolDef
	callTool func(ctx context.Context, step, tool string, args json.RawMessage) (result any, isError bool, errMessage string, err error)
}

// New builds a Server for one project. view answers the step-list/status
// tools, px answers the proxy-info tool, rerun and export are the engine's
// live-session hooks for the rerun_step and export_step tools. tools and
// callTool add every plugin-declared tool alongside the five above.
func New(
	project, domain string,
	view stepViewer,
	px egressViewer,
	rerun func(ctx context.Context, step string, cascade bool) error,
	export func(ctx context.Context, step string) (map[string]string, error),
	tools []ToolDef,
	callTool func(ctx context.Context, step, tool string, args json.RawMessage) (result any, isError bool, errMessage string, err error),
) *Server {
	return &Server{
		project:  project,
		domain:   domain,
		view:     view,
		proxy:    px,
		rerun:    rerun,
		export:   export,
		tools:    tools,
		callTool: callTool,
	}
}

// Handler builds the MCP tool set and wraps it in the Streamable HTTP
// transport. Mount it at [Path].
func (s *Server) Handler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "kevin", Version: version.String}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_steps",
		Description: "List every step in the running kevin environment: its name, display " +
			"label, current status (pending, running, ready, failed, skipped, removing, or " +
			"removed), which plugin type backs it (kind, provider, idempotent), and which " +
			"other steps it depends on. Call this first to see what the environment contains " +
			"and to find a step name for get_step, rerun_step, or export_step.",
	}, s.listSteps)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_step",
		Description: "Get everything list_steps reports about one step, plus its detail rows " +
			"(e.g. an exposed address or routed hostname the step published) and its buffered " +
			"log output. Use this to see why a step failed, or what address/hostname it exposed " +
			"once ready.",
	}, s.getStep)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "rerun_step",
		Description: "Re-run one step - useful after fixing a config problem that made it fail, " +
			"or to recreate a resource. With cascade=true, also re-runs the step's dependents: a " +
			"dependent that already completed only re-runs if its step type is idempotent (safe " +
			"to call Up on again), but a dependent that never completed (skipped because this " +
			"step had failed) always re-runs. Returns the names of every step whose status " +
			"actually changed as a result.",
	}, s.rerunStep)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "export_step",
		Description: "Get the environment variables that let an external command reach what a " +
			"step created - e.g. KUBECONFIG for a Kubernetes cluster step, or a database URL for " +
			"a container step. This is the same data `kevin connect <step>` exports to a shell; " +
			"use it to point an external tool (kubectl, psql, redis-cli, ...) at a resource this " +
			"environment created. Only a step type that supports export returns anything; others " +
			"return an error.",
	}, s.exportStep)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_proxy_info",
		Description: "Get the kevin proxy's address, every hostname it currently routes to a " +
			"workload (e.g. \"web.kevin.home\" to a container's published port), and the egress " +
			"allow list that controls which external hosts a step may reach. Use this to find a " +
			"routable hostname for a step, or to check why an outbound request from inside the " +
			"environment might be denied.",
	}, s.getProxyInfo)

	for _, def := range s.tools {
		srv.AddTool(&mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: json.RawMessage(def.InputSchema)}, s.toolHandler(def))
	}

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}
