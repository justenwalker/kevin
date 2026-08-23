// Command kevin-relay has two mutually exclusive modes, one binary either
// way, selected by subcommand.
//
// forward answers DNS for the environment domain and forwards HTTP and
// HTTPS traffic to the host proxy. It runs inside a container on the shared
// docker network. A workload reaches a step under the domain with no proxy
// configuration, because the relay resolves the domain and forwards the
// traffic to the proxy on the host.
//
// socks5-gateway runs a SOCKS5 relay instead - the opposite direction, run
// as a Pod inside a kind cluster so a client outside the cluster can reach
// an arbitrary in-cluster address.
//
// Flags configure the relay, because the container or pod that runs it
// carries no config file:
//
//	kevin-relay forward --domain kevin.home --proxy host.docker.internal:18080
//	kevin-relay socks5-gateway --listen :1080
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/justenwalker/kevin/internal/logging"
)

var log = logging.New("relay")

// config holds the flags that configure one relay process.
type config struct {
	domain      string
	proxyAddr   string
	self        string
	dnsListen   string
	httpListen  string
	httpsListen string
	upstreamDNS string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), interruptSignals...)
	defer cancel()

	root := rootCommand()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "kevin-relay:", err)
		return 1
	}
	return 0
}

// rootCommand builds the kevin-relay command tree: forward and
// socks5-gateway, kevin-relay's two mutually exclusive modes.
func rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "kevin-relay",
		Short:         "Relay traffic for one kevin environment",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(forwardCommand(), socks5GatewayCommand())
	return root
}

// forwardCommand answers DNS for the environment domain and forwards HTTP
// and HTTPS traffic to the host proxy.
func forwardCommand() *cobra.Command {
	var cfg config

	cmd := &cobra.Command{
		Use:   "forward",
		Short: "Answer DNS for the environment domain and forward HTTP/HTTPS to the host proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveForward(cmd.Context(), cfg)
		},
	}

	bindForwardFlags(cmd.Flags(), &cfg)
	for _, name := range []string{"domain", "proxy"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}

	return cmd
}

// bindForwardFlags defines the forward command's flags on fs, writing parsed
// values into cfg. Split out from forwardCommand so a test can parse flags
// into a config without going through cobra.Command.Execute, which would
// also run the command body.
func bindForwardFlags(fs *pflag.FlagSet, cfg *config) {
	fs.StringVar(&cfg.domain, "domain", "", "the environment domain, such as kevin.home (required)")
	fs.StringVar(&cfg.proxyAddr, "proxy", "", "the host proxy address, such as host.docker.internal:18080 (required)")
	fs.StringVar(&cfg.self, "self", "", "the address the DNS server answers with (default: resolved from the network interfaces)")
	fs.StringVar(&cfg.dnsListen, "dns-listen", ":53", "the address the DNS server listens on")
	fs.StringVar(&cfg.httpListen, "http-listen", ":80", "the address the HTTP forwarder listens on")
	fs.StringVar(&cfg.httpsListen, "https-listen", ":443", "the address the HTTPS forwarder listens on")
	fs.StringVar(&cfg.upstreamDNS, "upstream-dns", "127.0.0.11:53", "the DNS server for a query outside the domain")
}

