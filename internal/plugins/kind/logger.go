package kind

import (
	"bytes"

	"github.com/justenwalker/kevin/plugin"
)

// maxLineLength caps how much of a line emitWriter buffers before it gives
// up waiting for a newline and emits what it has - otherwise a line the kind
// binary never terminates would grow the buffer without bound. It matches
// bufio.Scanner's default token size.
const maxLineLength = 64 * 1024

// streamStdout and streamStderr name the two streams out.Log accepts.
const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// stdoutWriter and stderrWriter build an emitWriter for the named stream, for
// wiring straight into a kindcmd call's stdout/stderr writer arguments.
func stdoutWriter(out plugin.Emitter) *emitWriter {
	return &emitWriter{out: out, stream: streamStdout}
}

func stderrWriter(out plugin.Emitter) *emitWriter {
	return &emitWriter{out: out, stream: streamStderr}
}

// emitWriter forwards each complete line written to it to out.Log under
// stream - the kind binary can run for minutes, so its stdout/stderr stream
// live through this rather than buffering until it exits. A caller may write
// a partial line - emitWriter buffers it until a later write completes it
// with a newline.
type emitWriter struct {
	out    plugin.Emitter
	stream string

	buf bytes.Buffer
}

func (w *emitWriter) Write(p []byte) (int, error) {
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
