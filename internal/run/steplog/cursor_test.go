package steplog

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursor(t *testing.T) {
	t.Run("round-trips an offset", func(t *testing.T) {
		c := encodeCursor(12345)
		got, err := decodeCursor(c)
		require.NoError(t, err)
		assert.Equal(t, int64(12345), got)
	})

	t.Run("an empty cursor decodes to offset zero", func(t *testing.T) {
		got, err := decodeCursor("")
		require.NoError(t, err)
		assert.Zero(t, got)
	})

	t.Run("rejects a malformed cursor", func(t *testing.T) {
		_, err := decodeCursor("not valid base64!!")
		require.ErrorIs(t, err, ErrInvalidCursor)
	})

	t.Run("rejects a cursor with an unrecognized version byte", func(t *testing.T) {
		buf := make([]byte, cursorLen)
		buf[0] = 0xFF
		bad := Cursor(base64.RawURLEncoding.EncodeToString(buf))

		_, err := decodeCursor(bad)
		require.ErrorIs(t, err, ErrInvalidCursor)
	})
}
