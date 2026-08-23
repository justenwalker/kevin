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

// TestReapSkipsTheRelay proves that reap leaves a running relay container in
// place: reap runs after the caller stops the relay in the normal flow, but
// it must not also treat a still-running relay as an orphan and remove it.
func TestReapSkipsTheRelay(t *testing.T) {
	requireDocker(t)

	project := "kevin-reap-relay-test"
	network := NetworkName(project)
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, map[string]string{
		cri.LabelProject: project,
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

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
