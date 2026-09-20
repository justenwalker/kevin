// Package steplog reads a step's durable log: the full-history NDJSON file
// internal/engine writes alongside the console's own bounded in-memory
// tail (internal/session.Store). A Cursor lets a caller ask for only what
// was logged since it last looked.
package steplog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Entry is one logged line from a step's durable output.
type Entry struct {
	Time   time.Time
	Step   string
	Stream string
	Text   string
}

// rawEntry mirrors the JSON slog.JSONHandler writes for one step log line -
// see internal/engine/ndjsonlog.go and its call site.
type rawEntry struct {
	Time   time.Time `json:"time"`
	Msg    string    `json:"msg"`
	Step   string    `json:"step"`
	Stream string    `json:"stream"`
}

// ReadSince reads every entry logged to the NDJSON file at path after
// cursor since, optionally filtered to one step (step == "" returns every
// step's entries, interleaved in the order they were written). It returns
// the entries and the cursor to pass as since on the next call.
func ReadSince(path, step string, since Cursor) ([]Entry, Cursor, error) {
	offset, err := decodeCursor(since)
	if err != nil {
		return nil, "", fmt.Errorf("steplog: %s: %w", path, err)
	}

	f, err := os.Open(path) //nolint:gosec // path is caller-constructed from the project's own workspace
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("steplog: %s: %w", path, ErrNotFound)
		}
		return nil, "", fmt.Errorf("steplog: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, "", fmt.Errorf("steplog: %s: %w", path, err)
	}
	if offset > size {
		return nil, "", fmt.Errorf("steplog: %s: %w", path, ErrStaleCursor)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, "", fmt.Errorf("steplog: %s: %w", path, err)
	}

	dec := json.NewDecoder(f)
	pos := offset
	var entries []Entry
	for {
		var raw rawEntry
		if err := dec.Decode(&raw); err != nil {
			// A partial trailing line from a concurrent writer ends the
			// stream mid-token (io.ErrUnexpectedEOF) rather than cleanly
			// (io.EOF) - either way, stop here and leave it for the next
			// call; pos was only advanced after a fully decoded entry, so
			// it never points into the partial line.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, "", fmt.Errorf("steplog: %s: decode entry: %w", path, err)
		}
		pos = offset + dec.InputOffset()
		if step != "" && raw.Step != step {
			continue
		}
		entries = append(entries, Entry{Time: raw.Time, Step: raw.Step, Stream: raw.Stream, Text: raw.Msg})
	}
	return entries, encodeCursor(pos), nil
}
