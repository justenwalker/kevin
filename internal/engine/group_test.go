package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/session"
)

// TestRegisterScopeStepsHidesGroupNeedsFromMembers proves the console drops
// only a member's group-shared needs (config/group.go's unionNeeds already
// put them on every member, so the group's own row already draws that
// edge) - a need unique to that member, such as a sibling reference or its
// own cross-scope need, still reaches the console.
func TestRegisterScopeStepsHidesGroupNeedsFromMembers(t *testing.T) {
	r := &run{
		steps: map[string]config.Step{
			"net":        {Uses: "echo:echo"},
			"db.primary": {Uses: "echo:echo", Needs: []string{"net"}},
			"db.replica": {Uses: "echo:echo", Needs: []string{"net", "primary", "setup.cluster"}},
		},
		groups: map[string]config.Group{
			"db": {Needs: []string{"net"}, Members: []string{"primary", "replica"}},
		},
	}

	store := session.NewStore()
	require.NoError(t, r.registerScopeSteps(store))

	byName := make(map[string]session.Step)
	for _, s := range store.Snapshot().Steps {
		byName[s.Name] = s
	}

	assert.Equal(t, []string{"net"}, byName["db"].Needs, "the group's own row still draws the shared needs edge")
	assert.Empty(t, byName["db.primary"].Needs, "a member must not redraw its group's shared needs")
	assert.Equal(t, []string{"db.primary", "setup.cluster"}, byName["db.replica"].Needs,
		"a sibling reference (as its qualified step name) and a need unique to this member survive - only the group-shared one is dropped")
}
