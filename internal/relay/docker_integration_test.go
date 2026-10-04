//go:build integration

package relay_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/relay"
)

var dockerClient = docker.Client{}

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// fixtureImage builds a throwaway image that stays running whatever
// arguments Start passes it. Start always runs a container with
// "forward --domain ... --proxy ..." as its command, and a plain image has
// no entrypoint to receive them: the container tries to exec "forward" as a
// program and fails before it ever starts.
func fixtureImage(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	dockerfile := "FROM busybox:stable\nENTRYPOINT [\"sh\", \"-c\", \"sleep 300\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600))

	const tag = "kevin-relay-test-fixture:latest"
	cmd := exec.CommandContext(t.Context(), "docker", "build", "-t", tag, dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return tag
}

func TestStartAndClose(t *testing.T) {
	requireDocker(t)
	image := fixtureImage(t)

	network := "kevin-relay-test"
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: "relay-test"},
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	name := "kevin-relay-test-relay"
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })

	r, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   "relay-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     image,
		Authority: newTestAuthority(t),
	})
	require.NoError(t, err)

	info, err := dockerClient.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, name, info.Name, "the relay container must carry the project prefix and the relay suffix")
	assert.True(t, info.Running, "the relay container must be running")

	labels, err := dockerClient.ListByLabel(t.Context(), cri.LabelRole, relay.Role)
	require.NoError(t, err)
	assert.Contains(t, labels, name, "the relay container must carry the role label")

	assert.Regexp(t, `^\d+\.\d+\.\d+\.\d+$`, r.Addr(),
		"Addr must report the container address on the shared network")
	assert.Regexp(t, `^127\.0\.0\.1:\d+$`, r.SOCKS5Addr(),
		"SOCKS5Addr must report the loopback address the socks5 gateway is published on")

	udpAddrs := r.SOCKS5UDPAddrs()
	assert.Len(t, udpAddrs, 16, "the default pool size must be published")
	for port, addr := range udpAddrs {
		assert.Regexp(t, `^\d+$`, port)
		assert.Regexp(t, `^127\.0\.0\.1:\d+$`, addr)
	}

	require.NoError(t, r.Close())
	_, err = dockerClient.Inspect(t.Context(), name)
	require.ErrorIs(t, err, cri.ErrNotFound, "Close must remove the container")

	// Close is idempotent.
	require.NoError(t, r.Close())
}

func TestStartReusesRunningContainer(t *testing.T) {
	requireDocker(t)
	image := fixtureImage(t)

	network := "kevin-relay-reuse-test"
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: "relay-reuse-test"},
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	name := "kevin-relay-reuse-test-relay"
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })

	opts := relay.Options{
		Project:   "relay-reuse-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     image,
		Authority: newTestAuthority(t),
	}

	first, err := relay.Start(t.Context(), dockerClient, opts)
	require.NoError(t, err)
	firstID, err := dockerClient.Inspect(t.Context(), name)
	require.NoError(t, err)

	second, err := relay.Start(t.Context(), dockerClient, opts)
	require.NoError(t, err)
	assert.Equal(t, first.Addr(), second.Addr(), "a second Start must reuse the running container, not recreate it")

	secondID, err := dockerClient.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, firstID.ID, secondID.ID, "the container must not have been recreated")

	require.NoError(t, second.Close())
}

// TestStartReplacesADriftedContainer proves that Start does not reuse a
// running relay whose recorded ProxyAddr no longer matches - the situation
// a reused relay is in on every process after the one that created it,
// since the host proxy's own address is an ephemeral port chosen fresh
// each process.
func TestStartReplacesADriftedContainer(t *testing.T) {
	requireDocker(t)
	image := fixtureImage(t)

	network := "kevin-relay-drift-test"
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: "relay-drift-test"},
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	name := "kevin-relay-drift-test-relay"
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })

	authority := newTestAuthority(t)
	_, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   "relay-drift-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     image,
		Authority: authority,
	})
	require.NoError(t, err)
	firstID, err := dockerClient.Inspect(t.Context(), name)
	require.NoError(t, err)

	second, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   "relay-drift-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:29090", // a different process's proxy port
		Image:     image,
		Authority: authority,
	})
	require.NoError(t, err)

	secondID, err := dockerClient.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.NotEqual(t, firstID.ID, secondID.ID, "a drifted ProxyAddr must replace the container, not reuse it")

	require.NoError(t, second.Close())
}

func TestLookup(t *testing.T) {
	requireDocker(t)
	image := fixtureImage(t)

	network := "kevin-relay-lookup-test"
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: "relay-lookup-test"},
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	name := "kevin-relay-lookup-test-relay"
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })

	absent, err := relay.Lookup(t.Context(), dockerClient, "relay-lookup-test", network)
	require.NoError(t, err)
	assert.Nil(t, absent, "Lookup must report nil, nil when no relay is running")

	started, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   "relay-lookup-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     image,
		Authority: newTestAuthority(t),
	})
	require.NoError(t, err)

	found, err := relay.Lookup(t.Context(), dockerClient, "relay-lookup-test", network)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, started.Addr(), found.Addr())
	assert.Equal(t, started.SOCKS5Addr(), found.SOCKS5Addr())
	assert.Equal(t, started.SOCKS5UDPAddrs(), found.SOCKS5UDPAddrs())

	require.NoError(t, found.Close())
	afterClose, err := relay.Lookup(t.Context(), dockerClient, "relay-lookup-test", network)
	require.NoError(t, err)
	assert.Nil(t, afterClose, "Lookup must report nil, nil once the container is removed")
}
