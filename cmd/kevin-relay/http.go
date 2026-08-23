package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

// handleHTTP reads every request that conn carries, in absolute-URI proxy
// form, to proxyAddr, and copies each response back. It loops until conn
// closes or a read fails.
func handleHTTP(ctx context.Context, conn net.Conn, proxyAddr string) {
	defer func() { _ = conn.Close() }()

	client := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(client)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Ctx(ctx).Debug("relay: http: read request failed", "error", err)
			}
			return
		}

		if err := forwardHTTP(ctx, conn, proxyAddr, req); err != nil {
			log.Ctx(ctx).Debug("relay: http: forward request failed", "error", err, "host", req.Host)
			return
		}
	}
}

// forwardHTTP sends req to proxyAddr in absolute-URI proxy form, and copies
// the response to client.
func forwardHTTP(ctx context.Context, client io.Writer, proxyAddr string, req *http.Request) error {
	var d net.Dialer
	upstream, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("relay: dial proxy: %w", err)
	}
	defer func() { _ = upstream.Close() }()

	req.URL.Scheme = "http"
	req.URL.Host = req.Host
	if err = req.WriteProxy(upstream); err != nil {
		return fmt.Errorf("relay: write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		return fmt.Errorf("relay: read response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err = resp.Write(client); err != nil {
		return fmt.Errorf("relay: write response: %w", err)
	}
	return nil
}
