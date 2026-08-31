package engine

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// LogsFile holds the durable, full record of every step's log lines for one
// session, in newline-delimited JSON. The in-memory console keeps only a
// small live tail; this file is the full history.
const LogsFile = "logs.ndjson"

// TrafficFile holds the durable, full record of every proxy request for one
// session, in newline-delimited JSON. The console's Proxy tab keeps only a
// bounded live tail; this file is the full history.
const TrafficFile = "traffic.ndjson"

// ndjsonLog is a JSON-lines slog.Logger over a session-scoped file.
type ndjsonLog struct {
	*slog.Logger
	io.Closer
}

// openNDJSONLog truncates and opens filename fresh under workspace.
func openNDJSONLog(workspace, filename string) (ndjsonLog, error) {
	path := filepath.Join(workspace, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return ndjsonLog{}, fmt.Errorf("supervisor: open %s: %w", path, err)
	}
	return ndjsonLog{Logger: slog.New(slog.NewJSONHandler(f, nil)), Closer: f}, nil
}
