package main

import (
	"bufio"
	"context"
	"fmt"
	"net"

	"github.com/justenwalker/kevin/protos/pb"
)

// RegisterCapture implements [pb.RelayControlServer]. It records id's
// network namespace path for the coming transparent-capture mechanism; the
// path is not yet acted on.
func (p *relayProcess) RegisterCapture(ctx context.Context, req *pb.RegisterCaptureRequest) (*pb.RegisterCaptureResponse, error) {
	p.mu.Lock()
	if p.netnsPaths == nil {
		p.netnsPaths = make(map[string]string)
	}
	p.netnsPaths[req.GetId()] = req.GetNetnsPath()
	p.mu.Unlock()

	log.Ctx(ctx).Debug("relay: registered capture", "id", req.GetId(), "netns_path", req.GetNetnsPath())
	return &pb.RegisterCaptureResponse{}, nil
}

// EnsureListener implements [pb.RelayControlServer]. It opens a listener for
// each of req.Ports beyond the relay's always-on 80 and 443.
func (p *relayProcess) EnsureListener(_ context.Context, req *pb.EnsureListenerRequest) (*pb.EnsureListenerResponse, error) {
	for _, port := range req.GetPorts() {
		if err := p.ensureListener(int(port)); err != nil {
			return nil, fmt.Errorf("relay: open listener for port %d: %w", port, err)
		}
	}
	return &pb.EnsureListenerResponse{}, nil
}

// ensureListener opens a listener on port and serves it on the run
// goroutine group, unless port is already served - by the fixed :80/:443
// listeners, or by an earlier ensureListener call for the same port. It
// must not be called before run has set runCtx/runGrp.
func (p *relayProcess) ensureListener(port int) error {
	if port == 80 || port == 443 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.extraLns == nil {
		p.extraLns = make(map[int]net.Listener)
	}
	if _, ok := p.extraLns[port]; ok {
		return nil
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(p.runCtx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("relay: listen intercept port %d: %w", port, err)
	}
	p.extraLns[port] = ln
	p.runGrp.Go(func() error { return serveIntercept(p.runCtx, ln, p.proxyAddr, port) })
	return nil
}

// serveIntercept accepts connections on ln - bound for an external route's
// declared port beyond the fixed :80/:443 pair - and dispatches each to
// handleHTTPS or handleHTTP depending on whether its first byte looks like
// a TLS handshake record.
func serveIntercept(ctx context.Context, ln net.Listener, proxyAddr string, port int) error {
	return acceptLoop(ctx, ln, func(conn net.Conn) {
		br := bufio.NewReader(conn)
		first, err := br.Peek(1)
		if err != nil {
			_ = conn.Close()
			return
		}

		pc := &peekedConn{Conn: conn, r: br}
		if first[0] == recordTypeHandshake {
			handleHTTPS(ctx, pc, proxyAddr, port)
			return
		}
		handleHTTP(ctx, pc, proxyAddr)
	})
}

// peekedConn is a net.Conn whose Read replays r's buffered bytes before
// falling through to the underlying connection, so peeking a byte to
// decide a protocol doesn't lose it for the handler that runs next.
type peekedConn struct {
	net.Conn

	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		return n, fmt.Errorf("relay: read: %w", err)
	}
	return n, nil
}
