package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pluginhost"
)

// TestApplySchema proves applySchema catches a rendered with block that
// doesn't actually satisfy its step type's schema - the case config-validate
// time couldn't yet, since a bare "${...}" marker's real value (a declared
// variable's, or a needs/setup-sourced one) is only known once it's
// rendered - and fills in the schema's defaults for a plugin.
func TestApplySchema(t *testing.T) {
	caps := map[string]pluginhost.Info{
		"myplugin": {Steps: []pluginhost.StepInfo{
			{Name: "mystep", Schema: []byte(`#Config: {port: int, wait: string | *"5m"}`)},
		}},
	}
	step := config.Step{Uses: config.StepRef{Plugin: "myplugin", Step: "mystep"}, With: json.RawMessage(`{"port":"${vars.port}"}`)}

	t.Run("a rendered value matching the schema passes, with defaults filled in", func(t *testing.T) {
		r := &run{caps: caps}
		got, err := r.applySchema("app", step, json.RawMessage(`{"port":3}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"port":3,"wait":"5m"}`, string(got))
	})

	t.Run("a value the user set beats the default", func(t *testing.T) {
		r := &run{caps: caps}
		got, err := r.applySchema("app", step, json.RawMessage(`{"port":3,"wait":"1s"}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"port":3,"wait":"1s"}`, string(got))
	})

	t.Run("a rendered value of the wrong type fails", func(t *testing.T) {
		r := &run{caps: caps}
		_, err := r.applySchema("app", step, json.RawMessage(`{"port":"not-an-int"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app.with")
	})

	t.Run("an empty with block still gets the defaults", func(t *testing.T) {
		r := &run{caps: map[string]pluginhost.Info{"myplugin": {Steps: []pluginhost.StepInfo{
			{Name: "mystep", Schema: []byte(`#Config: {wait: string | *"5m"}`)},
		}}}}
		got, err := r.applySchema("app", config.Step{Uses: step.Uses}, nil)
		require.NoError(t, err)
		assert.JSONEq(t, `{"wait":"5m"}`, string(got))
	})

	t.Run("an unknown plugin passes the block through", func(t *testing.T) {
		r := &run{caps: map[string]pluginhost.Info{}}
		got, err := r.applySchema("app", step, json.RawMessage(`{"port":"not-an-int"}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"port":"not-an-int"}`, string(got))
	})
}

func TestSensitiveWithFields(t *testing.T) {
	t.Run("no schema reports no fields", func(t *testing.T) {
		fields, err := sensitiveWithFields(nil)
		require.NoError(t, err)
		assert.Nil(t, fields)
	})

	t.Run("only a @sensitive() field is reported", func(t *testing.T) {
		schema := []byte(`#Config: {
			image?: string
			password?: string @sensitive()
		}`)
		fields, err := sensitiveWithFields(schema)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{"password": true}, fields)
	})

	t.Run("a malformed schema errors", func(t *testing.T) {
		_, err := sensitiveWithFields([]byte(`#Config: {`))
		require.Error(t, err)
	})
}
