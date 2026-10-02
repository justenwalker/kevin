package termui_test

import (
	"bytes"
	"context"
	"os"
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
		assert.Contains(t, buf.String(), "1 pending")
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
		r.Render([]session.Step{{Name: "web", Label: longLabel, State: session.Running}})

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

	t.Run("collapses pending, ready, and skipped steps into one summary line", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{
			{Name: "alpha", Label: "alpha", State: session.Pending},
			{Name: "bravo", Label: "bravo", State: session.Pending},
			{Name: "charlie", Label: "charlie", State: session.Ready},
			{Name: "delta", Label: "delta", State: session.Skipped},
			{Name: "echo", Label: "echo", State: session.Running},
		})

		out := buf.String()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		require.Len(t, lines, 2, "one row for the running step, one summary line for the rest")
		assert.Contains(t, out, "echo")
		assert.Contains(t, out, "2 pending")
		assert.Contains(t, out, "1 ready")
		assert.Contains(t, out, "1 skipped")
		for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
			assert.NotContains(t, out, name, "a folded step's own label shouldn't appear")
		}
	})

	t.Run("never folds a failed step into the summary", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{
			{Name: "ok", Label: "ok", State: session.Ready},
			{Name: "bad", Label: "bad", State: session.Failed, Message: "boom"},
		})

		out := buf.String()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		require.Len(t, lines, 2, "the failed step gets its own row, the ready step folds into the summary")
		assert.Contains(t, out, "bad")
		assert.Contains(t, out, "boom")
		assert.Contains(t, out, "1 ready")
		assert.NotContains(t, out, "failed,", "a failed step must never appear in the summary's own count")
	})

	t.Run("no summary line when every step is running", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		r.Render([]session.Step{
			{Name: "a", Label: "a", State: session.Running},
			{Name: "b", Label: "b", State: session.Running},
		})

		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		require.Len(t, lines, 2, "no non-running steps means no summary line at all")
	})

	for _, tt := range []struct {
		state session.State
		icon  string
	}{
		{session.Failed, "✘"},
		{session.Running, "⠋"},
	} {
		t.Run("marks a "+string(tt.state)+" step with its icon", func(t *testing.T) {
			var buf bytes.Buffer
			termui.New(&buf).Render([]session.Step{{Name: "a", Label: "a", State: tt.state}})

			assert.Contains(t, buf.String(), tt.icon)
		})
	}

	t.Run("draws into a file that is not a terminal at the default width", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "out")
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })

		termui.New(f).Render([]session.Step{{Name: "a", Label: "a", State: session.Running}})

		out, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		assert.Contains(t, string(out), "running")
	})

	t.Run("fills the bar for a progress above one", func(t *testing.T) {
		var buf bytes.Buffer
		termui.New(&buf).Render([]session.Step{{Name: "a", Label: "a", State: session.Running, Progress: 1.5}})

		assert.NotContains(t, buf.String(), "░")
	})
}

type fakeViewer struct{ steps []session.Step }

func (f fakeViewer) Snapshot() session.View { return session.View{Steps: f.steps} }

func TestStart(t *testing.T) {
	t.Run("stop draws one final frame", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)

		stop := r.Start(t.Context(), fakeViewer{steps: []session.Step{{Name: "web", Label: "web", State: session.Ready}}})
		stop()

		assert.Contains(t, buf.String(), "1 ready")
	})

	t.Run("a canceled context draws a final frame", func(t *testing.T) {
		var buf bytes.Buffer
		r := termui.New(&buf)
		ctx, cancel := context.WithCancel(t.Context())

		stop := r.Start(ctx, fakeViewer{steps: []session.Step{{Name: "web", Label: "web", State: session.Failed, Message: "boom"}}})
		cancel()
		stop()

		assert.Contains(t, buf.String(), "boom")
	})
}
