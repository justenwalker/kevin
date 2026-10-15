package expr_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config/expr"
	"github.com/justenwalker/kevin/internal/output"
)

func deps() map[string]output.Map {
	return map[string]output.Map{
		"cluster": {"kubeconfig": output.Value{String: "/tmp/kubeconfig"}, "context": output.Value{String: "kind-demo"}},
	}
}

func sysDeps() map[string]output.Map {
	return map[string]output.Map{
		"cluster": {"expose_postgres": output.Value{String: "socks5://127.0.0.1:1080/postgres:5432"}},
	}
}

func render(raw json.RawMessage, step string, scopes expr.Scopes) (json.RawMessage, error) {
	tpl, err := expr.Parse(raw)
	if err != nil {
		return nil, err
	}
	return tpl.Render(step, scopes)
}

func referencedSteps(raw json.RawMessage) ([]string, []string, error) {
	tpl, err := expr.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	refs := tpl.Refs()
	return refs.Needs, refs.Setup, nil
}

func referencedVars(raw json.RawMessage) ([]string, error) {
	tpl, err := expr.Parse(raw)
	if err != nil {
		return nil, err
	}
	return tpl.Refs().Vars, nil
}

func fieldSensitive(raw json.RawMessage, scopes expr.Scopes, sensitiveVars map[string]bool) (bool, error) {
	tpl, err := expr.Parse(raw)
	if err != nil {
		return false, err
	}
	return tpl.Sensitive(scopes, sensitiveVars), nil
}

