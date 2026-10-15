package engine

import (
	"context"

	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
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
	AddRoutes(routes ...proxy.Route)
	AllowEgress(hosts ...string)
}

// trafficRelay is the part of the relay that a step's result changes, and
// that shutdown stops.
type trafficRelay interface {
	EnsureListener(ctx context.Context, host string, ports []int) error
	RegisterCapture(ctx context.Context, id, netnsPath string, excludeCIDRs []string) error
	ApplyFault(ctx context.Context, f step.Fault) error
	ClearFault(ctx context.Context, id string) error
	Close() error
}

var (
	_ stepPlugin   = (*pluginhost.Client)(nil)
	_ routeTable   = (*proxy.Proxy)(nil)
	_ trafficRelay = (*relay.Relay)(nil)
)

// stepPlugins lifts launched plugin clients to the interface the run uses.
func stepPlugins(clients map[string]*pluginhost.Client) map[string]stepPlugin {
	out := make(map[string]stepPlugin, len(clients))
	for name, c := range clients {
		out[name] = c
	}
	return out
}
