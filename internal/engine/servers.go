package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"syscall"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/httpserver"
	"github.com/justenwalker/kevin/internal/proxy"
)

// serving is a background Serve call and the means to stop it.
type serving struct {
	stop context.CancelFunc
	done <-chan error
	once sync.Once
	err  error
}

// Close stops serving and reports how it ended. Close is idempotent.
func (s *serving) Close() error {
	s.once.Do(func() {
		s.stop()
		s.err = <-s.done
	})
	return s.err
}

// consoleServer is a running console and the means to stop it.
type consoleServer struct {
	serving

	addr string
}

// startConsole binds the listener and serves handler on it.
func startConsole(ctx context.Context, handler http.Handler, listen string) (*consoleServer, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("supervisor: listen on %s: %w", listen, err)
	}

	serveCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- httpserver.Serve(serveCtx, ln, handler) }()

	return &consoleServer{serving: serving{stop: stop, done: done}, addr: ln.Addr().String()}, nil
}

// proxyServer is a running proxy and the means to stop it.
type proxyServer struct {
	serving

	proxy *proxy.Proxy
	addr  string

	// gatewayAddr is where the proxy listens on the docker bridge gateway.
	// The relay reaches the proxy there, from inside the shared network.
	gatewayAddr string
}

// proxyOptions configures [startProxy].
type proxyOptions struct {
	// Network is the shared docker network. The gateway listener binds its
	// gateway address.
	Network string

	// Listen is the address of the primary, host-facing listener.
	Listen string

	// GatewayPort is the gateway listener's port, used as-is with no
	// fallback - a bind failure is a hard error.
	GatewayPort int

	Domain      string
	Allow       []string
	Deny        bool
	Passthrough bool

	// OnRecord receives one Record per request.
	OnRecord func(proxy.Record)
}

// startProxy binds the listener and serves on it. The listener binds before
// the DAG runs, because kind copies the proxy address into its node containers
// when it creates them, and the address must not change afterwards.
//
// startProxy also binds a second listener on the gateway address of
// opts.Network, when the host can bind that address. A container on that
// network reaches the proxy there. The proxy never binds 0.0.0.0.
func startProxy(ctx context.Context, rt gatewayLookup, authority *ca.CA, opts proxyOptions) (*proxyServer, error) {
	p, err := proxy.New(authority, proxy.Options{
		Domain:      opts.Domain,
		Allow:       opts.Allow,
		Deny:        opts.Deny,
		Passthrough: opts.Passthrough,
		OnRecord:    opts.OnRecord,
	})
	if err != nil {
		return nil, err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("supervisor: listen on %s: %w", opts.Listen, err)
	}

	listeners := []net.Listener{ln}
	closeAll := func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}

	gateway, err := rt.NetworkGateway(ctx, opts.Network)
	if err != nil {
		closeAll()
		return nil, err
	}

	gatewayAddr := ln.Addr().String()
	if gateway.V4.IsValid() {
		gatewayLn, bindErr := bindGatewayAddr(ctx, &lc, gateway.V4, opts.GatewayPort)
		switch {
		case bindErr == nil:
			listeners = append(listeners, gatewayLn)
			gatewayAddr = gatewayLn.Addr().String()
		case errors.Is(bindErr, syscall.EADDRNOTAVAIL):
			// Docker Desktop on macOS and Windows runs the daemon inside a VM.
			// The gateway address exists only inside that VM, and the host
			// cannot bind it there. host.docker.internal already reaches a
			// listener on the host loopback, so the primary listener covers
			// the relay too.
		default:
			closeAll()
			return nil, bindErr
		}
	}
	if gateway.V6.IsValid() {
		// Best-effort: a relay reaches the proxy over host.docker.internal
		// or the v4 gateway bind above regardless, so a v6 bind failure here
		// is not fatal.
		if gatewayLn, bindErr := bindGatewayAddr(ctx, &lc, gateway.V6, opts.GatewayPort); bindErr == nil {
			listeners = append(listeners, gatewayLn)
		}
	}

	// The proxy must outlive an interrupt, because removal of a step can still
	// need it.
	serveCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- p.Serve(serveCtx, listeners...) }()

	return &proxyServer{
		serving:     serving{stop: stop, done: done},
		proxy:       p,
		addr:        ln.Addr().String(),
		gatewayAddr: gatewayAddr,
	}, nil
}

// bindGatewayAddr listens on addr:port, for a proxy listener on one of a
// docker network's gateway addresses.
func bindGatewayAddr(ctx context.Context, lc *net.ListenConfig, addr netip.Addr, port int) (net.Listener, error) {
	hostPort := net.JoinHostPort(addr.String(), strconv.Itoa(port))
	ln, err := lc.Listen(ctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("supervisor: listen on %s: %w", hostPort, err)
	}
	return ln, nil
}
