package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/justenwalker/kevin/protos/pb"
)

// TestSetInterceptsOpensADeclaredPort proves a SetIntercepts control call
// opens a listener for a declared port beyond the fixed :80/:443 pair,
// dispatching a TLS connection on it the same way the fixed :443 listener
// does - CONNECTing to the proxy for the declared port, not a literal 443.
func (s *RelayProcessSuite) TestSetInterceptsOpensADeclaredPort() {
	t := s.T()

	connectLine := make(chan string, 1)
	connectLn := startConnectStub(t, connectLine)
	defer func() { _ = connectLn.Close() }()

	clientTLS := newTestControlEnv(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	proc, err := newRelayProcess(ctx, config{
		domain:        relayTestDomain,
		proxyAddr:     connectLn.Addr().String(),
		self:          "10.20.30.40",
		dnsListen:     "127.0.0.1:0",
		httpListen:    "127.0.0.1:0",
		httpsListen:   "127.0.0.1:0",
		socks5Listen:  "127.0.0.1:0",
		controlListen: "127.0.0.1:0",
		upstreamDNS:   s.upstreamPC.LocalAddr().String(),
	})
	s.Require().NoError(err)

	done := make(chan error, 1)
	go func() { done <- proc.run(ctx) }()

	conn, err := grpc.NewClient(proc.controlAddr(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	s.Require().NoError(err)
	defer func() { _ = conn.Close() }()

	const sni = "s3.us-east-1.amazonaws.com"
	_, err = pb.NewRelayControlClient(conn).SetIntercepts(t.Context(), &pb.SetInterceptsRequest{Owner: "web", Intercepts: []*pb.InterceptRoute{{Host: sni, Ports: []int32{8443}}}})
	s.Require().NoError(err)

	dnsClient := dns.Client{Timeout: 2 * time.Second}
	dnsReq := new(dns.Msg)
	dnsReq.SetQuestion(sni+".", dns.TypeA)
	dnsReply, _, err := dnsClient.Exchange(dnsReq, proc.dnsAddr())
	s.Require().NoError(err)
	s.Require().Len(dnsReply.Answer, 1, "a kind pod with no netns the relay can capture needs the relay's own DNS answer to reach the interception")
	a, ok := dnsReply.Answer[0].(*dns.A)
	s.Require().True(ok)
	s.Equal("198.18.0.1", a.A.String(), "an intercept host resolves to a fake IP, not self")

	var d net.Dialer
	raw, err := d.DialContext(t.Context(), "tcp", "127.0.0.1:8443")
	s.Require().NoError(err)

	tlsConn := tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	handshakeDone := make(chan struct{})
	go func() {
		_ = tlsConn.HandshakeContext(t.Context())
		close(handshakeDone)
	}()

	select {
	case line := <-connectLine:
		s.Equal("CONNECT "+sni+":8443 HTTP/1.1", line, "the declared port, not a literal 443, must reach the proxy")
	case <-time.After(2 * time.Second):
		s.Fail("the stub proxy never saw a CONNECT request")
	}

	_ = raw.Close()
	<-handshakeDone
	cancel()
	s.Require().NoError(<-done)
}

// startInterceptRelay runs a relay process and returns it with a control
// client. The relay stops when the test ends.
func (s *RelayProcessSuite) startInterceptRelay() (*relayProcess, pb.RelayControlClient) { //nolint:ireturn // the generated gRPC client is an interface
	t := s.T()
	clientTLS := newTestControlEnv(t)

	ctx, cancel := context.WithCancel(t.Context())
	proc, err := newRelayProcess(ctx, config{
		domain:        relayTestDomain,
		proxyAddr:     s.proxyStub.Listener.Addr().String(),
		self:          "10.20.30.40",
		dnsListen:     "127.0.0.1:0",
		httpListen:    "127.0.0.1:0",
		httpsListen:   "127.0.0.1:0",
		socks5Listen:  "127.0.0.1:0",
		controlListen: "127.0.0.1:0",
		upstreamDNS:   s.upstreamPC.LocalAddr().String(),
	})
	s.Require().NoError(err)

	done := make(chan error, 1)
	go func() { done <- proc.run(ctx) }()

	conn, err := grpc.NewClient(proc.controlAddr(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	s.Require().NoError(err)
	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
		<-done
	})
	return proc, pb.NewRelayControlClient(conn)
}

// freeTCPPort returns a port nothing listens on.
func freeTCPPort(t *testing.T) int32 {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return int32(addr.Port)
}

func tcpListening(t *testing.T, port int32) bool {
	t.Helper()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func setIntercepts(t *testing.T, client pb.RelayControlClient, owner string, routes ...*pb.InterceptRoute) error {
	t.Helper()
	_, err := client.SetIntercepts(t.Context(), &pb.SetInterceptsRequest{Owner: owner, Intercepts: routes})
	return err
}

// TestSetInterceptsReleasesWhatNoOwnerNames proves a port stays open while
// any owner names it, closes once none does, and that the host stops being
// intercepted at the same time.
func (s *RelayProcessSuite) TestSetInterceptsReleasesWhatNoOwnerNames() {
	t := s.T()
	proc, client := s.startInterceptRelay()
	port := freeTCPPort(t)

	s.Require().NoError(setIntercepts(t, client, "a", &pb.InterceptRoute{Host: "a.example.com", Ports: []int32{port}}))
	s.Require().NoError(setIntercepts(t, client, "b", &pb.InterceptRoute{Host: "b.example.com", Ports: []int32{port}}))
	s.True(tcpListening(t, port))
	s.True(proc.intercept.matchesIntercept("a.example.com."))

	s.Require().NoError(setIntercepts(t, client, "a"))
	s.True(tcpListening(t, port), "b still names the port")
	s.False(proc.intercept.matchesIntercept("a.example.com."))
	s.True(proc.intercept.matchesIntercept("b.example.com."))

	s.Require().NoError(setIntercepts(t, client, "b", &pb.InterceptRoute{Host: "b.example.com"}))
	s.False(tcpListening(t, port), "no owner names the port any more")

	s.Require().NoError(setIntercepts(t, client, "b"))
	s.Require().NoError(setIntercepts(t, client, "never-registered"))
	s.False(proc.intercept.matchesIntercept("b.example.com."))
}

// TestSetInterceptsSharedHosts proves a host two owners name, and a wildcard
// host, stay intercepted until the last owner drops them.
func (s *RelayProcessSuite) TestSetInterceptsSharedHosts() {
	t := s.T()
	proc, client := s.startInterceptRelay()

	s.Require().NoError(setIntercepts(t, client, "a", &pb.InterceptRoute{Host: "shared.example.com"}, &pb.InterceptRoute{Host: "*.wild.example.com"}))
	s.Require().NoError(setIntercepts(t, client, "b", &pb.InterceptRoute{Host: "shared.example.com"}))
	s.True(proc.intercept.matchesIntercept("x.wild.example.com."))

	s.Require().NoError(setIntercepts(t, client, "a"))
	s.True(proc.intercept.matchesIntercept("shared.example.com."), "b still names it")
	s.False(proc.intercept.matchesIntercept("x.wild.example.com."))

	s.Require().NoError(setIntercepts(t, client, "b"))
	s.False(proc.intercept.matchesIntercept("shared.example.com."))
}

// TestSetInterceptsConcurrent proves overlapping calls leave the state of
// the last word each owner said.
func (s *RelayProcessSuite) TestSetInterceptsConcurrent() {
	t := s.T()
	proc, client := s.startInterceptRelay()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			owner := fmt.Sprintf("o%d", i)
			for range 5 {
				s.NoError(setIntercepts(t, client, owner, &pb.InterceptRoute{Host: owner + ".example.com"}))
				s.NoError(setIntercepts(t, client, owner))
			}
			s.NoError(setIntercepts(t, client, owner, &pb.InterceptRoute{Host: owner + ".example.com"}))
		})
	}
	wg.Wait()

	for i := range 8 {
		s.True(proc.intercept.matchesIntercept(fmt.Sprintf("o%d.example.com.", i)))
	}
}

// TestSetInterceptsListenFailure proves a port that cannot open fails the
// call, and that the owner can still clear what it registered.
func (s *RelayProcessSuite) TestSetInterceptsListenFailure() {
	t := s.T()
	proc, client := s.startInterceptRelay()

	var lc net.ListenConfig
	taken, err := lc.Listen(t.Context(), "tcp", ":0")
	s.Require().NoError(err)
	defer func() { _ = taken.Close() }()
	addr, ok := taken.Addr().(*net.TCPAddr)
	s.Require().True(ok)

	err = setIntercepts(t, client, "a", &pb.InterceptRoute{Host: "a.example.com", Ports: []int32{int32(addr.Port)}})
	s.Require().Error(err)

	s.Require().NoError(setIntercepts(t, client, "a"))
	s.False(proc.intercept.matchesIntercept("a.example.com."))
}
