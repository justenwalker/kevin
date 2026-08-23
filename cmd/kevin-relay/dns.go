package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sync/errgroup"
)

// dnsTTL is the time to live of an answer that the relay generates. A short
// value lets a client pick up a step that restarts.
const dnsTTL = 30

// dnsTimeout bounds a forwarded query to the upstream resolver.
const dnsTimeout = 5 * time.Second

// dnsRelay answers a query for the environment domain, and forwards every
// other query to the upstream resolver.
type dnsRelay struct {
	domain   string
	self     string
	upstream string
	client   dns.Client
}

// newDNSRelay builds a relay for domain. self is the address that an A
// query under domain resolves to. upstream is the resolver for every other
// query.
func newDNSRelay(domain, self, upstream string) *dnsRelay {
	return &dnsRelay{
		domain:   normalizeDomain(domain),
		self:     self,
		upstream: upstream,
		client:   dns.Client{Timeout: dnsTimeout},
	}
}

// ServeDNS implements [dns.Handler].
func (r *dnsRelay) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	ctx := context.Background()

	if len(req.Question) == 1 && matchesDomain(req.Question[0].Name, r.domain) {
		if err := w.WriteMsg(r.answer(req)); err != nil {
			log.Ctx(ctx).Debug("relay: dns: write answer failed", "error", err)
		}
		return
	}

	reply, err := r.forward(w, req)
	if err != nil {
		log.Ctx(ctx).Debug("relay: dns: forward failed", "error", err)
		dns.HandleFailed(w, req)
		return
	}
	if err := w.WriteMsg(reply); err != nil {
		log.Ctx(ctx).Debug("relay: dns: write reply failed", "error", err)
	}
}

// answer builds the reply for a query under the domain. A qtype of A
// resolves to self. Every other qtype gets an empty NOERROR answer: an
// empty AAAA is what makes a dual-stack client fall back to the A record
// instead of failing.
func (r *dnsRelay) answer(req *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true

	q := req.Question[0]
	if q.Qtype == dns.TypeA {
		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN A %s", q.Name, dnsTTL, r.self))
		if err == nil {
			m.Answer = append(m.Answer, rr)
		}
	}
	return m
}

// forward sends req to the upstream resolver, over the same transport that
// the client used, and returns the reply unchanged.
func (r *dnsRelay) forward(w dns.ResponseWriter, req *dns.Msg) (*dns.Msg, error) {
	client := r.client
	if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		client.Net = "tcp"
	}

	reply, _, err := client.Exchange(req, r.upstream)
	if err != nil {
		return nil, fmt.Errorf("relay: exchange with %s: %w", r.upstream, err)
	}
	return reply, nil
}

// normalizeDomain lowercases domain and removes a trailing dot, so a
// comparison against a DNS question name needs no further cleanup.
func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSuffix(domain, "."))
}

// matchesDomain reports whether name, a DNS question name, is domain or a
// name under domain. domain must already be normalized with
// [normalizeDomain]. The comparison ignores case and a trailing dot.
func matchesDomain(name, domain string) bool {
	name = normalizeDomain(name)
	if name == domain {
		return true
	}
	return strings.HasSuffix(name, "."+domain)
}

// dnsServer runs the UDP and the TCP listener of the relay's DNS service.
type dnsServer struct {
	udp *dns.Server
	tcp *dns.Server
}

// bindDNSServer binds addr for UDP and for TCP, both served by handler. It
// resolves an ephemeral port such as ":0" before run starts, so a caller
// reads the bound address back with addr.
func bindDNSServer(ctx context.Context, addr string, handler dns.Handler) (*dnsServer, error) {
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return nil, fmt.Errorf("relay: listen dns udp: %w", err)
	}
	ln, err := lc.Listen(ctx, "tcp", pc.LocalAddr().String())
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("relay: listen dns tcp: %w", err)
	}

	return &dnsServer{
		udp: &dns.Server{PacketConn: pc, Net: "udp", Handler: handler},
		tcp: &dns.Server{Listener: ln, Net: "tcp", Handler: handler},
	}, nil
}

// addr is the bound address of the DNS service.
func (s *dnsServer) addr() string { return s.udp.PacketConn.LocalAddr().String() }

// run serves both listeners until ctx is done or one of them fails.
func (s *dnsServer) run(ctx context.Context) error {
	grp, ctx := errgroup.WithContext(ctx)
	grp.Go(func() error { return runDNSServer(ctx, s.udp) })
	grp.Go(func() error { return runDNSServer(ctx, s.tcp) })
	return grp.Wait() //nolint:wrapcheck // runDNSServer already wraps its own error; wrapping again here would double it
}

// runDNSServer serves srv until ctx is done, and shuts srv down when it is.
// srv must already carry a bound Listener or PacketConn.
func runDNSServer(ctx context.Context, srv *dns.Server) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ActivateAndServe() }()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("relay: dns %s server: %w", srv.Net, err)
		}
		return nil
	case <-ctx.Done():
		_ = srv.ShutdownContext(context.WithoutCancel(ctx))
		<-errCh
		return nil
	}
}
