package steplog

import (
	"encoding/base64"
	"encoding/binary"
)

// Cursor is an opaque position in one step's durable log. The zero value
// ("") means "from the beginning." Treat it as opaque: round-trip exactly
// what ReadSince returned, never construct or inspect one.
type Cursor string

// cursorVersion1 packs a plain byte offset. Bumping this lets a later
// version carry more than an offset - e.g. a marker to detect a cursor
// left over from a truncated, now-different logs.ndjson - without
// changing ReadSince's signature or any caller's field.
const cursorVersion1 = 0x01

// cursorLen is cursorVersion1's encoded length: one version byte plus an
// 8-byte big-endian offset.
const cursorLen = 9

func encodeCursor(offset int64) Cursor {
	buf := make([]byte, cursorLen)
	buf[0] = cursorVersion1
	binary.BigEndian.PutUint64(buf[1:], uint64(offset)) //nolint:gosec // offset is a file position, never negative
	return Cursor(base64.RawURLEncoding.EncodeToString(buf))
}

func decodeCursor(c Cursor) (int64, error) {
	if c == "" {
		return 0, nil
	}
	buf, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil || len(buf) != cursorLen || buf[0] != cursorVersion1 {
		return 0, ErrInvalidCursor
	}
	return int64(binary.BigEndian.Uint64(buf[1:])), nil //nolint:gosec // encodeCursor never writes a value wide enough to overflow int64
}
