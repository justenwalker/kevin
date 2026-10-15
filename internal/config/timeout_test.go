package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeout(t *testing.T) {
	t.Run("decodes a positive duration", func(t *testing.T) {
		var got Timeout
		require.NoError(t, json.Unmarshal([]byte(`"90s"`), &got))
		assert.Equal(t, Timeout(90*time.Second), got)
	})

	t.Run("rejects anything else", func(t *testing.T) {
		for _, in := range []string{`"soon"`, `"0s"`, `"-5s"`, `""`} {
			var got Timeout
			require.ErrorIs(t, json.Unmarshal([]byte(in), &got), ErrBadTimeout, in)
			assert.Zero(t, got)
		}
	})
}
