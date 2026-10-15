package console

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/ui/browser"
)

func renderString(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, c.Render(t.Context(), &sb))
	return sb.String()
}

func TestProxySetup(t *testing.T) {
	tests := []struct {
		name    string
		browser browser.Kind
		want    string
	}{
		{name: "firefox", browser: browser.Firefox, want: "Firefox keeps its own proxy settings"},
		{name: "chrome", browser: browser.Chrome, want: "--proxy-pac-url="},
		{name: "safari", browser: browser.Safari, want: "Safari follows macOS"},
		{name: "any other browser", browser: browser.Unknown, want: "Point your browser's proxy settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := renderString(t, Page(View{Browser: tt.browser, View: session.View{ProxyAddr: "127.0.0.1:8080"}}))

			assert.Contains(t, body, tt.want)
			assert.Contains(t, body, "export HTTP_PROXY=http://127.0.0.1:8080")
		})
	}
}

func TestStepItemKinds(t *testing.T) {
	tests := []struct {
		name string
		step session.Step
		want []string
		not  []string
	}{
		{
			name: "a compact step is one muted line",
			step: session.Step{Name: "probe", Label: "probe", Compact: true, State: session.Ready, Provider: "builtin", Message: "ok"},
			want: []string{`class="compact state-ready"`, `class="msg"`},
			not:  []string{"status-line"},
		},
		{
			name: "a plugin with an icon shows it",
			step: session.Step{Name: "web", Label: "web", Provider: "acme", Icon: []byte("png")},
			want: []string{`class="plugin-icon"`, "data:image/png;base64,", `alt="acme"`},
		},
		{
			name: "a plugin without an icon gets the default",
			step: session.Step{Name: "web", Label: "web", Provider: "acme"},
			want: []string{"plugin-icon-default"},
		},
		{
			name: "a builtin without an icon gets none",
			step: session.Step{Name: "web", Label: "web", Provider: "builtin"},
			not:  []string{"plugin-icon"},
		},
		{
			name: "an action and a probe show their kind text",
			step: session.Step{Name: "web", Label: "web", Kind: "action"},
			want: []string{"kind-action"},
		},
		{
			name: "an idempotent step carries a badge",
			step: session.Step{Name: "web", Label: "web", Idempotent: true},
			want: []string{"badge-idempotent"},
		},
		{
			name: "a step that reads setup outputs names them",
			step: session.Step{Name: "web", Label: "web", Needs: []config.Need{{Step: "db"}, {Setup: true, Step: "cluster"}}},
			want: []string{"reads setup: cluster", `data-needs="db"`},
		},
		{
			name: "a running step shows a progress bar",
			step: session.Step{Name: "web", Label: "web", State: session.Running, Progress: 0.5},
			want: []string{`id="bar-web"`, "width:50%"},
			not:  []string{"bar-hidden"},
		},
		{
			name: "a removing step with no estimate hides its bar",
			step: session.Step{Name: "web", Label: "web", State: session.Removing},
			want: []string{"bar-hidden"},
		},
		{
			name: "a failed step shows its message and one rerun button",
			step: session.Step{Name: "web", Label: "web", State: session.Failed, Message: "boom"},
			want: []string{"boom", "cascade-only"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := renderString(t, StepItem(tt.step))

			for _, w := range tt.want {
				assert.Contains(t, body, w)
			}
			for _, n := range tt.not {
				assert.NotContains(t, body, n)
			}
		})
	}
}

func TestGroupItem(t *testing.T) {
	t.Run("renders the header, the badge and every member", func(t *testing.T) {
		group := session.Step{Name: "infra", Label: "infra", IsGroup: true, State: session.Failed, Message: "member failed", Needs: []config.Need{{Setup: true, Step: "cluster"}}}
		members := []session.Step{{Name: "db", Label: "db"}, {Name: "cache", Label: "cache", Compact: true}}

		body := renderString(t, GroupItem(group, members))

		assert.Contains(t, body, `id="group-toggle-infra"`)
		assert.Contains(t, body, "member failed")
		assert.Contains(t, body, "reads setup: cluster")
		assert.Contains(t, body, `id="step-db"`)
		assert.Contains(t, body, `id="step-cache"`)
	})
}

func TestGroupInPage(t *testing.T) {
	t.Run("nests a group's members and leaves them out of the top level", func(t *testing.T) {
		body := renderString(t, Page(View{View: session.View{
			Steps: []session.Step{
				{Name: "infra", Label: "infra", IsGroup: true, State: session.Ready},
				{Name: "db", Label: "db", Group: "infra", State: session.Ready},
				{Name: "web", Label: "web", State: session.Running},
			},
			Logs:     []session.Line{{Step: "web", Stream: "stdout", Text: "listening"}},
			StepLogs: map[string][]session.Line{"web": {{Step: "web", Stream: "stderr", Text: "warn"}}},
			Requests: []session.Request{{Method: "GET", Host: "example.com", Path: "/", Status: 200}},
		}}))

		assert.Contains(t, body, `class="group-members"`)
		assert.Contains(t, body, "listening")
		assert.Contains(t, body, "warn")
		assert.Contains(t, body, "example.com")
		assert.NotContains(t, body, `<dialog id="detail-infra"`, "a group has no detail dialog")
		assert.Contains(t, body, `<dialog id="detail-db"`)
	})
}

