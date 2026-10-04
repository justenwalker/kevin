//go:build integration

package container

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
	"github.com/justenwalker/kevin/plugin"
)

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := (docker.Client{}).Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// testEnv creates a network for one test and returns the environment that a
// step receives. A network alias needs a user-defined network, thus the
// default bridge does not work here.
func testEnv(t *testing.T) plugin.Env {
	t.Helper()
	requireDocker(t)

	env := plugin.Env{
		Project: "kevin-plugin-test",
		Network: "kevin-plugin-test",
		Domain:  "kevin.home",
	}
	client, err := docker.New(nil)
	require.NoError(t, err)
	require.NoError(t, client.NetworkCreate(t.Context(), env.Network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: env.Project},
	}))
	t.Cleanup(func() {
		_ = client.NetworkRemove(context.WithoutCancel(t.Context()), env.Network)
	})
	return env
}

func TestUp(t *testing.T) {
	t.Run("reports a bad timeout", func(t *testing.T) {
		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "api",
			Config: []byte(`{"image":"nginx","start_timeout":"soon"}`),
		}, &noopEmitter{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start_timeout")
	})

	t.Run("brings up and tears down against docker", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "web")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"nginx:alpine","expose":{"http":{"port":80}}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		assert.Equal(t, name, result.Outputs["name"].Reveal())
		assert.NotEmpty(t, result.Outputs["id"])
		assert.NotEmpty(t, result.Outputs["ip"], "the step must publish its address on the shared network")
		assert.Regexp(t, hostPortPattern, result.Outputs["host_80"],
			"the published port must be a host-reachable address, already accepting connections")

		require.Len(t, result.ExposedPorts, 1)
		require.Len(t, result.Details, 1, "every exposed port must also appear on the card")
		assert.Equal(t, result.ExposedPorts[0].Detail(), result.Details[0])
		require.Len(t, result.Containers, 1)
		assert.NotEmpty(t, result.Containers[0].NetnsPath, "the relay needs this to transparently capture the container's egress")

		info, err := (docker.Client{}).Inspect(t.Context(), name)
		require.NoError(t, err)
		assert.True(t, info.Running)

		// A second Up replaces the container instead of failing on the name.
		_, err = Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"busybox:stable","cmd":["sleep","60"]}`),
		}, &noopEmitter{})
		require.NoError(t, err, "Up must be idempotent")

		require.NoError(t, Container{}.Down(t.Context(), &plugin.DownRequest{Step: "web", Env: env}, &noopEmitter{}))

		_, err = (docker.Client{}).Inspect(t.Context(), name)
		require.ErrorIs(t, err, cri.ErrNotFound)

		// Down is idempotent.
		require.NoError(t, Container{}.Down(t.Context(), &plugin.DownRequest{Step: "web", Env: env}, &noopEmitter{}))
	})

	t.Run("builds and runs an image against docker", func(t *testing.T) {
		env := testEnv(t)
		env.ProjectDir = t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(env.ProjectDir, "Dockerfile"),
			[]byte("FROM busybox:stable\nCMD [\"sleep\", \"60\"]\n"), 0o600))
		name := containerName(env.Project, "app")
		t.Cleanup(func() {
			ctx := context.WithoutCancel(t.Context())
			_ = (docker.Client{}).Remove(ctx, name)
			_, _ = exec.CommandContext(ctx, "docker", "image", "rm", imageTag(env.Project, "app")).CombinedOutput()
		})

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "app",
			Env:    env,
			Config: []byte(`{"build":{"context":"."}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		info, err := (docker.Client{}).Inspect(t.Context(), name)
		require.NoError(t, err)
		assert.True(t, info.Running)
	})

	t.Run("exposes a raw TCP port against docker", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "web")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		result, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "web",
			Env:    env,
			Config: []byte(`{"image":"nginx:alpine","expose":{"http":{"port":80}}}`),
		}, &noopEmitter{})
		require.NoError(t, err)

		require.Len(t, result.ExposedPorts, 1)
		assert.Equal(t, "http", result.ExposedPorts[0].Name)
		assert.Equal(t, "tcp", result.ExposedPorts[0].Protocol)
		assert.Regexp(t, hostPortPattern, result.ExposedPorts[0].Upstream,
			"the expose entry must point at a published port on the host, already accepting connections")

		require.Len(t, result.Details, 1, "the exposed port must also appear on the card")
		assert.Equal(t, plugin.Detail{Label: "tcp http", Value: plugin.String(result.ExposedPorts[0].Upstream), Copyable: true}, result.Details[0])
	})

	t.Run("fails when the container stops during startup", func(t *testing.T) {
		env := testEnv(t)
		name := containerName(env.Project, "boom")
		t.Cleanup(func() { _ = (docker.Client{}).Remove(context.WithoutCancel(t.Context()), name) })

		_, err := Container{}.Up(t.Context(), &plugin.UpRequest{
			Step:   "boom",
			Env:    env,
			Config: []byte(`{"image":"busybox:stable","cmd":["sh","-c","exit 3"],"start_timeout":"20s"}`),
		}, &noopEmitter{})

		require.ErrorIs(t, err, ErrExited)
		assert.Contains(t, err.Error(), "code 3")
	})
}
