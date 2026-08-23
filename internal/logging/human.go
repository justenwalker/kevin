package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// levelColor maps a level to its ANSI color code. A level between two
// constants (e.g. slog.LevelInfo+1) takes the color of the constant below it.
var levelColor = map[slog.Level]string{
	slog.LevelDebug: "\x1b[90m", // gray
	slog.LevelInfo:  "\x1b[36m", // cyan
	slog.LevelWarn:  "\x1b[33m", // yellow
	slog.LevelError: "\x1b[31m", // red
}

const ansiReset = "\x1b[0m"

// HumanHandler is an [slog.Handler] that writes one short, aligned line per
// record: "15:04:05 INFO  message key=value ...". A HumanHandler is safe
// for concurrent use.
type HumanHandler struct {
	w      io.Writer
	mu     *sync.Mutex
	level  slog.Leveler
	color  bool
	attrs  []slog.Attr
	prefix string // dotted group prefix applied to every attr key
}

// NewHuman returns a [HumanHandler] writing to w. Records below level are
// dropped. When color is true, the level is ANSI-colored.
func NewHuman(w io.Writer, level slog.Leveler, color bool) *HumanHandler {
	return &HumanHandler{w: w, mu: &sync.Mutex{}, level: level, color: color}
}

// Enabled implements [slog.Handler].
func (h *HumanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle implements [slog.Handler].
func (h *HumanHandler) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer
	buf.WriteString(r.Time.Format("15:04:05"))
	buf.WriteByte(' ')

	writeLevel(&buf, r.Level, h.color)
	buf.WriteByte(' ')
	buf.WriteString(r.Message)

	for _, a := range h.attrs {
		writeAttr(&buf, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&buf, prefixAttr(h.prefix, a))
		return true
	})
	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("human log handler: write: %w", err)
	}
	return nil
}

func writeAttr(buf *bytes.Buffer, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	buf.WriteByte(' ')
	buf.WriteString(a.Key)
	buf.WriteByte('=')
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\"") {
		v = strconv.Quote(v)
	}
	buf.WriteString(v)
}

func writeLevel(buf *bytes.Buffer, level slog.Level, color bool) {
	if color {
		buf.WriteString(levelColor[level])
		_, _ = fmt.Fprintf(buf, "%-5s", level.String())
		buf.WriteString(ansiReset)
		return
	}
	_, _ = fmt.Fprintf(buf, "%-5s", level.String())
}

// WithAttrs implements [slog.Handler].
func (h *HumanHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.attrs = make([]slog.Attr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(next.attrs, h.attrs)
	for _, a := range attrs {
		next.attrs = append(next.attrs, prefixAttr(h.prefix, a))
	}
	return &next
}

// WithGroup implements [slog.Handler].
func (h *HumanHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.prefix = h.prefix + name + "."
	return &next
}

func prefixAttr(prefix string, a slog.Attr) slog.Attr {
	if prefix == "" {
		return a
	}
	return slog.Attr{Key: prefix + a.Key, Value: a.Value}
}
