package engine

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
)

// newTestNetwork creates a project's network, removed on cleanup, and
// returns its name.
func newTestNetwork(t *testing.T, project string) string {
	t.Helper()
	network := NetworkName(project)
	require.NoError(t, dockerClient.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: project},
	}))
	t.Cleanup(func() {
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
	})
	return network
}

// reapRuntime is a cri.Runtime double for reap: Remove fails for one name,
// and every Remove and NetworkRemove call is recorded.
type reapRuntime struct {
	cri.Runtime

	names      []string
	failOn     string
	removed    []string
	networkRem bool
}

func (f *reapRuntime) ListByLabel(_ context.Context, label, _ string) ([]string, error) {
	if label == cri.LabelProject {
		return f.names, nil
	}
	return nil, nil
}

func (f *reapRuntime) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	if name == f.failOn {
		return assert.AnError
	}
	return nil
}

func (f *reapRuntime) NetworkRemove(context.Context, string) error {
	f.networkRem = true
	return nil
}

// TestReapContinuesPastRemoveError proves that one orphan failing to remove
// does not skip the remaining orphans or the network removal.
func TestReapContinuesPastRemoveError(t *testing.T) {
	rt := &reapRuntime{names: []string{"a", "b", "c"}, failOn: "a"}
	r := &run{cfg: &config.Config{Project: "reap-continue"}, runtime: rt, scope: config.ScopeEnv, events: io.Discard}

	err := r.reap(t.Context())

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"a", "b", "c"}, rt.removed)
	assert.True(t, rt.networkRem, "the network is still removed after an orphan fails")
}
