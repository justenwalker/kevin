package main

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"net"
	"slices"

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
	target := captureTarget{netnsPath: req.GetNetns().GetPath(), excludeCIDRs: req.GetNetns().GetExcludeCidrs()}
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

// SetIntercepts implements [pb.RelayControlServer]. It replaces what
// req.Owner registered, then recomputes the whole relay's intercept state
// from every owner: the relay's own DNS answers a query for each named host -
// the only way a workload with no network namespace the relay can capture (a
// kind pod) reaches the interception - and a listener is open for each named
// port beyond the always-on 80 and 443, with none left for a port no owner
// names any more. It then re-applies capture to every already-registered
// container, so a route declared after some containers already exist still
// reaches them and a dropped port stops being redirected.
//
// An owner is recorded before its listeners open, so a failed call leaves
// its intercepts in place for the caller to clear with an empty list. Capture
// is re-applied even then, so no container keeps a redirect to a port that
// just closed.
func (p *relayProcess) SetIntercepts(ctx context.Context, req *pb.SetInterceptsRequest) (*pb.SetInterceptsResponse, error) {
	// Steps come up in parallel, so two calls can overlap: one call applies
	// the totals it computed, start to finish, before the next computes its own.
	p.interceptMu.Lock()
	defer p.interceptMu.Unlock()
	defer p.reapplyCapture(ctx)

	p.mu.Lock()
	if len(req.GetIntercepts()) == 0 {
		delete(p.intercepts, req.GetOwner())
	} else {
		if p.intercepts == nil {
			p.intercepts = make(map[string][]*pb.InterceptRoute)
		}
		p.intercepts[req.GetOwner()] = req.GetIntercepts()
	}
	hosts, ports := p.interceptTotalsLocked()
	p.mu.Unlock()

	p.intercept.SetIntercepts(hosts)
	if err := p.syncListeners(ctx, ports); err != nil {
		return nil, err
	}
	return &pb.SetInterceptsResponse{}, nil
}

// interceptTotalsLocked returns every host and port some owner intercepts.
// The caller holds p.mu.
func (p *relayProcess) interceptTotalsLocked() ([]string, map[int]struct{}) {
	var hosts []string
	ports := make(map[int]struct{})
	for _, routes := range p.intercepts {
		for _, route := range routes {
			if route.GetHost() != "" {
				hosts = append(hosts, route.GetHost())
			}
			for _, port := range route.GetPorts() {
				ports[int(port)] = struct{}{}
			}
		}
	}
	return hosts, ports
}

// capturePorts returns every TCP port currently captured: the relay's
// always-on 80 and 443, plus any port an intercept route has opened.
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

// extraListener is a listener opened for an intercept port beyond the
// always-on 80 and 443.
type extraListener struct {
	ln     net.Listener
	cancel context.CancelFunc
}

// syncListeners opens a listener for each of ports not already served and
// closes the listener of each extra port not in ports. Closing a listener
// also cancels the connections already open on it. Port 80 and 443 are
// served by the fixed listeners and are skipped. It must not be called
// before run has set runCtx/runGrp. A listener lives as long as runCtx, not
// the ctx of the call that opened it.
func (p *relayProcess) syncListeners(ctx context.Context, ports map[int]struct{}) error { //nolint:contextcheck // a listener lives as long as runCtx, not the RPC that opened it
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.extraLns == nil {
		p.extraLns = make(map[int]extraListener)
	}

	for port, extra := range p.extraLns {
		if _, ok := ports[port]; !ok {
			extra.cancel()
			_ = extra.ln.Close() // so a call that reopens the port at once finds it free
			delete(p.extraLns, port)
		}
	}

	for _, port := range slices.Sorted(maps.Keys(ports)) {
		if port == 80 || port == 443 {
			continue
		}
		if _, ok := p.extraLns[port]; ok {
			continue
		}
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("relay: open listener for port %d: %w", port, err)
		}
		lnCtx, cancel := context.WithCancel(p.runCtx)
		p.extraLns[port] = extraListener{ln: ln, cancel: cancel}
		p.runGrp.Go(func() error { return serveIntercept(lnCtx, ln, p.proxyAddr, port, p.intercept.fakeIPs) })
	}
	return nil
}

// serveIntercept accepts connections on ln - bound for an intercept route's
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
