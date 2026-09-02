package expr_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
)

func deps() map[string]dag.Outputs {
	return map[string]dag.Outputs{
		"cluster": {"kubeconfig": output.Value{String: "/tmp/kubeconfig"}, "context": output.Value{String: "kind-demo"}},
	}
}

func sysDeps() map[string]dag.Outputs {
	return map[string]dag.Outputs{
		"cluster": {"expose_postgres": output.Value{String: "socks5://127.0.0.1:1080/postgres:5432"}},
	}
}

func TestRender(t *testing.T) {
	t.Run("literal passthrough", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"b","n":1,"list":["x","y"]}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)
		assert.Equal(t, string(raw), string(out), "Render must not change a value with no marker")
	})

	t.Run("one expression", func(t *testing.T) {
		raw := json.RawMessage(`{"kubeconfig":"${needs.cluster.out.kubeconfig}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/tmp/kubeconfig", v["kubeconfig"])
	})

	t.Run("multiple expressions in one string", func(t *testing.T) {
		raw := json.RawMessage(`{"url":"https://${needs.cluster.out.context}.${needs.cluster.out.kubeconfig}/"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "https://kind-demo./tmp/kubeconfig/", v["url"])
	})

	t.Run("nested object and array", func(t *testing.T) {
		raw := json.RawMessage(`{"values":{"a":["${needs.cluster.out.context}","literal"]}}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]map[string][]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "kind-demo", v["values"]["a"][0])
		assert.Equal(t, "literal", v["values"]["a"][1])
	})

	t.Run("a missing key error mentions needs", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.other.out.kubeconfig}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a step not listed in needs")
		assert.Contains(t, err.Error(), "needs")
	})

	t.Run("a non-string result errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${1 + 1}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a non-string expression result")
		assert.Contains(t, err.Error(), "must evaluate to a string")
	})

	t.Run("a compile error surfaces", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		assert.Error(t, err, "expected a compile error for broken CEL syntax")
	})

	t.Run("an unbalanced marker errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.out.kubeconfig"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		assert.Error(t, err, `expected an error for an unbalanced "${"`)
	})

	t.Run("a system expression", func(t *testing.T) {
		raw := json.RawMessage(`{"address":"${needs.cluster.system.expose_postgres}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "socks5://127.0.0.1:1080/postgres:5432", v["address"])
	})

	t.Run("out and system are independent namespaces", func(t *testing.T) {
		d := map[string]dag.Outputs{"cluster": {"x": output.Value{String: "from-out"}}}
		s := map[string]dag.Outputs{"cluster": {"x": output.Value{String: "from-system"}}}

		raw := json.RawMessage(`{"a":"${needs.cluster.out.x}","b":"${needs.cluster.system.x}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: d, System: s})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-out", v["a"], "needs.cluster.out.x must not see system's value for the same key")
		assert.Equal(t, "from-system", v["b"], "needs.cluster.system.x must not see out's value for the same key")
	})

	t.Run("a missing system key errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.system.no_such_key}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a key absent from a step's (present but empty) system map")
	})

	// deps() has "cluster" but sysDeps() carries nothing for it here - the
	// system sub-namespace must still exist (as an empty map), not be an
	// absent key, so referencing needs.<step>.system errors on the missing
	// entry within it, not on "system" itself being undefined.
	t.Run("system is an empty map, not a missing key, for a step with no system outputs", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.system.size() == 0}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: map[string]dag.Outputs{}})
		require.Error(t, err, "expected a non-string-result error, not a missing-key error, proving needs.cluster.system resolved to an empty map")
		assert.Contains(t, err.Error(), "must evaluate to a string")
	})

	t.Run("an env expression", func(t *testing.T) {
		t.Setenv("KEVIN_EXPR_TEST_VAR", "from-env")
		raw := json.RawMessage(`{"a":"${env.KEVIN_EXPR_TEST_VAR}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-env", v["a"])
	})

	t.Run("an unset env var errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${env.KEVIN_EXPR_TEST_UNSET_VAR}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for an unset environment variable")
	})

	t.Run("has() gives a default for an unset env var", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${has(env.KEVIN_EXPR_TEST_UNSET_VAR) ? env.KEVIN_EXPR_TEST_UNSET_VAR : \"fallback\"}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "fallback", v["a"])
	})

	t.Run("needs and env in the same string", func(t *testing.T) {
		t.Setenv("KEVIN_EXPR_TEST_VAR", "from-env")
		raw := json.RawMessage(`{"url":"https://${needs.cluster.out.context}.${env.KEVIN_EXPR_TEST_VAR}/"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "https://kind-demo.from-env/", v["url"])
	})

	t.Run("a setup expression", func(t *testing.T) {
		setupDeps := map[string]dag.Outputs{"cluster": {"kubeconfig": output.Value{String: "/setup/kubeconfig"}}}
		raw := json.RawMessage(`{"kubeconfig":"${setup.cluster.out.kubeconfig}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Setup: setupDeps})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/setup/kubeconfig", v["kubeconfig"])
	})

	t.Run("needs and setup are independent variables", func(t *testing.T) {
		d := map[string]dag.Outputs{"cluster": {"x": output.Value{String: "from-needs"}}}
		setupDeps := map[string]dag.Outputs{"cluster": {"x": output.Value{String: "from-setup"}}}

		raw := json.RawMessage(`{"a":"${needs.cluster.out.x}","b":"${setup.cluster.out.x}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: d, Setup: setupDeps})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-needs", v["a"], "needs.cluster must not see setup's value for a step of the same name")
		assert.Equal(t, "from-setup", v["b"], "setup.cluster must not see needs's value for a step of the same name")
	})

	t.Run("a project expression", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${project.root_cert}"}`)
		out, err := expr.Render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps(), Project: map[string]string{"root_cert": "/home/user/.kevin/root.crt"}})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/home/user/.kevin/root.crt", v["a"])
	})

	t.Run("a missing project key errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${project.no_such_key}"}`)
		_, err := expr.Render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps(), Project: map[string]string{"root_cert": "/x"}})
		require.Error(t, err, "expected an error for a project key that was never set")
	})
}
