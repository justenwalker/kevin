//go:build integration

package podman

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
)

func TestExec(t *testing.T) {
	t.Run("runs inside a container", func(t *testing.T) {
		requirePodman(t)
		c := Client{}

		name := "kevin-podman-exec-test"
		_, err := c.Run(t.Context(), cri.RunSpec{
			Image: "busybox:stable",
			Name:  name,
			Cmd:   []string{"sh", "-c", "sleep 300"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

		out, err := c.Exec(t.Context(), name, "echo", "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", strings.TrimSpace(out), "Exec must return the standard output of the command")
	})

	t.Run("reports a missing container", func(t *testing.T) {
		requirePodman(t)
		c := Client{}

		_, err := c.Exec(t.Context(), "kevin-podman-exec-test-absent", "echo", "hello")
		require.Error(t, err)
		assert.ErrorIs(t, err, cri.ErrNotFound)
	})
}

func TestExecInput(t *testing.T) {
	t.Run("feeds standard input to the command", func(t *testing.T) {
		requirePodman(t)
		c := Client{}

		name := "kevin-podman-exec-input-test"
		_, err := c.Run(t.Context(), cri.RunSpec{
			Image: "busybox:stable",
			Name:  name,
			Cmd:   []string{"sh", "-c", "sleep 300"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

		out, err := c.ExecInput(t.Context(), name, strings.NewReader("hello\n"), "cat")
		require.NoError(t, err)
		assert.Equal(t, "hello", strings.TrimSpace(out), "ExecInput must feed stdin to the command")
	})
}

func TestNetworkConnect(t *testing.T) {
	requirePodman(t)
	c := Client{}

	network := "kevin-podman-network-connect-test"
	require.NoError(t, c.NetworkCreate(t.Context(), network, cri.NetworkOptions{}))
	t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

	name := "kevin-podman-network-connect-test-container"
	_, err := c.Run(t.Context(), cri.RunSpec{
		Image:  "busybox:stable",
		Name:   name,
		Cmd:    []string{"sh", "-c", "sleep 300"},
		CapAdd: []string{"NET_ADMIN"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

	require.NoError(t, c.NetworkConnect(t.Context(), network, name))
	require.NoError(t, c.NetworkConnect(t.Context(), network, name), "NetworkConnect must be idempotent")

	info, err := c.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.Contains(t, info.IPs, network, "the container must carry an address on the connected network")

	gw, err := c.NetworkGateway(t.Context(), network)
	require.NoError(t, err)
	routes, err := c.Exec(t.Context(), name, "ip", "route")
	require.NoError(t, err)
	assert.Contains(t, routes, "default via "+gw.V4.String(), "the connected network must carry the default route")
}

// TestNetworkRemoveToleratesActiveEndpoints proves NetworkRemove leaves a
// network in place, instead of erroring, when a container is still on it -
// a container podman itself created outside kevin's own tracking, such as a
// builtin:kubernetes node joined directly through the "kind" CLI.
func TestNetworkRemoveToleratesActiveEndpoints(t *testing.T) {
	requirePodman(t)
	c := Client{}

	network := "kevin-podman-network-remove-in-use-test"
	require.NoError(t, c.NetworkCreate(t.Context(), network, cri.NetworkOptions{}))
	t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

	name := "kevin-podman-network-remove-in-use-test-container"
	_, err := c.Run(t.Context(), cri.RunSpec{
		Image:   "busybox:stable",
		Name:    name,
		Network: network,
		Cmd:     []string{"sh", "-c", "sleep 300"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

	require.NoError(t, c.NetworkRemove(t.Context(), network), "a network still in use must not be an error")

	_, err = c.NetworkGateway(t.Context(), network)
	require.NoError(t, err, "the network must still exist")

	require.NoError(t, c.Remove(t.Context(), name))
	require.NoError(t, c.NetworkRemove(t.Context(), network))

	_, err = c.NetworkGateway(t.Context(), network)
	assert.ErrorIs(t, err, cri.ErrNotFound, "the network must be gone once nothing is left on it")
}

func TestNetworkGateway(t *testing.T) {
	requirePodman(t)
	c := Client{}

	t.Run("returns the network's ipv4 gateway", func(t *testing.T) {
		network := "kevin-podman-network-gateway-test"
		require.NoError(t, c.NetworkCreate(t.Context(), network, cri.NetworkOptions{}))
		t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

		gw, err := c.NetworkGateway(t.Context(), network)
		require.NoError(t, err)
		assert.True(t, gw.V4.IsValid(), "the gateway must carry an ipv4 address")
		assert.False(t, gw.V6.IsValid(), "a network without IPv6 must be ipv4-only")
	})

	t.Run("returns both gateways for a dual-stack network", func(t *testing.T) {
		network := "kevin-podman-network-gateway-v6-test"
		require.NoError(t, c.NetworkCreate(t.Context(), network, cri.NetworkOptions{IPv6: true}))
		t.Cleanup(func() { _ = c.NetworkRemove(context.WithoutCancel(t.Context()), network) })

		gw, err := c.NetworkGateway(t.Context(), network)
		require.NoError(t, err)
		assert.True(t, gw.V4.IsValid(), "a dual-stack network must still carry an ipv4 gateway")
		assert.True(t, gw.V6.IsValid(), "a dual-stack network must carry an ipv6 gateway")
	})

	t.Run("reports ErrNotFound for a missing network", func(t *testing.T) {
		_, err := c.NetworkGateway(t.Context(), "kevin-podman-network-gateway-test-absent")
		assert.ErrorIs(t, err, cri.ErrNotFound)
	})
}

func TestListByLabel(t *testing.T) {
	requirePodman(t)
	c := Client{}

	name := "kevin-podman-list-by-label-test"
	_, err := c.Run(t.Context(), cri.RunSpec{
		Image:  "busybox:stable",
		Name:   name,
		Cmd:    []string{"sh", "-c", "sleep 300"},
		Labels: map[string]string{"kevin.list-test": "yes"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Remove(context.WithoutCancel(t.Context()), name) })

	names, err := c.ListByLabel(t.Context(), "kevin.list-test", "yes")
	require.NoError(t, err)
	assert.Contains(t, names, name)

	names, err = c.ListByLabel(t.Context(), "kevin.list-test", "no-such-value")
	require.NoError(t, err)
	assert.NotContains(t, names, name)
}

func TestSave(t *testing.T) {
	requirePodman(t)
	c := Client{}

	rc, err := c.Save(t.Context(), "busybox:stable")
	require.NoError(t, err)

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "Save must stream a non-empty tar archive")
	require.NoError(t, rc.Close())
}

// requirePodman skips a test when podman does not answer.
func requirePodman(t *testing.T) {
	t.Helper()
	if err := (Client{}).Available(t.Context()); err != nil {
		t.Skip("podman is unavailable:", err)
	}
}
