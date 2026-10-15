package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/podman"
	"github.com/justenwalker/kevin/internal/step"
)

func TestParse(t *testing.T) {
	t.Run("empty name means docker", func(t *testing.T) {
		e, err := Parse("")
		require.NoError(t, err)
		assert.Equal(t, step.EngineDocker, e)
	})

	t.Run("docker", func(t *testing.T) {
		e, err := Parse("docker")
		require.NoError(t, err)
		assert.Equal(t, step.EngineDocker, e)
	})

	t.Run("podman", func(t *testing.T) {
		e, err := Parse("podman")
		require.NoError(t, err)
		assert.Equal(t, step.EnginePodman, e)
	})

	t.Run("an unknown engine reports ErrUnsupported", func(t *testing.T) {
		_, err := Parse("bogus")
		require.ErrorIs(t, err, ErrUnsupported)
	})
}

func TestFor(t *testing.T) {
	assert.IsType(t, docker.Client{}, For(step.EngineDocker))
	assert.IsType(t, podman.Client{}, For(step.EnginePodman))
}

// TestDetect checks Detect's docker-first tie-break and ErrNoEngine fallback
// against whatever container engines this test machine actually has -
// docker.Client/podman.Client shell out for real with no mock seam, matching
// how docker_test.go and podman_test.go already test Available.
func TestDetect(t *testing.T) {
	ctx := t.Context()
	name, err := Detect(ctx)

	if (docker.Client{}).Available(ctx) == nil {
		require.NoError(t, err)
		assert.Equal(t, step.EngineDocker, name, "docker wins the tie-break when both are available")
		return
	}

	if (podman.Client{}).Available(ctx) == nil {
		require.NoError(t, err)
		assert.Equal(t, step.EnginePodman, name)
		return
	}

	require.ErrorIs(t, err, ErrNoEngine)
}
