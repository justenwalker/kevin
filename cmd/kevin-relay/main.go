// Command kevin-relay has two subcommands, one binary either way.
//
// forward answers DNS for the environment domain, forwards HTTP and HTTPS
// traffic to the host proxy, and runs a SOCKS5 gateway. It runs inside a
// container on the shared docker network. A workload reaches a step under
// the domain with no proxy configuration, because the relay resolves the
// domain and forwards the traffic to the proxy on the host; the host, in
// the opposite direction, reaches an arbitrary address on the docker
// network (such as a builtin:container step's expose entry with
// relay: true) through the SOCKS5 gateway's one published port.
//
// socks5-gateway runs the same SOCKS5 relay standalone, without the
// DNS/HTTP/HTTPS listeners - run as a Pod inside a kind cluster so a client
// outside the cluster can reach an arbitrary in-cluster address.
//
// Flags configure the relay, because the container or pod that runs it
// carries no config file:
//
//	kevin-relay forward --domain kevin.home --proxy host.docker.internal:18080 --socks5-listen :1080
//	kevin-relay socks5-gateway --listen :1080
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/protos/pb"
)

var log = logging.New("relay")

// config holds the flags that configure one relay process.
type config struct {
	domain        string
	proxyAddr     string
	self          string
	dnsListen     string
	httpListen    string
	httpsListen   string
	socks5Listen  string
	controlListen string
	upstreamDNS   string
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
	fs.StringVar(&cfg.socks5Listen, "socks5-listen", ":1080", "the address the SOCKS5 gateway listens on")
	fs.StringVar(&cfg.controlListen, "control-listen", ":8053", "the address the intercept control endpoint listens on")
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
			var lc net.ListenConfig
			ln, err := lc.Listen(cmd.Context(), "tcp", listen)
			if err != nil {
				return fmt.Errorf("relay: listen socks5: %w", err)
			}
			return serveSOCKS5(cmd.Context(), ln)
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
	dns        *dnsServer
	httpsLn    net.Listener
	httpLn     net.Listener
	socks5Ln   net.Listener
	controlLn  net.Listener
	controlSrv *grpc.Server
	proxyAddr  string
	self       selfAddrs

	// runCtx and runGrp let a control RPC handler (RegisterCapture,
	// EnsureListener), dispatched on its own goroutine by controlSrv, join
	// the same lifecycle and goroutine group run started. Set once by run,
	// before controlSrv starts accepting.
	runCtx context.Context //nolint:containedctx // set once by run, read-only afterward; see the field doc
	runGrp *errgroup.Group

	mu         sync.Mutex
	extraLns   map[int]net.Listener // opened on demand, for a port beyond :80/:443
	netnsPaths map[string]string    // step id -> container network namespace path
}

// newRelayProcess resolves self when cfg.self is empty, then binds the DNS,
// the HTTP, the HTTPS, the SOCKS5, and the control listeners. A caller
// reads back an ephemeral address with dnsAddr, httpAddr, httpsAddr, or
// socks5Addr before run starts.
func newRelayProcess(ctx context.Context, cfg config) (*relayProcess, error) {
	self := selfAddrs{V4: cfg.self}
	if cfg.self == "" {
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
	socks5Ln, err := lc.Listen(ctx, "tcp", cfg.socks5Listen)
	if err != nil {
		return nil, fmt.Errorf("relay: listen socks5: %w", err)
	}
	controlLn, err := lc.Listen(ctx, "tcp", cfg.controlListen)
	if err != nil {
		return nil, fmt.Errorf("relay: listen control: %w", err)
	}

	controlTLS, err := controlTLSConfig()
	if err != nil {
		return nil, err
	}
	controlSrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(controlTLS)))

	relay := newDNSRelay(cfg.domain, self, cfg.upstreamDNS)
	dnsSrv, err := bindDNSServer(ctx, cfg.dnsListen, relay)
	if err != nil {
		return nil, err
	}

	log.Ctx(ctx).Info("relay starting",
		"domain", cfg.domain, "self_v4", self.V4, "self_v6", self.V6, "proxy", cfg.proxyAddr,
		"dns_listen", dnsSrv.addr(), "http_listen", httpLn.Addr(), "https_listen", httpsLn.Addr(),
		"socks5_listen", socks5Ln.Addr(), "control_listen", controlLn.Addr())

	p := &relayProcess{
		dns: dnsSrv, httpsLn: httpsLn, httpLn: httpLn, socks5Ln: socks5Ln,
		controlLn: controlLn, controlSrv: controlSrv, proxyAddr: cfg.proxyAddr, self: self,
	}
	pb.RegisterRelayControlServer(controlSrv, p)
	return p, nil
}

