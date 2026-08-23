// Package termui draws a live-updating list of steps to a terminal: one row
// per step, its state, and a progress bar when an estimate exists for it.
package termui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/justenwalker/kevin/internal/session"
)

// tick is how often Renderer redraws while it runs.
const tick = 120 * time.Millisecond

// barWidth is the fixed width of a step's progress bar, in characters.
const barWidth = 20

// maxLabelWidth caps how much room one long label takes from the rest of
// the line.
const maxLabelWidth = 28

// defaultWidth is the line width Render truncates to when w isn't a
// terminal Render can query the size of.
const defaultWidth = 80

// spinner is the frame sequence a running or removing step's icon cycles
// through.
var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Renderer redraws a block of step rows in place on w, an actual terminal.
// Every Render call after the first moves the cursor back to the top of its
// own previous block before drawing over it.
type Renderer struct {
	w     io.Writer // the terminal Renderer draws to
	lines int       // the row count of the last frame drawn
	frame int       // the spinner frame index, advanced by each Render call
}

// New returns a Renderer that draws to w.
func New(w io.Writer) *Renderer {
	return &Renderer{w: w}
}

// Render draws one frame for steps, in the order given. Each row is
// truncated to the terminal width: r.lines (the cursor-up count the next
// Render uses to overwrite this frame) counts printed lines, not steps, so
// a row that wrapped onto a second physical line would desync it.
func (r *Renderer) Render(steps []session.Step) {
	if r.lines > 0 {
		// \r first: the terminal's own line discipline can echo input (a
		// pressed Ctrl-C shows as a literal "^C") onto the blank line below
		// the last frame, moving the cursor off column 1. Cursor-up doesn't
		// change the column, so without this the next erase starts mid-row
		// and leaves that row's leading characters behind.
		_, _ = fmt.Fprintf(r.w, "\r\x1b[%dA\x1b[J", r.lines)
	}
	width := termWidth(r.w)
	labelW := labelWidth(steps)
	for _, s := range steps {
		line := truncate(formatStep(s, labelW, r.frame), width)
		_, _ = fmt.Fprintln(r.w, line)
	}
	r.lines = len(steps)
	r.frame++
}

// termWidth returns the terminal column width of w, or defaultWidth if w
// isn't a terminal Render can query the size of.
func termWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return defaultWidth
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return defaultWidth
	}
	return width
}

// truncate shortens line to at most width runes, marking a cut with a
// trailing ellipsis.
func truncate(line string, width int) string {
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

// Viewer is the read-only session state Start needs - satisfied by
// [*console.Server].
type Viewer interface {
	Snapshot() session.View
}

// Start renders one frame per tick until ctx is done, reading the current
// step state from view each time. The returned stop func cancels the
// ticker and blocks for one final frame. Call stop exactly once.
func (r *Renderer) Start(ctx context.Context, view Viewer) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.Render(view.Snapshot().Steps)
				return
			case <-ticker.C:
				r.Render(view.Snapshot().Steps)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// labelWidth is the column width every row pads its label to: the
// longest label present, capped at maxLabelWidth.
func labelWidth(steps []session.Step) int {
	width := 0
	for _, s := range steps {
		if len(s.Label) > width {
			width = len(s.Label)
		}
	}
	if width > maxLabelWidth {
		width = maxLabelWidth
	}
	return width
}

// formatStep renders one step's row: an icon for its state, its label,
// the state name, and - for a running or removing step with an estimate
// - a progress bar. A failed step's message follows on the same row.
func formatStep(s session.Step, labelWidth, frame int) string {
	label := s.Label
	if len(label) > labelWidth {
		label = label[:labelWidth-1] + "…"
	}
	line := fmt.Sprintf(" %s %-*s  %-9s", stateIcon(s.State, frame), labelWidth, label, s.State)
	if s.Progress > 0 && (s.State == session.Running || s.State == session.Removing) {
		line += "  " + bar(s.Progress)
	}
	if s.State == session.Failed && s.Message != "" {
		line += "  " + s.Message
	}
	return line
}

// stateIcon is the one character that marks a step's row.
func stateIcon(state session.State, frame int) string {
	switch state {
	case session.Ready, session.Removed:
		return "✔"
	case session.Failed:
		return "✘"
	case session.Running, session.Removing:
		return string(spinner[frame%len(spinner)])
	case session.Pending:
		return " "
	case session.Skipped:
		return "-"
	default:
		return " "
	}
}

// bar draws a fixed-width progress bar for fraction, a value in [0,1].
func bar(fraction float64) string {
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction * barWidth)
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "]"
}
