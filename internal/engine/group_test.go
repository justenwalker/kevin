package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/session"
)

func TestEvalGroupOutputs(t *testing.T) {
	members := map[string]output.Map{
		"primary": {
			"dsn":  output.Value{String: "postgres://secret", Sensitive: true},
			"port": output.Value{String: "5432"},
		},
	}
	got, err := evalGroupOutputs("db", map[string]string{
		"dsn":  "${needs.primary.out.dsn}",
		"port": "${needs.primary.out.port}",
		"mode": "readwrite",
	}, members)
	require.NoError(t, err)

	assert.Equal(t, output.Value{String: "postgres://secret", Sensitive: true}, got["dsn"], "a re-exported sensitive output stays sensitive")
	assert.Equal(t, output.Value{String: "5432"}, got["port"])
	assert.Equal(t, output.Value{String: "readwrite"}, got["mode"])
}

// TestRegisterScopeStepsHidesGroupNeedsFromMembers proves the console drops
// only a member's group-shared needs (config/group.go's unionNeeds already
// put them on every member, so the group's own row already draws that
// edge) - a need unique to that member, such as a sibling reference or its
// own cross-scope need, still reaches the console.
func TestRegisterScopeStepsHidesGroupNeedsFromMembers(t *testing.T) {
	r := &run{
		steps: map[string]config.Step{
			"net":        {Uses: config.StepRef{Plugin: "echo", Step: "echo"}},
			"db.primary": {Uses: config.StepRef{Plugin: "echo", Step: "echo"}, Needs: []config.Need{{Step: "net"}}},
			"db.replica": {Uses: config.StepRef{Plugin: "echo", Step: "echo"}, Needs: []config.Need{{Step: "net"}, {Step: "primary"}, {Setup: true, Step: "cluster"}}},
		},
		groups: map[string]config.Group{
			"db": {Needs: []config.Need{{Step: "net"}}, Members: []string{"primary", "replica"}},
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
