package proxy_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/things-go/go-socks5"

	"github.com/justenwalker/kevin/internal/proxy"
)

// startSOCKS5 runs a bare SOCKS5 server on a loopback port until the test
// ends, and returns its address. Mirrors cmd/kevin-relay/socks5.go's
// serveSOCKS5, the same server a builtin:kind relay pod actually runs.
func startSOCKS5(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() { _ = socks5.NewServer().Serve(ln) }()
	t.Cleanup(cancel)

	return ln.Addr().String()
}

func TestRoutesThroughASOCKS5RelayToTheRealUpstream(t *testing.T) {
	p, client := startTestProxyWithClient(t)
	target := createTestUpstream(t, "from the cluster")
	relay := startSOCKS5(t)

	p.AddRoutes(proxy.Route{
		Host:     "app.kevin.test",
		Upstream: "socks5://" + relay + "/" + getTestURLHost(t, target.URL),
	})

	resp, body := getTestURL(t, client, "http://app.kevin.test/")

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "from the cluster", body)
	assert.Equal(t, "app.kevin.test", resp.Header.Get("X-Seen-Host"),
		"the workload must see the hostname the client asked for, not the relay target")
}

func TestTwoRoutesThroughTheSameRelayReachTheirOwnUpstream(t *testing.T) {
	p, client := startTestProxyWithClient(t)
	relay := startSOCKS5(t)
	first := createTestUpstream(t, "from first")
	second := createTestUpstream(t, "from second")

	p.AddRoutes(
		proxy.Route{Host: "first.kevin.test", Upstream: "socks5://" + relay + "/" + getTestURLHost(t, first.URL)},
		proxy.Route{Host: "second.kevin.test", Upstream: "socks5://" + relay + "/" + getTestURLHost(t, second.URL)},
	)

	// A request to first leaves an idle keep-alive connection through the
	// relay pooled. A request to second, sharing the same relay, must not
	// get that connection handed back to it - the pool has to key on the
	// real target, not the relay address every relay route rewrites
	// r.URL.Host to.
	_, firstBody := getTestURL(t, client, "http://first.kevin.test/")
	require.Equal(t, "from first", firstBody)

	_, secondBody := getTestURL(t, client, "http://second.kevin.test/")
	assert.Equal(t, "from second", secondBody)
}

func TestRouteThroughAnUnreachableRelayFailsCleanly(t *testing.T) {
	p, client := startTestProxyWithClient(t)

	// Nothing listens here: the relay dial itself must fail, not hang or
	// panic, and the client must see a normal gateway error.
	p.AddRoutes(proxy.Route{
		Host:     "broken.kevin.test",
		Upstream: "socks5://127.0.0.1:1/unreachable:80",
	})

	resp, _ := getTestURL(t, client, "http://broken.kevin.test/")
	assert.Equal(t, 502, resp.StatusCode)
}
