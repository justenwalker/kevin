package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardFlags(t *testing.T) {
	t.Run("applies defaults", func(t *testing.T) {
		cfg := parseForwardFlags(t, []string{"--domain", "kevin.home", "--proxy", "host.docker.internal:18080"})

		assert.Equal(t, "kevin.home", cfg.domain)
		assert.Equal(t, "host.docker.internal:18080", cfg.proxyAddr)
		assert.Empty(t, cfg.self, "self must stay empty so the relay resolves it at start")
		assert.Equal(t, ":53", cfg.dnsListen)
		assert.Equal(t, ":80", cfg.httpListen)
		assert.Equal(t, ":443", cfg.httpsListen)
		assert.Equal(t, "127.0.0.11:53", cfg.upstreamDNS)
	})

	t.Run("overrides every default", func(t *testing.T) {
		cfg := parseForwardFlags(t, []string{
			"--domain", "kevin.home",
			"--proxy", "host.docker.internal:18080",
			"--self", "172.20.0.9",
			"--dns-listen", "127.0.0.1:5353",
			"--http-listen", "127.0.0.1:8080",
			"--https-listen", "127.0.0.1:8443",
			"--upstream-dns", "8.8.8.8:53",
		})

		assert.Equal(t, "172.20.0.9", cfg.self)
		assert.Equal(t, "127.0.0.1:5353", cfg.dnsListen)
		assert.Equal(t, "127.0.0.1:8080", cfg.httpListen)
		assert.Equal(t, "127.0.0.1:8443", cfg.httpsListen)
		assert.Equal(t, "8.8.8.8:53", cfg.upstreamDNS)
	})
}

// parseForwardFlags parses args with the same flags forwardCommand binds,
// without going through cobra.Command.Execute (which would also run the
// command body and try to bind the relay's listeners).
func parseForwardFlags(t *testing.T, args []string) config {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var cfg config
	bindForwardFlags(fs, &cfg)
	require.NoError(t, fs.Parse(args))
	return cfg
}

func TestForwardCommandRequiresDomainAndProxy(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no domain", args: []string{"--proxy", "p:1"}},
		{name: "no proxy", args: []string{"--domain", "kevin.home"}},
		{name: "neither", args: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := forwardCommand()
			cmd.SetArgs(tt.args)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			require.Error(t, cmd.Execute())
		})
	}
}

func TestSocks5GatewayCommandRequiresListen(t *testing.T) {
	cmd := socks5GatewayCommand()
	cmd.SetArgs(nil)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.Error(t, cmd.Execute())
}

func TestPickAddressSkipsLoopbackAndIPv6(t *testing.T) {
	tests := []struct {
		name    string
		addrs   []net.Addr
		want    string
		wantErr error
	}{
		{
			name: "one usable ipv4 address",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
				&net.IPNet{IP: net.ParseIP("172.20.0.9"), Mask: net.CIDRMask(16, 32)},
			},
			want: "172.20.0.9",
		},
		{
			name: "an ipv6 address before the ipv4 address",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
				&net.IPNet{IP: net.ParseIP("172.20.0.9"), Mask: net.CIDRMask(16, 32)},
			},
			want: "172.20.0.9",
		},
		{
			name:    "only loopback",
			addrs:   []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}},
			wantErr: ErrNoAddress,
		},
		{
			name:    "no addresses",
			wantErr: ErrNoAddress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickAddress(tt.addrs)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAcceptLoop(t *testing.T) {
	t.Run("stops cleanly when the context is done", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errCh := make(chan error, 1)
		go func() { errCh <- acceptLoop(ctx, ln, func(net.Conn) {}) }()

		cancel()

		select {
		case err := <-errCh:
			require.NoError(t, err, "a shutdown through the context must not be reported as a failure")
		case <-time.After(2 * time.Second):
			t.Fatal("acceptLoop did not stop after the context was canceled")
		}
	})

	t.Run("runs handle for each connection", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		got := make(chan struct{}, 1)
		go func() {
			_ = acceptLoop(ctx, ln, func(conn net.Conn) {
				_ = conn.Close()
				got <- struct{}{}
			})
		}()

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("acceptLoop never ran handle for the connection")
		}
	})
}
