// Package pluginwire holds what the supervisor and a plugin process both need
// to speak go-plugin to each other. It is kept out of the plugin SDK so that
// plugin authors do not see it.
package pluginwire

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/justenwalker/kevin/protos/pb"
)

// ProtocolVersion is the version of the wire protocol. A supervisor refuses a
// plugin that reports a different version.
const ProtocolVersion = 1

// Handshake is the magic that the supervisor and the plugin exchange. A
// binary that a user runs directly prints a message and exits.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "KEVIN_PLUGIN",
	MagicCookieValue: "0b0f5a3c-2a4f-4d9d-9a55-3f3b5f2f2f34",
}

// Name is the key that the plugin is dispensed under. There is exactly one.
const Name = "step"

// GRPCPlugin adapts the plugin service to go-plugin. A supervisor uses a zero
// GRPCPlugin to get a client. A plugin sets Register to serve.
type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin

	// Register adds the plugin service to the gRPC server of the plugin.
	Register func(*grpc.Server)
}

// GRPCServer registers the plugin service with the gRPC server of the plugin.
func (p *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	if p.Register != nil {
		p.Register(s)
	}
	return nil
}

// GRPCClient returns the client that the supervisor uses to call the plugin.
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pb.NewPluginClient(c), nil
}

// PortsToProto lowers a port-keyed address map to its wire form.
func PortsToProto(m map[int]string) map[int32]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[int32]string, len(m))
	for port, addr := range m {
		out[int32(port)] = addr //nolint:gosec // a port always fits in int32
	}
	return out
}

// PortsFromProto lifts a wire port-keyed address map.
func PortsFromProto(m map[int32]string) map[int]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[int]string, len(m))
	for port, addr := range m {
		out[int(port)] = addr
	}
	return out
}
