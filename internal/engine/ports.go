package engine

import (
	"context"

	"github.com/justenwalker/kevin/internal/container"
	"github.com/justenwalker/kevin/internal/plugins/launcher"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/step"
)

// stepPlugin is the part of a plugin client that brings steps up and down.
type stepPlugin interface {
	Up(ctx context.Context, req step.UpRequest, ev step.Events) (step.Result, error)
	Down(ctx context.Context, req step.DownRequest, ev step.Events) error
	Export(ctx context.Context, req step.ExportRequest) (step.ExportResult, error)
	CallTool(ctx context.Context, req step.ToolCall) (step.ToolResult, error)
}

// routeTable is the part of the proxy that a step's result changes.
type routeTable interface {
	SetStep(step string, routes []proxy.Route, egress []string)
	ClearStep(step string)
}

// trafficRelay is the part of the relay that a step's result changes, and
// that shutdown stops.
type trafficRelay interface {
	SetIntercepts(ctx context.Context, owner string, intercepts []relay.Intercept) error
	RegisterCapture(ctx context.Context, id, netnsPath string, excludeCIDRs []string) error
	ApplyFault(ctx context.Context, f step.Fault) error
	ClearFault(ctx context.Context, id string) error
	Close() error
}

// gatewayLookup is what the proxy needs of the container engine: the
// gateway address of the project network.
type gatewayLookup interface {
	NetworkGateway(ctx context.Context, name string) (container.Gateway, error)
}

// networkPreparer is what Run needs before any step: a reachable engine and
// the project network.
type networkPreparer interface {
	container.Prober
	NetworkCreate(ctx context.Context, name string, opts container.NetworkOptions) error
}

// projectRuntime is what a run asks of the container engine once its network
// exists: finding and removing the project's containers and its network.
type projectRuntime interface {
	container.Lister
	container.Containers
	NetworkRemove(ctx context.Context, name string) error
}

// hostRuntime is what bootstrap hands to Run, Teardown and PlanCommand.
type hostRuntime interface {
	networkPreparer
	gatewayLookup
	projectRuntime
}

var (
	_ hostRuntime  = container.Runtime(nil)
	_ stepPlugin   = (*launcher.Client)(nil)
	_ routeTable   = (*proxy.Proxy)(nil)
	_ trafficRelay = (*relay.Relay)(nil)
)

// stepPlugins lifts launched plugin clients to the interface the run uses.
func stepPlugins(clients map[string]*launcher.Client) map[string]stepPlugin {
	out := make(map[string]stepPlugin, len(clients))
	for name, c := range clients {
		out[name] = c
	}
	return out
}

// egressView adapts the proxy to the route and egress view the MCP server
// reads.
type egressView struct{ proxy *proxy.Proxy }

func (v egressView) Routes() []runapi.Route {
	routes := v.proxy.Routes()
	out := make([]runapi.Route, len(routes))
	for i, r := range routes {
		out[i] = runapi.Route{Host: r.Host, Upstream: r.Upstream, Relay: r.Relay, TLS: r.TLS}
	}
	return out
}

func (v egressView) EgressPolicy() runapi.EgressPolicy {
	return runapi.EgressPolicy(v.proxy.EgressPolicy())
}
