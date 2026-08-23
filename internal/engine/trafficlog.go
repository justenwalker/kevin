package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/justenwalker/kevin/internal/console"
)

// TrafficFile holds the durable, full record of every proxy request for one
// session, in newline-delimited JSON. The console's Proxy tab keeps only a
// bounded live tail; this file is the full history.
const TrafficFile = "traffic.ndjson"

// trafficLog is a session-scoped JSON-lines writer. A nil *trafficLog is
// valid and no-ops, matching timingStore's nil-receiver-safe pattern.
type trafficLog struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// openTrafficLog truncates and opens TrafficFile fresh for one session.
func openTrafficLog(workspace string) (*trafficLog, io.Closer, error) {
	path := filepath.Join(workspace, TrafficFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return nil, nil, fmt.Errorf("supervisor: open %s: %w", path, err)
	}
	return &trafficLog{enc: json.NewEncoder(f)}, f, nil
}

// Record appends r as one JSON line. A write failure is logged, not
// returned - losing one traffic-log line isn't worth failing a request
// over.
func (t *trafficLog) Record(ctx context.Context, r console.Request) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.enc.Encode(r); err != nil {
		log.Ctx(ctx).Warn("write traffic log", "error", err)
	}
}