func TestRender(t *testing.T) {
	t.Run("literal passthrough", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"b","n":1,"list":["x","y"]}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)
		assert.Equal(t, string(raw), string(out), "Render must not change a value with no marker")
	})

	t.Run("one expression", func(t *testing.T) {
		raw := json.RawMessage(`{"kubeconfig":"${needs.cluster.out.kubeconfig}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/tmp/kubeconfig", v["kubeconfig"])
	})

	t.Run("multiple expressions in one string", func(t *testing.T) {
		raw := json.RawMessage(`{"url":"https://${needs.cluster.out.context}.${needs.cluster.out.kubeconfig}/"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "https://kind-demo./tmp/kubeconfig/", v["url"])
	})

	t.Run("nested object and array", func(t *testing.T) {
		raw := json.RawMessage(`{"values":{"a":["${needs.cluster.out.context}","literal"]}}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]map[string][]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "kind-demo", v["values"]["a"][0])
		assert.Equal(t, "literal", v["values"]["a"][1])
	})

	t.Run("a missing key error mentions needs", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.other.out.kubeconfig}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a step not listed in needs")
		assert.Contains(t, err.Error(), "needs")
	})

	t.Run("a compile error surfaces", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		assert.Error(t, err, "expected a compile error for broken CEL syntax")
	})

	t.Run("an unbalanced marker errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.out.kubeconfig"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		assert.Error(t, err, `expected an error for an unbalanced "${"`)
	})

	t.Run("a system expression", func(t *testing.T) {
		raw := json.RawMessage(`{"address":"${needs.cluster.system.expose_postgres}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "socks5://127.0.0.1:1080/postgres:5432", v["address"])
	})

	t.Run("out and system are independent namespaces", func(t *testing.T) {
		d := map[string]output.Map{"cluster": {"x": output.Value{String: "from-out"}}}
		s := map[string]output.Map{"cluster": {"x": output.Value{String: "from-system"}}}

		raw := json.RawMessage(`{"a":"${needs.cluster.out.x}","b":"${needs.cluster.system.x}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: d, System: s})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-out", v["a"], "needs.cluster.out.x must not see system's value for the same key")
		assert.Equal(t, "from-system", v["b"], "needs.cluster.system.x must not see out's value for the same key")
	})

	t.Run("a missing system key errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.system.no_such_key}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a key absent from a step's (present but empty) system map")
	})

	// deps() has "cluster" but sysDeps() carries nothing for it here - the
	// system sub-namespace must still exist (as an empty map), not be an
	// absent key, so referencing needs.<step>.system errors on the missing
	// entry within it, not on "system" itself being undefined.
	t.Run("system is an empty map, not a missing key, for a step with no system outputs", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${needs.cluster.system.size() == 0}"}`)
		out, err := render(raw, "app", expr.Scopes{Needs: deps(), System: map[string]output.Map{}})
		require.NoError(t, err, "expected needs.cluster.system to resolve to an empty map, not a missing-key error")

		var v map[string]bool
		require.NoError(t, json.Unmarshal(out, &v))
		assert.True(t, v["a"], "needs.cluster.system must be an empty map (size 0), not absent")
	})

	t.Run("an env expression", func(t *testing.T) {
		t.Setenv("KEVIN_EXPR_TEST_VAR", "from-env")
		raw := json.RawMessage(`{"a":"${env.KEVIN_EXPR_TEST_VAR}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps(), Env: expr.HostEnv()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-env", v["a"])
	})

	t.Run("an unset env var errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${env.KEVIN_EXPR_TEST_UNSET_VAR}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps(), Env: expr.HostEnv()})
		require.Error(t, err, "expected an error for an unset environment variable")
	})

	t.Run("has() gives a default for an unset env var", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${has(env.KEVIN_EXPR_TEST_UNSET_VAR) ? env.KEVIN_EXPR_TEST_UNSET_VAR : \"fallback\"}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps(), Env: expr.HostEnv()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "fallback", v["a"])
	})

	t.Run("needs and env in the same string", func(t *testing.T) {
		t.Setenv("KEVIN_EXPR_TEST_VAR", "from-env")
		raw := json.RawMessage(`{"url":"https://${needs.cluster.out.context}.${env.KEVIN_EXPR_TEST_VAR}/"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps(), Env: expr.HostEnv()})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "https://kind-demo.from-env/", v["url"])
	})

	t.Run("env comes only from Scopes.Env", func(t *testing.T) {
		t.Setenv("KEVIN_EXPR_TEST_VAR", "from-env")
		raw := json.RawMessage(`{"a":"${env.KEVIN_EXPR_TEST_VAR}"}`)
		_, err := render(raw, "app", expr.Scopes{Env: map[string]string{}})
		require.Error(t, err)
	})

	t.Run("a setup expression", func(t *testing.T) {
		setupDeps := map[string]output.Map{"cluster": {"kubeconfig": output.Value{String: "/setup/kubeconfig"}}}
		raw := json.RawMessage(`{"kubeconfig":"${setup.cluster.out.kubeconfig}"}`)
		out, err := render(raw, "step", expr.Scopes{Setup: setupDeps})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/setup/kubeconfig", v["kubeconfig"])
	})

	t.Run("needs and setup are independent variables", func(t *testing.T) {
		d := map[string]output.Map{"cluster": {"x": output.Value{String: "from-needs"}}}
		setupDeps := map[string]output.Map{"cluster": {"x": output.Value{String: "from-setup"}}}

		raw := json.RawMessage(`{"a":"${needs.cluster.out.x}","b":"${setup.cluster.out.x}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: d, Setup: setupDeps})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "from-needs", v["a"], "needs.cluster must not see setup's value for a step of the same name")
		assert.Equal(t, "from-setup", v["b"], "setup.cluster must not see needs's value for a step of the same name")
	})

	t.Run("a project expression", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${project.root_cert}"}`)
		out, err := render(raw, "step", expr.Scopes{Needs: deps(), System: sysDeps(), Project: map[string]string{"root_cert": "/home/user/.kevin/root.crt"}})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "/home/user/.kevin/root.crt", v["a"])
	})

	t.Run("a missing project key errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${project.no_such_key}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps(), Project: map[string]string{"root_cert": "/x"}})
		require.Error(t, err, "expected an error for a project key that was never set")
	})

	t.Run("a vars expression", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${vars.region}"}`)
		out, err := render(raw, "step", expr.Scopes{Vars: map[string]any{"region": "us-east-1"}})
		require.NoError(t, err)

		var v map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "us-east-1", v["a"])
	})

	t.Run("a missing var key errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${vars.no_such_key}"}`)
		_, err := render(raw, "app", expr.Scopes{Vars: map[string]any{"region": "us-east-1"}})
		require.Error(t, err, "expected an error for a variable that was never set")
	})
}

// TestRenderTypedValues covers the whole-leaf, non-string substitution a
// bare "${...}" marker gets (see [expr.BareMarker]), split out of TestRender
// to keep that function's size in check.
func TestRenderTypedValues(t *testing.T) {
	t.Run("a bare non-string expression evaluates to its native type", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"${1 + 1}"}`)
		out, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.NoError(t, err)

		var v map[string]int
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, 2, v["a"])
	})

	t.Run("a non-string result interpolated into surrounding text errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"count-${1 + 1}"}`)
		_, err := render(raw, "app", expr.Scopes{Needs: deps(), System: sysDeps()})
		require.Error(t, err, "expected an error for a non-string expression result spliced into surrounding text")
		assert.Contains(t, err.Error(), "must evaluate to a string")
	})

	t.Run("a bare int variable substitutes its native type", func(t *testing.T) {
		raw := json.RawMessage(`{"replicas":"${vars.replicas}"}`)
		out, err := render(raw, "step", expr.Scopes{Vars: map[string]any{"replicas": int64(3)}})
		require.NoError(t, err)

		var v map[string]int
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, 3, v["replicas"])
	})

	t.Run("a bare bool variable substitutes its native type", func(t *testing.T) {
		raw := json.RawMessage(`{"strict":"${vars.strict}"}`)
		out, err := render(raw, "step", expr.Scopes{Vars: map[string]any{"strict": true}})
		require.NoError(t, err)

		var v map[string]bool
		require.NoError(t, json.Unmarshal(out, &v))
		assert.True(t, v["strict"])
	})

	t.Run("a bare list variable substitutes its native type", func(t *testing.T) {
		raw := json.RawMessage(`{"tags":"${vars.tags}"}`)
		out, err := render(raw, "step", expr.Scopes{Vars: map[string]any{"tags": []any{"a", "b"}}})
		require.NoError(t, err)

		var v map[string][]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, []string{"a", "b"}, v["tags"])
	})

	t.Run("a bare struct variable substitutes its native type", func(t *testing.T) {
		raw := json.RawMessage(`{"labels":"${vars.labels}"}`)
		out, err := render(raw, "step", expr.Scopes{Vars: map[string]any{"labels": map[string]any{"env": "prod"}}})
		require.NoError(t, err)

		var v map[string]map[string]string
		require.NoError(t, json.Unmarshal(out, &v))
		assert.Equal(t, "prod", v["labels"]["env"])
	})

	t.Run("a non-string variable interpolated into surrounding text errors", func(t *testing.T) {
		raw := json.RawMessage(`{"a":"count-${vars.replicas}"}`)
		_, err := render(raw, "app", expr.Scopes{Vars: map[string]any{"replicas": int64(3)}})
		require.Error(t, err, "expected an error for a non-string variable spliced into surrounding text")
		assert.Contains(t, err.Error(), "must evaluate to a string")
	})
}

