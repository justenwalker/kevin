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

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/protos/pb"
)

// fakeControlServer records the last call each RPC received, and returns
// err from every RPC when set.
type fakeControlServer struct {
	pb.UnimplementedRelayControlServer

	lastEnsureListener  *pb.EnsureListenerRequest
	lastRegisterCapture *pb.RegisterCaptureRequest
	err                 error
}

func (s *fakeControlServer) EnsureListener(_ context.Context, req *pb.EnsureListenerRequest) (*pb.EnsureListenerResponse, error) {
	s.lastEnsureListener = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.EnsureListenerResponse{}, nil
}

func (s *fakeControlServer) RegisterCapture(_ context.Context, req *pb.RegisterCaptureRequest) (*pb.RegisterCaptureResponse, error) {
	s.lastRegisterCapture = req
	if s.err != nil {
		return nil, s.err
	}
	return &pb.RegisterCaptureResponse{}, nil
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

func TestRelayEnsureListener(t *testing.T) {
	t.Run("sends the requested ports", func(t *testing.T) {
		fake := &fakeControlServer{}
		r := newTestRelay(t, fake)

		err := r.EnsureListener(t.Context(), []int{443, 8443})
		require.NoError(t, err)
		require.NotNil(t, fake.lastEnsureListener)
		assert.Equal(t, []int32{443, 8443}, fake.lastEnsureListener.GetPorts())
	})

	t.Run("wraps a server error", func(t *testing.T) {
		wantErr := status.Error(codes.Internal, "boom")
		fake := &fakeControlServer{err: wantErr}
		r := newTestRelay(t, fake)

		err := r.EnsureListener(t.Context(), []int{443})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestRelayRegisterCapture(t *testing.T) {
	fake := &fakeControlServer{}
	r := newTestRelay(t, fake)

	err := r.RegisterCapture(t.Context(), "web", "/var/run/docker/netns/abc123")
	require.NoError(t, err)
	require.NotNil(t, fake.lastRegisterCapture)
	assert.Equal(t, "web", fake.lastRegisterCapture.GetId())
	assert.Equal(t, "/var/run/docker/netns/abc123", fake.lastRegisterCapture.GetNetnsPath())
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
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)
	return authority
}
