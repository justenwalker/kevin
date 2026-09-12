package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/podman"
)

func TestNew(t *testing.T) {
	t.Run("empty name means docker", func(t *testing.T) {
		rt, err := New("", nil)
		require.NoError(t, err)
		assert.IsType(t, docker.Client{}, rt)
	})

	t.Run("docker", func(t *testing.T) {
		rt, err := New("docker", nil)
		require.NoError(t, err)
		assert.IsType(t, docker.Client{}, rt)
	})

	t.Run("podman", func(t *testing.T) {
		rt, err := New("podman", nil)
		require.NoError(t, err)
		assert.IsType(t, podman.Client{}, rt)
	})

	t.Run("an unknown engine reports ErrUnsupported", func(t *testing.T) {
		_, err := New("bogus", nil)
		require.ErrorIs(t, err, ErrUnsupported)
	})
}

// TestDetect checks Detect's docker-first tie-break and ErrNoEngine fallback
// against whatever container engines this test machine actually has -
// docker.Client/podman.Client shell out for real with no mock seam, matching
// how docker_test.go and podman_test.go already test Available.
func TestDetect(t *testing.T) {
	ctx := t.Context()
	name, err := Detect(ctx)

	dockerClient, dockerErr := docker.New(nil)
	require.NoError(t, dockerErr)
	if dockerClient.Available(ctx) == nil {
		require.NoError(t, err)
		assert.Equal(t, "docker", name, "docker wins the tie-break when both are available")
		return
	}

	podmanClient, podmanErr := podman.New(nil)
	require.NoError(t, podmanErr)
	if podmanClient.Available(ctx) == nil {
		require.NoError(t, err)
		assert.Equal(t, "podman", name)
		return
	}

	require.ErrorIs(t, err, ErrNoEngine)
}
