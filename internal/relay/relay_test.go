package relay_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/state"
)

// newTestAuthority builds a fresh project authority for a test - Start
// mints the control channel's certificates off it.
func newTestAuthority(t *testing.T) *ca.CA {
	t.Helper()
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)
	return authority
}

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
