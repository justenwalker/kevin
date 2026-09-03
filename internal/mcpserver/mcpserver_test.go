package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/session"
)

// fakeView is a stub of the state a [*console.Server] would otherwise
// provide, so these tests need no engine and no Docker.
type fakeView struct {
	v session.View
}

func (f fakeView) Snapshot() session.View { return f.v }

// fakeProxy is a stub of the state a [*proxy.Proxy] would otherwise
// provide.
type fakeProxy struct {
	routes    []proxy.Route
	allow     []string
	wildcards []string
	deny      bool
}

func (f fakeProxy) Routes() []proxy.Route { return f.routes }

func (f fakeProxy) EgressAllowList() ([]string, []string, bool) {
	return f.allow, f.wildcards, f.deny
}

// newTestServer builds a Server against fake session state and connects an
// MCP client to it over Streamable HTTP.
func newTestServer(t *testing.T,
	rerun func(ctx context.Context, step string, cascade bool) error,
	export func(ctx context.Context, step string) (map[string]output.Value, error),
	tools []mcpserver.ToolDef,
	dispatch func(ctx context.Context, step, tool string, args json.RawMessage) (any, bool, string, error),
) *mcp.ClientSession {
	t.Helper()

	view := fakeView{v: session.View{
		ProxyAddr: "127.0.0.1:9999",
		Steps: []session.Step{
			{
				Name: "api", State: session.Ready, Kind: "resource", Provider: "builtin",
				Idempotent: true, Needs: []string{"network"},
				Details: []session.Detail{
					{Label: "url", Value: "http://api.kevin.home"},
					{Label: "password", Value: "hunter2", Sensitive: true},
				},
			},
		},
		StepLogs: map[string][]session.Line{
			"api": {{Step: "api", Stream: "stdout", Text: "listening"}},
		},
	}}
	px := fakeProxy{
		routes:    []proxy.Route{{Host: "api.kevin.home", Upstream: "api:8080"}},
		allow:     []string{"api.github.com"},
		wildcards: []string{".example.com"},
		deny:      true,
	}

	s := mcpserver.New("demo", "kevin.home", view, px, rerun, export, tools, dispatch)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callTool(t *testing.T, sess *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, out))
}

func TestTools(t *testing.T) {
	t.Run("list_steps", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport, nil, nil)
		res := callTool(t, sess, "list_steps", struct{}{})
		require.False(t, res.IsError)

		var out mcpserver.ListStepsOutput
		decodeStructured(t, res, &out)
		require.Len(t, out.Steps, 1)
		assert.Equal(t, "api", out.Steps[0].Name)
		assert.Equal(t, "ready", out.Steps[0].State)
		assert.Equal(t, []string{"network"}, out.Steps[0].Needs)
	})

	t.Run("get_step", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport, nil, nil)
		res := callTool(t, sess, "get_step", mcpserver.GetStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.GetStepOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "api", out.Name)
		require.Len(t, out.Details, 2)
		assert.Equal(t, "url", out.Details[0].Label)
		require.Len(t, out.Logs, 1)
		assert.Equal(t, "listening", out.Logs[0].Text)
	})

	t.Run("get_step masks a sensitive detail's value", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport, nil, nil)
		res := callTool(t, sess, "get_step", mcpserver.GetStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.GetStepOutput
		decodeStructured(t, res, &out)
		require.Len(t, out.Details, 2)
		assert.Equal(t, "password", out.Details[1].Label)
		assert.True(t, out.Details[1].Sensitive)
		assert.NotContains(t, out.Details[1].Value, "hunter2")
		assert.Equal(t, "********", out.Details[1].Value)
	})

	t.Run("get_step unknown name is a tool error", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport, nil, nil)
		res := callTool(t, sess, "get_step", mcpserver.GetStepInput{Name: "ghost"})
		assert.True(t, res.IsError)
	})

	t.Run("rerun_step", func(t *testing.T) {
		var gotName string
		var gotCascade bool
		rerun := func(_ context.Context, step string, cascade bool) error {
			gotName, gotCascade = step, cascade
			return nil
		}
		sess := newTestServer(t, rerun, noopExport, nil, nil)
		res := callTool(t, sess, "rerun_step", mcpserver.RerunStepInput{Name: "api", Cascade: true})
		require.False(t, res.IsError)

		assert.Equal(t, "api", gotName)
		assert.True(t, gotCascade)
		var out mcpserver.RerunStepOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "api", out.Name)
	})

	t.Run("export_step", func(t *testing.T) {
		export := func(_ context.Context, step string) (map[string]output.Value, error) {
			assert.Equal(t, "api", step)
			return map[string]output.Value{"kubeconfig": {String: "/tmp/kubeconfig"}}, nil
		}
		sess := newTestServer(t, noopRerun, export, nil, nil)
		res := callTool(t, sess, "export_step", mcpserver.ExportStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.ExportStepOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "api", out.Name)
		require.Len(t, out.Out, 1)
		assert.Equal(t, mcpserver.DetailRow{Label: "kubeconfig", Value: "/tmp/kubeconfig"}, out.Out[0])
	})

	t.Run("export_step masks a sensitive value and sorts rows by label", func(t *testing.T) {
		export := func(context.Context, string) (map[string]output.Value, error) {
			return map[string]output.Value{
				"zebra":    {String: "z"},
				"password": {String: "hunter2", Sensitive: true},
			}, nil
		}
		sess := newTestServer(t, noopRerun, export, nil, nil)
		res := callTool(t, sess, "export_step", mcpserver.ExportStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.ExportStepOutput
		decodeStructured(t, res, &out)
		require.Len(t, out.Out, 2)
		assert.Equal(t, mcpserver.DetailRow{Label: "password", Value: "********", Sensitive: true}, out.Out[0],
			"a sensitive value must be masked, never the real secret")
		assert.Equal(t, mcpserver.DetailRow{Label: "zebra", Value: "z"}, out.Out[1])
	})

	t.Run("get_proxy_info", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport, nil, nil)
		res := callTool(t, sess, "get_proxy_info", struct{}{})
		require.False(t, res.IsError)

		var out mcpserver.GetProxyInfoOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "127.0.0.1:9999", out.Addr)
		assert.Equal(t, "kevin.home", out.Domain)
		require.Len(t, out.Routes, 1)
		assert.Equal(t, "api.kevin.home", out.Routes[0].Host)
		assert.True(t, out.Egress.Deny)
		assert.Equal(t, []string{"api.github.com"}, out.Egress.Allow)
		assert.Equal(t, []string{".example.com"}, out.Egress.Wildcards)
	})
}

