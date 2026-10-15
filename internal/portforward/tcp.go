package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/net/proxy"
)

// TCP is one local loopback TCP listener that transparently
// forwards every accepted connection through a SOCKS5 dial to a fixed
// target, so a plain TCP client (psql, curl, ...) can use it without being
// SOCKS5-aware itself. Addr and Close come from the embedded net.Listener -
// Close stops accepting new connections; a connection already piping keeps
// running until its own two sides close. Create one with [StartTCP].
type TCP struct {
	net.Listener

	dialer proxy.ContextDialer
	target string
}

// StartTCP opens a loopback listener for spec and starts accepting
// connections on it, each forwarded through spec.Relay to spec.Target.
func StartTCP(ctx context.Context, spec Spec) (*TCP, error) {
	dialer, err := proxy.SOCKS5("tcp", spec.Relay, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("portforward: %s: socks5 dialer: %w", spec.Name, err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("portforward: socks5 dialer does not support DialContext")
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", spec.listenAddr())
	if err != nil {
		return nil, fmt.Errorf("portforward: %s: listen: %w", spec.Name, err)
	}

	pf := &TCP{Listener: ln, dialer: contextDialer, target: spec.Target}
	go pf.accept() //nolint:contextcheck // deliberate - see the comment on handle for why
	return pf, nil
}

// accept accepts connections until pf's listener is closed, handling each
// one in its own goroutine. A single connection's dial or copy failure
// never stops the loop - only the listener closing (by the caller, at
// teardown) does, which Accept reports as an error and accept then returns
// on.
func (pf *TCP) accept() {
	for {
		conn, err := pf.Accept()
		if err != nil {
			return
		}
		go pf.handle(conn)
	}
}

// handle dials pf.target through pf.dialer for one accepted connection and
// pipes bytes both ways until either side closes. A dial failure closes
// conn immediately; the client sees a reset, same as it would against any
// unreachable plain TCP target.
//
// The dial deliberately uses context.Background(), not the context
// StartTCP was given: that context comes from the DAG walk's
// NodeFunc closure, which is errgroup.WithContext-derived and gets
// canceled the moment the whole walk finishes - often within milliseconds
// of the step that created this forward reaching "ready". A forward must
// keep accepting new connections for the rest of the session, long after
// its own step's Up call returned; passing the walk-scoped context through
// here would cancel every dial for a connection accepted after that point,
// closing it before any bytes move. The listener itself, not this context,
// is what stops new work - pf.Close() at session teardown ends Accept and
// the accept loop returns.
func (pf *TCP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	upstream, err := pf.dialer.DialContext(context.Background(), "tcp", pf.target)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	pipe(conn, upstream)
}

// pipe copies bytes bidirectionally between a and b until either side
// closes. It returns once the first direction ends; the caller's deferred
// closes on both ends then unblock whichever copy is still running - the
// same shape internal/proxy's pipeUpgrade uses for the HTTP-upgrade case.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
