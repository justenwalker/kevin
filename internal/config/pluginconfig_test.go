package config_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestResolvePluginConfigs(t *testing.T) {
	t.Run("renders a vars marker and re-validates against the plugin's schema", func(t *testing.T) {
		cfg := &config.Config{
			Variables:      map[string]config.Variable{"replicas": {Type: []byte("int")}},
			VariableValues: map[string]any{"replicas": int64(3)},
			Plugins: map[string]config.PluginSpec{
				"echo": {Config: json.RawMessage(`{"replicas":"${vars.replicas}"}`)},
			},
		}
		schemas := map[string]config.PluginSchemas{
			"echo": {Config: []byte(`#Config: replicas: int`)},
		}
		require.NoError(t, cfg.ResolvePluginConfigs(schemas))
		assert.JSONEq(t, `{"replicas":3}`, string(cfg.Plugins["echo"].Config))
	})

	t.Run("a rendered value that does not satisfy the schema errors", func(t *testing.T) {
		cfg := &config.Config{
			Variables:      map[string]config.Variable{"replicas": {Type: []byte("string")}},
			VariableValues: map[string]any{"replicas": "not-an-int"},
			Plugins: map[string]config.PluginSpec{
				"echo": {Config: json.RawMessage(`{"replicas":"${vars.replicas}"}`)},
			},
		}
		schemas := map[string]config.PluginSchemas{
			"echo": {Config: []byte(`#Config: replicas: int`)},
		}
		err := cfg.ResolvePluginConfigs(schemas)
		require.ErrorIs(t, err, config.ErrInvalid)
	})

	t.Run("a config block with no marker is left unrendered and unvalidated again", func(t *testing.T) {
		cfg := &config.Config{
			Plugins: map[string]config.PluginSpec{
				"echo": {Config: json.RawMessage(`{"greeting":"hi"}`)},
			},
		}
		require.NoError(t, cfg.ResolvePluginConfigs(map[string]config.PluginSchemas{}))
		assert.JSONEq(t, `{"greeting":"hi"}`, string(cfg.Plugins["echo"].Config))
	})

	t.Run("a plugin with no config block is skipped", func(t *testing.T) {
		cfg := &config.Config{Plugins: map[string]config.PluginSpec{"echo": {}}}
		require.NoError(t, cfg.ResolvePluginConfigs(map[string]config.PluginSchemas{}))
	})

	t.Run("project vars are available to a config block", func(t *testing.T) {
		cfg := &config.Config{
			Dir: "/home/user/myproject",
			Plugins: map[string]config.PluginSpec{
				"echo": {Config: json.RawMessage(`{"dir":"${project.dir}"}`)},
			},
		}
		require.NoError(t, cfg.ResolvePluginConfigs(map[string]config.PluginSchemas{}))
		assert.JSONEq(t, `{"dir":"/home/user/myproject"}`, string(cfg.Plugins["echo"].Config))
	})
}
