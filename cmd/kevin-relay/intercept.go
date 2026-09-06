package main

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"net"

	"github.com/justenwalker/kevin/protos/pb"
)

// captureTarget is one registered network namespace: the path applyCapture
// needs to reach it, and the exclusion list that shapes which ruleset it
// gets - see applyCapture's own doc comment for what an empty vs.
// non-empty list means.
type captureTarget struct {
	netnsPath    string
	excludeCIDRs []string
}

// RegisterCapture implements [pb.RelayControlServer]. It installs the
// transparent-capture ruleset for id's network namespace, for every port
// currently captured, and records the target only once that succeeds - a
// failed call leaves nothing behind for reapplyCapture to retry against.
func (p *relayProcess) RegisterCapture(ctx context.Context, req *pb.RegisterCaptureRequest) (*pb.RegisterCaptureResponse, error) {
	target := captureTarget{netnsPath: req.GetNetnsPath(), excludeCIDRs: req.GetExcludeCidrs()}
	if err := applyCapture(target.netnsPath, p.capturePorts(), p.self, target.excludeCIDRs); err != nil {
		return nil, fmt.Errorf("relay: apply capture for %q: %w", req.GetId(), err)
	}

	p.mu.Lock()
	if p.netnsPaths == nil {
		p.netnsPaths = make(map[string]captureTarget)
	}
	p.netnsPaths[req.GetId()] = target
	p.mu.Unlock()

	log.Ctx(ctx).Debug("relay: registered capture", "id", req.GetId(), "netns_path", target.netnsPath)
	return &pb.RegisterCaptureResponse{}, nil
}

// EnsureListener implements [pb.RelayControlServer]. It registers an
// External route: when req.Host is set, the relay's own DNS answers a query
// for it - the only way a workload with no network namespace the relay can
// capture (a kind pod) reaches the interception. It opens a listener for
// each of req.Ports beyond the relay's always-on 80 and 443, then
// re-applies capture to every already-registered container - so a route
// declared after some containers already exist still reaches them.
func (p *relayProcess) EnsureListener(ctx context.Context, req *pb.EnsureListenerRequest) (*pb.EnsureListenerResponse, error) {
	if host := req.GetHost(); host != "" {
		p.intercept.AddIntercept(host)
	}
	for _, port := range req.GetPorts() {
		if err := p.ensureListener(int(port)); err != nil {
			return nil, fmt.Errorf("relay: open listener for port %d: %w", port, err)
		}
	}
	p.reapplyCapture(ctx)
	return &pb.EnsureListenerResponse{}, nil
}

// capturePorts returns every TCP port currently captured: the relay's
// always-on 80 and 443, plus any port an External route has opened.
func (p *relayProcess) capturePorts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	ports := make([]int, 0, 2+len(p.extraLns))
	ports = append(ports, 80, 443)
	for port := range p.extraLns {
		ports = append(ports, port)
	}
	return ports
}

// reapplyCapture re-installs the capture ruleset for every registered
// namespace with the current port set. A target whose netns is gone - it
// was removed since RegisterCapture ran - fails silently and is evicted,
// rather than failing the caller.
func (p *relayProcess) reapplyCapture(ctx context.Context) {
	p.mu.Lock()
	targets := make(map[string]captureTarget, len(p.netnsPaths))
	maps.Copy(targets, p.netnsPaths)
	p.mu.Unlock()

	ports := p.capturePorts()
	for id, target := range targets {
		if err := applyCapture(target.netnsPath, ports, p.self, target.excludeCIDRs); err != nil {
			log.Ctx(ctx).Debug("relay: re-apply capture failed, evicting", "error", err, "id", id, "netns_path", target.netnsPath)
			p.mu.Lock()
			delete(p.netnsPaths, id)
			p.mu.Unlock()
		}
	}
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
	p.runGrp.Go(func() error { return serveIntercept(p.runCtx, ln, p.proxyAddr, port, p.intercept.fakeIPs) })
	return nil
}

// serveIntercept accepts connections on ln - bound for an external route's
// declared port beyond the fixed :80/:443 pair - and dispatches each: a
// fake-IP match tunnels directly with no protocol assumption at all,
// anything else falls through to handleHTTPS or handleHTTP by peeking
// whether its first byte looks like a TLS handshake record.
func serveIntercept(ctx context.Context, ln net.Listener, proxyAddr string, port int, fakeIPs *fakeIPPool) error {
	return acceptLoop(ctx, ln, func(conn net.Conn) {
		dispatch(ctx, conn, proxyAddr, port, fakeIPs, func(conn net.Conn) {
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
