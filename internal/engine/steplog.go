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

// openStepLog truncates and opens LogsFile fresh for one Run/Teardown
// session - it's session-scoped diagnostic output, not accumulated state
// like timings.json.
func openStepLog(workspace string) (*slog.Logger, io.Closer, error) {
	path := filepath.Join(workspace, LogsFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return nil, nil, fmt.Errorf("supervisor: open %s: %w", path, err)
	}
	return slog.New(slog.NewJSONHandler(f, nil)), f, nil
}
