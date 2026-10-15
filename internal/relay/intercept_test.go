package relay

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/justenwalker/kevin/internal/pki/ca"
	"github.com/justenwalker/kevin/internal/statedir"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/protos/pb"
)

// fakeControlServer records the last call each RPC received, and returns
// err from every RPC when set.
type fakeControlServer struct {
	pb.UnimplementedRelayControlServer

	lastSetIntercepts   *pb.SetInterceptsRequest
	lastRegisterCapture *pb.RegisterCaptureRequest
	lastApplyFault      *pb.ApplyFaultRequest
	lastClearFault      *pb.ClearFaultRequest
	err                 error
}

func (s *fakeControlServer) SetIntercepts(_ context.Context, req *pb.SetInterceptsRequest) (*pb.SetInterceptsResponse, error) {
	s.lastSetIntercepts = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.SetInterceptsResponse{}, nil
}

func (s *fakeControlServer) RegisterCapture(_ context.Context, req *pb.RegisterCaptureRequest) (*pb.RegisterCaptureResponse, error) {
	s.lastRegisterCapture = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.RegisterCaptureResponse{}, nil
}

func (s *fakeControlServer) ApplyFault(_ context.Context, req *pb.ApplyFaultRequest) (*pb.ApplyFaultResponse, error) {
	s.lastApplyFault = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.ApplyFaultResponse{}, nil
}

func (s *fakeControlServer) ClearFault(_ context.Context, req *pb.ClearFaultRequest) (*pb.ClearFaultResponse, error) {
	s.lastClearFault = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.ClearFaultResponse{}, nil
}

// newTestRelay starts fake on a plaintext local gRPC server and returns a
// Relay whose client dials it directly - control-channel TLS is covered
// separately by TestDialControl and TestControlServerPEM, not here.
func newTestRelay(t *testing.T, fake *fakeControlServer) *Relay {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	pb.RegisterRelayControlServer(srv, fake)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return &Relay{controlAddr: ln.Addr().String(), conn: conn, client: pb.NewRelayControlClient(conn)}
}

