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
	export func(ctx context.Context, step string) (map[string]string, error),
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

	s := mcpserver.New("demo", "kevin.home", view, px, rerun, export)
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
	noopRerun := func(context.Context, string, bool) error { return nil }
	noopExport := func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil }

	t.Run("list_steps", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport)
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
		sess := newTestServer(t, noopRerun, noopExport)
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
		sess := newTestServer(t, noopRerun, noopExport)
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
		sess := newTestServer(t, noopRerun, noopExport)
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
		sess := newTestServer(t, rerun, noopExport)
		res := callTool(t, sess, "rerun_step", mcpserver.RerunStepInput{Name: "api", Cascade: true})
		require.False(t, res.IsError)

		assert.Equal(t, "api", gotName)
		assert.True(t, gotCascade)
		var out mcpserver.RerunStepOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "api", out.Name)
	})

	t.Run("export_step", func(t *testing.T) {
		export := func(_ context.Context, step string) (map[string]string, error) {
			assert.Equal(t, "api", step)
			return map[string]string{"KUBECONFIG": "/tmp/kubeconfig"}, nil
		}
		sess := newTestServer(t, noopRerun, export)
		res := callTool(t, sess, "export_step", mcpserver.ExportStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.ExportStepOutput
		decodeStructured(t, res, &out)
		assert.Equal(t, "api", out.Name)
		assert.Equal(t, "/tmp/kubeconfig", out.Env["KUBECONFIG"])
	})

	t.Run("get_proxy_info", func(t *testing.T) {
		sess := newTestServer(t, noopRerun, noopExport)
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