// run serves DNS, HTTP, HTTPS, the SOCKS5 gateway, and the control gRPC
// server until ctx is done or one of them fails.
func (p *relayProcess) run(ctx context.Context) error {
	grp, ctx := errgroup.WithContext(ctx)
	p.runCtx, p.runGrp = ctx, grp
	grp.Go(func() error { return p.dns.run(ctx) })
	grp.Go(func() error {
		return acceptLoop(ctx, p.httpsLn, func(conn net.Conn) { handleHTTPS(ctx, conn, p.proxyAddr, 443) })
	})
	grp.Go(func() error {
		return acceptLoop(ctx, p.httpLn, func(conn net.Conn) { handleHTTP(ctx, conn, p.proxyAddr) })
	})
	grp.Go(func() error { return serveSOCKS5(ctx, p.socks5Ln) })
	grp.Go(func() error { return p.runControl(ctx) })
	return grp.Wait() //nolint:wrapcheck // each sub-server already wraps its own error; wrapping again here would double it
}

// runControl serves the control gRPC server until ctx is done or it fails.
func (p *relayProcess) runControl(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- p.controlSrv.Serve(p.controlLn) }()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("relay: control server: %w", err)
		}
		return nil
	case <-ctx.Done():
		p.controlSrv.GracefulStop()
		<-errCh
		return nil
	}
}

// dnsAddr is the bound address of the DNS listener.
func (p *relayProcess) dnsAddr() string { return p.dns.addr() }

// httpAddr is the bound address of the HTTP forwarder.
func (p *relayProcess) httpAddr() string { return p.httpLn.Addr().String() }

// httpsAddr is the bound address of the HTTPS forwarder.
func (p *relayProcess) httpsAddr() string { return p.httpsLn.Addr().String() }

// socks5Addr is the bound address of the SOCKS5 gateway.
func (p *relayProcess) socks5Addr() string { return p.socks5Ln.Addr().String() }

// controlAddr is the bound address of the intercept control endpoint.
func (p *relayProcess) controlAddr() string { return p.controlLn.Addr().String() }

// selfAddrs holds the address the DNS server answers a matching A or AAAA
// query with, in each address family an interface carries one for. An empty
// field means the relay has no usable address in that family - the AAAA
// side of dnsRelay.answer falls back to an empty NOERROR when V6 is empty,
// same as an IPv4-only relay always has.
type selfAddrs struct {
	V4 string
	V6 string
}

// resolveSelf picks the addresses that the DNS server answers with when
// -self is empty.
func resolveSelf() (selfAddrs, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return selfAddrs{}, fmt.Errorf("relay: list network interfaces: %w", err)
	}
	return pickAddress(addrs)
}

// pickAddress returns the first non-loopback, non-link-local address of
// each family in addrs. A link-local address (169.254.0.0/16, fe80::/10) is
// skipped: it needs a zone/scope id to dial, and other containers on the
// network reach the relay by a routable address instead.
func pickAddress(addrs []net.Addr) (selfAddrs, error) {
	var out selfAddrs
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			if out.V4 == "" {
				out.V4 = ip4.String()
			}
			continue
		}
		if out.V6 == "" {
			out.V6 = ip.String()
		}
	}
	if out.V4 == "" && out.V6 == "" {
		return selfAddrs{}, ErrNoAddress
	}
	return out, nil
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