func TestRelaySetIntercepts(t *testing.T) {
	t.Run("sends the owner, the hosts and the requested ports", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.SetIntercepts(t.Context(), "web", []Intercept{{Host: "s3.us-east-1.amazonaws.com", Ports: []int{443, 8443}}})
		require.NoError(t, err)
		require.NotNil(t, fake.lastSetIntercepts)
		assert.Equal(t, "web", fake.lastSetIntercepts.GetOwner())
		require.Len(t, fake.lastSetIntercepts.GetIntercepts(), 1)
		assert.Equal(t, "s3.us-east-1.amazonaws.com", fake.lastSetIntercepts.GetIntercepts()[0].GetHost())
		assert.Equal(t, []int32{443, 8443}, fake.lastSetIntercepts.GetIntercepts()[0].GetPorts())
	})

	t.Run("no intercepts sends an empty list for the owner", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		require.NoError(t, r.SetIntercepts(t.Context(), "web", nil))

		assert.Equal(t, "web", fake.lastSetIntercepts.GetOwner())
		assert.Empty(t, fake.lastSetIntercepts.GetIntercepts())
	})

	t.Run("wraps a server error", func(t *testing.T) {
		wantErr := status.Error(codes.Internal, "boom")
		fake := &fakeControlServer{err: wantErr}
		r := newTestRelay(t, fake)

		err := r.SetIntercepts(t.Context(), "web", []Intercept{{Ports: []int{443}}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestRelayRegisterCapture(t *testing.T) {
	t.Run("a workload namespace, no exclusions", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.RegisterCapture(t.Context(), "web", "/var/run/docker/netns/abc123", nil)
		require.NoError(t, err)
		require.NotNil(t, fake.lastRegisterCapture)
		assert.Equal(t, "web", fake.lastRegisterCapture.GetId())
		assert.Equal(t, "/var/run/docker/netns/abc123", fake.lastRegisterCapture.GetNetns().GetPath())
		assert.Empty(t, fake.lastRegisterCapture.GetNetns().GetExcludeCidrs())
	})

	t.Run("a router namespace, with exclusions", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.RegisterCapture(t.Context(), "cluster/node1", "/var/run/docker/netns/def456",
			[]string{"10.244.0.0/16", "10.96.0.0/12"})
		require.NoError(t, err)
		require.NotNil(t, fake.lastRegisterCapture)
		assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/12"}, fake.lastRegisterCapture.GetNetns().GetExcludeCidrs())
	})
}

func TestRelayApplyFault(t *testing.T) {
	t.Run("sends the fault's fields", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.ApplyFault(t.Context(), step.Fault{
			ID: "backend_fault", NetnsPath: "/proc/123/ns/net", Interface: "eth1",
			DelayMS: 500, JitterMS: 100, LossPercent: 10,
			CorruptPercent: 1, DuplicatePercent: 2, ReorderPercent: 3,
			RateKbit: 400,
		})
		require.NoError(t, err)
		require.NotNil(t, fake.lastApplyFault)
		assert.Equal(t, "backend_fault", fake.lastApplyFault.GetFault().GetId())
		assert.Equal(t, "/proc/123/ns/net", fake.lastApplyFault.GetFault().GetNetnsPath())
		assert.Equal(t, "eth1", fake.lastApplyFault.GetFault().GetInterface())
		assert.Equal(t, int32(500), fake.lastApplyFault.GetFault().GetDelayMs())
		assert.Equal(t, int32(100), fake.lastApplyFault.GetFault().GetJitterMs())
		assert.InDelta(t, 10, fake.lastApplyFault.GetFault().GetLossPercent(), 0)
		assert.InDelta(t, 1, fake.lastApplyFault.GetFault().GetCorruptPercent(), 0)
		assert.InDelta(t, 2, fake.lastApplyFault.GetFault().GetDuplicatePercent(), 0)
		assert.InDelta(t, 3, fake.lastApplyFault.GetFault().GetReorderPercent(), 0)
		assert.Equal(t, int32(400), fake.lastApplyFault.GetFault().GetRateKbit())
	})

	t.Run("wraps a server error", func(t *testing.T) {
		wantErr := status.Error(codes.Internal, "boom")
		fake := &fakeControlServer{err: wantErr}
		r := newTestRelay(t, fake)

		err := r.ApplyFault(t.Context(), step.Fault{ID: "backend_fault"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestRelayClearFault(t *testing.T) {
	t.Run("sends the id", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.ClearFault(t.Context(), "backend_fault")
		require.NoError(t, err)
		require.NotNil(t, fake.lastClearFault)
		assert.Equal(t, "backend_fault", fake.lastClearFault.GetId())
	})

	t.Run("wraps a server error", func(t *testing.T) {
		wantErr := status.Error(codes.Internal, "boom")
		fake := &fakeControlServer{err: wantErr}
		r := newTestRelay(t, fake)

		err := r.ClearFault(t.Context(), "backend_fault")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestDialControl(t *testing.T) {
	authority := newTestAuthority(t)
	r := &Relay{controlAddr: "127.0.0.1:0"}

	err := r.dialControl(authority)
	require.NoError(t, err, "dialControl must not block on a live connection - it only needs to succeed lazily")
	assert.NotNil(t, r.conn)
	assert.NotNil(t, r.client)
	t.Cleanup(func() { _ = r.conn.Close() })
}

func TestControlServerPEM(t *testing.T) {
	authority := newTestAuthority(t)

	certPEM, keyPEM, err := controlServerPEM(authority)
	require.NoError(t, err)

	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	require.NoError(t, err)
	assert.Len(t, cert.Certificate, 3, "leaf, then the intermediate, then the root")
}

// newTestAuthority builds a fresh project authority for a test.
func newTestAuthority(t *testing.T) *ca.CA {
	t.Helper()
	t.Setenv(statedir.UserStateDirEnv, t.TempDir())
	t.Setenv(statedir.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)
	return authority
}
