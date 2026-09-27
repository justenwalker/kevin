package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestResolveVariables(t *testing.T) {
	t.Run("a default is used when nothing external supplies a value", func(t *testing.T) {
		cfg := &config.Config{Variables: map[string]config.Variable{
			"region": {Default: new("us-east-1")},
		}}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{}))
		assert.Equal(t, map[string]string{"region": "us-east-1"}, cfg.VariableValues)
	})

	t.Run("a file value overrides the default", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vars.env")
		require.NoError(t, os.WriteFile(path, []byte("region=us-west-2\n"), 0o600))

		cfg := &config.Config{Variables: map[string]config.Variable{
			"region": {Default: new("us-east-1")},
		}}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{File: path}))
		assert.Equal(t, "us-west-2", cfg.VariableValues["region"])
	})

	t.Run("an env var overrides the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vars.env")
		require.NoError(t, os.WriteFile(path, []byte("region=us-west-2\n"), 0o600))
		t.Setenv("KEVIN_VAR_REGION", "eu-west-1")

		cfg := &config.Config{Variables: map[string]config.Variable{"region": {}}}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{File: path}))
		assert.Equal(t, "eu-west-1", cfg.VariableValues["region"])
	})

	t.Run("a --var entry overrides the env var", func(t *testing.T) {
		t.Setenv("KEVIN_VAR_REGION", "eu-west-1")

		cfg := &config.Config{Variables: map[string]config.Variable{"region": {}}}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{Set: []string{"region=ap-south-1"}}))
		assert.Equal(t, "ap-south-1", cfg.VariableValues["region"])
	})

	t.Run("a required variable with no source anywhere errors", func(t *testing.T) {
		cfg := &config.Config{Variables: map[string]config.Variable{"region": {}}}
		err := cfg.ResolveVariables(config.VariableInputs{})
		require.ErrorIs(t, err, config.ErrRequiredVariable)
		assert.Contains(t, err.Error(), "region")
	})

	t.Run("a malformed --var entry errors", func(t *testing.T) {
		cfg := &config.Config{Variables: map[string]config.Variable{"region": {Default: new("x")}}}
		err := cfg.ResolveVariables(config.VariableInputs{Set: []string{"region"}})
		require.ErrorIs(t, err, config.ErrMalformedVariable)
	})

	t.Run("an unknown file or --var key is ignored, not an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vars.env")
		require.NoError(t, os.WriteFile(path, []byte("unrelated=1\n"), 0o600))

		cfg := &config.Config{Variables: map[string]config.Variable{"region": {Default: new("us-east-1")}}}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{File: path, Set: []string{"also_unrelated=2"}}))
		assert.Equal(t, map[string]string{"region": "us-east-1"}, cfg.VariableValues)
	})

	t.Run("no declared variables reports an empty, non-nil result", func(t *testing.T) {
		cfg := &config.Config{}
		require.NoError(t, cfg.ResolveVariables(config.VariableInputs{}))
		assert.Empty(t, cfg.VariableValues)
	})
}

func TestSensitiveVariables(t *testing.T) {
	cfg := &config.Config{Variables: map[string]config.Variable{
		"region":  {Default: new("us-east-1")},
		"api_key": {Sensitive: true},
	}}
	assert.Equal(t, map[string]bool{"api_key": true}, cfg.SensitiveVariables())
}
