package plugin

import "github.com/justenwalker/kevin/protos/pb"

// RouteMode selects how the proxy handles a route's client-facing
// connection - independent of Route.TLS, which only says whether Upstream
// itself speaks TLS.
type RouteMode int32

//go:generate go tool -modfile=../tools.mod stringer -type=RouteMode -linecomment
const (
	// RouteModeMITM is the zero value: terminate the client's TLS and
	// re-sign it with kevin's own leaf, then route the decrypted request
	// normally.
	RouteModeMITM RouteMode = iota // mitm

	// RouteModePassthrough tunnels the client's TLS through untouched, so
	// the client validates Upstream's real certificate directly. Only
	// meaningful when TLS is true.
	RouteModePassthrough // passthrough

	// RouteModeRaw tunnels the connection byte for byte, with no TLS or
	// HTTP assumption at all, for a raw TCP protocol. TLS must be false.
	RouteModeRaw // raw
)

// routeModeToProto maps the plugin SDK's RouteMode to the wire enum.
func routeModeToProto(m RouteMode) pb.RouteMode {
	switch m {
	case RouteModeMITM:
		return pb.RouteMode_ROUTE_MODE_MITM
	case RouteModePassthrough:
		return pb.RouteMode_ROUTE_MODE_PASSTHROUGH
	case RouteModeRaw:
		return pb.RouteMode_ROUTE_MODE_RAW
	default:
		return pb.RouteMode_ROUTE_MODE_MITM
	}
}
