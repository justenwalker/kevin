package relay_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/relay"
)

var dockerClient = docker.Client{}

func TestRefPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		configured string
		want       string
	}{
		{
			name:       "the environment wins over configured and the default",
			env:        "from-env:dev",
			configured: "from-config:dev",
			want:       "from-env:dev",
		},
		{
			name:       "configured wins over the default when the environment is absent",
			env:        "",
			configured: "from-config:dev",
			want:       "from-config:dev",
		},
		{
			name:       "the default applies when neither the environment nor configured is set",
			env:        "",
			configured: "",
			want:       relay.Image,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(relay.ImageEnvVar, tt.env)
			assert.Equal(t, tt.want, relay.Ref(tt.configured))
		})
	}
}

func TestRefRepoTagOverride(t *testing.T) {
	tests := []struct {
		name       string
		repo       string
		tag        string
		configured string
		want       string
	}{
		{
			name:       "repo override keeps configured's tag",
			repo:       "mirror.example.com/kevin-relay",
			configured: "ghcr.io/justenwalker/kevin/relay:v1.2.3",
			want:       "mirror.example.com/kevin-relay:v1.2.3",
		},
		{
			name:       "tag override keeps configured's repo",
			tag:        "canary",
			configured: "ghcr.io/justenwalker/kevin/relay:v1.2.3",
			want:       "ghcr.io/justenwalker/kevin/relay:canary",
		},
		{
			name:       "a registry:port isn't mistaken for a tag separator",
			repo:       "other-mirror.example.com:5000/kevin-relay",
			configured: "registry.local:5000/kevin-relay",
			want:       "other-mirror.example.com:5000/kevin-relay",
		},
		{
			name:       "repo override applies to the default image too",
			repo:       "mirror.example.com/kevin-relay",
			configured: "",
			want:       "mirror.example.com/kevin-relay:" + mustTag(t, relay.Image),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(relay.ImageEnvVar, "")
			t.Setenv(relay.RepoEnvVar, tt.repo)
			t.Setenv(relay.TagEnvVar, tt.tag)
			assert.Equal(t, tt.want, relay.Ref(tt.configured))
		})
	}
}

// mustTag returns the tag portion of a "repo:tag" image reference.
func mustTag(t *testing.T, image string) string {
	t.Helper()
	_, tag, ok := strings.Cut(image, ":")
	require.True(t, ok, "image %q has no tag", image)
	return tag
}

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
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, map[string]string{
		cri.LabelProject: "relay-test",
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})

	name := "kevin-relay-test-relay"
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), name) })

	r, err := relay.Start(t.Context(), relay.Options{
		Project:   "relay-test",
		Network:   network,
		Domain:    "kevin.home",
		ProxyAddr: "host.docker.internal:18080",
		Image:     image,
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

	require.NoError(t, r.Close())
	_, err = dockerClient.Inspect(t.Context(), name)
	require.ErrorIs(t, err, cri.ErrNotFound, "Close must remove the container")

	// Close is idempotent.
	require.NoError(t, r.Close())
}
