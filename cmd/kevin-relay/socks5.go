package main

import (
	"context"
	"fmt"
	"net"

	"github.com/things-go/go-socks5"
)

// serveSOCKS5 runs a SOCKS5 server on listen until ctx is done. This is the
// relay's other job: reached from inside a kind cluster's node, it lets a
// client outside the cluster dial an arbitrary in-cluster address - the
// opposite direction from the domain relay's job, same binary.
func serveSOCKS5(ctx context.Context, listen string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("relay: listen socks5: %w", err)
	}
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