// socks5GatewayCommand runs a SOCKS5 relay for a client outside a kind
// cluster to reach an arbitrary in-cluster address.
func socks5GatewayCommand() *cobra.Command {
	var listen string

	cmd := &cobra.Command{
		Use:   "socks5-gateway",
		Short: "Run a SOCKS5 relay for a client outside the cluster to reach an in-cluster address",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveSOCKS5(cmd.Context(), listen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "the address the SOCKS5 relay listens on (required)")
	if err := cmd.MarkFlagRequired("listen"); err != nil {
		panic(err)
	}
	return cmd
}

// serveForward creates the relay listeners and runs them until ctx is done
// or one of them fails.
func serveForward(ctx context.Context, cfg config) error {
	p, err := newRelayProcess(ctx, cfg)
	if err != nil {
		return err
	}
	return p.run(ctx)
}

// relayProcess holds every listener that the relay binds before it starts
// serving traffic.
type relayProcess struct {
	dns       *dnsServer
	httpsLn   net.Listener
	httpLn    net.Listener
	proxyAddr string
}

// newRelayProcess resolves self when cfg.self is empty, then binds the DNS,
// the HTTP, and the HTTPS listeners. A caller reads back an ephemeral
// address with dnsAddr, httpAddr, or httpsAddr before run starts.
func newRelayProcess(ctx context.Context, cfg config) (*relayProcess, error) {
	self := cfg.self
	if self == "" {
		addr, err := resolveSelf()
		if err != nil {
			return nil, err
		}
		self = addr
	}

	var lc net.ListenConfig
	httpsLn, err := lc.Listen(ctx, "tcp", cfg.httpsListen)
	if err != nil {
		return nil, fmt.Errorf("relay: listen https: %w", err)
	}
	httpLn, err := lc.Listen(ctx, "tcp", cfg.httpListen)
	if err != nil {
		return nil, fmt.Errorf("relay: listen http: %w", err)
	}

	relay := newDNSRelay(cfg.domain, self, cfg.upstreamDNS)
	dnsSrv, err := bindDNSServer(ctx, cfg.dnsListen, relay)
	if err != nil {
		return nil, err
	}

	log.Ctx(ctx).Info("relay starting",
		"domain", cfg.domain, "self", self, "proxy", cfg.proxyAddr,
		"dns_listen", dnsSrv.addr(), "http_listen", httpLn.Addr(), "https_listen", httpsLn.Addr())

	return &relayProcess{dns: dnsSrv, httpsLn: httpsLn, httpLn: httpLn, proxyAddr: cfg.proxyAddr}, nil
}

// run serves DNS, HTTP, and HTTPS until ctx is done or one of them fails.
func (p *relayProcess) run(ctx context.Context) error {
	grp, ctx := errgroup.WithContext(ctx)
	grp.Go(func() error { return p.dns.run(ctx) })
	grp.Go(func() error {
		return acceptLoop(ctx, p.httpsLn, func(conn net.Conn) { handleHTTPS(ctx, conn, p.proxyAddr) })
	})
	grp.Go(func() error {
		return acceptLoop(ctx, p.httpLn, func(conn net.Conn) { handleHTTP(ctx, conn, p.proxyAddr) })
	})
	return grp.Wait() //nolint:wrapcheck // each sub-server already wraps its own error; wrapping again here would double it
}

// dnsAddr is the bound address of the DNS listener.
func (p *relayProcess) dnsAddr() string { return p.dns.addr() }

// httpAddr is the bound address of the HTTP forwarder.
func (p *relayProcess) httpAddr() string { return p.httpLn.Addr().String() }

// httpsAddr is the bound address of the HTTPS forwarder.
func (p *relayProcess) httpsAddr() string { return p.httpsLn.Addr().String() }

// resolveSelf picks the address that the DNS server answers with when -self
// is empty.
func resolveSelf() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("relay: list network interfaces: %w", err)
	}
	return pickAddress(addrs)
}

// pickAddress returns the first non-loopback IPv4 address in addrs.
func pickAddress(addrs []net.Addr) (string, error) {
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() {
			continue
		}
		return ip4.String(), nil
	}
	return "", ErrNoAddress
}

// acceptLoop accepts a connection from ln until ctx is done, and runs handle
// for each one in its own goroutine. acceptLoop closes ln when ctx is done.
func acceptLoop(ctx context.Context, ln net.Listener, handle func(net.Conn)) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // a canceled context is a clean shutdown
			}
			return fmt.Errorf("relay: accept on %s: %w", ln.Addr(), err)
		}
		go handle(conn)
	}
}