func TestPluginTools(t *testing.T) {
	demoTool := mcpserver.ToolDef{
		Name:        "builtin_widget_query",
		Description: "runs a demo query",
		InputSchema: []byte(`{"type":"object","properties":{"step":{"type":"string"},"sql":{"type":"string"}}}`),
	}

	t.Run("strips step from arguments before dispatching", func(t *testing.T) {
		var gotStep, gotTool string
		var gotArgs json.RawMessage
		dispatch := func(_ context.Context, step, tool string, args json.RawMessage) (any, bool, string, error) {
			gotStep, gotTool, gotArgs = step, tool, args
			return map[string]string{"ok": "yes"}, false, "", nil
		}
		sess := newTestServer(t, noopRerun, noopExport, []mcpserver.ToolDef{demoTool}, dispatch)

		res := callTool(t, sess, "builtin_widget_query", map[string]any{"step": "db", "sql": "select 1"})
		require.False(t, res.IsError)

		assert.Equal(t, "db", gotStep)
		assert.Equal(t, "builtin_widget_query", gotTool)
		assert.JSONEq(t, `{"sql":"select 1"}`, string(gotArgs), "the step property must not reach the plugin's own arguments")

		var out map[string]string
		decodeStructured(t, res, &out)
		assert.Equal(t, "yes", out["ok"])
	})

	t.Run("a tool-reported failure sets IsError with the message as content", func(t *testing.T) {
		dispatch := func(context.Context, string, string, json.RawMessage) (any, bool, string, error) {
			return nil, true, "no such table", nil
		}
		sess := newTestServer(t, noopRerun, noopExport, []mcpserver.ToolDef{demoTool}, dispatch)

		res := callTool(t, sess, "builtin_widget_query", map[string]any{"step": "db"})
		require.True(t, res.IsError)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "no such table", text.Text)
	})

	t.Run("a dispatch error is an MCP protocol error, not a tool result", func(t *testing.T) {
		dispatch := func(context.Context, string, string, json.RawMessage) (any, bool, string, error) {
			return nil, false, "", assert.AnError
		}
		sess := newTestServer(t, noopRerun, noopExport, []mcpserver.ToolDef{demoTool}, dispatch)

		_, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "builtin_widget_query", Arguments: map[string]any{"step": "db"}})
		require.Error(t, err)
	})
}

func noopRerun(context.Context, string, bool) error { return nil }

func noopExport(context.Context, string) (map[string]output.Value, error) {
	return map[string]output.Value{}, nil
}
