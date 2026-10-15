package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pki/ca"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/statedir"
)

func TestEgressView(t *testing.T) {
	t.Setenv(statedir.UserStateDirEnv, t.TempDir())
	t.Setenv(statedir.ProjectStateDirEnv, t.TempDir())
	authority, err := ca.NewManager("cwd", "", "egress-view", ca.Options{}).LoadOrGenerateIntermediate()
	require.NoError(t, err)
	p, err := proxy.New(authority, proxy.Options{Domain: "kevin.home", Allow: []string{"api.github.com"}, Deny: true})
	require.NoError(t, err)
	p.SetStep("web", []proxy.Route{{Host: "web.kevin.home", Upstream: "127.0.0.1:8080", Relay: "127.0.0.1:1080", TLS: true}}, nil)

	view := egressView{proxy: p}

	assert.Equal(t, []runapi.Route{{Host: "web.kevin.home", Upstream: "127.0.0.1:8080", Relay: "127.0.0.1:1080", TLS: true}}, view.Routes())
	policy := view.EgressPolicy()
	assert.Equal(t, []string{"api.github.com"}, policy.Allow)
	assert.Empty(t, policy.Wildcards)
	assert.True(t, policy.Deny)
}
