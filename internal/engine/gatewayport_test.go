package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// bindGatewayPort reserves a free port on the gateway address and reports
// it, or skips the test - Docker Desktop's VM on macOS/Windows makes the
// gateway address unbindable from the host at all (EADDRNOTAVAIL), the
// same case startProxy itself falls back on.
func bindGatewayPort(t *testing.T, gateway netip.Addr) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort(gateway.String(), "0"))
	if err != nil && errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Skip("the gateway address is not bindable from the host here:", err)
	}
	require.NoError(t, err)
	return ln
}

func TestGatewayPortRoundTrips(t *testing.T) {
	workspace := t.TempDir()

	assert.Equal(t, 0, loadGatewayPort(workspace), "an empty workspace has no recorded port")

	saveGatewayPort(workspace, 54321)
	assert.Equal(t, 54321, loadGatewayPort(workspace), "loadGatewayPort must report what saveGatewayPort wrote")
}

func TestGatewayPortIgnoresGarbage(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, gatewayPortFile), []byte("not-a-port"), 0o600))

	assert.Equal(t, 0, loadGatewayPort(workspace), "unparsable content must report 0, not an error")
}

// TestStartProxyPinsGatewayPort proves that a nonzero opts.GatewayPort is
// used as-is for the gateway listener, instead of whatever loadGatewayPort
// would otherwise report.
func TestStartProxyPinsGatewayPort(t *testing.T) {
	requireDocker(t)

	cfg := &config.Config{Project: "kevin-gwport-pin-test", Dir: t.TempDir()}
	workspace, authority, err := prepare(t.Context(), cfg)
	require.NoError(t, err)
	network := NetworkName(cfg.Project)
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	gateway, err := dockerClient.NetworkGateway(t.Context(), network)
	require.NoError(t, err)

	// Reserve a free port on the gateway address, then free it again -
	// startProxy is asked to pin exactly that port back.
	probe := bindGatewayPort(t, gateway)
	pinnedPort := mustPort(t, probe.Addr().String())
	require.NoError(t, probe.Close())

	server, err := startProxy(t.Context(), authority, proxyOptions{
		Network:     network,
		Workspace:   workspace,
		Listen:      "127.0.0.1:0",
		GatewayPort: pinnedPort,
		Domain:      "kevin.test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	assert.Equal(t, pinnedPort, mustPort(t, server.gatewayAddr))
	assert.Equal(t, 0, loadGatewayPort(workspace), "a pinned port must not be persisted to the auto-reuse file")
}

// TestStartProxyPinnedGatewayPortConflict proves that startProxy fails
// outright when opts.GatewayPort is already taken, rather than silently
// falling back to a different port - a pinned port is pinned for a
// reason, so a caller must learn it did not get what it asked for.
func TestStartProxyPinnedGatewayPortConflict(t *testing.T) {
	requireDocker(t)

	cfg := &config.Config{Project: "kevin-gwport-conflict-test", Dir: t.TempDir()}
	workspace, authority, err := prepare(t.Context(), cfg)
	require.NoError(t, err)
	network := NetworkName(cfg.Project)
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	gateway, err := dockerClient.NetworkGateway(t.Context(), network)
	require.NoError(t, err)

	held := bindGatewayPort(t, gateway)
	defer func() { _ = held.Close() }()
	heldPort := mustPort(t, held.Addr().String())

	_, err = startProxy(t.Context(), authority, proxyOptions{
		Network:     network,
		Workspace:   workspace,
		Listen:      "127.0.0.1:0",
		GatewayPort: heldPort,
		Domain:      "kevin.test",
	})
	require.Error(t, err, "a pinned port already in use must fail, not fall back silently")
}

// mustPort parses the port out of addr, failing the test if it doesn't.
func mustPort(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port
}
