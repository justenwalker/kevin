// Package httpserver runs an http.Handler on a listener until ctx ends.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ReadHeaderTimeout bounds how long Serve waits for request headers.
const ReadHeaderTimeout = 30 * time.Second

// Serve runs handler on ln until ctx is done, then closes the server.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpserver: serve: %w", err)
	}
	return nil
}
