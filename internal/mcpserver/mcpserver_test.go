package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/session"
)

// fakeView is a stub of the state a [*console.Server] would otherwise
// provide, so these tests need no engine and no Docker. v is a pointer: a
// test's rerun stub can mutate the pointed-to View, and Snapshot reflects
// the change on its next call.
type fakeView struct {
	v *session.View
}

func (f fakeView) Snapshot() session.View { return *f.v }

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

// defaultView is the fake session state most tests run against.
func defaultView() *session.View {
	return &session.View{
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
	}
}

// newLogsFile writes an NDJSON durable-log fixture - the shape
// internal/engine/ndjsonlog.go writes for a running session - with one
// "listening" line for the "api" step, and returns its path.
func newLogsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs.ndjson")
	line := `{"time":"2026-01-01T00:00:00Z","level":"INFO","msg":"listening","step":"api","stream":"stdout"}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	return path
}

// appendLogLine appends one more NDJSON line to a file newLogsFile built.
func appendLogLine(t *testing.T, path, step, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"time":"2026-01-01T00:00:01Z","level":"INFO","msg":"` + text + `","step":"` + step + `","stream":"stdout"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
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
	return newTestServerWithView(t, defaultView(), rerun, export, tools, dispatch)
}

// newTestServerWithView is newTestServer for a test that needs to mutate the
// session view (e.g. from a rerun stub) after the Server is built.
func newTestServerWithView(t *testing.T, view *session.View,
	rerun func(ctx context.Context, step string, cascade bool) error,
	export func(ctx context.Context, step string) (map[string]output.Value, error),
	tools []mcpserver.ToolDef,
	dispatch func(ctx context.Context, step, tool string, args json.RawMessage) (any, bool, string, error),
) *mcp.ClientSession {
	t.Helper()
	return newTestServerWithLogs(t, view, newLogsFile(t), rerun, export, tools, dispatch)
}

// newTestServerWithLogs is newTestServerWithView for a test that needs to
// append to the durable log file (e.g. a since-cursor test) after the
// Server is built.
func newTestServerWithLogs(t *testing.T, view *session.View, logsPath string,
	rerun func(ctx context.Context, step string, cascade bool) error,
	export func(ctx context.Context, step string) (map[string]output.Value, error),
	tools []mcpserver.ToolDef,
	dispatch func(ctx context.Context, step, tool string, args json.RawMessage) (any, bool, string, error),
) *mcp.ClientSession {
	t.Helper()

	px := fakeProxy{
		routes:    []proxy.Route{{Host: "api.kevin.home", Upstream: "api:8080"}},
		allow:     []string{"api.github.com"},
		wildcards: []string{".example.com"},
		deny:      true,
	}

	s := mcpserver.New("demo", "kevin.home", logsPath, fakeView{v: view}, px, rerun, export, tools, dispatch)
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
		assert.NotEmpty(t, out.Cursor)
	})

	t.Run("get_step with since only returns what was logged after that cursor", func(t *testing.T) {
		logsPath := newLogsFile(t)
		sess := newTestServerWithLogs(t, defaultView(), logsPath, noopRerun, noopExport, nil, nil)

		res := callTool(t, sess, "get_step", mcpserver.GetStepInput{Name: "api"})
		var first mcpserver.GetStepOutput
		decodeStructured(t, res, &first)
		require.Len(t, first.Logs, 1)
		require.NotEmpty(t, first.Cursor)

		appendLogLine(t, logsPath, "api", "second line")

		res = callTool(t, sess, "get_step", mcpserver.GetStepInput{Name: "api", Since: first.Cursor})
		require.False(t, res.IsError)
		var second mcpserver.GetStepOutput
		decodeStructured(t, res, &second)
		require.Len(t, second.Logs, 1, "only the line appended after the cursor, not the whole history again")
		assert.Equal(t, "second line", second.Logs[0].Text)
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
		assert.Empty(t, out.Steps, "nothing changed state, so nothing should be reported")
	})

	t.Run("rerun_step reports changed step details and proxy denials", func(t *testing.T) {
		view := defaultView()
		rerun := func(_ context.Context, _ string, _ bool) error {
			view.Steps[0].State = session.Failed
			view.Requests = []session.Request{
				{Time: time.Now().Add(2 * time.Hour), Denied: true, Method: "GET", Host: "second.example.com", Path: "/two"},
				{Time: time.Now().Add(time.Hour), Denied: true, Method: "GET", Host: "blocked.example.com", Path: "/data"},
				{Time: time.Now().Add(time.Hour), Denied: false, Host: "api.github.com"},
				{Time: time.Now().Add(-time.Hour), Denied: true, Host: "stale.example.com"},
			}
			return nil
		}
		sess := newTestServerWithView(t, view, rerun, noopExport, nil, nil)
		res := callTool(t, sess, "rerun_step", mcpserver.RerunStepInput{Name: "api"})
		require.False(t, res.IsError)

		var out mcpserver.RerunStepOutput
		decodeStructured(t, res, &out)
		require.Len(t, out.Steps, 1)
		assert.Equal(t, "api", out.Steps[0].Name)
		assert.Equal(t, "failed", out.Steps[0].State)

		require.Len(t, out.Denials, 2)
		assert.Equal(t, "blocked.example.com", out.Denials[0].Host, "oldest denial first")
		assert.Equal(t, "/data", out.Denials[0].Path)
		assert.Equal(t, "second.example.com", out.Denials[1].Host)
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
