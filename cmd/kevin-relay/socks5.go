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
// udpPorts is the pre-published port pool a UDP ASSOCIATE session binds
// from; an empty udpPorts serves CONNECT normally but fails every
// ASSOCIATE immediately.
func serveSOCKS5(ctx context.Context, ln net.Listener, udpPorts []int) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	log.Ctx(ctx).Info("socks5 relay starting", "listen", ln.Addr(), "udp_pool_size", len(udpPorts))
	srv := socks5.NewServer(socks5.WithAssociateHandle(newAssociateHandler(newUDPPool(udpPorts))))
	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return fmt.Errorf("relay: socks5 serve: %w", err)
	}
	return nil
}
