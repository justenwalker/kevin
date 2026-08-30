package engine

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// newTestNetwork creates a project's network, removed on cleanup, and
// returns its name.
func newTestNetwork(t *testing.T, project string) string {
	t.Helper()
	network := NetworkName(project)
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, map[string]string{
		cri.LabelProject: project,
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})
	return network
}

// TestReapSkipsTheRelay proves that reap leaves a running relay container in
// place: reap runs after the caller stops the relay in the normal flow, but
// it must not also treat a still-running relay as an orphan and remove it.
func TestReapSkipsTheRelay(t *testing.T) {
	requireDocker(t)

	project := "kevin-reap-relay-test"
	newTestNetwork(t, project)

	relayName := "kevin-" + project + "-relay"
	orphanName := "kevin-" + project + "-orphan"
	for _, name := range []string{relayName, orphanName} {
		t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })
	}

	_, err := dockerClient.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  relayName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: project,
			cri.LabelRole:    relay.Role,
		},
	})
	require.NoError(t, err)

	_, err = dockerClient.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  orphanName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: project,
		},
	})
	require.NoError(t, err)

	r := &run{cfg: &config.Config{Project: project}, events: io.Discard}
	require.NoError(t, r.reap(t.Context()))

	_, err = dockerClient.Inspect(t.Context(), relayName)
	require.NoError(t, err, "reap must leave a container that carries the relay role in place")

	_, err = dockerClient.Inspect(t.Context(), orphanName)
	require.ErrorIs(t, err, cri.ErrNotFound, "reap must still remove an orphan that carries no role")

	assert.NoError(t, dockerClient.Remove(context.WithoutCancel(t.Context()), relayName))
}

// TestReapKeepsTheOtherScopeAlive proves that reap, run from an env-scope
// shutdown, leaves a still-running setup-scope container - and the network
// both scopes share - in place. Without this, "kevin run"'s own exit would
// treat a setup step's container as an orphan and remove the project
// network out from under a setup scope meant to persist across runs.
func TestReapKeepsTheOtherScopeAlive(t *testing.T) {
	requireDocker(t)

	project := "kevin-reap-scope-test"
	network := newTestNetwork(t, project)

	dbName := "kevin-" + project + "-db"
	orphanName := "kevin-" + project + "-orphan"
	for _, name := range []string{dbName, orphanName} {
		t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })
	}

	_, err := dockerClient.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  dbName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: project,
			cri.LabelScope:   cri.ScopeLabel(project, config.ScopeSetup),
			cri.LabelURN:     cri.URNLabel(project, config.ScopeSetup, "db"),
		},
	})
	require.NoError(t, err)

	_, err = dockerClient.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  orphanName,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: project,
			cri.LabelScope:   cri.ScopeLabel(project, config.ScopeEnv),
			cri.LabelURN:     cri.URNLabel(project, config.ScopeEnv, "orphan"),
		},
	})
	require.NoError(t, err)

	cfg := &config.Config{Project: project}
	r := &run{cfg: cfg, scope: config.ScopeEnv, events: io.Discard}
	require.NoError(t, r.reap(t.Context()))

	_, err = dockerClient.Inspect(t.Context(), dbName)
	require.NoError(t, err, "reap must leave a live setup-scope container in place")

	_, err = dockerClient.Inspect(t.Context(), orphanName)
	require.ErrorIs(t, err, cri.ErrNotFound, "reap must still remove an orphan of its own scope")

	_, gwErr := dockerClient.NetworkGateway(t.Context(), network)
	require.NoError(t, gwErr, "reap must leave the network in place while setup is still live")

	assert.NoError(t, dockerClient.Remove(context.WithoutCancel(t.Context()), dbName))
}

// TestReapRemovesNetworkWhenOtherScopeNeverRan proves that with no
// container carrying the other scope's label, the network does not get
// pinned in place forever - only a live container of that scope counts.
func TestReapRemovesNetworkWhenOtherScopeNeverRan(t *testing.T) {
	requireDocker(t)

	project := "kevin-reap-scope-unused-test"
	network := newTestNetwork(t, project)

	cfg := &config.Config{Project: project}
	r := &run{cfg: cfg, scope: config.ScopeEnv, events: io.Discard}
	require.NoError(t, r.reap(t.Context()))

	_, gwErr := dockerClient.NetworkGateway(t.Context(), network)
	require.ErrorIs(t, gwErr, cri.ErrNotFound, "reap must remove the network when the setup scope was never brought up")
}
