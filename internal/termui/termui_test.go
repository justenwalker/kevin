package termui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/termui"
)

func TestRender(t *testing.T) {
	t.Run("the first frame draws no cursor movement", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{{Name: "web", Label: "web", State: session.Pending}})

		assert.NotContains(t, buf.String(), "\x1b[", "the first frame has nothing above it to redraw over")
		assert.Contains(t, buf.String(), "web")
	})

	t.Run("later frames move the cursor up first", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{{Name: "web", Label: "web", State: session.Pending}})
		buf.Reset()
		r.Render([]session.Step{{Name: "web", Label: "web", State: session.Running}})

		assert.True(t, strings.HasPrefix(buf.String(), "\r\x1b[1A\x1b[J"),
			"a redraw must return to column 1, move up exactly as many lines as the previous frame drew, then clear them")
	})

	t.Run("shows state and a bar for a running step with an estimate", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{{Name: "cluster", Label: "cluster", State: session.Running, Progress: 0.5}})

		out := buf.String()
		assert.Contains(t, out, "cluster")
		assert.Contains(t, out, "running")
		assert.Contains(t, out, "[", "a step with a progress estimate must show a bar")
	})

	t.Run("omits the bar with no estimate", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{{Name: "cluster", Label: "cluster", State: session.Running, Progress: 0}})

		assert.NotContains(t, buf.String(), "[", "no estimate means no bar, same as the console's own gate")
	})

	t.Run("shows the failure message", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{{Name: "web", Label: "web", State: session.Failed, Message: "exit code 1"}})

		assert.Contains(t, buf.String(), "exit code 1")
	})

	t.Run("truncates a long label", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		longLabel := strings.Repeat("x", 40)
		r.Render([]session.Step{{Name: "web", Label: longLabel, State: session.Pending}})

		assert.NotContains(t, buf.String(), longLabel)
		assert.Contains(t, buf.String(), "…")
	})

	t.Run("truncates a long failure message to one physical line", func(t *testing.T) {
		// A row wider than the terminal wraps onto a second physical line,
		// which desyncs the next frame's cursor-up count (it counts steps,
		// not printed lines) from what's actually on screen.
		var buf bytes.Buffer
		r := termui.New(&buf)

		longMessage := strings.Repeat("e", 200)
		r.Render([]session.Step{{Name: "cluster", Label: "cluster", State: session.Failed, Message: longMessage}})

		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		require.Len(t, lines, 1, "one step must draw exactly one physical line")
		assert.LessOrEqual(t, len([]rune(lines[0])), 80)
	})
}
