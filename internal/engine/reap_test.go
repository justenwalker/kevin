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

// stubRuntime is a projectRuntime that finds nothing and removes without
// error. Doubles embed it and override what they observe.
type stubRuntime struct{}

func (stubRuntime) ListByLabel(context.Context, string, string) ([]string, error) { return nil, nil }

func (stubRuntime) Run(context.Context, cri.RunSpec) (string, error) { return "", nil }

func (stubRuntime) Remove(context.Context, string) error { return nil }

func (stubRuntime) Inspect(context.Context, string) (cri.Container, error) {
	return cri.Container{}, nil
}

func (stubRuntime) NetworkRemove(context.Context, string) error { return nil }

// reapRuntime is a projectRuntime double for reap: Remove fails for one
// name, and every Remove and NetworkRemove call is recorded.
type reapRuntime struct {
	stubRuntime

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
