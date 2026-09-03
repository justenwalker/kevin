package main

import (
	"context"
	"fmt"
	"net"

	"github.com/things-go/go-socks5"
)

// serveSOCKS5 runs a SOCKS5 server on ln until ctx is done. This is the
// relay's other job: reached either as a kind cluster's pod (a client
// outside the cluster dials an arbitrary in-cluster address) or as a
// listener on the domain relay itself (a client outside the docker network
// dials an arbitrary container on it) - same server, same binary.
func serveSOCKS5(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	log.Ctx(ctx).Info("socks5 relay starting", "listen", ln.Addr())
	if err := socks5.NewServer().Serve(ln); err != nil && ctx.Err() == nil {
		return fmt.Errorf("relay: socks5 serve: %w", err)
	}
	return nil
}
