package wire

import (
	"testing"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestGRPCPlugin(t *testing.T) {
	t.Run("GRPCServer runs Register", func(t *testing.T) {
		s := grpc.NewServer()
		p := &GRPCPlugin{Register: func(s *grpc.Server) { pb.RegisterPluginServer(s, pb.UnimplementedPluginServer{}) }}

		require.NoError(t, p.GRPCServer(nil, s))

		_, ok := s.GetServiceInfo()[pb.Plugin_ServiceDesc.ServiceName]
		assert.True(t, ok, "GRPCServer must register the plugin service")
	})

	t.Run("GRPCServer with no Register registers nothing", func(t *testing.T) {
		s := grpc.NewServer()

		require.NoError(t, (&GRPCPlugin{}).GRPCServer(nil, s))

		assert.Empty(t, s.GetServiceInfo())
	})

	t.Run("GRPCClient returns a plugin client bound to the connection", func(t *testing.T) {
		conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		defer conn.Close() //nolint:errcheck // test cleanup

		client, err := (&GRPCPlugin{}).GRPCClient(t.Context(), (*goplugin.GRPCBroker)(nil), conn)
		require.NoError(t, err)
		assert.Implements(t, (*pb.PluginClient)(nil), client)
	})
}

func TestPorts(t *testing.T) {
	t.Run("round trips a map", func(t *testing.T) {
		in := map[int]string{40000: "127.0.0.1:41000", 40001: "127.0.0.1:41001"}

		assert.Equal(t, in, PortsFromProto(PortsToProto(in)))
	})

	t.Run("an empty map stays nil", func(t *testing.T) {
		assert.Nil(t, PortsToProto(nil))
		assert.Nil(t, PortsFromProto(map[int32]string{}))
	})
}
