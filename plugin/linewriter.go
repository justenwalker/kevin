package plugin

import (
	"bytes"
	"io"
)

// maxLineLength caps how much of a line NewLineWriter buffers before it
// gives up waiting for a newline and emits what it has - otherwise a
// subprocess that never terminates a line would grow the buffer without
// bound. It matches bufio.Scanner's default token size.
const maxLineLength = 64 * 1024

// NewLineWriter returns an io.Writer that forwards each complete line
// written to it to out.Log under stream ("stdout" or "stderr") - for
// wiring straight into a shelled-out subprocess's Stdout/Stderr, so its
// output streams to the console and the log as it runs instead of
// buffering until the process exits. A caller may write a partial line;
// the writer buffers it until a later write completes it with a newline.
func NewLineWriter(out Emitter, stream string) io.Writer {
	return &lineWriter{out: out, stream: stream}
}

// lineWriter is NewLineWriter's implementation.
type lineWriter struct {
	out    Emitter
	stream string

	buf bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			if n := w.buf.Len(); n > maxLineLength {
				line := string(w.buf.Next(n))
				w.out.Log(w.stream, line+"...")
			}
			return len(p), nil
		}
		line := string(bytes.TrimRight(w.buf.Next(i + 1)[:i], "\r\n"))
		w.out.Log(w.stream, line)
	}
}
