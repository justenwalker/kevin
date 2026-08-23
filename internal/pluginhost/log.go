package pluginhost

import (
	"bytes"
	"io"
	"log/slog"
	"sync"

	"github.com/hashicorp/go-hclog"
)

// hclogToSlog returns an hclog logger that forwards every line to logger at
// debug level. Attach any identifying attributes (such as which plugin this
// is) to logger before calling hclogToSlog.
func hclogToSlog(logger *slog.Logger) (hclog.Logger, io.Closer) { //nolint:ireturn // hclog.ClientConfig itself asks for its interface type, not a concrete one
	w := &slogWriter{logger: logger}
	return hclog.New(&hclog.LoggerOptions{
		Level:  hclog.Debug,
		Output: w,
	}), w
}

// maxLineLength caps how much of a line slogWriter buffers before it gives
// up waiting for a newline and logs what it has - otherwise a plugin that
// never terminates a line would grow the buffer without bound. It matches
// bufio.Scanner's default token size.
const maxLineLength = 64 * 1024

// slogWriter forwards each complete line written to it to logger at debug
// level. A caller may write a partial line - slogWriter buffers it until a
// later write completes it with a newline, the way an io.Writer backing a
// log stream must. A line longer than maxLineLength is logged in pieces
// instead of buffered indefinitely.
type slogWriter struct {
	logger *slog.Logger

	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *slogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			if n := w.buf.Len(); n > maxLineLength {
				line := string(w.buf.Next(n))
				w.logger.Debug(line + "...")
			}
			// buffer until next newline
			return len(p), nil
		}
		line := string(bytes.TrimRight(w.buf.Next(i + 1)[:i], "\r\n"))
		w.logger.Debug(line)
	}
}

// Close flushes any buffered data to logger.
func (w *slogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.buf.Len() > 0 {
		w.logger.Debug(w.buf.String())
	}
	return nil
}