func TestStepUpdate(t *testing.T) {
	t.Run("a group updates its header and card, with no detail panels", func(t *testing.T) {
		body := renderString(t, StepUpdate(session.Step{Name: "infra", Label: "infra", IsGroup: true, State: session.Ready}))

		assert.Contains(t, body, `id="group-header-infra"`)
		assert.Contains(t, body, `id="card-infra"`)
		assert.NotContains(t, body, "dpanel-infra")
	})

	t.Run("a step updates its row, its panels and its card", func(t *testing.T) {
		body := renderString(t, StepUpdate(session.Step{Name: "web", Label: "web", Kind: "resource", Idempotent: true, Details: []session.Detail{{Label: "a", Value: "b"}}}))

		assert.Contains(t, body, `id="step-web"`)
		assert.Contains(t, body, `id="dpanel-web-details"`)
		assert.Contains(t, body, `class="card exposed"`)
	})
}

func TestDetailRows(t *testing.T) {
	tests := []struct {
		name string
		rows []session.Detail
		want []string
		not  []string
	}{
		{name: "no rows", want: []string{"none"}},
		{
			name: "a plain value",
			rows: []session.Detail{{Label: "image", Value: "postgres:16"}},
			want: []string{"detail-label", "postgres:16"},
			not:  []string{"copy-btn", "<a "},
		},
		{
			name: "a link",
			rows: []session.Detail{{Value: "http://x", Href: "http://x"}},
			want: []string{`href="http://x"`, "noreferrer"},
			not:  []string{"detail-label"},
		},
		{
			name: "a copyable value",
			rows: []session.Detail{{Label: "addr", Value: "localhost:1", Copyable: true}},
			want: []string{`data-copy="localhost:1"`},
		},
		{
			name: "a sensitive value never shows",
			rows: []session.Detail{{Label: "pw", Value: "hunter2", Href: "http://hunter2", Sensitive: true}},
			want: []string{"detail-sensitive"},
			not:  []string{"hunter2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := renderString(t, detailRows(tt.rows))

			for _, w := range tt.want {
				assert.Contains(t, body, w)
			}
			for _, n := range tt.not {
				assert.NotContains(t, body, n)
			}
		})
	}
}

func TestOutOfBand(t *testing.T) {
	tests := []struct {
		name string
		c    templ.Component
		want []string
	}{
		{name: "progress", c: oobProgress(session.StepProgress{Name: "web", Progress: 0.25}), want: []string{`hx-target="#bar-web"`, "width:25%"}},
		{name: "all log", c: oobLog(session.Line{Step: "web", Stream: "stdout", Text: "hi"}), want: []string{`hx-target="#log-all"`, "stream-stdout"}},
		{name: "step log", c: oobStepLog(session.Line{Step: "web", Stream: "stderr", Text: "hi"}), want: []string{`hx-target="#log-web"`, "stream-stderr"}},
		{name: "traffic", c: oobTraffic(session.Request{Method: "GET", Host: "h", Status: 404}), want: []string{`hx-target="#traffic"`, "404"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := renderString(t, tt.c)

			for _, w := range tt.want {
				assert.Contains(t, body, w)
			}
		})
	}
}

func TestTrafficRow(t *testing.T) {
	t.Run("shows a dash when the request has no status", func(t *testing.T) {
		body := renderString(t, TrafficRow(session.Request{Method: "CONNECT", Host: "h", Denied: true, Routed: true}))

		assert.Contains(t, body, "<td>-</td>")
		assert.Contains(t, body, "denied")
	})
}

func TestSnapshot(t *testing.T) {
	t.Run("replaces every region with the current state", func(t *testing.T) {
		body := renderString(t, Snapshot(View{View: session.View{
			Steps:    []session.Step{{Name: "web", Label: "web"}},
			Logs:     []session.Line{{Step: "web", Stream: "stdout", Text: "all-line"}},
			StepLogs: map[string][]session.Line{"web": {{Step: "web", Stream: "stdout", Text: "web-line"}}},
			Requests: []session.Request{{Method: "GET", Host: "h", Status: 200}},
		}}))

		assert.Contains(t, body, `id="step-web"`)
		assert.Contains(t, body, "all-line")
		assert.Contains(t, body, "web-line")
		assert.Contains(t, body, `hx-target="#traffic" hx-swap="innerHTML"`)
	})
}

func TestMcpSetup(t *testing.T) {
	t.Run("names the URL a client registers", func(t *testing.T) {
		body := renderString(t, mcpSetup(View{McpURL: "http://127.0.0.1:1/_mcp"}))

		assert.Contains(t, body, "http://127.0.0.1:1/_mcp")
	})
}
