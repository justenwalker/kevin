package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
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
		assert.Empty(t, cfg.udpRelayPorts, "no UDP ASSOCIATE capacity by default")
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
			"--udp-relay-ports", "40000-40015",
		})

		assert.Equal(t, "172.20.0.9", cfg.self)
		assert.Equal(t, "127.0.0.1:5353", cfg.dnsListen)
		assert.Equal(t, "127.0.0.1:8080", cfg.httpListen)
		assert.Equal(t, "127.0.0.1:8443", cfg.httpsListen)
		assert.Equal(t, "8.8.8.8:53", cfg.upstreamDNS)
		assert.Equal(t, "40000-40015", cfg.udpRelayPorts)
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
		want    selfAddrs
		wantErr error
	}{
		{
			name: "one usable ipv4 address",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
				&net.IPNet{IP: net.ParseIP("172.20.0.9"), Mask: net.CIDRMask(16, 32)},
			},
			want: selfAddrs{V4: "172.20.0.9"},
		},
		{
			name: "a link-local ipv6 address before the ipv4 address",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
				&net.IPNet{IP: net.ParseIP("172.20.0.9"), Mask: net.CIDRMask(16, 32)},
			},
			want: selfAddrs{V4: "172.20.0.9"},
		},
		{
			name: "a dual-stack interface",
			addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("172.20.0.9"), Mask: net.CIDRMask(16, 32)},
				&net.IPNet{IP: net.ParseIP("fd00::9"), Mask: net.CIDRMask(64, 128)},
			},
			want: selfAddrs{V4: "172.20.0.9", V6: "fd00::9"},
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

func TestServeForward(t *testing.T) {
	loopback := config{
		domain: "kevin.home", proxyAddr: "127.0.0.1:1", self: "127.0.0.1", upstreamDNS: "127.0.0.1:1",
		dnsListen: "127.0.0.1:0", httpListen: "127.0.0.1:0", httpsListen: "127.0.0.1:0",
		socks5Listen: "127.0.0.1:0", controlListen: "127.0.0.1:0",
	}

	t.Run("binds every listener and stops with its context", func(t *testing.T) {
		controlCertEnv(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		p, err := newRelayProcess(ctx, loopback)
		require.NoError(t, err)
		for _, addr := range []string{p.dnsAddr(), p.httpAddr(), p.httpsAddr(), p.socks5Addr(), p.controlAddr()} {
			assert.NotEmpty(t, addr)
		}

		done := make(chan error, 1)
		go func() { done <- p.run(ctx) }()

		req := new(dns.Msg)
		req.SetQuestion("web.kevin.home.", dns.TypeA)
		client := dns.Client{Timeout: time.Second}
		require.Eventually(t, func() bool {
			_, _, err := client.Exchange(req, p.dnsAddr())
			return err == nil
		}, 5*time.Second, 20*time.Millisecond, "the DNS server never started answering")
		cancel()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the relay kept running after its context ended")
		}
	})

	t.Run("serveForward reports a listener that cannot bind", func(t *testing.T) {
		controlCertEnv(t)
		var lc net.ListenConfig
		taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = taken.Close() })
		cfg := loopback
		cfg.httpsListen = taken.Addr().String()

		err = serveForward(t.Context(), cfg)

		require.ErrorContains(t, err, "listen https")
	})

	t.Run("rejects a malformed UDP port range", func(t *testing.T) {
		controlCertEnv(t)
		cfg := loopback
		cfg.udpRelayPorts = "nope"

		_, err := newRelayProcess(t.Context(), cfg)

		require.ErrorIs(t, err, ErrInvalidUDPRelayPorts)
	})

	t.Run("fails without control TLS material", func(t *testing.T) {
		t.Setenv(tlsCertEnv, "")

		_, err := newRelayProcess(t.Context(), loopback)

		require.ErrorContains(t, err, "control tls certificate")
	})
}

func TestSocks5GatewayCommand(t *testing.T) {
	t.Run("serves until its context ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cmd := socks5GatewayCommand()
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true

		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(ctx) }()
		cancel()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the gateway kept running after its context ended")
		}
	})

	t.Run("rejects a malformed UDP port range", func(t *testing.T) {
		cmd := socks5GatewayCommand()
		cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--udp-relay-ports", "nope"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true

		require.ErrorIs(t, cmd.ExecuteContext(t.Context()), ErrInvalidUDPRelayPorts)
	})

	t.Run("reports an address it cannot bind", func(t *testing.T) {
		cmd := socks5GatewayCommand()
		cmd.SetArgs([]string{"--listen", "not an address"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true

		require.ErrorContains(t, cmd.ExecuteContext(t.Context()), "listen socks5")
	})
}

func TestRun(t *testing.T) {
	t.Run("exits 1 for an unknown command", func(t *testing.T) {
		assert.Equal(t, 1, run([]string{"nonsense"}))
	})

	t.Run("exits 1 for a command that fails", func(t *testing.T) {
		assert.Equal(t, 1, run([]string{"socks5-gateway", "--listen", "not an address"}))
	})
}

func TestRootCommand(t *testing.T) {
	commands := rootCommand().Commands()
	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, c.Name())
	}

	assert.ElementsMatch(t, []string{"forward", "socks5-gateway", "port-forward"}, names)
}
