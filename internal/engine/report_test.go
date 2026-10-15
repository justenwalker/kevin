package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/run/session"
)

func TestInputRows(t *testing.T) {
	t.Run("no with block reports no rows", func(t *testing.T) {
		rows, err := inputRows(config.Step{}, nil, expr.Scopes{}, nil, nil)
		require.NoError(t, err)
		assert.Nil(t, rows)
	})

	t.Run("a field only the schema's defaults added is not an input", func(t *testing.T) {
		raw := json.RawMessage(`{"image":"postgres:16"}`)
		rendered := json.RawMessage(`{"image":"postgres:16","wait":"5m"}`)

		rows, err := inputRows(config.Step{With: raw}, rendered, expr.Scopes{}, nil, nil)

		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "image", Value: "postgres:16", Copyable: true}}, rows)
	})

	t.Run("one row per top-level field, sorted, with the rendered value, and a copy button since neither is sensitive", func(t *testing.T) {
		raw := json.RawMessage(`{"image":"postgres:16","port":"${needs.db.out.port}"}`)
		rendered := json.RawMessage(`{"image":"postgres:16","port":"5432"}`)
		rows, err := inputRows(config.Step{With: raw}, rendered, expr.Scopes{Needs: map[string]output.Map{
			"db": {"port": output.Value{String: "5432"}},
		}}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{
			{Label: "image", Value: "postgres:16", Copyable: true},
			{Label: "port", Value: "5432", Copyable: true},
		}, rows)
	})

	t.Run("a field referencing a sensitive value is marked Sensitive and not Copyable", func(t *testing.T) {
		raw := json.RawMessage(`{"password":"${needs.db.out.password}"}`)
		rendered := json.RawMessage(`{"password":"hunter2"}`)
		rows, err := inputRows(config.Step{With: raw}, rendered, expr.Scopes{Needs: map[string]output.Map{
			"db": {"password": output.Value{String: "hunter2", Sensitive: true}},
		}}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a field the schema marks sensitive is redacted even as a literal with no marker at all", func(t *testing.T) {
		raw := json.RawMessage(`{"password":"hunter2"}`)
		rows, err := inputRows(config.Step{With: raw}, raw, expr.Scopes{}, map[string]bool{"password": true}, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a field referencing a sensitive variable is redacted", func(t *testing.T) {
		raw := json.RawMessage(`{"token":"${vars.api_key}"}`)
		rendered := json.RawMessage(`{"token":"sk-123"}`)
		rows, err := inputRows(config.Step{With: raw}, rendered, expr.Scopes{Vars: map[string]any{"api_key": "sk-123"}}, nil, map[string]bool{"api_key": true})
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "token", Value: "sk-123", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a non-string field keeps its compact JSON form", func(t *testing.T) {
		raw := json.RawMessage(`{"ports":[5432,5433]}`)
		rows, err := inputRows(config.Step{With: raw}, raw, expr.Scopes{}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "ports", Value: "[5432,5433]", Copyable: true}}, rows)
	})
}

func TestOutputRows(t *testing.T) {
	t.Run("no outputs reports no rows", func(t *testing.T) {
		assert.Nil(t, outputRows(nil))
	})

	t.Run("one row per output, sorted, sensitive rows not copyable", func(t *testing.T) {
		rows := outputRows(map[string]output.Value{
			"endpoint": {String: "localhost:55432"},
			"password": {String: "hunter2", Sensitive: true},
		})
		assert.Equal(t, []session.Detail{
			{Label: "endpoint", Value: "localhost:55432", Copyable: true},
			{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false},
		}, rows)
	})
}