func TestReferencedSteps(t *testing.T) {
	t.Run("no marker at all", func(t *testing.T) {
		needs, setup, err := referencedSteps(json.RawMessage(`{"a":"b"}`))
		require.NoError(t, err)
		assert.Empty(t, needs)
		assert.Empty(t, setup)
	})

	t.Run("a needs reference", func(t *testing.T) {
		needs, setup, err := referencedSteps(json.RawMessage(`{"a":"${needs.cluster.out.kubeconfig}"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{"cluster"}, needs)
		assert.Empty(t, setup)
	})

	t.Run("a setup reference", func(t *testing.T) {
		needs, setup, err := referencedSteps(json.RawMessage(`{"a":"${setup.db.out.dsn}"}`))
		require.NoError(t, err)
		assert.Empty(t, needs)
		assert.Equal(t, []string{"db"}, setup)
	})

	t.Run("finds references nested in objects, arrays, and multiple markers in one string", func(t *testing.T) {
		raw := json.RawMessage(`{
			"list": ["${needs.a.out.x}", "${needs.b.out.y}"],
			"obj": {"k": "${setup.c.out.z}"},
			"combined": "${needs.d.out.x}-${setup.e.out.y}"
		}`)
		needs, setup, err := referencedSteps(raw)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"a", "b", "d"}, needs)
		assert.ElementsMatch(t, []string{"c", "e"}, setup)
	})

	t.Run("a reference inside has() is still found", func(t *testing.T) {
		needs, _, err := referencedSteps(json.RawMessage(`{"a":"${has(needs.cluster.out.x) ? needs.cluster.out.x : \"default\"}"}`))
		require.NoError(t, err)
		assert.Contains(t, needs, "cluster")
	})

	t.Run("env and project references are not needs or setup", func(t *testing.T) {
		needs, setup, err := referencedSteps(json.RawMessage(`{"a":"${env.HOME}", "b":"${project.root_cert}"}`))
		require.NoError(t, err)
		assert.Empty(t, needs)
		assert.Empty(t, setup)
	})

	t.Run("an unbalanced marker errors", func(t *testing.T) {
		_, _, err := referencedSteps(json.RawMessage(`{"a":"${needs.cluster"}`))
		require.Error(t, err)
	})

	t.Run("a syntax error in the expression errors", func(t *testing.T) {
		_, _, err := referencedSteps(json.RawMessage(`{"a":"${needs..cluster}"}`))
		require.Error(t, err)
	})
}

func TestReferencedVars(t *testing.T) {
	t.Run("no marker at all", func(t *testing.T) {
		refs, err := referencedVars(json.RawMessage(`{"a":"b"}`))
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("a var reference", func(t *testing.T) {
		refs, err := referencedVars(json.RawMessage(`{"a":"${vars.region}"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{"region"}, refs)
	})

	t.Run("finds references nested in objects, arrays, and multiple markers in one string", func(t *testing.T) {
		raw := json.RawMessage(`{
			"list": ["${vars.a}", "${vars.b}"],
			"obj": {"k": "${vars.c}"},
			"combined": "${vars.d}-${vars.e}"
		}`)
		refs, err := referencedVars(raw)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"a", "b", "c", "d", "e"}, refs)
	})

	t.Run("a reference inside has() is still found", func(t *testing.T) {
		refs, err := referencedVars(json.RawMessage(`{"a":"${has(vars.region) ? vars.region : \"default\"}"}`))
		require.NoError(t, err)
		assert.Contains(t, refs, "region")
	})

	t.Run("needs, setup, env, and project references are not vars", func(t *testing.T) {
		raw := json.RawMessage(`{
			"a":"${needs.cluster.out.x}", "b":"${setup.db.out.x}",
			"c":"${env.HOME}", "d":"${project.root_cert}"
		}`)
		refs, err := referencedVars(raw)
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("an unbalanced marker errors", func(t *testing.T) {
		_, err := referencedVars(json.RawMessage(`{"a":"${vars.region"}`))
		require.Error(t, err)
	})

	t.Run("a syntax error in the expression errors", func(t *testing.T) {
		_, err := referencedVars(json.RawMessage(`{"a":"${vars..region}"}`))
		require.Error(t, err)
	})
}

func TestFieldSensitive(t *testing.T) {
	sensitiveDeps := map[string]output.Map{
		"db": {
			"password": output.Value{String: "hunter2", Sensitive: true},
			"host":     output.Value{String: "localhost"},
		},
	}
	sensitiveSystem := map[string]output.Map{
		"db": {"internal_addr": output.Value{String: "10.0.0.5", Sensitive: true}},
	}
	sensitiveSetup := map[string]output.Map{
		"cluster": {"token": output.Value{String: "s3cr3t", Sensitive: true}},
	}

	t.Run("no marker at all", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"plain-value"`), expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.False(t, sensitive)
	})

	t.Run("a marker referencing a non-sensitive value", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${needs.db.out.host}"`), expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.False(t, sensitive)
	})

	t.Run("a needs.out marker referencing a sensitive value", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${needs.db.out.password}"`), expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.True(t, sensitive)
	})

	t.Run("a needs.system marker referencing a sensitive value", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${needs.db.system.internal_addr}"`), expr.Scopes{System: sensitiveSystem}, nil)
		require.NoError(t, err)
		assert.True(t, sensitive)
	})

	t.Run("a setup.out marker referencing a sensitive value", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${setup.cluster.out.token}"`), expr.Scopes{Setup: sensitiveSetup}, nil)
		require.NoError(t, err)
		assert.True(t, sensitive)
	})

	t.Run("one sensitive reference among several marks the whole field", func(t *testing.T) {
		raw := json.RawMessage(`"${needs.db.out.host}-${needs.db.out.password}"`)
		sensitive, err := fieldSensitive(raw, expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.True(t, sensitive, "a field combining a plain and a sensitive reference must still be treated as sensitive")
	})

	t.Run("a sensitive reference nested in an object or array is still found", func(t *testing.T) {
		raw := json.RawMessage(`{"a":["${needs.db.out.password}"]}`)
		sensitive, err := fieldSensitive(raw, expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.True(t, sensitive)
	})

	t.Run("a reference to an unknown step or key is not sensitive", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${needs.other.out.x}"`), expr.Scopes{Needs: sensitiveDeps}, nil)
		require.NoError(t, err)
		assert.False(t, sensitive, "an unresolved reference reports false rather than erroring - this is a display aid, not a validator")
	})

	t.Run("an unbalanced marker errors", func(t *testing.T) {
		_, err := fieldSensitive(json.RawMessage(`"${needs.db.out.password"`), expr.Scopes{Needs: sensitiveDeps}, nil)
		require.Error(t, err)
	})

	t.Run("a var reference to a name sensitiveVars marks is sensitive", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${vars.api_key}"`), expr.Scopes{}, map[string]bool{"api_key": true})
		require.NoError(t, err)
		assert.True(t, sensitive)
	})

	t.Run("a var reference to a name sensitiveVars does not mark is not sensitive", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${vars.region}"`), expr.Scopes{}, map[string]bool{"api_key": true})
		require.NoError(t, err)
		assert.False(t, sensitive)
	})

	t.Run("a var reference with a nil sensitiveVars is not sensitive", func(t *testing.T) {
		sensitive, err := fieldSensitive(json.RawMessage(`"${vars.api_key}"`), expr.Scopes{}, nil)
		require.NoError(t, err)
		assert.False(t, sensitive)
	})
}

