package plugin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture records what a line writer emits.
type capture struct {
	stdout []string
	stderr []string
}

func (c *capture) Log(stream, text string) {
	if stream == "stderr" {
		c.stderr = append(c.stderr, text)
		return
	}
	c.stdout = append(c.stdout, text)
}

func (c *capture) Progress(string, int64, int64) {}

func TestNewLineWriter(t *testing.T) {
	t.Run("forwards complete lines to the named stream", func(t *testing.T) {
		out := &capture{}
		w := NewLineWriter(out, "stderr")

		_, err := w.Write([]byte("creating cluster demo\nwaiting for control plane\n"))
		require.NoError(t, err)
		assert.Equal(t, []string{"creating cluster demo", "waiting for control plane"}, out.stderr)
		assert.Empty(t, out.stdout)
	})

	t.Run("buffers a partial line until a later write completes it", func(t *testing.T) {
		out := &capture{}
		w := NewLineWriter(out, "stdout")

		_, err := w.Write([]byte("creating clus"))
		require.NoError(t, err)
		assert.Empty(t, out.stdout, "no newline yet, nothing to emit")

		_, err = w.Write([]byte("ter demo\n"))
		require.NoError(t, err)
		assert.Equal(t, []string{"creating cluster demo"}, out.stdout)
	})

	t.Run("strips a trailing carriage return", func(t *testing.T) {
		out := &capture{}
		w := NewLineWriter(out, "stdout")

		_, err := w.Write([]byte("ready\r\n"))
		require.NoError(t, err)
		assert.Equal(t, []string{"ready"}, out.stdout)
	})

	t.Run("emits a line that never terminates once it grows too large", func(t *testing.T) {
		out := &capture{}
		w := NewLineWriter(out, "stdout")

		_, err := w.Write([]byte(strings.Repeat("x", maxLineLength+1)))
		require.NoError(t, err)
		require.Len(t, out.stdout, 1)
		assert.True(t, strings.HasSuffix(out.stdout[0], "..."))
	})
}
