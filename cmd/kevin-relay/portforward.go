package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// udpIdleTimeout is how long a UDP client mapping lives without traffic.
const udpIdleTimeout = 30 * time.Second

// flagTCP names the port-forward flag that carries the TCP port.
const flagTCP = "tcp"

// portForwardCommand forwards the relay's TCP and UDP ports to the same
// ports on another host, so one container can publish a cluster node's
// relay ports.
func portForwardCommand() *cobra.Command {
	var target, udpRelayPorts string
	var tcpPort int

	cmd := &cobra.Command{
		Use:   "port-forward",
		Short: "Forward the relay's TCP and UDP ports to a target host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			udpPorts, err := parseUDPRelayPorts(udpRelayPorts)
			if err != nil {
				return err
			}
			return servePortForward(cmd.Context(), target, tcpPort, udpPorts)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "the host the ports are forwarded to (required)")
	cmd.Flags().IntVar(&tcpPort, flagTCP, 0, "the TCP port to forward (required)")
	cmd.Flags().StringVar(&udpRelayPorts, "udp-relay-ports", "", "the \"<start>-<end>\" UDP port range to forward (default: no UDP ports)")
	for _, name := range []string{"target", flagTCP} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
	return cmd
}

// servePortForward listens on tcpPort and each of udpPorts and forwards them
// to the same port on target until ctx is done or a listener fails.
func servePortForward(ctx context.Context, target string, tcpPort int, udpPorts []int) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(tcpPort))
	if err != nil {
		return fmt.Errorf("relay: port-forward: listen tcp %d: %w", tcpPort, err)
	}
	conns := make([]net.PacketConn, 0, len(udpPorts))
	closeAll := func() {
		_ = ln.Close()
		for _, c := range conns {
			_ = c.Close()
		}
	}
	for _, port := range udpPorts {
		c, err := lc.ListenPacket(ctx, "udp", ":"+strconv.Itoa(port))
		if err != nil {
			closeAll()
			return fmt.Errorf("relay: port-forward: listen udp %d: %w", port, err)
		}
		conns = append(conns, c)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return forwardTCP(gctx, ln, net.JoinHostPort(target, strconv.Itoa(tcpPort)))
	})
	for i, c := range conns {
		addr := net.JoinHostPort(target, strconv.Itoa(udpPorts[i]))
		g.Go(func() error { return forwardUDP(gctx, c, addr) })
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("relay: port-forward: %w", err)
	}
	return nil
}

// forwardTCP accepts connections on ln and copies each one to a fresh
// connection to addr, resolved on every dial. It closes ln before returning.
func forwardTCP(ctx context.Context, ln net.Listener, addr string) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		client, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("relay: port-forward: %w", ctx.Err())
			}
			return fmt.Errorf("relay: port-forward: accept: %w", err)
		}
		go func() {
			defer client.Close() //nolint:errcheck // best-effort teardown
			var d net.Dialer
			upstream, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				log.Ctx(ctx).Debug("relay: port-forward: dial failed", "error", err, "addr", addr)
				return
			}
			defer upstream.Close() //nolint:errcheck // best-effort teardown
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
			go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
			<-done
		}()
	}
}

// forwardUDP relays datagrams between each client of conn and addr. Each
// client address gets its own connected socket to addr, dropped after
// udpIdleTimeout without traffic, so replies return to the right client.
// It closes conn before returning.
func forwardUDP(ctx context.Context, conn net.PacketConn, addr string) error {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var mu sync.Mutex
	sessions := make(map[string]net.Conn)
	buf := make([]byte, 64*1024)
	for {
		n, client, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("relay: port-forward: %w", ctx.Err())
			}
			return fmt.Errorf("relay: port-forward: read udp: %w", err)
		}
		key := client.String()
		mu.Lock()
		up, ok := sessions[key]
		if !ok {
			var d net.Dialer
			up, err = d.DialContext(ctx, "udp", addr)
			if err != nil {
				mu.Unlock()
				log.Ctx(ctx).Debug("relay: port-forward: udp dial failed", "error", err, "addr", addr)
				continue
			}
			sessions[key] = up
			go func() {
				replyUDP(conn, client, up)
				mu.Lock()
				delete(sessions, key)
				mu.Unlock()
				_ = up.Close()
			}()
		}
		mu.Unlock()
		_ = up.SetDeadline(time.Now().Add(udpIdleTimeout))
		if _, err := up.Write(buf[:n]); err != nil {
			log.Ctx(ctx).Debug("relay: port-forward: udp write failed", "error", err, "addr", addr)
		}
	}
}

// replyUDP copies datagrams from up back to client until up goes idle or
// closes.
func replyUDP(conn net.PacketConn, client net.Addr, up net.Conn) {
	buf := make([]byte, 64*1024)
	for {
		n, err := up.Read(buf)
		if err != nil {
			return
		}
		_ = up.SetDeadline(time.Now().Add(udpIdleTimeout))
		if _, err := conn.WriteTo(buf[:n], client); err != nil {
			return
		}
	}
}