func TestBareMarker(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		wantExp string
		wantOK  bool
	}{
		{name: "a bare marker", s: "${vars.x}", wantExp: "vars.x", wantOK: true},
		{name: "leading text", s: "prefix-${vars.x}", wantOK: false},
		{name: "trailing text", s: "${vars.x}-suffix", wantOK: false},
		{name: "no marker", s: "plain", wantOK: false},
		{name: "empty string", s: "", wantOK: false},
		{name: "unbalanced marker", s: "${vars.x", wantOK: false},
		{name: "two markers", s: "${vars.x}${vars.y}", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exprStr, ok := expr.BareMarker(tt.s)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantExp, exprStr)
			}
		})
	}
}

func TestTemplateFieldSensitive(t *testing.T) {
	scopes := expr.Scopes{Needs: map[string]output.Map{
		"db": {"password": output.Value{String: "hunter2", Sensitive: true}, "host": output.Value{String: "localhost"}},
	}}
	tpl, err := expr.Parse(json.RawMessage(`{"pass":"${needs.db.out.password}","host":"${needs.db.out.host}","key":"${vars.k}","plain":"x"}`))
	require.NoError(t, err)

	assert.True(t, tpl.FieldSensitive("pass", scopes, nil))
	assert.False(t, tpl.FieldSensitive("host", scopes, nil))
	assert.True(t, tpl.FieldSensitive("key", scopes, map[string]bool{"k": true}))
	assert.False(t, tpl.FieldSensitive("plain", scopes, nil))
	assert.False(t, tpl.FieldSensitive("missing", scopes, nil))
	assert.True(t, tpl.Sensitive(scopes, nil), "the whole template is sensitive when any field is")
}

func TestTemplateConcurrentUse(t *testing.T) {
	tpl, err := expr.Parse(json.RawMessage(`{"a":"${needs.db.out.host}","b":"${vars.k}"}`))
	require.NoError(t, err)
	scopes := expr.Scopes{
		Needs: map[string]output.Map{"db": {"host": output.Value{String: "h"}}},
		Vars:  map[string]any{"k": "v"},
	}

	rendered := make([]json.RawMessage, 16)
	errs := make([]error, 16)
	var wg sync.WaitGroup
	for i := range rendered {
		wg.Go(func() {
			rendered[i], errs[i] = tpl.Render("s", scopes)
			tpl.FieldSensitive("a", scopes, nil)
			tpl.Refs()
		})
	}
	wg.Wait()

	for i := range rendered {
		require.NoError(t, errs[i])
		assert.JSONEq(t, `{"a":"h","b":"v"}`, string(rendered[i]))
	}
}
