package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchesDomain(t *testing.T) {
	tests := []struct {
		name   string
		qname  string
		domain string
		want   bool
	}{
		{name: "the bare domain", qname: "kevin.home.", domain: "kevin.home", want: true},
		{name: "a subdomain", qname: "web.kevin.home.", domain: "kevin.home", want: true},
		{name: "a deeper subdomain", qname: "api.web.kevin.home.", domain: "kevin.home", want: true},
		{
			name: "a name that merely ends with the same letters", qname: "notkevin.home.",
			domain: "kevin.home", want: false,
		},
		{name: "a different domain", qname: "web.other.test.", domain: "kevin.home", want: false},
		{name: "mixed case", qname: "WEB.Kevin.Home.", domain: "kevin.home", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesDomain(tt.qname, tt.domain)
			assert.Equal(t, tt.want, got, "a suffix match must require a dot boundary")
		})
	}
}

func TestDNSRelayAnswers(t *testing.T) {
	t.Run("an A query under the domain", func(t *testing.T) {
		relay := newDNSRelay("kevin.home", "10.0.0.5", "127.0.0.11:53")

		req := new(dns.Msg)
		req.SetQuestion("web.kevin.home.", dns.TypeA)

		reply := relay.answer(req)

		require.Len(t, reply.Answer, 1, "an A query under the domain must get one answer")
		a, ok := reply.Answer[0].(*dns.A)
		require.True(t, ok, "the answer must be an A record")
		assert.Equal(t, "10.0.0.5", a.A.String())
		assert.Equal(t, dns.RcodeSuccess, reply.Rcode)
	})

	t.Run("an AAAA query with no records and no error", func(t *testing.T) {
		relay := newDNSRelay("kevin.home", "10.0.0.5", "127.0.0.11:53")

		req := new(dns.Msg)
		req.SetQuestion("web.kevin.home.", dns.TypeAAAA)

		reply := relay.answer(req)

		assert.Empty(t, reply.Answer, "an AAAA query must get no answer, not an error")
		assert.Equal(t, dns.RcodeSuccess, reply.Rcode, "an empty AAAA must answer NOERROR, not NXDOMAIN")
	})
}

func TestDNSServerForwardsAQueryOutsideTheDomain(t *testing.T) {
	upstream := startFakeUpstreamDNS(t)

	relay := newDNSRelay("kevin.home", "10.0.0.5", upstream)
	srv, err := bindDNSServer(t.Context(), "127.0.0.1:0", relay)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = srv.run(ctx) }()

	client := dns.Client{Timeout: 2 * time.Second}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	reply, _, err := client.Exchange(req, srv.addr())
	require.NoError(t, err)
	require.Len(t, reply.Answer, 1, "a query outside the domain must reach the upstream and come back unchanged")
	a, ok := reply.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "93.184.216.34", a.A.String())
}

// startFakeUpstreamDNS runs a minimal DNS server that answers every A query
// with a fixed address, and returns its address.
func startFakeUpstreamDNS(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		rr, err := dns.NewRR(req.Question[0].Name + " 30 IN A 93.184.216.34")
		if err == nil {
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})

	srv := &dns.Server{PacketConn: pc, Handler: mux}
	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.ShutdownContext(t.Context()) })
	<-ready

	return pc.LocalAddr().String()
}
